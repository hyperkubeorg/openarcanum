package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/coder/websocket"
)

// What a closed connection logs about its error says what kind it was,
// never the peer's address (docs/operations.md "Logs").
func TestErrKindNamesNoAddress(t *testing.T) {
	peer := &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 51234}
	local := &net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 7000}
	reset := &net.OpError{Op: "write", Net: "tcp", Source: local, Addr: peer, Err: &os.SyscallError{Syscall: "write", Err: syscall.ECONNRESET}}
	for err, want := range map[error]string{
		nil: "",
		fmt.Errorf("failed to write frame: %w", reset):                                          "write: connection reset by peer",
		&net.OpError{Op: "read", Net: "tcp", Addr: peer, Err: errors.New("x " + peer.String())}: "read: *errors.errorString",
		fmt.Errorf("failed to read: %w", io.EOF):                                                "EOF",
		fmt.Errorf("ping: %w", context.DeadlineExceeded):                                        "context deadline exceeded",
		fmt.Errorf("closed: %w", websocket.CloseError{Code: websocket.StatusGoingAway}):         "StatusGoingAway",
		errors.New("peer " + peer.String()):                                                     "*errors.errorString",
	} {
		got := errKind(err)
		if got != want {
			t.Errorf("errKind(%v) = %q, want %q", err, got, want)
		}
		if strings.Contains(got, "203.0.113.7") || strings.Contains(got, "2001:db8::1") {
			t.Errorf("errKind(%v) names an address: %q", err, got)
		}
	}
}
