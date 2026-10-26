package ops

import (
	"bufio"
	"fmt"
	"regexp"
	"strings"
)

// sampleLine is one sample in the text format: name, optional labels,
// value (no timestamps: the nodes never write them).
var sampleLine = regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{([a-zA-Z_][a-zA-Z0-9_]*="(\\.|[^"\\])*"(,[a-zA-Z_][a-zA-Z0-9_]*="(\\.|[^"\\])*")*)?\})? (-?[0-9.e+-]+|\+Inf|-Inf|NaN)$`)

// CheckExposition validates text in the Prometheus text format 0.0.4 as
// the nodes write it: every line is a HELP, a TYPE or a sample; each
// family has HELP then TYPE before its samples; sample names belong to the
// family (with _bucket, _sum and _count for histograms); no family appears
// twice. It returns each family's type. Tests of every node use it on a
// real scrape.
func CheckExposition(text string) (map[string]string, error) {
	types := map[string]string{}
	var family, kind string
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "# HELP "):
			f := strings.SplitN(line[7:], " ", 2)
			if _, dup := types[f[0]]; dup {
				return nil, fmt.Errorf("line %d: family %s twice", n, f[0])
			}
			family, kind = f[0], ""
			types[family] = ""
		case strings.HasPrefix(line, "# TYPE "):
			f := strings.Fields(line[7:])
			if len(f) != 2 || f[0] != family {
				return nil, fmt.Errorf("line %d: TYPE without its HELP: %q", n, line)
			}
			switch f[1] {
			case "counter", "gauge", "histogram":
			default:
				return nil, fmt.Errorf("line %d: type %q", n, f[1])
			}
			kind = f[1]
			types[family] = kind
		default:
			m := sampleLine.FindStringSubmatch(line)
			if m == nil {
				return nil, fmt.Errorf("line %d is not a sample: %q", n, line)
			}
			name := m[1]
			ok := name == family
			if kind == "histogram" {
				ok = name == family+"_bucket" || name == family+"_sum" || name == family+"_count"
			}
			if !ok || kind == "" {
				return nil, fmt.Errorf("line %d: sample %s outside its family (%s)", n, name, family)
			}
		}
	}
	return types, sc.Err()
}
