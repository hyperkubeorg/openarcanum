package sfu

import (
	"log/slog"

	"github.com/pion/logging"
)

// VoiceFormat is voiceFormat, for the tests in package sfu_test.
var VoiceFormat = voiceFormat

// NewPionLogs is pionLogs, for the tests in package sfu_test.
func NewPionLogs(l *slog.Logger) logging.LoggerFactory { return pionLogs{l} }

// MaxRemoved and MaxRemembered are the removal bounds, for the tests in
// package sfu_test.
const (
	MaxRemoved    = maxRemoved
	MaxRemembered = maxRemembered
)

// Removals reports how many removals the relay remembers in all (its
// queue included) and for call id (tests).
func (s *SFU) Removals(id []byte) (all, queued, inCall int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, queued = len(s.removed), len(s.removedQueue)
	if c := s.calls[string(id)]; c != nil {
		c.mu.Lock()
		inCall = max(len(c.removed), len(c.removedOrder))
		c.mu.Unlock()
	}
	return all, queued, inCall
}
