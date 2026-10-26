// Package clusterset verifies, fetches and caches signed maintainer sets
// (docs/protocol.md §7.5): the list of nodes of one maintainer cluster,
// signed by the cluster's operator key. Maintainer nodes use it to know
// whom to trust; community servers and relays use it to reach every member
// of the clusters they publish to.
package clusterset

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/version"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

const (
	// MaxNodes bounds a set.
	MaxNodes = 8
	// MaxAhead is how far a set's issued_at may be ahead of the verifier's
	// clock.
	MaxAhead = 24 * time.Hour
	// MaxLifetime bounds expires_at - issued_at.
	MaxLifetime = 2 * 365 * 24 * time.Hour
	// DefaultLifetime is what `meta operator sign-set` uses by default.
	DefaultLifetime = 180 * 24 * time.Hour

	RoleRoot   = "root"
	RoleMember = "member"
)

// ErrNotNewer refuses a set whose seq isn't above the one held.
var ErrNotNewer = errors.New("a maintainer set with this seq or a higher one is already held")

// Verify checks a signed set: signed by operator (when operator isn't nil,
// the set must name it), unexpired at now, issued no more than MaxAhead in
// the future, 1 to MaxNodes members with http(s) URLs, valid and distinct
// node keys and URLs, and roles "root" or "member".
func Verify(s *wire.SignedMaintainerSet, operator []byte, now time.Time) (*wire.MaintainerSet, error) {
	if s == nil {
		return nil, errors.New("no maintainer set")
	}
	var set wire.MaintainerSet
	if err := proto.Unmarshal(s.Set, &set); err != nil {
		return nil, errors.New("malformed maintainer set")
	}
	if !identity.ValidPublic(set.OperatorKey) {
		return nil, errors.New("the maintainer set's operator key is malformed")
	}
	if operator != nil && !bytes.Equal(operator, set.OperatorKey) {
		return nil, errors.New("the maintainer set is for another operator key")
	}
	if err := identity.Verify(set.OperatorKey, identity.CtxMaintainerSet, s.Set, s.Signature); err != nil {
		return nil, errors.New("the maintainer set's signature is invalid")
	}
	issued, expires := time.UnixMilli(set.IssuedAt), time.UnixMilli(set.ExpiresAt)
	switch {
	case issued.After(now.Add(MaxAhead)):
		return nil, errors.New("the maintainer set is issued in the future")
	case !expires.After(now):
		return nil, errors.New("the maintainer set has expired; the operator must sign a new one")
	case !expires.After(issued) || expires.Sub(issued) > MaxLifetime:
		return nil, errors.New("the maintainer set's lifetime is out of range")
	case len(set.Nodes) == 0 || len(set.Nodes) > MaxNodes:
		return nil, fmt.Errorf("a maintainer set names 1 to %d nodes", MaxNodes)
	}
	keys, urls := map[string]bool{}, map[string]bool{}
	for _, n := range set.Nodes {
		u, err := CleanURL(n.Url)
		if err != nil || u != n.Url {
			return nil, fmt.Errorf("maintainer set: node URL %q must be an http(s) URL without a trailing slash", n.Url)
		}
		if !identity.ValidPublic(n.NodeKey) || keys[string(n.NodeKey)] || urls[n.Url] {
			return nil, errors.New("maintainer set: node keys and URLs must be valid and distinct")
		}
		if n.Role != RoleRoot && n.Role != RoleMember {
			return nil, fmt.Errorf("maintainer set: role %q is neither root nor member", n.Role)
		}
		if len(n.Region) > 32 {
			return nil, errors.New("maintainer set: region too long")
		}
		keys[string(n.NodeKey)], urls[n.Url] = true, true
	}
	return &set, nil
}

// CleanURL checks a maintainer node's base URL (http or https) and drops a
// trailing slash.
func CleanURL(u string) (string, error) {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") || len(u) <= len("https://") || strings.ContainsAny(u, "# \t\n") {
		return "", fmt.Errorf("%q isn't an http(s) URL", u)
	}
	return u, nil
}

// Sign makes a signed set (the operator's tool, `meta operator sign-set`).
func Sign(op *identity.Key, set *wire.MaintainerSet) (*wire.SignedMaintainerSet, error) {
	set.OperatorKey = op.Public
	b, err := proto.Marshal(set)
	if err != nil {
		return nil, err
	}
	return &wire.SignedMaintainerSet{Set: b, Signature: op.Sign(identity.CtxMaintainerSet, b)}, nil
}

// ParseEntry reads a configured maintainer entry: "URL" or
// "URL#OPERATORKEY" (the cluster's operator key, base64url).
func ParseEntry(e string) (url string, operator []byte, err error) {
	u, k, hasKey := strings.Cut(strings.TrimSpace(e), "#")
	if url, err = CleanURL(u); err != nil {
		return "", nil, err
	}
	if hasKey {
		operator, err = identity.Decode(k)
		if err != nil || !identity.ValidPublic(operator) {
			return "", nil, fmt.Errorf("%q: the operator key after # isn't a base64url Ed25519 key", e)
		}
	}
	return url, operator, nil
}

// Fetch gets GET /v1/maintainers from a node and verifies it for operator.
func Fetch(ctx context.Context, hc *http.Client, url string, operator []byte, now time.Time) (*wire.MaintainerSet, *wire.SignedMaintainerSet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(url, "/")+"/v1/maintainers?operator="+identity.Encode(operator), nil)
	if err != nil {
		return nil, nil, err
	}
	version.SetRequest(req)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, nil, err
	}
	if err := version.CheckResponse(resp); err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("%s has no maintainer set (%s)", url, resp.Status)
	}
	var signed wire.SignedMaintainerSet
	if err := proto.Unmarshal(b, &signed); err != nil {
		return nil, nil, errors.New("malformed maintainer set")
	}
	set, err := Verify(&signed, operator, now)
	if err != nil {
		return nil, nil, err
	}
	return set, &signed, nil
}

// Cache keeps the newest verified set per operator key, refreshed from the
// clusters' own members: community servers and relays expand their
// configured maintainer entries with it.
type Cache struct {
	HTTP    *http.Client
	Now     func() time.Time
	Refresh time.Duration

	mu   sync.Mutex
	sets map[string]*cached
}

type cached struct {
	set     *wire.MaintainerSet
	fetched time.Time
	tried   time.Time
}

// NewCache makes a cache refreshing every refresh.
func NewCache(hc *http.Client, refresh time.Duration) *Cache {
	return &Cache{HTTP: hc, Now: time.Now, Refresh: refresh, sets: map[string]*cached{}}
}

// Expand turns configured entries (URL or URL#OPERATORKEY) into the URLs to
// use: each cluster's members in set order (its own URL first when the set
// isn't known yet), then plain URLs, without duplicates. Sets older than
// Refresh are fetched again from the cluster's members (failures keep the
// cached set until it expires).
func (c *Cache) Expand(ctx context.Context, entries []string, extraOperator []byte) []string {
	var out []string
	seen := map[string]bool{}
	add := func(u string) {
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	for _, e := range entries {
		url, op, err := ParseEntry(e)
		if err != nil {
			continue
		}
		if op == nil {
			op = extraOperator
		}
		if op == nil {
			add(url)
			continue
		}
		set := c.get(ctx, op, url)
		if set == nil {
			add(url)
			continue
		}
		for _, n := range set.Nodes {
			add(n.Url)
		}
		add(url)
	}
	return out
}

// Set is the cached set for operator (nil if none is known).
func (c *Cache) Set(operator []byte) *wire.MaintainerSet {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.sets[string(operator)]; e != nil && time.UnixMilli(e.set.ExpiresAt).After(c.Now()) {
		return e.set
	}
	return nil
}

func (c *Cache) get(ctx context.Context, operator []byte, via string) *wire.MaintainerSet {
	now := c.Now()
	c.mu.Lock()
	e := c.sets[string(operator)]
	stale := e == nil || now.Sub(e.fetched) >= c.Refresh
	recent := e != nil && now.Sub(e.tried) < time.Minute
	c.mu.Unlock()
	if stale && !recent {
		urls := []string{via}
		if e != nil {
			for _, n := range e.set.Nodes {
				if n.Url != via {
					urls = append(urls, n.Url)
				}
			}
		}
		for _, u := range urls {
			fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			set, _, err := Fetch(fctx, c.HTTP, u, operator, now)
			cancel()
			if err != nil {
				continue
			}
			c.mu.Lock()
			if cur := c.sets[string(operator)]; cur == nil || set.Seq >= cur.set.Seq {
				c.sets[string(operator)] = &cached{set: set, fetched: now, tried: now}
			}
			c.mu.Unlock()
			break
		}
		c.mu.Lock()
		if cur := c.sets[string(operator)]; cur != nil {
			cur.tried = now
		}
		c.mu.Unlock()
	}
	return c.Set(operator)
}
