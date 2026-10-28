// Package sfu is the relay's selective forwarding unit for calls
// (docs/protocol.md §12).
//
// Each participant has one WebRTC peer connection with the relay. The relay
// forwards the media it receives to the other participants without being
// able to read it: every frame is end-to-end encrypted with SFrame by the
// clients. It holds no keys and no state beyond the calls in progress.
//
//   - Admission: a grant signed by the community server's key, for a call id
//     that commits to that key (grant.go).
//   - Audio: one shared outgoing track per source; server mute stops
//     forwarding it, server deafen detaches it from that subscriber.
//   - Video: senders publish simulcast layers; each subscriber has a forwarder
//     per source that follows the layer the receiver asked for (none until it
//     asks: receivers ask only for what their user watches), switching only
//     at a keyframe and rewriting sequence numbers and timestamps so the
//     receiver sees one continuous stream.
//   - Listening: a receiver says which audio it doesn't want (deafened,
//     someone muted for itself) and which screens' audio it does; the rest
//     isn't forwarded to it.
//   - Watchers: each sender is told how many receivers each kind of its
//     media has (and which video layers they want), so it sends nothing
//     nobody takes.
//   - Signaling: clients offer once (with every transceiver they may use);
//     afterwards only the relay offers, when other participants' tracks come
//     and go.
package sfu

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v4"

	"github.com/hyperkubeorg/openarcanum/internal/callauth"
	"github.com/hyperkubeorg/openarcanum/internal/ops"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Config is the relay's media configuration (docs/operations.md).
type Config struct {
	// UDP port for all media (one socket, ICE multiplexed); 0 picks one.
	UDPPort int
	// Address to bind the media socket to; empty is every interface.
	ListenIP string
	// Addresses to advertise to clients (the relay's public IPs behind NAT).
	// Empty advertises the local interface addresses.
	PublicIPs []string
	// Loopback candidates, for tests and single-machine setups.
	IncludeLoopback bool
	MaxCalls        int
	// Participants in all calls on this relay, and from one network (an
	// IPv4 address or IPv6 /64, ops.NetworkKey: anyone can sign grants for
	// a call of their own; these bound what one network can take).
	MaxPeers      int
	MaxPeersPerIP int
	// What one participant may send, in bits per second; more is dropped
	// rather than forwarded to everyone.
	PeerBitrate int
	// When set, only calls run by these community server keys are carried.
	AllowedServers [][]byte
	Log            *slog.Logger
}

// Signaler delivers relay -> client call messages on the client's relay
// connection.
type Signaler interface {
	CallSignal(*wire.CallSignal)
	CallState(*wire.CallState)
	CallWatchers(*wire.CallWatchers)
}

// Kinds of media a participant sends.
const (
	KindAudio       = "audio"
	KindCamera      = "camera"
	KindScreen      = "screen"
	KindScreenAudio = "screen-audio"
)

var validKind = map[string]bool{KindAudio: true, KindCamera: true, KindScreen: true, KindScreenAudio: true}

// SFU runs every call on one relay.
type SFU struct {
	cfg  Config
	api  *webrtc.API
	conn *net.UDPConn
	log  *slog.Logger

	// Media bytes (RTP payloads) taken in from senders and forwarded to
	// receivers, for the relay's metrics: how much the calls carry, never
	// whose.
	mediaIn, mediaOut atomic.Int64

	mu    sync.Mutex
	calls map[string]*call
	// Removed participants (call id + participant id), until their grants
	// can no longer be used: the call itself may end before then. The
	// queue has them oldest first; past maxRemembered the oldest go early.
	removed      map[string]time.Time
	removedQueue []removal
	peers        int
	peersByIP    map[string]int
}

type removal struct {
	key   string
	until time.Time
}

// maxRemembered bounds the removals the relay remembers across its calls.
const maxRemembered = 1 << 16

// New opens the media socket and builds the WebRTC stack.
func New(cfg Config) (*SFU, error) {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.MaxCalls == 0 {
		cfg.MaxCalls = 1000
	}
	if cfg.MaxPeers == 0 {
		cfg.MaxPeers = 5000
	}
	if cfg.MaxPeersPerIP == 0 {
		cfg.MaxPeersPerIP = 20
	}
	if cfg.PeerBitrate == 0 {
		cfg.PeerBitrate = 8_000_000
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP(cfg.ListenIP), Port: cfg.UDPPort})
	if err != nil {
		return nil, fmt.Errorf("media socket: %w", err)
	}
	m := &webrtc.MediaEngine{}
	if err := registerCodecs(m); err != nil {
		conn.Close()
		return nil, err
	}
	reg := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, reg); err != nil {
		conn.Close()
		return nil, err
	}
	se := webrtc.SettingEngine{}
	se.LoggerFactory = pionLogs{cfg.Log}
	se.SetLite(true)
	se.SetICEUDPMux(webrtc.NewICEUDPMux(pionLogs{cfg.Log}.NewLogger("ice"), conn))
	se.SetIncludeLoopbackCandidate(cfg.IncludeLoopback)
	if len(cfg.PublicIPs) > 0 {
		// Host candidates carry the public addresses instead of the local
		// ones (what the deprecated SetNAT1To1IPs with host candidates did).
		if err := se.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{
			External: cfg.PublicIPs, AsCandidateType: webrtc.ICECandidateTypeHost, Mode: webrtc.ICEAddressRewriteReplace,
		}); err != nil {
			conn.Close()
			return nil, fmt.Errorf("public addresses: %w", err)
		}
	}
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6})
	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(reg), webrtc.WithSettingEngine(se))
	return &SFU{cfg: cfg, api: api, conn: conn, log: cfg.Log, calls: map[string]*call{}, removed: map[string]time.Time{}, peersByIP: map[string]int{}}, nil
}

// OpusFmtp is the Opus format the relay negotiates. Constant bitrate and no
// DTX: with Opus's default variable bitrate a packet's size follows what is
// said (published attacks recover phrases and the language from the sizes
// alone), and DTX would mark every pause with tiny packets. The client
// insists on the same whatever a relay says (call.js hardenSdp;
// docs/threat-model.md "Calls").
const OpusFmtp = "minptime=10;useinbandfec=1;cbr=1;usedtx=0"

// voiceFormat sets constant bitrate and no DTX on every Opus format in an
// SDP the relay sends (Pion answers with the offer's format parameters, so
// registering OpusFmtp alone wouldn't say so).
func voiceFormat(s string) string {
	lines := strings.Split(s, "\r\n")
	opus := map[string]bool{} // payload type -> has a format line
	for _, l := range lines {
		if rest, ok := strings.CutPrefix(l, "a=rtpmap:"); ok {
			if pt, codec, ok := strings.Cut(rest, " "); ok && strings.HasPrefix(strings.ToLower(codec), "opus/") {
				opus[pt] = false
			}
		}
	}
	for i, l := range lines {
		rest, ok := strings.CutPrefix(l, "a=fmtp:")
		if !ok {
			continue
		}
		pt, params, _ := strings.Cut(rest, " ")
		if _, ok := opus[pt]; !ok {
			continue
		}
		opus[pt] = true
		var keep []string
		for _, p := range strings.Split(params, ";") {
			p = strings.TrimSpace(p)
			if k, _, _ := strings.Cut(p, "="); k != "" && k != "cbr" && k != "usedtx" {
				keep = append(keep, p)
			}
		}
		lines[i] = "a=fmtp:" + pt + " " + strings.Join(append(keep, "cbr=1", "usedtx=0"), ";")
	}
	out := make([]string, 0, len(lines)+len(opus))
	for _, l := range lines {
		out = append(out, l)
		if rest, ok := strings.CutPrefix(l, "a=rtpmap:"); ok {
			pt, _, _ := strings.Cut(rest, " ")
			if has, isOpus := opus[pt]; isOpus && !has {
				out = append(out, "a=fmtp:"+pt+" cbr=1;usedtx=0")
			}
		}
	}
	return strings.Join(out, "\r\n")
}

// registerCodecs: Opus for audio, VP8 for video (the codecs the client's
// engine offers everywhere), with the header
// extensions simulcast needs. Nothing else: in particular not the RFC 6464
// audio level, which a sender would put in the clear on every packet (who
// speaks, and how loudly); voice activity indicators come from the decrypted
// audio in each receiving client.
func registerCodecs(m *webrtc.MediaEngine) error {
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2, SDPFmtpLine: OpusFmtp},
		PayloadType:        111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return err
	}
	fb := []webrtc.RTCPFeedback{{Type: "goog-remb"}, {Type: "ccm", Parameter: "fir"}, {Type: "nack"}, {Type: "nack", Parameter: "pli"}, {Type: "transport-cc"}}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000, RTCPFeedback: fb},
		PayloadType:        96,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return err
	}
	for _, uri := range []string{sdp.SDESMidURI, sdp.SDESRTPStreamIDURI, sdp.SDESRepairRTPStreamIDURI} {
		if err := m.RegisterHeaderExtension(webrtc.RTPHeaderExtensionCapability{URI: uri}, webrtc.RTPCodecTypeVideo); err != nil {
			return err
		}
	}
	return m.RegisterHeaderExtension(webrtc.RTPHeaderExtensionCapability{URI: sdp.SDESMidURI}, webrtc.RTPCodecTypeAudio)
}

// Addr is the media socket's address.
func (s *SFU) Addr() *net.UDPAddr { return s.conn.LocalAddr().(*net.UDPAddr) }

// Close ends every call and closes the media socket.
func (s *SFU) Close() error {
	s.mu.Lock()
	calls := make([]*call, 0, len(s.calls))
	for _, c := range s.calls {
		calls = append(calls, c)
	}
	s.mu.Unlock()
	for _, c := range calls {
		c.end("the relay is shutting down")
	}
	return s.conn.Close()
}

// Calls is the number of calls in progress (metrics, tests).
func (s *SFU) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// Participants is the number of people in calls on this relay (metrics).
func (s *SFU) Participants() int {
	s.mu.Lock()
	calls := make([]*call, 0, len(s.calls))
	for _, c := range s.calls {
		calls = append(calls, c)
	}
	s.mu.Unlock()
	n := 0
	for _, c := range calls {
		c.mu.Lock()
		n += len(c.peers)
		c.mu.Unlock()
	}
	return n
}

// MediaBytes is how many media payload bytes this relay has taken in and
// forwarded since it started (metrics).
func (s *SFU) MediaBytes() (in, out int64) { return s.mediaIn.Load(), s.mediaOut.Load() }

// Join admits a participant on a grant and answers their offer. ip is the
// participant's address; the per-address limit counts its network (an
// IPv4 address or IPv6 /64), so one host can't take more places by using
// more addresses of its /64.
func (s *SFU) Join(ctx context.Context, j *wire.CallJoin, sig Signaler, ip string) (*Peer, string, error) {
	ip = ops.NetworkKey(ip)
	g, err := callauth.VerifyGrant(j.Grant, time.Now())
	if err != nil {
		return nil, "", err
	}
	if len(s.cfg.AllowedServers) > 0 && !listHas(s.cfg.AllowedServers, g.ServerKey) {
		return nil, "", errors.New("this relay doesn't carry calls for that server")
	}
	seen := map[string]bool{}
	for mid, k := range j.Kinds {
		if !validKind[k] || len(mid) > 16 || seen[k] {
			return nil, "", errors.New("unknown or repeated media kind")
		}
		seen[k] = true
	}
	// The control the participant brought (from its session), checked
	// against the key that runs this call.
	var ctl *wire.CallControl
	if j.Control != nil {
		v, err := callauth.VerifyControl(j.Control, g.ServerKey)
		if err != nil || !bytes.Equal(v.CallId, g.CallId) {
			return nil, "", errors.New("the call's control isn't valid")
		}
		ctl = v
	}
	key := string(g.CallId)
	s.mu.Lock()
	s.sweepRemovedLocked()
	if _, gone := s.removed[key+string(g.Participant)]; gone {
		s.mu.Unlock()
		return nil, "", errors.New("you were removed from this call")
	}
	if s.peers >= s.cfg.MaxPeers || s.peersByIP[ip] >= s.cfg.MaxPeersPerIP {
		s.mu.Unlock()
		return nil, "", errors.New("this relay is carrying as many calls as it can")
	}
	c := s.calls[key]
	if c == nil {
		if len(s.calls) >= s.cfg.MaxCalls {
			s.mu.Unlock()
			return nil, "", errors.New("this relay is carrying as many calls as it can")
		}
		c = newCall(s, g)
		s.calls[key] = c
	}
	c.joining++
	s.peers++
	s.peersByIP[ip]++
	s.mu.Unlock()
	var once sync.Once
	release := func() {
		once.Do(func() {
			s.mu.Lock()
			s.peers--
			if s.peersByIP[ip]--; s.peersByIP[ip] <= 0 {
				delete(s.peersByIP, ip)
			}
			s.mu.Unlock()
		})
	}
	if ctl != nil {
		c.applyControl(ctl)
	}
	p, answer, err := c.join(ctx, g, j, sig, release)
	s.mu.Lock()
	c.joining--
	s.mu.Unlock()
	if err != nil {
		release()
		s.dropIfIdle(c)
		return nil, "", err
	}
	return p, answer, nil
}

// Update applies a moderator control from the community server.
func (s *SFU) Update(sc *wire.SignedCallControl) error {
	// The call id inside tells us which server key must have signed it.
	id, err := callauth.ControlCallID(sc)
	if err != nil {
		return err
	}
	s.mu.Lock()
	c := s.calls[string(id)]
	s.mu.Unlock()
	if c == nil {
		return errors.New("no such call here")
	}
	verified, err := callauth.VerifyControl(sc, c.serverKey)
	if err != nil {
		return err
	}
	c.applyControl(verified)
	return nil
}

// rememberRemoved keeps removals until any grant for them has expired.
func (s *SFU) rememberRemoved(callID []byte, participants [][]byte) {
	if len(participants) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepRemovedLocked()
	until := time.Now().Add(callauth.MaxGrantLifetime)
	for _, p := range participants {
		key := string(callID) + string(p)
		s.removed[key] = until
		s.removedQueue = append(s.removedQueue, removal{key, until})
	}
	for len(s.removedQueue) > maxRemembered {
		s.forgetOldestLocked()
	}
}

// sweepRemovedLocked drops expired removals (s.mu held).
func (s *SFU) sweepRemovedLocked() {
	now := time.Now()
	for len(s.removedQueue) > 0 && now.After(s.removedQueue[0].until) {
		s.forgetOldestLocked()
	}
}

// forgetOldestLocked pops the queue's first removal; the participant is
// forgotten unless a later removal of them is still queued (s.mu held).
func (s *SFU) forgetOldestLocked() {
	r := s.removedQueue[0]
	s.removedQueue[0] = removal{}
	s.removedQueue = s.removedQueue[1:]
	if s.removed[r.key].Equal(r.until) {
		delete(s.removed, r.key)
	}
}

// dropIfIdle forgets a call nobody is in or joining: the last participant
// left, or the only join was refused (removed, full, a bad offer), which
// would otherwise keep one of the relay's MaxCalls places forever. Lock
// order: SFU.mu, then call.mu.
func (s *SFU) dropIfIdle(c *call) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.joining > 0 || s.calls[string(c.id)] != c {
		return
	}
	c.mu.Lock()
	empty := len(c.peers) == 0
	c.mu.Unlock()
	if empty {
		delete(s.calls, string(c.id))
	}
}

func (s *SFU) dropCall(c *call) {
	s.mu.Lock()
	if s.calls[string(c.id)] == c {
		delete(s.calls, string(c.id))
	}
	s.mu.Unlock()
}

func pid(b []byte) string { return hex.EncodeToString(b) }

// isVP8Keyframe reports whether an RTP packet starts a VP8 keyframe. The VP8
// payload header byte stays in the clear (SFrame leaves the codec's frame
// header unencrypted but authenticated, docs/protocol.md §12).
func isVP8Keyframe(p *rtp.Packet) bool {
	b := p.Payload
	if len(b) < 1 {
		return false
	}
	start := b[0]&0x10 != 0 && b[0]&0x07 == 0
	i := 1
	if b[0]&0x80 != 0 {
		if len(b) < 2 {
			return false
		}
		x := b[1]
		i = 2
		if x&0x80 != 0 {
			if len(b) <= i {
				return false
			}
			if b[i]&0x80 != 0 {
				i += 2
			} else {
				i++
			}
		}
		if x&0x40 != 0 {
			i++
		}
		if x&0x30 != 0 {
			i++
		}
	}
	return start && len(b) > i && b[i]&0x01 == 0
}

func pli(ssrc webrtc.SSRC) []rtcp.Packet {
	return []rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(ssrc)}}
}

type (
	rtcpPLI = rtcp.PictureLossIndication
	rtcpFIR = rtcp.FullIntraRequest
)
