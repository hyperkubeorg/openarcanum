package version

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// Middleware negotiates the maintainer API version on every /v1/ request.
// A request without a version in common is refused with 426 Upgrade
// Required, the server's versions in Header and a sentence saying which side
// is outdated; otherwise the chosen version is echoed in Header.
func Middleware(supported []uint32, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsAPIPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		offered := r.Header.Get(Header)
		// A browser can't set headers on a WebSocket: a watch says its
		// versions in the URL (?v=), as it would in the header.
		if offered == "" && strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			offered = r.URL.Query().Get("v")
		}
		v, err := Choose(supported, offered)
		w.Header().Set(Header, Format(supported))
		if err != nil {
			var me *MismatchError
			msg := "this maintainer node speaks protocol " + Format(supported)
			if errors.As(err, &me) && me.PeerOutdated {
				msg += "; update OpenArcanum"
			} else {
				msg += "; this maintainer node is outdated"
			}
			http.Error(w, msg, http.StatusUpgradeRequired)
			return
		}
		w.Header().Set(Header, strconv.FormatUint(uint64(v), 10))
		next.ServeHTTP(w, r)
	})
}

// SetRequest announces this build's versions on an outgoing API request.
func SetRequest(r *http.Request) { r.Header.Set(Header, Format(Supported())) }

// CheckResponse turns a 426 reply into a MismatchError (seen from this
// side), and refuses a successful reply that chose a version this build
// doesn't speak.
func CheckResponse(resp *http.Response) error {
	theirs := Parse(resp.Header.Get(Header))
	if resp.StatusCode == http.StatusUpgradeRequired {
		_, err := Negotiate(Supported(), theirs)
		if err == nil {
			err = &MismatchError{Ours: Supported(), Theirs: theirs}
		}
		return err
	}
	if resp.StatusCode == http.StatusOK && len(theirs) == 1 {
		if _, err := Negotiate(Supported(), theirs); err != nil {
			return err
		}
	}
	return nil
}

// Transport adds Header to every request it carries.
type Transport struct{ Base http.RoundTripper }

func (t Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	SetRequest(r)
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r)
}
