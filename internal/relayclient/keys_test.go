package relayclient

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestKeyBook(t *testing.T) {
	a, b := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)
	const u1, u2 = "ws://relay-one.example/ws", "ws://relay-two.example/ws"

	var kb KeyBook
	// A relay that knows its URL: accepted only where it says it is.
	if err := kb.check(u1, a, u1); err != nil {
		t.Fatal(err)
	}
	if err := kb.check(u2, a, u1); err == nil || !strings.Contains(err.Error(), "own URL") {
		t.Fatalf("relay announcing another URL: %v", err)
	}
	// A relay without a URL: its key at one URL only.
	if err := kb.check(u2, b, ""); err != nil {
		t.Fatal(err)
	}
	if err := kb.check(u2, b, ""); err != nil {
		t.Fatalf("same key, same URL again: %v", err)
	}
	if err := kb.check(u1, b, ""); !errors.Is(err, ErrKeyElsewhere) {
		t.Fatalf("URL-less key at a second URL: %v", err)
	}
	// A relay restarting with a fresh key at its URL is fine.
	if err := kb.check(u2, a, ""); err != nil {
		t.Fatalf("new key at a known URL: %v", err)
	}
	// Pinned keys win.
	if err := kb.Pin("WS://Relay-One.example:80/ws/", b); err != nil {
		t.Fatal(err)
	}
	if err := kb.check(u1, a, u1); !errors.Is(err, ErrPinnedKey) {
		t.Fatalf("unpinned key at a pinned URL: %v", err)
	}
	if err := kb.Pin(u1, []byte{1}); err == nil {
		t.Fatal("pinned a malformed key")
	}
}
