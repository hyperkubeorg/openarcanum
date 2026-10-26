package meta

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/relayurl"
)

// Proof of control for the relay registry (docs/protocol.md §7.3). A
// heartbeat names a URL and is signed by a relay key, but anyone can sign a
// heartbeat naming any URL. Before the registry lists a URL for a key, the
// node fetches the key the URL serves (a plain GET of the http(s) form of
// the URL, which a relay answers with its registry key) and requires it to
// be the heartbeat's key. The first key proven for a URL keeps it while its
// heartbeats are fresh (relay_ttl).
//
// The fetch goes to an address someone else chose, so it is bounded: no
// redirects, no proxy, public addresses only (unless allow_private_relays),
// a short timeout, a tiny body, a few at a time, and at most one per URL
// per relayProofRetry. A result is kept for relayProofTTL.

const (
	relayProofTTL     = 10 * time.Minute
	relayProofRetry   = 30 * time.Second
	relayProofTimeout = 5 * time.Second
	relayProofMax     = 10_000
	relayProofsAtOnce = 8
)

type relayProof struct {
	key []byte // the key the URL served (nil: the fetch failed)
	at  time.Time
}

type relayProofs struct {
	mu    sync.Mutex
	m     map[string]relayProof
	slots chan struct{}
	fetch func(ctx context.Context, url string) ([]byte, error)
}

func newRelayProofs(allowPrivate bool) *relayProofs {
	tr := &http.Transport{
		DialContext:            relayurl.Dialer(allowPrivate, relayProofTimeout),
		TLSHandshakeTimeout:    relayProofTimeout,
		ResponseHeaderTimeout:  relayProofTimeout,
		MaxResponseHeaderBytes: 8 << 10,
		DisableKeepAlives:      true,
	}
	hc := &http.Client{
		Transport:     tr,
		Timeout:       relayProofTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	p := &relayProofs{m: map[string]relayProof{}, slots: make(chan struct{}, relayProofsAtOnce)}
	p.fetch = func(ctx context.Context, url string) ([]byte, error) { return fetchRelayKey(ctx, hc, url) }
	return p
}

// fetchRelayKey gets the registry key a relay URL serves.
func fetchRelayKey(ctx context.Context, hc *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, relayurl.HTTPURL(url), nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, errors.New("no key")
	}
	k, err := identity.Decode(strings.TrimSpace(string(b)))
	if err != nil || !identity.ValidPublic(k) {
		return nil, errors.New("no key")
	}
	return k, nil
}

// errNotProven is the refusal for a heartbeat whose URL doesn't serve its
// key. It says nothing about why (the fetch's error could describe the
// network behind the URL).
func errNotProven() error {
	return forbidden("the relay URL doesn't serve this heartbeat's relay key (a plain GET of the URL must return it; docs/protocol.md §7.3)")
}

// prove checks that url serves key. With wait false (replication) it never
// fetches inline: an unknown URL is refused routinely and checked in the
// background, so a later heartbeat for it is accepted.
func (p *relayProofs) prove(ctx context.Context, url string, key []byte, now time.Time, wait bool) error {
	p.mu.Lock()
	e, ok := p.m[url]
	p.mu.Unlock()
	if ok && now.Sub(e.at) < relayProofTTL {
		if e.key != nil && bytes.Equal(e.key, key) {
			return nil
		}
		if now.Sub(e.at) < relayProofRetry {
			return errNotProven()
		}
	}
	select {
	case p.slots <- struct{}{}:
	default:
		return limited("too many relay checks under way; try again shortly")
	}
	if !wait {
		go func() {
			defer func() { <-p.slots }()
			ctx, cancel := context.WithTimeout(context.Background(), relayProofTimeout)
			defer cancel()
			p.record(url, now, p.fetchKey(ctx, url))
		}()
		return errNotProven()
	}
	served := p.fetchKey(ctx, url)
	<-p.slots
	p.record(url, now, served)
	if served == nil || !bytes.Equal(served, key) {
		return errNotProven()
	}
	return nil
}

func (p *relayProofs) fetchKey(ctx context.Context, url string) []byte {
	k, err := p.fetch(ctx, url)
	if err != nil {
		return nil
	}
	return k
}

// record keeps what url served at now, within relayProofMax entries
// (expired ones go first, then any).
func (p *relayProofs) record(url string, now time.Time, key []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.m[url]; !ok && len(p.m) >= relayProofMax {
		for u, e := range p.m {
			if now.Sub(e.at) >= relayProofTTL {
				delete(p.m, u)
			}
		}
		for u := range p.m {
			if len(p.m) < relayProofMax {
				break
			}
			delete(p.m, u)
		}
	}
	p.m[url] = relayProof{key: key, at: now}
}

// SetRelayProof replaces how the registry proves that a relay URL serves a
// heartbeat's key (tests whose relay URLs point nowhere); nil restores the
// fetch.
func (s *Server) SetRelayProof(f func(ctx context.Context, url string, key []byte) error) {
	s.relayProofHook = f
}

// proveRelay is the registry's proof-of-control check for a heartbeat (nil
// error: url may be listed for key).
func (s *Server) proveRelay(ctx context.Context, url string, key []byte, wait bool) error {
	if !s.cfg.AllowPrivateRelays {
		if err := relayurl.CheckPublic(url); err != nil {
			return bad(err.Error())
		}
	}
	if !s.cfg.RelayProof {
		return nil
	}
	if f := s.relayProofHook; f != nil {
		return f(ctx, url, key)
	}
	return s.relayProofs.prove(ctx, url, key, s.now(), wait)
}
