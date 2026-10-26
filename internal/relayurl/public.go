package relayurl

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"syscall"
	"time"
)

// Relay URLs that nodes accept from others (the maintainer's relay
// registry) and then dial or hand to community servers that dial them must
// point at the public internet: a relay URL on 127.0.0.1, 10.0.0.0/8 or a
// name like "redis" or "metadata.internal" would make those nodes reach
// into their own networks for whoever posted it (blind SSRF). Development
// setups (scripts/dev-up.sh, kind, the tests) run everything on 127.0.0.1
// and turn this off with a setting (allow_private_relays on maintainer
// nodes).

// ErrNotPublic is returned for a relay URL or address that isn't on the
// public internet.
var ErrNotPublic = errors.New("relay URL must be a public address or a public DNS name")

// CheckPublic refuses a canonical relay URL whose host is a non-public IP
// address or a name that can only be local: localhost and its subdomains,
// the .local, .internal, .lan and .home.arpa suffixes, names of one label
// (resolved through search domains, such as Kubernetes service names) and
// names whose last label is numeric (which some resolvers read as an
// address, such as 2130706433 or 0x7f.1). A DNS name can still resolve to a
// private address; nodes that dial such URLs check the address they connect
// to with DialControl.
func CheckPublic(canonical string) error {
	h := Host(canonical)
	if a, err := netip.ParseAddr(h); err == nil {
		if !PublicAddr(a) {
			return ErrNotPublic
		}
		return nil
	}
	labels := strings.Split(h, ".")
	last := labels[len(labels)-1]
	if len(labels) < 2 || strings.Trim(last, "0123456789") == "" || strings.HasPrefix(last, "0x") {
		return ErrNotPublic
	}
	for _, s := range []string{"localhost", "local", "internal", "lan", "home.arpa", "localdomain"} {
		if h == s || strings.HasSuffix(h, "."+s) {
			return ErrNotPublic
		}
	}
	return nil
}

// Address ranges that aren't the public internet beyond what netip's
// predicates cover: shared address space (CGNAT), "this network", IETF
// protocol assignments, benchmarking, reserved, and NAT64 (which can embed
// any IPv4 address, private ones included).
var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("100::/64"),
}

// PublicAddr reports whether a is a public unicast address: not loopback,
// private, link-local, multicast, unspecified or one of the special ranges
// above (IPv4-mapped IPv6 addresses are judged as IPv4).
func PublicAddr(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() || a.IsUnspecified() || a.Zone() != "" {
		return false
	}
	for _, p := range nonPublic {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// DialControl is a net.Dialer Control function that refuses connections to
// addresses that aren't public (after DNS resolution, so a public name
// that resolves to 127.0.0.1 is refused too).
func DialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	a, err := netip.ParseAddr(host)
	if err != nil || !PublicAddr(a) {
		return fmt.Errorf("%w (dialing a non-public address)", ErrNotPublic)
	}
	return nil
}

// Dialer is a dialer for relay URLs taken from others: with allowPrivate
// false it connects to public addresses only (DialControl).
func Dialer(allowPrivate bool, timeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: timeout}
	if !allowPrivate {
		d.Control = DialControl
	}
	return d.DialContext
}

// HTTPURL is the http(s) URL of the same place as a canonical relay URL
// (ws -> http, wss -> https), where a relay answers a plain GET with its
// registry key (docs/protocol.md §7.3).
func HTTPURL(canonical string) string {
	switch {
	case strings.HasPrefix(canonical, "wss://"):
		return "https://" + strings.TrimPrefix(canonical, "wss://")
	case strings.HasPrefix(canonical, "ws://"):
		return "http://" + strings.TrimPrefix(canonical, "ws://")
	}
	return canonical
}
