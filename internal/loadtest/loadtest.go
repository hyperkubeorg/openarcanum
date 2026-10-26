//go:build load

// Package loadtest holds the load tests for the community server, relay
// and maintainer node, each run as its real binary
// pinned to the target's CPU count, with clients on the other cores. They
// run only with `-tags load` (`make load`); docs/load-tests.md records the
// results.
package loadtest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Settings from the environment, so an operator can rerun a test at another
// size or on other cores without editing it.
func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

func envString(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func envDuration(name string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

// serverCPUs is the cores the server under test is pinned to (the target's
// vCPU count): LOAD_SERVER_CPUS, "0,1" by default.
func serverCPUs() string {
	if v := os.Getenv("LOAD_SERVER_CPUS"); v != "" {
		return v
	}
	return "0,1"
}

func cpuCount(list string) int {
	n := 0
	for _, part := range strings.Split(list, ",") {
		if a, b, ok := strings.Cut(part, "-"); ok {
			lo, _ := strconv.Atoi(a)
			hi, _ := strconv.Atoi(b)
			n += hi - lo + 1
		} else if part != "" {
			n++
		}
	}
	return n
}

// serverBinary is a built server binary: $OPENARCANUM_BIN/name, or bin/name at the
// repository root.
func serverBinary(t testing.TB, name string) string {
	t.Helper()
	dir := os.Getenv("OPENARCANUM_BIN")
	if dir == "" {
		_, file, _, _ := runtime.Caller(0)
		dir = filepath.Join(filepath.Dir(file), "..", "..", "bin")
	}
	p := filepath.Join(dir, name)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("%s not built (run `make build`): %v", p, err)
	}
	return p
}

func freePort(t testing.TB) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func waitPort(t testing.TB, port int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("port %d never opened", port)
}

// server is a server binary running pinned to serverCPUs, with GOMAXPROCS
// set to match, as on a machine with that many vCPUs.
type server struct {
	cmd  *exec.Cmd
	cpus int
	log  *os.File
}

func startServer(t testing.TB, env []string, bin string, args ...string) *server {
	t.Helper()
	return startPinned(t, serverCPUs(), env, bin, args...)
}

// startPinned runs a binary on the given cores with GOMAXPROCS to match.
func startPinned(t testing.TB, cpus string, env []string, bin string, args ...string) *server {
	t.Helper()
	logDir := os.Getenv("LOAD_LOG_DIR") // keep server logs there (default: the test's temp dir)
	if logDir == "" {
		logDir = t.TempDir()
	}
	logf, err := os.Create(filepath.Join(logDir, filepath.Base(bin)+".log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("taskset", append([]string{"-c", cpus, bin}, args...)...)
	cmd.Env = append(append(os.Environ(), fmt.Sprintf("GOMAXPROCS=%d", cpuCount(cpus))), env...)
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &server{cmd: cmd, cpus: cpuCount(cpus), log: logf}
	t.Cleanup(s.stop)
	return s
}

func (s *server) stop() {
	if s.cmd.Process != nil && s.cmd.ProcessState == nil {
		_ = s.cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = s.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = s.cmd.Process.Kill()
			<-done
		}
	}
	_ = s.log.Close()
}

// cpuSeconds is the process's user+system CPU time so far (/proc/pid/stat;
// taskset execs the server, so the pid is the server's).
func (s *server) cpuSeconds(t testing.TB) float64 {
	t.Helper()
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", s.cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	// Fields after the command name, which is in parentheses.
	f := strings.Fields(string(b[strings.LastIndexByte(string(b), ')')+2:]))
	utime, _ := strconv.ParseFloat(f[11], 64)
	stime, _ := strconv.ParseFloat(f[12], 64)
	return (utime + stime) / 100 // USER_HZ is 100 on Linux
}

// memory is the process's current and peak resident set, in MiB.
func (s *server) memory(t testing.TB) (rss, peak float64) {
	t.Helper()
	f, err := os.Open(fmt.Sprintf("/proc/%d/status", s.cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), ":")
		kb, _ := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 64)
		switch k {
		case "VmRSS":
			rss = kb / 1024
		case "VmHWM":
			peak = kb / 1024
		}
	}
	return rss, peak
}

// latencies collects durations from many goroutines.
type latencies struct {
	mu sync.Mutex
	d  []time.Duration
}

func (l *latencies) add(d time.Duration) {
	l.mu.Lock()
	l.d = append(l.d, d)
	l.mu.Unlock()
}

type summary struct {
	Count int     `json:"count"`
	P50   float64 `json:"p50_ms"`
	P95   float64 `json:"p95_ms"`
	P99   float64 `json:"p99_ms"`
	Max   float64 `json:"max_ms"`
}

func (l *latencies) summary() summary {
	l.mu.Lock()
	d := append([]time.Duration(nil), l.d...)
	l.mu.Unlock()
	if len(d) == 0 {
		return summary{}
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	q := func(p float64) float64 {
		i := int(math.Ceil(p*float64(len(d)))) - 1
		return float64(d[max(i, 0)].Microseconds()) / 1000
	}
	return summary{Count: len(d), P50: q(0.50), P95: q(0.95), P99: q(0.99), Max: q(1)}
}

// report prints a result as one JSON line (for docs/load-tests.md) and, when
// LOAD_RESULTS names a file, appends it there.
func report(t testing.TB, name string, result any) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"test": name, "at": time.Now().UTC().Format(time.RFC3339), "result": result})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("RESULT %s", b)
	if p := os.Getenv("LOAD_RESULTS"); p != "" {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		_, err = fmt.Fprintf(f, "%s\n", b)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}
