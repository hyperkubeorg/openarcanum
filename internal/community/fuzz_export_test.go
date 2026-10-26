package community

import (
	"context"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// DetachedSession is a session for one member and device with no transport
// behind it, so fuzz tests can drive the request dispatcher directly instead
// of through a Noise handshake per input. Admission and request handling are
// the production code paths; only the transport is missing.
type DetachedSession struct{ ss *sess }

// NewDetachedSession admits a session the way serveStream does.
func (s *Server) NewDetachedSession(ctx context.Context, member, device []byte, name string) *DetachedSession {
	sctx, cancel := context.WithCancel(ctx)
	ss := &sess{srv: s, member: member, device: device, name: truncate(name, 64), out: make(chan []byte, 1024), ctx: sctx, cancel: cancel}
	ss.setStatus(s.admitSession(ctx, ss))
	return &DetachedSession{ss: ss}
}

// Status is the session's current member status.
func (d *DetachedSession) Status() wire.MemberStatus { return d.ss.getStatus() }

// Handle decodes and answers one raw request exactly as serveStream's loop
// does, then discards whatever the server pushed to this session meanwhile.
func (d *DetachedSession) Handle(ctx context.Context, raw []byte) *wire.ServerMessage {
	defer d.drain()
	var m wire.ClientMessage
	if err := proto.Unmarshal(raw, &m); err != nil || m.Id == 0 {
		return &wire.ServerMessage{Body: errBody(errInvalid("malformed request"))}
	}
	reply := d.ss.srv.dispatch(ctx, d.ss, &m)
	reply.ReplyTo = m.Id
	return reply
}

func (d *DetachedSession) drain() {
	for {
		select {
		case <-d.ss.out:
		default:
			return
		}
	}
}

// Close ends the session the way serveStream's deferred cleanup does.
func (d *DetachedSession) Close() {
	s := d.ss.srv
	s.smu.Lock()
	delete(s.sessions, d.ss)
	s.smu.Unlock()
	s.voiceDisconnect(d.ss)
	d.ss.close("session ended")
}
