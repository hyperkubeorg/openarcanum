package sframe

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

// The RFC 9605 test vectors (clients/desktop/tests/vectors/sframe, from the RFC
// repository): headers, and the full frame for suite 4.
func TestRFCVectors(t *testing.T) {
	raw, err := os.ReadFile("../../../clients/desktop/tests/vectors/sframe/test-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Header []struct {
			KID     uint64 `json:"kid"`
			CTR     uint64 `json:"ctr"`
			Encoded string `json:"encoded"`
		} `json:"header"`
		SFrame []struct {
			Suite    int    `json:"cipher_suite"`
			KID      uint64 `json:"kid"`
			CTR      uint64 `json:"ctr"`
			BaseKey  string `json:"base_key"`
			Metadata string `json:"metadata"`
			PT       string `json:"pt"`
			CT       string `json:"ct"`
		} `json:"sframe"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	h := func(s string) []byte { b, _ := hex.DecodeString(s); return b }
	if len(v.Header) == 0 {
		t.Fatal("no header vectors")
	}
	for _, x := range v.Header {
		if got := Header(x.KID, x.CTR); !bytes.Equal(got, h(x.Encoded)) {
			t.Fatalf("header kid=%d ctr=%d: %x, want %s", x.KID, x.CTR, got, x.Encoded)
		}
		kid, ctr, n, err := ParseHeader(h(x.Encoded))
		if err != nil || kid != x.KID || ctr != x.CTR || n != len(h(x.Encoded)) {
			t.Fatalf("parse %s: %d %d %d %v", x.Encoded, kid, ctr, n, err)
		}
	}
	seen := false
	for _, x := range v.SFrame {
		if x.Suite != Suite {
			continue
		}
		seen = true
		k, err := NewKey(h(x.BaseKey), x.KID)
		if err != nil {
			t.Fatal(err)
		}
		if got := k.Seal(x.CTR, h(x.Metadata), h(x.PT)); !bytes.Equal(got, h(x.CT)) {
			t.Fatalf("seal: %x, want %s", got, x.CT)
		}
		pt, err := k.Open(h(x.CT), h(x.Metadata))
		if err != nil || !bytes.Equal(pt, h(x.PT)) {
			t.Fatalf("open: %x %v", pt, err)
		}
	}
	if !seen {
		t.Fatal("no vector for suite 4")
	}
}
