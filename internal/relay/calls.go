package relay

import (
	"context"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/callauth"
	"github.com/hyperkubeorg/openarcanum/internal/sfu"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// maxCallsPerConn: a client is in one call at a time; two covers switching.
const maxCallsPerConn = 2

// SetSFU turns on calls through this relay (docs/protocol.md §12).
func (s *Server) SetSFU(f *sfu.SFU) { s.sfu = f }

// callSignaler sends the SFU's messages for one call on the client's
// connection.
type callSignaler struct {
	c  *conn
	id []byte
}

func (cs callSignaler) CallSignal(m *wire.CallSignal) {
	m.CallId = cs.id
	cs.c.sendFrame(&wire.RelayFrame{Kind: &wire.RelayFrame_CallSignal{CallSignal: m}})
}

func (cs callSignaler) CallState(m *wire.CallState) {
	m.CallId = cs.id
	cs.c.sendFrame(&wire.RelayFrame{Kind: &wire.RelayFrame_CallState{CallState: m}})
	if m.Ended != "" {
		cs.c.smu.Lock()
		delete(cs.c.calls, string(cs.id))
		cs.c.smu.Unlock()
	}
}

// CallWatchers goes only to the participant it is about, on the connection
// that joined with its grant.
func (cs callSignaler) CallWatchers(m *wire.CallWatchers) {
	m.CallId = cs.id
	cs.c.sendFrame(&wire.RelayFrame{Kind: &wire.RelayFrame_CallWatchers{CallWatchers: m}})
}

func (c *conn) callJoin(m *wire.CallJoin) {
	if c.srv.sfu == nil {
		c.sendError(m.Req, wire.ErrorCode_ERROR_CODE_NO_CALLS, "this relay doesn't carry calls")
		return
	}
	if len(m.Offer) > 64<<10 || len(m.Kinds) > 8 {
		c.sendError(m.Req, wire.ErrorCode_ERROR_CODE_BAD_FRAME, "offer too large")
		return
	}
	if !c.srv.callAllowed(m.GetGrant()) {
		c.sendError(m.Req, wire.ErrorCode_ERROR_CODE_CALL_REFUSED, notDedicatedMsg)
		return
	}
	c.smu.Lock()
	full := len(c.calls)+c.joining >= maxCallsPerConn
	if !full {
		c.joining++
	}
	c.smu.Unlock()
	if full {
		c.sendError(m.Req, wire.ErrorCode_ERROR_CODE_LIMIT, "already in a call on this connection")
		return
	}
	// Answering waits for the relay's ICE candidates; don't hold up the
	// connection's other frames.
	go func() {
		ctx, cancel := context.WithTimeout(c.ctx, 15*time.Second)
		defer cancel()
		var callID []byte
		if g, err := callauth.VerifyGrant(m.GetGrant(), time.Now()); err == nil {
			callID = g.CallId
		}
		peer, answer, err := c.srv.sfu.Join(ctx, m, callSignaler{c, callID}, c.ip)
		c.smu.Lock()
		c.joining--
		if err == nil {
			if c.closing {
				c.smu.Unlock()
				peer.Leave()
				return
			}
			if old := c.calls[string(callID)]; old != nil {
				defer old.Leave()
			}
			c.calls[string(callID)] = peer
		}
		c.smu.Unlock()
		if err != nil {
			c.sendError(m.Req, wire.ErrorCode_ERROR_CODE_CALL_REFUSED, err.Error())
			return
		}
		c.sendFrame(&wire.RelayFrame{Kind: &wire.RelayFrame_CallAnswer{CallAnswer: &wire.CallAnswer{CallId: callID, Answer: answer, Req: m.Req}}})
	}()
}

func (c *conn) callPeer(id []byte) *sfu.Peer {
	c.smu.Lock()
	defer c.smu.Unlock()
	return c.calls[string(id)]
}

func (c *conn) callSignal(m *wire.CallSignal) {
	p := c.callPeer(m.CallId)
	if p == nil {
		c.sendError(0, wire.ErrorCode_ERROR_CODE_CALL_REFUSED, "not in that call")
		return
	}
	if len(m.Sdp) > 64<<10 {
		c.sendError(0, wire.ErrorCode_ERROR_CODE_BAD_FRAME, "description too large")
		return
	}
	if err := p.Signal(m); err != nil {
		c.sendError(0, wire.ErrorCode_ERROR_CODE_BAD_FRAME, err.Error())
	}
}

func (c *conn) callLayer(m *wire.CallLayer) {
	p := c.callPeer(m.CallId)
	if p == nil {
		c.sendError(0, wire.ErrorCode_ERROR_CODE_CALL_REFUSED, "not in that call")
		return
	}
	if err := p.Layer(m); err != nil {
		c.sendError(0, wire.ErrorCode_ERROR_CODE_BAD_FRAME, err.Error())
	}
}

// callListen: what audio this connection's participant wants forwarded.
func (c *conn) callListen(m *wire.CallListen) {
	p := c.callPeer(m.CallId)
	if p == nil {
		c.sendError(0, wire.ErrorCode_ERROR_CODE_CALL_REFUSED, "not in that call")
		return
	}
	if err := p.Listen(m); err != nil {
		c.sendError(0, wire.ErrorCode_ERROR_CODE_BAD_FRAME, err.Error())
	}
}

func (c *conn) callLeave(m *wire.CallLeave) {
	c.smu.Lock()
	p := c.calls[string(m.CallId)]
	delete(c.calls, string(m.CallId))
	c.smu.Unlock()
	if p != nil {
		p.Leave()
	}
}

// callUpdate: a moderator control from the community server. Any connection
// may carry one; the SFU checks it was signed by the key that runs the call.
func (c *conn) callUpdate(m *wire.CallUpdate) {
	if c.srv.sfu == nil {
		c.sendError(0, wire.ErrorCode_ERROR_CODE_NO_CALLS, "this relay doesn't carry calls")
		return
	}
	if err := c.srv.sfu.Update(m.Control); err != nil {
		c.sendError(0, wire.ErrorCode_ERROR_CODE_CALL_REFUSED, err.Error())
	}
}

// leaveCalls ends this connection's calls when it closes.
func (c *conn) leaveCalls() {
	c.smu.Lock()
	c.closing = true
	peers := make([]*sfu.Peer, 0, len(c.calls))
	for _, p := range c.calls {
		peers = append(peers, p)
	}
	c.calls = map[string]*sfu.Peer{}
	c.smu.Unlock()
	for _, p := range peers {
		p.Leave()
	}
}
