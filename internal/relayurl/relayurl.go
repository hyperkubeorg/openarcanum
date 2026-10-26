// Package relayurl puts relay URLs in one canonical form, so URLs that
// reach the same relay compare equal wherever they are accepted, stored or
// compared (the relay registry, admin decisions, nameplates, a relay's own
// public_url; docs/protocol.md §7.3).
//
// The canonical form is scheme "ws" or "wss" in lower case, the host in
// lower case without a trailing dot (IP addresses in their standard text
// form, IPv6 in brackets), the port only when it isn't the scheme's default
// (80 for ws, 443 for wss), and the path cleaned (no empty, "." or ".."
// segments, no trailing slash; an empty path for the root). URLs with user
// information, a query or a fragment aren't relay URLs and are refused.
package relayurl

import (
	"errors"
	"net/netip"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// MaxLen bounds a relay URL.
const MaxLen = 256

// Canonical returns raw's canonical form, or an error if raw isn't a usable
// relay URL.
func Canonical(raw string) (string, error) {
	if len(raw) > MaxLen {
		return "", errors.New("relay URL is too long")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("relay URL doesn't parse")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "ws" && scheme != "wss" {
		return "", errors.New("relay URL must be ws:// or wss://")
	}
	switch {
	case u.Opaque != "":
		return "", errors.New("relay URL must be ws://host/path or wss://host/path")
	case u.User != nil:
		return "", errors.New("relay URL may not carry a user name or password")
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#"):
		return "", errors.New("relay URL may not have a query or fragment")
	case strings.Contains(u.EscapedPath(), "%") || strings.ContainsFunc(u.Path, badPathRune):
		return "", errors.New("relay URL path may use only letters, digits and -._~/!$&'()*+,;=:@")
	}
	host, err := canonicalHost(u.Hostname())
	if err != nil {
		return "", err
	}
	port := u.Port()
	if port == "" && strings.HasSuffix(u.Host, ":") {
		return "", errors.New("relay URL has an empty port")
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", errors.New("relay URL port must be 1-65535")
		}
		if (scheme == "ws" && n == 80) || (scheme == "wss" && n == 443) {
			port = ""
		} else {
			port = strconv.Itoa(n)
		}
	}
	hostport := host
	if strings.Contains(host, ":") {
		hostport = "[" + host + "]"
	}
	if port != "" {
		hostport += ":" + port
	}
	p := u.Path
	if p != "" {
		p = path.Clean("/" + p)
		if p == "/" {
			p = ""
		}
	}
	return scheme + "://" + hostport + p, nil
}

func badPathRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return false
	}
	return !strings.ContainsRune("-._~/!$&'()*+,;=:@", r)
}

func canonicalHost(h string) (string, error) {
	if h == "" {
		return "", errors.New("relay URL has no host")
	}
	if a, err := netip.ParseAddr(h); err == nil {
		if a.Zone() != "" {
			return "", errors.New("relay URL may not use an IPv6 zone")
		}
		return a.Unmap().String(), nil
	}
	h = strings.TrimSuffix(strings.ToLower(h), ".")
	if h == "" || len(h) > 253 {
		return "", errors.New("relay URL host is not a valid name")
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("relay URL host is not a valid name")
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
				return "", errors.New("relay URL host must be an ASCII name (use the xn-- form) or an IP address")
			}
		}
	}
	return h, nil
}

// Host is the host of a canonical relay URL, without port or path: an admin
// removal applies to every relay URL on that host.
func Host(canonical string) string {
	u, err := url.Parse(canonical)
	if err != nil {
		return canonical
	}
	return u.Hostname()
}
