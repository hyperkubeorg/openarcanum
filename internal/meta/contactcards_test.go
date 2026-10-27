package meta_test

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// A permanent contact code's card: the token's holder writes, replaces and
// deletes it; anyone with the address reads it.
func TestContactCards(t *testing.T) {
	n := startNode(t, nil)
	token := bytes.Repeat([]byte{7}, 32)
	path := "/v1/cards/" + base64.RawURLEncoding.EncodeToString(meta.ContactCardAddress(token))
	if code := n.get(t, path, nil); code != http.StatusNotFound {
		t.Fatalf("before any put: %d", code)
	}
	for _, card := range [][]byte{[]byte("card one"), []byte("card two")} {
		if code := n.post(t, "/v1/cards/put", &wire.ContactCardPut{Token: token, Card: card}, nil); code != 200 {
			t.Fatalf("put: %d", code)
		}
		var got wire.ContactCardFetched
		if code := n.get(t, path, &got); code != 200 || !bytes.Equal(got.Card, card) || got.UpdatedAt == 0 {
			t.Fatalf("get: %d %q", code, got.Card)
		}
	}
	// Another token can't touch it: its address is elsewhere.
	other := bytes.Repeat([]byte{8}, 32)
	if code := n.post(t, "/v1/cards/delete", &wire.ContactCardDelete{Token: other}, nil); code != 200 {
		t.Fatalf("delete with another token: %d", code)
	}
	if code := n.get(t, path, nil); code != 200 {
		t.Fatalf("deleted by another token: %d", code)
	}
	if code := n.post(t, "/v1/cards/delete", &wire.ContactCardDelete{Token: token}, nil); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if code := n.get(t, path, nil); code != http.StatusNotFound {
		t.Fatalf("after delete: %d", code)
	}
	// Malformed requests and oversized cards are refused.
	if code := n.post(t, "/v1/cards/put", &wire.ContactCardPut{Token: token[:31], Card: []byte("x")}, nil); code != http.StatusBadRequest {
		t.Fatalf("short token: %d", code)
	}
	if code := n.post(t, "/v1/cards/put", &wire.ContactCardPut{Token: token}, nil); code != http.StatusBadRequest {
		t.Fatalf("empty card: %d", code)
	}
	if code := n.post(t, "/v1/cards/put", &wire.ContactCardPut{Token: token, Card: make([]byte, meta.MaxContactCard+1)}, nil); code == 200 {
		t.Fatal("oversized card accepted")
	}
	for _, p := range []string{"/v1/cards/abc", "/v1/cards/" + base64.RawURLEncoding.EncodeToString(make([]byte, 31))} {
		if code := n.get(t, p, nil); code != http.StatusBadRequest {
			t.Fatalf("%s: %d", p, code)
		}
	}
}
