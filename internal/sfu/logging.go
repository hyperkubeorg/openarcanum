package sfu

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/pion/logging"

	"github.com/hyperkubeorg/openarcanum/internal/httpx"
)

// pionLogs sends the WebRTC stack's logs to the relay's logger: errors and
// warnings at debug level (they're routine: closed sockets, peers that went
// away), without the peer addresses ICE puts in them, the rest dropped.
type pionLogs struct{ log *slog.Logger }

func (f pionLogs) NewLogger(scope string) logging.LeveledLogger {
	return pionLogger{f.log.With("pion", scope)}
}

type pionLogger struct{ log *slog.Logger }

func (l pionLogger) debug(msg string) {
	l.log.Log(context.Background(), slog.LevelDebug, httpx.RedactAddrs(msg))
}

func (pionLogger) Trace(string)                {}
func (pionLogger) Tracef(string, ...any)       {}
func (pionLogger) Debug(string)                {}
func (pionLogger) Debugf(string, ...any)       {}
func (pionLogger) Info(string)                 {}
func (pionLogger) Infof(string, ...any)        {}
func (l pionLogger) Warn(m string)             { l.debug(m) }
func (l pionLogger) Warnf(f string, a ...any)  { l.debug(fmt.Sprintf(f, a...)) }
func (l pionLogger) Error(m string)            { l.debug(m) }
func (l pionLogger) Errorf(f string, a ...any) { l.debug(fmt.Sprintf(f, a...)) }
