package meta

// CORS (docs/operations.md "TLS and browsers"): a web client served from
// its own site calls maintainer nodes from the browser, which needs the
// node's permission to read the answers.
//
// Allowing every origin ("*", the default) is safe here because the API
// has no ambient authority: no cookies, no HTTP authentication, no
// client-certificate or source-address trust. Every write carries its own
// proof in the body (a signature, a capability token, a mailbox key's
// proof), so a page on another site can do nothing a script on the
// attacker's own machine couldn't. Credentials are never allowed
// (Access-Control-Allow-Credentials is never sent), so a browser won't
// attach any either.
//
// Preflights are answered here, before the version check, the routes and
// the write rate limit: they cost no database work and no rate-limit
// tokens, and browsers cache them for corsMaxAge.

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/hyperkubeorg/openarcanum/internal/version"
)

const (
	// What the clients send: GET and POST, protobuf bodies (a non-simple
	// Content-Type) and the protocol version header.
	corsMethods = "GET, POST"
	corsHeaders = "Content-Type, " + version.Header
	// Seconds a browser may reuse a preflight (browsers cap it lower:
	// Chromium at 2 hours).
	corsMaxAge = "86400"
)

// validCORSOrigins checks cors_origins: "*" or scheme://host[:port]
// origins as browsers send them.
func validCORSOrigins(origins []string) error {
	for _, o := range origins {
		if o == "*" {
			continue
		}
		u, err := url.Parse(o)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" ||
			u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || strings.HasSuffix(o, "/") {
			return fmt.Errorf("cors_origins entry %q must be \"*\" or an origin like https://app.example.invalid (no path)", o)
		}
	}
	if len(origins) > 1 && slices.Contains(origins, "*") {
		return errors.New("cors_origins: \"*\" already allows every origin; list it alone")
	}
	return nil
}

// cors wraps the API with the CORS headers cors_origins allows.
func (s *Server) cors(next http.Handler) http.Handler {
	if len(s.cfg.CORSOrigins) == 0 {
		return next
	}
	anyOrigin := slices.Contains(s.cfg.CORSOrigins, "*")
	allowed := map[string]bool{}
	for _, o := range s.cfg.CORSOrigins {
		allowed[strings.ToLower(o)] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		if !anyOrigin {
			// The answer depends on the origin: caches must keep them apart.
			h.Add("Vary", "Origin")
		}
		preflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
		if !anyOrigin && !allowed[strings.ToLower(origin)] {
			if preflight {
				http.Error(w, "origin not allowed (cors_origins)", http.StatusForbidden)
				return
			}
			// Served as for any other client; the browser withholds the
			// answer from the page.
			next.ServeHTTP(w, r)
			return
		}
		if anyOrigin {
			h.Set("Access-Control-Allow-Origin", "*")
		} else {
			h.Set("Access-Control-Allow-Origin", origin)
		}
		if preflight {
			h.Set("Access-Control-Allow-Methods", corsMethods)
			h.Set("Access-Control-Allow-Headers", corsHeaders)
			h.Set("Access-Control-Max-Age", corsMaxAge)
			// Chromium's Private Network Access: a public page asking a
			// node on a private address (a home or office server).
			if r.Header.Get("Access-Control-Request-Private-Network") == "true" {
				h.Set("Access-Control-Allow-Private-Network", "true")
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// Let the page read the negotiated protocol version.
		h.Set("Access-Control-Expose-Headers", version.Header)
		next.ServeHTTP(w, r)
	})
}
