package ops

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
)

// The process's CPU time and memory, from /proc (Linux).
func TestProcessCPUAndMemory(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("read from /proc")
	}
	var out bytes.Buffer
	if _, err := NewRegistry().WriteTo(&out); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"process_cpu_seconds_total ", "process_resident_memory_bytes "} {
		i := strings.Index(out.String(), "\n"+name)
		if i < 0 {
			t.Fatalf("no %s in\n%s", name, out.String())
		}
		line := strings.SplitN(out.String()[i+1:], "\n", 2)[0]
		if strings.HasSuffix(line, " NaN") || strings.HasSuffix(line, " 0") && strings.HasPrefix(name, "process_resident") {
			t.Fatalf("%q", line)
		}
	}
}
