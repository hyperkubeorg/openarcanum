package version

import (
	"strings"
	"testing"
)

func TestReleaseString(t *testing.T) {
	oldRel, oldRev := Release, Revision
	t.Cleanup(func() { Release, Revision = oldRel, oldRev })

	Release, Revision = "2026.10.0-beta.1", "0123abcd4567"
	s := String("relay")
	if !strings.HasPrefix(s, "relay 2026.10.0-beta.1 (revision 0123abcd4567, protocol 1, go") {
		t.Fatalf("String = %q", s)
	}
	Revision = ""
	if BuildRevision() != "" && !strings.Contains(String("meta"), "revision "+BuildRevision()) {
		t.Fatalf("String without -X Revision = %q", String("meta"))
	}
}
