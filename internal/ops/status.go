package ops

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Check is a health check; nil means healthy. Its error text is shown to
// whoever asks, so it must describe the node's state and never data.
type Check func(ctx context.Context) error

// Status is what a node's status listener serves:
//
//	GET /healthz   200 "ok" while Live passes (the process works: its
//	               database answers), else 503 with the reason
//	GET /readyz    200 "ok" while Ready passes (it can serve users: for
//	               a community server, a relay is attached), else 503
//	GET /metrics   the Prometheus text exposition of Registry
type Status struct {
	Registry *Registry
	Live     Check
	Ready    Check
}

// checkTimeout bounds one health check.
const checkTimeout = 3 * time.Second

// Handler returns the status HTTP handler.
func (s Status) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", s.Registry)
	mux.HandleFunc("GET /healthz", checkHandler(s.Live))
	ready := s.Ready
	if ready == nil {
		ready = s.Live
	}
	mux.HandleFunc("GET /readyz", checkHandler(ready))
	return mux
}

func checkHandler(c Check) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if c != nil {
			ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
			defer cancel()
			if err := c(ctx); err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = fmt.Fprintf(w, "unhealthy: %v\n", err)
				return
			}
		}
		_, _ = io.WriteString(w, "ok\n")
	}
}

// ServeStatus starts the status listener on addr (Settings.MetricsListen)
// and stops it when ctx ends. It returns the bound address.
func ServeStatus(ctx context.Context, addr string, s Status, log *slog.Logger) (net.Addr, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("metrics_listen: %w", err)
	}
	hs := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second}
	go func() {
		if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && log != nil {
			log.Error("status listener stopped", "err", err)
		}
	}()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	if log != nil {
		log.Info("status listener (healthz, readyz, metrics)", "addr", ln.Addr().String())
	}
	return ln.Addr(), nil
}

// ---------------------------------------------------------------- HTTP instrumentation

// Instrument counts requests and times them per route and status code:
// <prefix>_http_requests_total{route,code} and
// <prefix>_http_request_duration_seconds{route}. The route is the pattern
// mux matched ("POST /v1/mailbox/drop"), or "unmatched"; never the path or
// query, which can carry ids. A handler that isn't a ServeMux (a wrapper
// added in front of one) is counted under the route "all"; InstrumentRoutes
// names its routes.
func Instrument(h http.Handler, reg *Registry, prefix string) http.Handler {
	mux, _ := h.(*http.ServeMux)
	return InstrumentRoutes(h, mux, reg, prefix)
}

// InstrumentRoutes is Instrument for a handler that wraps a ServeMux (e.g.
// with middleware): routes names each request's route, h serves it.
func InstrumentRoutes(h http.Handler, routes *http.ServeMux, reg *Registry, prefix string) http.Handler {
	mux, isMux := routes, routes != nil
	reqs := reg.CounterVec(prefix+"_http_requests_total", "HTTP requests by route pattern and status code.",
		Labels{Names: []string{"route", "code"}, Values: [][]string{nil, nil}})
	dur := reg.HistogramVec(prefix+"_http_request_duration_seconds", "HTTP request duration by route pattern.",
		DefaultBuckets, Labels{Names: []string{"route"}, Values: [][]string{nil}})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := "all"
		if isMux {
			if _, route = mux.Handler(r); route == "" {
				route = "unmatched"
			}
		}
		sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		start := time.Now()
		h.ServeHTTP(sw, r)
		dur.With(route).Observe(time.Since(start).Seconds())
		reqs.With(route, strconv.Itoa(sw.code)).Inc()
	})
}

type statusWriter struct {
	http.ResponseWriter
	code    int
	written bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.written {
		w.code, w.written = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	w.written = true
	return w.ResponseWriter.Write(p)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// ---------------------------------------------------------------- healthcheck command

// Healthcheck is the `<node> healthcheck URL` command for container
// health checks (the images have no shell or curl): it GETs URL and
// returns 0 for a 200 answer, 1 otherwise.
//
// A node serving TLS on its public listener (tls_mode) answers a plain
// http://127.0.0.1 check with "400 Client sent an HTTP request to an HTTPS
// server"; the check then asks again over https://, so the images' fixed
// health check keeps working. On a loopback address the certificate isn't
// verified: it names the node's public domain, and the check only asks
// whether the local process answers.
func Healthcheck(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: healthcheck URL   (for example http://127.0.0.1:7000/healthz)")
		return 2
	}
	u, err := url.Parse(args[0])
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "healthcheck:", err)
		return 2
	}
	status, body, err := healthGet(u)
	if err == nil && status == http.StatusBadRequest && u.Scheme == "http" && loopbackHost(u.Hostname()) &&
		strings.Contains(body, "HTTPS server") {
		u.Scheme = "https"
		status, body, err = healthGet(u)
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "healthcheck:", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "%d %s", status, body)
	if status != http.StatusOK {
		return 1
	}
	return 0
}

func healthGet(u *url.URL) (int, string, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	if u.Scheme == "https" && loopbackHost(u.Hostname()) {
		// Loopback liveness only (see Healthcheck): no data is exchanged.
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	res, err := client.Get(u.String())
	if err != nil {
		return 0, "", err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 512))
	return res.StatusCode, string(body), nil
}
