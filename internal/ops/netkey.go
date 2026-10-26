package ops

import (
	"net"
	"net/http"
)

// NetworkKey groups client addresses the way one subscriber gets them, for
// per-client limits: an IPv4 address (IPv4-mapped IPv6 included), or the /64
// of an IPv6 address. One host usually owns a whole /64, so per-address keys
// would be free to multiply. Anything that isn't an IP address is its own
// key.
func NetworkKey(ip string) string {
	a := net.ParseIP(ip)
	if a == nil {
		return ip
	}
	if v4 := a.To4(); v4 != nil {
		return v4.String()
	}
	return a.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// RemoteIP is the address of the TCP peer that sent r.
func RemoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
