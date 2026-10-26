package version

import (
	"runtime"
	"runtime/debug"
)

// Release is this build's release version: VERSION at the repository root
// (calendar versioning, YYYY.MM.PATCH with an optional -beta.N; docs/release.md
// "Versions"), set at link time by the Makefile and the Containerfile:
//
//	go build -ldflags "-X <module>/internal/version.Release=2026.10.0" ./cmd/...
//
// A plain `go build` leaves "dev". The release version says which build
// this is; the protocol version (Current) says what it can talk to.
var Release = "dev"

// Revision is the git commit the build came from, set at link time like
// Release. When it is empty, the revision Go recorded from the checkout
// (-buildvcs) is used, if any.
var Revision = ""

// BuildRevision is Revision, or the VCS revision in the binary's build info
// (first 12 hex digits, "+dirty" for a modified tree), or "".
func BuildRevision() string {
	if Revision != "" {
		return Revision
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	var rev, dirty string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "+dirty"
			}
		}
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if rev == "" {
		return ""
	}
	return rev + dirty
}

// String describes the build for `<command> version` and the startup log:
// "relay 2026.10.0 (revision 0123abcd4567, protocol 1, go1.27.3 linux/amd64)".
func String(command string) string {
	s := command + " " + Release + " ("
	if rev := BuildRevision(); rev != "" {
		s += "revision " + rev + ", "
	}
	return s + "protocol " + Format(Supported()) + ", " + runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH + ")"
}
