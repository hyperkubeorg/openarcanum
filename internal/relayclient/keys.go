package relayclient

import (
	"bytes"
	"errors"
	"fmt"
	"sync"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/relayid"
	"github.com/hyperkubeorg/openarcanum/internal/relayurl"
)

// KeyBook is what this process expects of relay keys (docs/protocol.md §2
// "Relay identity"): keys pinned per relay URL, and the URL each relay key
// that doesn't prove a URL was first seen at.
//
// A relay that knows its URL signs it into its Hello and checks it in what
// clients sign, so a key at another URL is caught there. A relay without a
// configured URL can't tell the URL apart, so the book accepts its key at
// one URL only (trust on first use, per process): a relay that presents
// such a relay's key and nonce under its own URL gets nothing signed.
type KeyBook struct {
	mu     sync.Mutex
	pinned map[string][]byte // canonical URL -> relay key
	seen   map[string]string // relay key -> canonical URL (URL-less relays)
}

// maxSeenKeys bounds the book; past it, it starts over (a process talks
// to a handful of relays).
const maxSeenKeys = 4096

// Keys is the process-wide book every Client consults.
var Keys = &KeyBook{}

// Errors from the book; Client methods that sign return them.
var (
	ErrPinnedKey    = errors.New("relayclient: relay key isn't the one pinned for this URL")
	ErrKeyElsewhere = errors.New("relayclient: this relay key was first seen at another URL (a relay presenting another relay's identity?)")
)

// Pin expects key for the relay at url (a pinned or registry key).
func (b *KeyBook) Pin(url string, key []byte) error {
	c, err := relayurl.Canonical(url)
	if err != nil {
		return err
	}
	if !identity.ValidPublic(key) {
		return errors.New("relayclient: not a relay key")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pinned == nil {
		b.pinned = map[string][]byte{}
	}
	b.pinned[c] = bytes.Clone(key)
	return nil
}

// check accepts relayKey for the relay dialled at url (canonical) whose
// Hello names helloURL.
func (b *KeyBook) check(url string, relayKey []byte, helloURL string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if want, ok := b.pinned[url]; ok && !bytes.Equal(want, relayKey) {
		return ErrPinnedKey
	}
	if helloURL != "" {
		if helloURL != url {
			return fmt.Errorf("relayclient: relay is %s, dialled as %s: dial it at its own URL", helloURL, url)
		}
		return nil
	}
	k := string(relayKey)
	if prev, ok := b.seen[k]; ok {
		if prev != url {
			return ErrKeyElsewhere
		}
		return nil
	}
	if b.seen == nil || len(b.seen) >= maxSeenKeys {
		b.seen = map[string]string{}
	}
	b.seen[k] = url
	return nil
}

// Binding is what this client signs for its relay connection (endpoint
// registration, fan-out tokens, moderation commands): the relay's proven
// key, the canonical URL it was dialled at, and the connection's nonce. It
// fails when the relay's identity doesn't fit that URL (Keys).
func (c *Client) Binding() (relayid.Binding, error) {
	url, err := relayurl.Canonical(c.URL)
	if err != nil {
		return relayid.Binding{}, fmt.Errorf("relayclient: relay URL: %w", err)
	}
	if err := c.keys.check(url, c.hello.RelayKey, c.hello.Url); err != nil {
		return relayid.Binding{}, err
	}
	return relayid.Binding{RelayKey: c.hello.RelayKey, URL: url, Nonce: c.hello.Nonce}, nil
}
