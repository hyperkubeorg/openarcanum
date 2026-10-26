package session

import (
	"errors"
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/version"
)

// A server one version ahead (speaking 1 and 2) still serves v1 clients,
// and a v2 client gets 2.
func TestSessionVersionNegotiation(t *testing.T) {
	f := newFixture(t)
	f.srv.Versions = version.SupportedFor(2)
	f.cli.Versions = []uint32{1}
	cs, ss, cerr, serr := handshake(t, f)
	if cerr != nil || serr != nil {
		t.Fatalf("v1 client on a v2 server: %v / %v", cerr, serr)
	}
	if cs.Version != 1 || ss.Version != 1 {
		t.Fatalf("versions %d/%d", cs.Version, ss.Version)
	}
	f.cli.Versions = version.SupportedFor(2)
	cs, ss, cerr, serr = handshake(t, f)
	if cerr != nil || serr != nil || cs.Version != 2 || ss.Version != 2 {
		t.Fatalf("v2 client on a v2 server: %v / %v", cerr, serr)
	}
	// The current build negotiates its current version.
	f = newFixture(t)
	cs, _, cerr, _ = handshake(t, f)
	if cerr != nil || cs.Version != version.Current {
		t.Fatalf("default handshake: %v, version %d", cerr, cs.Version)
	}
}

func TestSessionVersionMismatch(t *testing.T) {
	// An old client (v0 or v1) on a server that speaks 2 and 3: both sides
	// fail, and the client learns that it is the outdated one.
	for _, old := range [][]uint32{{0}, {1}} {
		f := newFixture(t)
		f.srv.Versions = []uint32{2, 3}
		f.cli.Versions = old
		_, _, cerr, serr := handshake(t, f)
		var me *version.MismatchError
		if serr == nil {
			t.Fatalf("server accepted client versions %v", old)
		}
		if !errors.As(cerr, &me) || me.PeerOutdated {
			t.Fatalf("client %v should be told to update: %v", old, cerr)
		}
	}
	// A new client on an old server: the client reports the server outdated.
	f := newFixture(t)
	f.srv.Versions = []uint32{1}
	f.cli.Versions = []uint32{2, 3}
	_, _, cerr, serr := handshake(t, f)
	var me *version.MismatchError
	if serr == nil || !errors.As(cerr, &me) || !me.PeerOutdated {
		t.Fatalf("old server: client %v, server %v", cerr, serr)
	}
}
