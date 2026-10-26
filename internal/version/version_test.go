package version

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestPolicy(t *testing.T) {
	if !slices.Equal(Supported(), []uint32{1}) {
		t.Fatalf("v1 build speaks %v", Supported())
	}
	if !slices.Equal(SupportedFor(2), []uint32{1, 2}) || !slices.Equal(SupportedFor(7), []uint32{6, 7}) {
		t.Fatal("a build must speak its version and the previous one")
	}
}

func TestNegotiate(t *testing.T) {
	cases := []struct {
		ours, theirs []uint32
		want         uint32
		peerOld      bool
	}{
		{[]uint32{1}, []uint32{1}, 1, false},
		{[]uint32{1, 2}, []uint32{1}, 1, false},    // a v2 server serves a v1 client
		{[]uint32{1}, []uint32{1, 2}, 1, false},    // a v2 client talks to a v1 server
		{[]uint32{1, 2}, []uint32{2, 3}, 2, false}, // highest in common
		{[]uint32{1, 2}, []uint32{0}, 0, true},     // v0 refused, peer outdated
		{[]uint32{1, 2}, nil, 0, true},             // nothing announced
		{[]uint32{2, 3}, []uint32{1}, 0, true},     // peer behind
		{[]uint32{1}, []uint32{2, 3}, 0, false},    // we are behind
		{[]uint32{1}, make([]uint32, 17), 0, true}, // over-long list
	}
	for _, c := range cases {
		got, err := Negotiate(c.ours, c.theirs)
		if got != c.want {
			t.Errorf("Negotiate(%v, %v) = %d, want %d", c.ours, c.theirs, got, c.want)
		}
		if c.want == 0 {
			var me *MismatchError
			if !errors.As(err, &me) || me.PeerOutdated != c.peerOld {
				t.Errorf("Negotiate(%v, %v): %v (peerOutdated want %v)", c.ours, c.theirs, err, c.peerOld)
			}
		}
	}
}

func TestParseFormat(t *testing.T) {
	if !slices.Equal(Parse(Format([]uint32{1, 2})), []uint32{1, 2}) {
		t.Fatal("round trip")
	}
	for _, bad := range []string{"x", "1,-2", "99999999999"} {
		if Parse(bad) != nil {
			t.Errorf("parsed %q", bad)
		}
	}
	if Parse("") != nil || len(Parse(" 1 , 2 ")) != 2 {
		t.Fatal("whitespace and empty handling")
	}
}

func TestMiddleware(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	srv := httptest.NewServer(Middleware(SupportedFor(2), ok))
	defer srv.Close()
	do := func(path, header string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		if header != "" {
			req.Header.Set(Header, header)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if r := do("/v1/x", "1"); r.StatusCode != 200 || r.Header.Get(Header) != "1" {
		t.Fatalf("v1 client on a v2 node: %d %q", r.StatusCode, r.Header.Get(Header))
	}
	if r := do("/v1/x", "1,2"); r.Header.Get(Header) != "2" {
		t.Fatalf("v2 client: %q", r.Header.Get(Header))
	}
	for _, h := range []string{"", "0", "3", "junk"} {
		r := do("/v1/x", h)
		if r.StatusCode != http.StatusUpgradeRequired || r.Header.Get(Header) != "1,2" {
			t.Fatalf("header %q: %d %q", h, r.StatusCode, r.Header.Get(Header))
		}
	}
	if r := do("/healthz", ""); r.StatusCode != 200 {
		t.Fatal("health check needs no version")
	}
	// The client side reads a 426 as a mismatch saying who's outdated.
	resp := &http.Response{StatusCode: http.StatusUpgradeRequired, Header: http.Header{http.CanonicalHeaderKey(Header): {"2,3"}}}
	var me *MismatchError
	if err := CheckResponse(resp); !errors.As(err, &me) || me.PeerOutdated {
		t.Fatalf("an old client must learn it's outdated: %v", err)
	}
	resp.Header.Set(Header, "0")
	if err := CheckResponse(resp); !errors.As(err, &me) || !me.PeerOutdated {
		t.Fatalf("an old server must be reported: %v", err)
	}
	if CheckResponse(&http.Response{StatusCode: 200, Header: http.Header{http.CanonicalHeaderKey(Header): {"1"}}}) != nil {
		t.Fatal("a good reply refused")
	}
	if CheckResponse(&http.Response{StatusCode: 200, Header: http.Header{http.CanonicalHeaderKey(Header): {"5"}}}) == nil {
		t.Fatal("a reply choosing a version we don't speak accepted")
	}
}

// FuzzHeader: any header value parses without panicking, never yields more
// than MaxListed versions, and negotiation either picks a version both
// speak or reports a mismatch.
func FuzzHeader(f *testing.F) {
	for _, s := range []string{"1", "1,2", "", "0", "x", "1,,2", "4294967295", "1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		vs := Parse(s)
		if len(vs) > MaxListed {
			t.Fatalf("%d versions", len(vs))
		}
		v, err := Choose(SupportedFor(2), s)
		if err == nil && (!slices.Contains(vs, v) || !slices.Contains(SupportedFor(2), v)) {
			t.Fatalf("chose %d from %v", v, vs)
		}
		if err == nil && v == 0 {
			t.Fatal("chose version 0")
		}
	})
}
