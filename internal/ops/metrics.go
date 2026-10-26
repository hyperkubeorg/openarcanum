package ops

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Prometheus text exposition format, version 0.0.4
// (https://prometheus.io/docs/instrumenting/exposition_formats/). Written
// here rather than pulling in client_golang: the nodes need counters,
// gauges and one histogram, and the format is small and stable.
//
// Metrics never carry content or per-user identifiers. Label values come
// from fixed, small sets chosen in code (route patterns, status codes,
// outcomes), never from request data; Registry enforces that every label
// value of a vector is one it was declared with.

// ContentType is the exposition format's media type.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

var (
	metricName = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	labelName  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

// Registry holds a node's metrics and writes them in the text format.
type Registry struct {
	mu      sync.Mutex
	metrics map[string]collector
}

type collector interface {
	help() string
	kind() string
	write(w *bufio.Writer, name string)
}

// NewRegistry returns a registry with the standard process metrics
// (process_start_time_seconds, go_goroutines, go_memstats_*).
func NewRegistry() *Registry {
	r := &Registry{metrics: map[string]collector{}}
	start := float64(time.Now().UnixNano()) / 1e9
	r.GaugeFunc("process_start_time_seconds", "Start time of the process since the Unix epoch in seconds.", func() float64 { return start })
	r.GaugeFunc("go_goroutines", "Number of goroutines that currently exist.", func() float64 { return float64(runtime.NumGoroutine()) })
	r.GaugeFunc("go_memstats_heap_alloc_bytes", "Bytes of allocated heap objects.", func() float64 {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return float64(m.HeapAlloc)
	})
	r.GaugeFunc("go_memstats_sys_bytes", "Bytes of memory obtained from the OS.", func() float64 {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return float64(m.Sys)
	})
	if runtime.GOOS == "linux" {
		r.GaugeFunc("process_open_fds", "Number of open file descriptors.", func() float64 {
			ents, err := os.ReadDir("/proc/self/fd")
			if err != nil {
				return math.NaN()
			}
			return float64(len(ents))
		})
	}
	return r
}

func (r *Registry) add(name string, c collector) {
	if !metricName.MatchString(name) {
		panic("ops: invalid metric name " + strconv.Quote(name))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.metrics[name]; dup {
		panic("ops: metric registered twice: " + name)
	}
	r.metrics[name] = c
}

// WriteTo writes every metric, sorted by name.
func (r *Registry) WriteTo(out io.Writer) (int64, error) {
	r.mu.Lock()
	names := make([]string, 0, len(r.metrics))
	for n := range r.metrics {
		names = append(names, n)
	}
	cs := make(map[string]collector, len(r.metrics))
	for n, c := range r.metrics {
		cs[n] = c
	}
	r.mu.Unlock()
	sort.Strings(names)
	cw := &countWriter{w: out}
	w := bufio.NewWriter(cw)
	for _, n := range names {
		c := cs[n]
		_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", n, escapeHelp(c.help()), n, c.kind())
		c.write(w, n)
	}
	err := w.Flush()
	return cw.n, err
}

// ServeHTTP serves the registry at a scrape endpoint.
func (r *Registry) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", ContentType)
	_, _ = r.WriteTo(w)
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

func escapeHelp(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(s)
}

func escapeLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`).Replace(s)
}

func formatValue(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case math.IsNaN(v):
		return "NaN"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

func labelString(names, values []string, extra ...string) string {
	if len(names) == 0 && len(extra) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, n := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		_, _ = fmt.Fprintf(&b, `%s="%s"`, n, escapeLabel(values[i]))
	}
	for i := 0; i+1 < len(extra); i += 2 {
		if b.Len() > 1 {
			b.WriteByte(',')
		}
		_, _ = fmt.Fprintf(&b, `%s="%s"`, extra[i], escapeLabel(extra[i+1]))
	}
	b.WriteByte('}')
	return b.String()
}

// atomicFloat is a float64 updated without locks.
type atomicFloat struct{ bits atomic.Uint64 }

func (f *atomicFloat) add(d float64) {
	for {
		old := f.bits.Load()
		if f.bits.CompareAndSwap(old, math.Float64bits(math.Float64frombits(old)+d)) {
			return
		}
	}
}

func (f *atomicFloat) set(v float64) { f.bits.Store(math.Float64bits(v)) }
func (f *atomicFloat) get() float64  { return math.Float64frombits(f.bits.Load()) }

// ---------------------------------------------------------------- counters and gauges

// Counter only goes up.
type Counter struct{ v atomicFloat }

func (c *Counter) Inc() { c.v.add(1) }

// Add adds d, which must not be negative.
func (c *Counter) Add(d float64) {
	if d < 0 {
		panic("ops: counter decreased")
	}
	c.v.add(d)
}

func (c *Counter) Value() float64 { return c.v.get() }

// Gauge goes up and down.
type Gauge struct{ v atomicFloat }

func (g *Gauge) Set(v float64) { g.v.set(v) }

func (g *Gauge) Add(d float64) { g.v.add(d) }

func (g *Gauge) Value() float64 { return g.v.get() }

type scalar struct {
	h, k string
	val  func() float64
}

func (s *scalar) help() string { return s.h }
func (s *scalar) kind() string { return s.k }
func (s *scalar) write(w *bufio.Writer, name string) {
	_, _ = fmt.Fprintf(w, "%s %s\n", name, formatValue(s.val()))
}

// Counter registers a counter without labels.
func (r *Registry) Counter(name, help string) *Counter {
	c := &Counter{}
	r.add(name, &scalar{help, "counter", c.Value})
	return c
}

// Gauge registers a gauge without labels.
func (r *Registry) Gauge(name, help string) *Gauge {
	g := &Gauge{}
	r.add(name, &scalar{help, "gauge", g.Value})
	return g
}

// GaugeFunc registers a gauge read from f at scrape time. f must be cheap
// and safe to call from any goroutine.
func (r *Registry) GaugeFunc(name, help string, f func() float64) {
	r.add(name, &scalar{help, "gauge", f})
}

// CounterFunc registers a counter read from f at scrape time (for counts a
// component already keeps). f must never decrease.
func (r *Registry) CounterFunc(name, help string, f func() float64) {
	r.add(name, &scalar{help, "counter", f})
}

// ---------------------------------------------------------------- labelled vectors

// Labels declares a vector's label names and, for each, every value it may
// take. Using any other value panics: label values are code-chosen, never
// request data. A nil value list accepts any value; only use it for values
// that come from code, such as the pattern a ServeMux matched (Instrument).
type Labels struct {
	Names  []string
	Values [][]string
}

func (l Labels) check() {
	if len(l.Names) != len(l.Values) {
		panic("ops: every label needs its allowed values")
	}
	for _, n := range l.Names {
		if !labelName.MatchString(n) || strings.HasPrefix(n, "__") || n == "le" {
			panic("ops: invalid label name " + strconv.Quote(n))
		}
	}
}

func (l Labels) key(values []string) string {
	if len(values) != len(l.Names) {
		panic(fmt.Sprintf("ops: %d label values for %d labels", len(values), len(l.Names)))
	}
	for i, v := range values {
		if l.Values[i] != nil && !slices.Contains(l.Values[i], v) {
			panic(fmt.Sprintf("ops: label %s=%q is not a declared value", l.Names[i], v))
		}
	}
	return strings.Join(values, "\xff")
}

// CounterVec is a counter per combination of label values.
type CounterVec struct {
	labels Labels
	h      string
	mu     sync.Mutex
	kids   map[string]*Counter
	vals   map[string][]string
}

// CounterVec registers a labelled counter.
func (r *Registry) CounterVec(name, help string, labels Labels) *CounterVec {
	labels.check()
	v := &CounterVec{labels: labels, h: help, kids: map[string]*Counter{}, vals: map[string][]string{}}
	r.add(name, v)
	return v
}

// With returns the counter for these label values (in declaration order).
func (v *CounterVec) With(values ...string) *Counter {
	k := v.labels.key(values)
	v.mu.Lock()
	defer v.mu.Unlock()
	c, ok := v.kids[k]
	if !ok {
		c = &Counter{}
		v.kids[k] = c
		v.vals[k] = append([]string(nil), values...)
	}
	return c
}

func (v *CounterVec) help() string { return v.h }
func (v *CounterVec) kind() string { return "counter" }
func (v *CounterVec) write(w *bufio.Writer, name string) {
	v.mu.Lock()
	keys := make([]string, 0, len(v.kids))
	for k := range v.kids {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	type row struct {
		labels string
		val    float64
	}
	rows := make([]row, 0, len(keys))
	for _, k := range keys {
		rows = append(rows, row{labelString(v.labels.Names, v.vals[k]), v.kids[k].Value()})
	}
	v.mu.Unlock()
	for _, r := range rows {
		_, _ = fmt.Fprintf(w, "%s%s %s\n", name, r.labels, formatValue(r.val))
	}
}

// ---------------------------------------------------------------- histograms

// DefaultBuckets suit request latencies in seconds (5 ms to 10 s).
var DefaultBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.2, 0.5, 1, 2.5, 5, 10}

// Histogram counts observations into cumulative buckets.
type Histogram struct {
	upper  []float64
	counts []atomic.Uint64
	count  atomic.Uint64
	sum    atomicFloat
}

func newHistogram(buckets []float64) *Histogram {
	if !sort.Float64sAreSorted(buckets) || len(buckets) == 0 {
		panic("ops: histogram buckets must be sorted and non-empty")
	}
	return &Histogram{upper: buckets, counts: make([]atomic.Uint64, len(buckets))}
}

// Observe records one value.
func (h *Histogram) Observe(v float64) {
	i := sort.SearchFloat64s(h.upper, v)
	if i < len(h.counts) {
		h.counts[i].Add(1)
	}
	h.count.Add(1)
	h.sum.add(v)
}

// Count is the number of observations.
func (h *Histogram) Count() uint64 { return h.count.Load() }

func (h *Histogram) writeSeries(w *bufio.Writer, name string, names, values []string) {
	var cum uint64
	for i, ub := range h.upper {
		cum += h.counts[i].Load()
		_, _ = fmt.Fprintf(w, "%s_bucket%s %d\n", name, labelString(names, values, "le", formatValue(ub)), cum)
	}
	n := h.count.Load()
	_, _ = fmt.Fprintf(w, "%s_bucket%s %d\n", name, labelString(names, values, "le", "+Inf"), n)
	_, _ = fmt.Fprintf(w, "%s_sum%s %s\n", name, labelString(names, values), formatValue(h.sum.get()))
	_, _ = fmt.Fprintf(w, "%s_count%s %d\n", name, labelString(names, values), n)
}

// HistogramVec is a histogram per combination of label values.
type HistogramVec struct {
	labels  Labels
	h       string
	buckets []float64
	mu      sync.Mutex
	kids    map[string]*Histogram
	vals    map[string][]string
}

// HistogramVec registers a labelled histogram.
func (r *Registry) HistogramVec(name, help string, buckets []float64, labels Labels) *HistogramVec {
	labels.check()
	newHistogram(buckets)
	v := &HistogramVec{labels: labels, h: help, buckets: buckets, kids: map[string]*Histogram{}, vals: map[string][]string{}}
	r.add(name, v)
	return v
}

// With returns the histogram for these label values.
func (v *HistogramVec) With(values ...string) *Histogram {
	k := v.labels.key(values)
	v.mu.Lock()
	defer v.mu.Unlock()
	h, ok := v.kids[k]
	if !ok {
		h = newHistogram(v.buckets)
		v.kids[k] = h
		v.vals[k] = append([]string(nil), values...)
	}
	return h
}

func (v *HistogramVec) help() string { return v.h }
func (v *HistogramVec) kind() string { return "histogram" }
func (v *HistogramVec) write(w *bufio.Writer, name string) {
	v.mu.Lock()
	keys := make([]string, 0, len(v.kids))
	for k := range v.kids {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	kids := make([]*Histogram, len(keys))
	vals := make([][]string, len(keys))
	for i, k := range keys {
		kids[i], vals[i] = v.kids[k], v.vals[k]
	}
	v.mu.Unlock()
	for i := range kids {
		kids[i].writeSeries(w, name, v.labels.Names, vals[i])
	}
}
