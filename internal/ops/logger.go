package ops

import (
	"io"
	"log/slog"
	"os"
)

// NewLogger builds a node's logger from its settings: text or JSON records
// at info or debug level (verbose forces debug), to stderr or to a rotated
// LogFile. close releases the file; call it when the node exits.
//
// What may be logged is a rule, not a setting: never message content,
// names, keys, join keys, tokens, passphrases or search queries; members,
// devices and servers only as fingerprints (docs/operations.md "Logs").
func NewLogger(s Settings, verbose bool, stderr io.Writer) (log *slog.Logger, closeFn func() error, err error) {
	if stderr == nil {
		stderr = os.Stderr
	}
	level := slog.LevelInfo
	if verbose || s.LogLevel == "debug" {
		level = slog.LevelDebug
	}
	out := stderr
	closeFn = func() error { return nil }
	if s.LogFile != "" {
		f, err := OpenRotating(s.LogFile, s.LogMaxSize, s.LogMaxAge.Duration, s.LogKeep, stderr)
		if err != nil {
			return nil, nil, err
		}
		out, closeFn = f, f.Close
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if s.LogFormat == "json" {
		h = slog.NewJSONHandler(out, opts)
	} else {
		h = slog.NewTextHandler(out, opts)
	}
	return slog.New(h), closeFn, nil
}
