// Package version is protocol version negotiation, shared by every protocol
// (docs/protocol.md §1 "Versions"): the relay WebSocket (Hello/Start), Noise
// sessions with community servers (ClientHello/ServerIdentity) and the
// maintainer HTTP API (the OpenArcanum-Protocol header).
//
// Each side announces every version it speaks; the highest one both speak is
// used. A build speaks its current version and the one before it, so a node
// one release ahead still serves clients one release behind. With nothing in
// common the side that notices says which peer is out of date.
package version

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Current is the protocol version this build implements. Version 1 is the
// first; there is no version before it, so this build speaks only 1.
const Current uint32 = 1

// Header carries version lists and choices on the maintainer HTTP API.
const Header = "OpenArcanum-Protocol"

// MaxListed bounds how many versions a peer may announce (a longer list is
// refused, so parsing peer input stays cheap).
const MaxListed = 16

// SupportedFor is the policy: the current version and the previous one.
// Version 0 never existed and is never supported.
func SupportedFor(current uint32) []uint32 {
	if current <= 1 {
		return []uint32{1}
	}
	return []uint32{current - 1, current}
}

// Supported is what this build speaks, oldest first.
func Supported() []uint32 { return SupportedFor(Current) }

// MismatchError means the two sides have no version in common.
type MismatchError struct {
	Ours, Theirs []uint32
	// PeerOutdated: every version the peer speaks is older than ours, so the
	// peer must be updated. Otherwise this side is the outdated one.
	PeerOutdated bool
}

func (e *MismatchError) Error() string {
	if e.PeerOutdated {
		return fmt.Sprintf("the other side is outdated (it speaks protocol %s; this build needs %s)", Format(e.Theirs), Format(e.Ours))
	}
	return fmt.Sprintf("this build is outdated (it speaks protocol %s; the other side needs %s); update OpenArcanum", Format(e.Ours), Format(e.Theirs))
}

// Negotiate picks the highest version in both lists. Zero entries in theirs
// are ignored (0 is "unset" on the wire); an empty or over-long list is a
// mismatch where the peer is outdated.
func Negotiate(ours, theirs []uint32) (uint32, error) {
	var best uint32
	clean := make([]uint32, 0, len(theirs))
	if len(theirs) <= MaxListed {
		for _, v := range theirs {
			if v != 0 {
				clean = append(clean, v)
			}
		}
	}
	for _, v := range clean {
		if v > best && slices.Contains(ours, v) {
			best = v
		}
	}
	if best != 0 {
		return best, nil
	}
	return 0, &MismatchError{Ours: ours, Theirs: clean, PeerOutdated: len(clean) == 0 || slices.Max(clean) < slices.Min(ours)}
}

// Format is a version list for headers and messages: "1,2".
func Format(vs []uint32) string {
	parts := make([]string, len(vs))
	for i, v := range vs {
		parts[i] = strconv.FormatUint(uint64(v), 10)
	}
	return strings.Join(parts, ",")
}

// Parse reads Format's output. Anything malformed yields an empty list.
func Parse(s string) []uint32 {
	var out []uint32
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := strconv.ParseUint(p, 10, 32)
		if err != nil || len(out) == MaxListed {
			return nil
		}
		out = append(out, uint32(v))
	}
	return out
}

// IsAPIPath says whether an HTTP path is part of the versioned maintainer API
// (health checks and metrics aren't).
func IsAPIPath(p string) bool { return strings.HasPrefix(p, "/v1/") }

// Choose reads a request's Header and negotiates against supported.
func Choose(supported []uint32, header string) (uint32, error) {
	return Negotiate(supported, Parse(header))
}
