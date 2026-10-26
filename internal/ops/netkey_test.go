package ops

import "testing"

func TestNetworkKeyGroupsIPv6Slash64(t *testing.T) {
	for in, want := range map[string]string{
		"192.0.2.7":                "192.0.2.7",
		"::ffff:192.0.2.7":         "192.0.2.7",
		"2001:db8:1:2:aaaa::1":     "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff:ffff::": "2001:db8:1:2::/64",
		"2001:db8:1:3::1":          "2001:db8:1:3::/64",
		"not an ip":                "not an ip",
	} {
		if got := NetworkKey(in); got != want {
			t.Errorf("NetworkKey(%q) = %q, want %q", in, got, want)
		}
	}
}
