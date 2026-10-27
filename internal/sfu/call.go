package sfu

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

type call struct {
	sfu       *SFU
	id        []byte
	serverKey []byte

	mu      sync.Mutex
	peers   map[string]*Peer
	control *wire.CallControl
	// Participants removed by any control so far (the current control
	// lists only recent ones), the newest maxRemoved of them in order.
	removed      map[string]bool
	removedOrder []string
	// Audio sources per (source, kind).
	audio map[string]*audioSource
	// Video sources per (source, kind).
	video map[string]*videoSource
	ended bool

	// Joins in progress (guarded by SFU.mu, not mu): a call nobody is in or
	// joining is dropped, so a refused first join doesn't keep it.
	joining int

	// The control's lists as sets (guarded by mu).
	sets controlSets

	// Held while working out and sending CallWatchers, so two changes
	// can't reach a sender in the wrong order. Taken before mu.
	wmu sync.Mutex
	// A CallWatchers round is due (watchTimer), or the last went out at
	// watchSent (guarded by mu): changes within watchDebounce make one.
	watchTimer *time.Timer
	watchSent  time.Time
}

func newCall(s *SFU, g *wire.CallGrant) *call {
	ctl := &wire.CallControl{CallId: g.CallId}
	return &call{
		sfu:       s,
		id:        g.CallId,
		serverKey: g.ServerKey,
		peers:     map[string]*Peer{},
		control:   ctl,
		sets:      setsOf(ctl),
		removed:   map[string]bool{},
		audio:     map[string]*audioSource{},
		video:     map[string]*videoSource{},
	}
}

// controlSets is a control's lists by participant id, so a check for one
// participant costs a lookup whatever the lists' length (a control may
// name a thousand).
type controlSets struct {
	muted, deafened, noVideo, noScreen, removed map[string]bool
}

func setsOf(ctl *wire.CallControl) controlSets {
	set := func(list [][]byte) map[string]bool {
		m := make(map[string]bool, len(list))
		for _, x := range list {
			m[pid(x)] = true
		}
		return m
	}
	return controlSets{muted: set(ctl.Muted), deafened: set(ctl.Deafened), noVideo: set(ctl.NoVideo), noScreen: set(ctl.NoScreen), removed: set(ctl.Removed)}
}

// audioMuted says whether the control silences a source's audio of kind:
// server mute or no Speak (Muted) silences both the microphone and screen
// audio (the relay can't tell what a track really carries); screen audio
// also goes with Screen Share (NoScreen).
func (cs controlSets) audioMuted(source, kind string) bool {
	return cs.muted[source] || (kind == KindScreenAudio && cs.noScreen[source])
}

// videoBlocked says whether the control stops a source's video of kind.
func (cs controlSets) videoBlocked(source, kind string) bool {
	if kind == KindScreen {
		return cs.noScreen[source]
	}
	return cs.noVideo[source]
}

func trackKey(source, kind string) string { return source + "/" + kind }

// audioSource is one participant's audio of one kind. Each receiver has its
// own outgoing track, written only while it listens: pausing one receiver
// never takes a track off its sender (a sender without a track can't start,
// and renegotiating one loses the stream id that says whose audio it is).
type audioSource struct {
	codec webrtc.RTPCodecCapability
	// The control silences it (server mute, no Speak, no Screen Share for
	// screen audio).
	muted atomic.Bool

	mu   sync.Mutex
	outs map[*Peer]*audioOut
}

// audioOut carries one audio source to one receiver.
type audioOut struct {
	src    *audioSource
	local  *webrtc.TrackLocalStaticRTP
	sender *webrtc.RTPSender
	on     atomic.Bool // the receiver listens to it
}

// defaultMaxParticipants applies when a grant names no limit.
const defaultMaxParticipants = 99

// maxRemoved bounds the removals a call remembers beyond its current
// control's list.
const maxRemoved = 4 * defaultMaxParticipants

func maxParticipants(g *wire.CallGrant) uint32 {
	if g.MaxParticipants == 0 || g.MaxParticipants > defaultMaxParticipants {
		return defaultMaxParticipants
	}
	return g.MaxParticipants
}

func listHas(list [][]byte, p []byte) bool {
	for _, x := range list {
		if bytes.Equal(x, p) {
			return true
		}
	}
	return false
}

// Peer is one participant's connection to the call.
type Peer struct {
	call  *call
	grant *wire.CallGrant
	id    string
	pc    *webrtc.PeerConnection
	sig   Signaler
	kinds map[string]string // mid -> kind

	mu          sync.Mutex
	answered    bool // the join answer is set; others' media may be added
	negotiating bool
	again       bool
	sends       map[string]bool       // kinds this peer is publishing
	audioOut    map[string]*audioOut  // (source, kind) -> carrying it to this peer
	videoOut    map[string]*forwarder // (source, kind) -> forwarder to this peer
	closed      bool                  // written under c.mu and p.mu

	// What this peer asked to receive (guarded by c.mu): the video layer per
	// (source, kind), kept before the video exists and while its source
	// reconnects (as the same participant id), and the audio it listens to.
	wants  map[string]string
	listen listening
	// The CallWatchers last sent to this peer (guarded by c.wmu).
	lastWatch []byte

	inbound *budget // what this peer may send, in bytes per second
	release func()  // gives back the relay-wide and per-address places
}

// listening is a peer's CallListen: deafened, microphones it muted for
// itself, and the screens whose audio it wants (participant hex ids).
type listening struct {
	deaf        bool
	noAudio     map[string]bool
	screenAudio map[string]bool
}

// maxWants bounds what one peer may ask for: video layers per (source,
// kind), and the lists in a CallListen.
const maxWants = 2 * defaultMaxParticipants

func (p *Peer) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

func (c *call) join(ctx context.Context, g *wire.CallGrant, j *wire.CallJoin, sig Signaler, release func()) (*Peer, string, error) {
	id := pid(g.Participant)
	c.mu.Lock()
	// The participant's earlier connection (one that dropped without a
	// leave, or a rejoin before it timed out) makes way: the grant is theirs.
	if old := c.peers[id]; old != nil {
		c.mu.Unlock()
		old.Leave()
		c.mu.Lock()
	}
	switch {
	case c.ended:
		c.mu.Unlock()
		return nil, "", errors.New("this call has ended")
	case c.removed[id] || c.sets.removed[id]:
		c.mu.Unlock()
		return nil, "", errors.New("you were removed from this call")
	case uint32(len(c.peers)) >= maxParticipants(g):
		c.mu.Unlock()
		return nil, "", errors.New("this call is full")
	}
	c.mu.Unlock()

	pc, err := c.sfu.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, "", err
	}
	p := &Peer{
		call: c, grant: g, id: id, pc: pc, sig: sig, kinds: j.Kinds,
		sends: map[string]bool{}, audioOut: map[string]*audioOut{}, videoOut: map[string]*forwarder{},
		inbound: newBudget(c.sfu.cfg.PeerBitrate / 8), release: release,
		wants: map[string]string{},
	}
	pc.OnTrack(p.onTrack)
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		if st == webrtc.PeerConnectionStateFailed || st == webrtc.PeerConnectionStateClosed {
			p.Leave()
		}
	})
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: j.Offer}); err != nil {
		pc.Close()
		return nil, "", errors.New("the offer isn't valid")
	}
	// Registered now, but others' media is only added once the answer is
	// set: adding tracks while the client's offer is open would renegotiate
	// mid-offer, and could land on the client's own send transceivers.
	// Checked again: a control (removal) or other joins may have arrived
	// while the offer was being read.
	c.mu.Lock()
	if c.ended || c.peers[id] != nil || c.removed[id] || uint32(len(c.peers)) >= maxParticipants(g) {
		c.mu.Unlock()
		pc.Close()
		return nil, "", errors.New("this call changed while joining; try again")
	}
	c.peers[id] = p
	c.mu.Unlock()
	answer, err := pc.CreateAnswer(nil)
	if err == nil {
		gather := webrtc.GatheringCompletePromise(pc)
		err = pc.SetLocalDescription(answer)
		select {
		case <-gather:
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	if err != nil {
		p.Leave()
		return nil, "", errors.New("couldn't answer the offer")
	}
	// Taken now: attaching below starts the next offer.
	// (Pion takes back only the answer it made; the participant's copy
	// asks for constant bitrate voice.)
	sdpAnswer := voiceFormat(pc.LocalDescription().SDP)
	c.mu.Lock()
	p.mu.Lock()
	p.answered = true
	p.mu.Unlock()
	for key, src := range c.audio {
		if key[:32] != id {
			p.attachAudioLocked(key, src)
		}
	}
	for key, vs := range c.video {
		if vs.source != id {
			p.attachVideoLocked(key, vs)
		}
	}
	c.mu.Unlock()
	c.broadcastState()
	c.notifyWatchers()
	return p, sdpAnswer, nil
}

// onTrack: a participant's media arrived (once per simulcast layer).
func (p *Peer) onTrack(remote *webrtc.TrackRemote, recv *webrtc.RTPReceiver) {
	kind := ""
	for _, t := range p.pc.GetTransceivers() {
		if t.Receiver() == recv {
			kind = p.kinds[t.Mid()]
		}
	}
	g := p.grant
	allowed := map[string]bool{KindAudio: g.Audio, KindCamera: g.Video, KindScreen: g.Screen, KindScreenAudio: g.Screen && g.Audio}
	if !allowed[kind] {
		// Not declared, or not permitted (Speak / Video / Screen Share): drain it
		// so the sender's congestion control stays sane, forward nothing.
		go drain(remote)
		return
	}
	c := p.call
	key := trackKey(p.id, kind)
	p.mu.Lock()
	p.sends[kind] = true
	p.mu.Unlock()
	if remote.Kind() == webrtc.RTPCodecTypeAudio {
		c.mu.Lock()
		if c.audio[key] != nil || p.isClosed() {
			// One track per kind (the join's kinds are unique), and none
			// after leaving.
			c.mu.Unlock()
			go drain(remote)
			return
		}
		src := &audioSource{codec: remote.Codec().RTPCodecCapability, outs: map[*Peer]*audioOut{}}
		src.muted.Store(c.sets.audioMuted(p.id, kind))
		c.audio[key] = src
		for _, other := range c.peers {
			if other != p {
				other.attachAudioLocked(key, src)
			}
		}
		c.mu.Unlock()
		c.broadcastState()
		go c.forwardAudio(p, remote, src)
		return
	}
	c.mu.Lock()
	vs := c.video[key]
	isNew := vs == nil
	if isNew {
		vs = &videoSource{call: c, owner: p, source: p.id, kind: kind, codec: remote.Codec().RTPCodecCapability, layers: map[string]*webrtc.TrackRemote{}, pc: p.pc}
		vs.blocked.Store(c.sets.videoBlocked(vs.source, kind))
		c.video[key] = vs
	}
	rid := remote.RID()
	vs.mu.Lock()
	vs.layers[rid] = remote
	vs.mu.Unlock()
	if isNew {
		for _, other := range c.peers {
			if other != p {
				other.attachVideoLocked(key, vs)
			}
		}
	}
	c.mu.Unlock()
	if isNew {
		c.broadcastState()
	}
	go vs.read(rid, remote)
}

func drain(t *webrtc.TrackRemote) {
	buf := make([]byte, 1500)
	for {
		if _, _, err := t.Read(buf); err != nil {
			return
		}
	}
}

func (c *call) forwardAudio(p *Peer, remote *webrtc.TrackRemote, src *audioSource) {
	var outs []*audioOut
	for {
		pkt, _, err := remote.ReadRTP()
		if err != nil {
			return
		}
		if src.muted.Load() || !p.inbound.allow(len(pkt.Payload)) {
			continue
		}
		stripExtensions(pkt)
		outs = outs[:0]
		src.mu.Lock()
		for _, o := range src.outs {
			if o.on.Load() {
				outs = append(outs, o)
			}
		}
		src.mu.Unlock()
		for _, o := range outs {
			_ = o.local.WriteRTP(pkt)
		}
	}
}

// attachAudioLocked adds an audio source to this peer (c.mu held).
func (p *Peer) attachAudioLocked(key string, src *audioSource) {
	if p.closed || !p.isAnswered() {
		return
	}
	source, kind, _ := strings.Cut(key, "/")
	local, err := webrtc.NewTrackLocalStaticRTP(src.codec, kind, source)
	if err != nil {
		return
	}
	sender, err := p.pc.AddTrack(local)
	if err != nil {
		return
	}
	out := &audioOut{src: src, local: local, sender: sender}
	out.on.Store(p.hearsLocked(source, kind))
	p.mu.Lock()
	p.audioOut[key] = out
	p.mu.Unlock()
	src.mu.Lock()
	src.outs[p] = out
	src.mu.Unlock()
	go readRTCP(sender)
	p.renegotiate()
}

// hearsLocked says whether this peer is to get a source's audio of a kind
// (c.mu held): not when the server deafened it, it deafened itself or
// muted that participant for itself; screen audio only while it asks for
// it (it watches that screen with its sound on).
func (p *Peer) hearsLocked(source, kind string) bool {
	if p.call.sets.deafened[p.id] || p.listen.deaf {
		return false
	}
	if kind == KindScreenAudio {
		return p.listen.screenAudio[source]
	}
	return !p.listen.noAudio[source]
}

// applyAudioLocked forwards or pauses each audio source to this peer as it
// listens now (c.mu held).
func (p *Peer) applyAudioLocked() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, o := range p.audioOut {
		source, kind, _ := strings.Cut(key, "/")
		o.on.Store(p.hearsLocked(source, kind))
	}
}

// attachVideoLocked adds a forwarder for a video source to this peer (c.mu held).
func (p *Peer) attachVideoLocked(key string, vs *videoSource) {
	if p.closed || !p.isAnswered() {
		return
	}
	local, err := webrtc.NewTrackLocalStaticRTP(vs.codec, vs.kind, vs.source)
	if err != nil {
		return
	}
	sender, err := p.pc.AddTrack(local)
	if err != nil {
		return
	}
	// Paused until this peer asks for a layer (it may have already, before
	// the video arrived).
	f := &forwarder{vs: vs, peer: p, local: local, sender: sender, want: p.wants[key]}
	p.mu.Lock()
	p.videoOut[key] = f
	p.mu.Unlock()
	vs.subscribe(f)
	go f.readRTCP()
	p.renegotiate()
}

func (p *Peer) isAnswered() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.answered
}

// stripExtensions removes the sender's RTP header extensions before a packet
// is forwarded: their ids are the sender's negotiation, and its mid and rid
// would make a receiver file the packet under one of its own transceivers.
func stripExtensions(p *rtp.Packet) {
	p.Extension = false
	p.ExtensionProfile = 0
	p.Extensions = nil
}

func readRTCP(s *webrtc.RTPSender) {
	buf := make([]byte, 1500)
	for {
		if _, _, err := s.Read(buf); err != nil {
			return
		}
	}
}

// renegotiate sends a new offer (serialized: one round at a time).
func (p *Peer) renegotiate() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	if p.negotiating {
		p.again = true
		p.mu.Unlock()
		return
	}
	p.negotiating = true
	p.mu.Unlock()
	go func() {
		offer, err := p.pc.CreateOffer(nil)
		if err == nil {
			gather := webrtc.GatheringCompletePromise(p.pc)
			if err = p.pc.SetLocalDescription(offer); err == nil {
				<-gather
				p.sig.CallSignal(&wire.CallSignal{CallId: p.call.id, Type: "offer", Sdp: voiceFormat(p.pc.LocalDescription().SDP)})
				return
			}
		}
		p.mu.Lock()
		p.negotiating = false
		p.mu.Unlock()
		p.call.sfu.log.Warn("call: renegotiation failed", "err", err)
	}()
}

// Signal handles the client's answer to a relay offer.
func (p *Peer) Signal(s *wire.CallSignal) error {
	if s.Type != "answer" {
		return errors.New("clients only answer; transceivers are declared at join")
	}
	if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: s.Sdp}); err != nil {
		return errors.New("the answer isn't valid")
	}
	p.mu.Lock()
	p.negotiating = false
	again := p.again
	p.again = false
	p.mu.Unlock()
	if again {
		p.renegotiate()
	}
	return nil
}

// Layer selects which simulcast layer of a source this peer receives ("" to
// stop). It may come before the source sends that video, or before the
// source is at this relay at all (the community server lists people
// before their join reaches the relay): it is kept (at most maxWants) and
// applies when the video arrives, and the source learns someone wants it
// (CallWatchers).
func (p *Peer) Layer(l *wire.CallLayer) error {
	switch l.Rid {
	case "", "l", "m", "h":
	default:
		return errors.New("unknown layer")
	}
	if l.Kind != KindCamera && l.Kind != KindScreen {
		return errors.New("no such video")
	}
	c := p.call
	src := pid(l.Source)
	key := trackKey(src, l.Kind)
	if len(l.Source) != 16 || src == p.id {
		return errors.New("no such participant")
	}
	c.mu.Lock()
	if p.wants[key] == l.Rid {
		// Asked for what it already gets: nothing to work out or tell.
		c.mu.Unlock()
		return nil
	}
	if l.Rid == "" {
		delete(p.wants, key)
	} else {
		if _, ok := p.wants[key]; !ok && len(p.wants) >= maxWants {
			c.pruneWantsLocked(p)
			if len(p.wants) >= maxWants {
				c.mu.Unlock()
				return errors.New("too many videos")
			}
		}
		p.wants[key] = l.Rid
	}
	p.mu.Lock()
	f := p.videoOut[key]
	p.mu.Unlock()
	c.mu.Unlock()
	if f != nil {
		f.choose(l.Rid)
	}
	c.notifyWatchers()
	return nil
}

// pruneWantsLocked forgets layers asked of participants no longer in the
// call (c.mu held).
func (c *call) pruneWantsLocked(p *Peer) {
	for key := range p.wants {
		if c.peers[key[:32]] == nil {
			delete(p.wants, key)
		}
	}
}

// Listen sets the audio this peer wants forwarded (CallListen).
func (p *Peer) Listen(m *wire.CallListen) error {
	if len(m.NoAudio) > maxWants || len(m.ScreenAudio) > maxWants {
		return errors.New("listen lists too long")
	}
	l := listening{deaf: m.Deaf, noAudio: map[string]bool{}, screenAudio: map[string]bool{}}
	for _, x := range m.NoAudio {
		l.noAudio[pid(x)] = true
	}
	for _, x := range m.ScreenAudio {
		l.screenAudio[pid(x)] = true
	}
	c := p.call
	c.mu.Lock()
	if l.deaf == p.listen.deaf && maps.Equal(l.noAudio, p.listen.noAudio) && maps.Equal(l.screenAudio, p.listen.screenAudio) {
		c.mu.Unlock()
		return nil
	}
	p.listen = l
	p.applyAudioLocked()
	c.mu.Unlock()
	c.notifyWatchers()
	return nil
}

// watchersLocked works out who receives each kind of a source's media
// (c.mu held): the other participants the relay would forward it to now,
// given the grant, the control and what each of them asked for.
func (c *call) watchersLocked(s *Peer) *wire.CallWatchers {
	g := s.grant
	cs := c.sets
	allowed := map[string]bool{
		KindAudio:       g.Audio && !cs.audioMuted(s.id, KindAudio),
		KindScreenAudio: g.Audio && g.Screen && !cs.audioMuted(s.id, KindScreenAudio),
		KindCamera:      g.Video && !cs.videoBlocked(s.id, KindCamera),
		KindScreen:      g.Screen && !cs.videoBlocked(s.id, KindScreen),
	}
	out := &wire.CallWatchers{CallId: c.id}
	for _, kind := range []string{KindAudio, KindCamera, KindScreen, KindScreenAudio} {
		w := &wire.CallWatch{Kind: kind}
		layers := map[string]bool{}
		for _, o := range c.peers {
			if o == s || !allowed[kind] {
				continue
			}
			switch kind {
			case KindAudio, KindScreenAudio:
				if o.hearsLocked(s.id, kind) {
					w.Receivers++
				}
			default:
				if rid := o.wants[trackKey(s.id, kind)]; rid != "" {
					w.Receivers++
					layers[rid] = true
				}
			}
		}
		for _, rid := range []string{"l", "m", "h"} {
			if layers[rid] {
				w.Layers = append(w.Layers, rid)
			}
		}
		out.Watches = append(out.Watches, w)
	}
	return out
}

// watchDebounce is how long changes to who receives what are gathered
// before CallWatchers go out again: working them out costs every
// participant a pass over the others, so a participant changing what it
// listens to at frame rate costs one pass per window, not one per frame.
const watchDebounce = 50 * time.Millisecond

// notifyWatchers tells each participant whose receivers changed: at once
// when none went out within watchDebounce, else once that has passed (one
// round for every change until then). Never called with c.mu held.
func (c *call) notifyWatchers() {
	c.mu.Lock()
	if c.watchTimer != nil {
		c.mu.Unlock()
		return
	}
	if wait := watchDebounce - time.Since(c.watchSent); wait > 0 {
		c.watchTimer = time.AfterFunc(wait, c.sendWatchers)
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	c.sendWatchers()
}

func (c *call) sendWatchers() {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	type send struct {
		p *Peer
		m *wire.CallWatchers
	}
	var sends []send
	c.mu.Lock()
	c.watchTimer, c.watchSent = nil, time.Now()
	for _, p := range c.peers {
		if p.isClosed() || !p.isAnswered() {
			continue
		}
		m := c.watchersLocked(p)
		b, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
		if err != nil || bytes.Equal(b, p.lastWatch) {
			continue
		}
		p.lastWatch = b
		sends = append(sends, send{p, m})
	}
	c.mu.Unlock()
	for _, x := range sends {
		x.p.sig.CallWatchers(x.m)
	}
}

// Leave removes the participant (their connection closed, or they left).
func (p *Peer) Leave() {
	c := p.call
	c.mu.Lock()
	p.mu.Lock()
	was := p.closed
	p.closed = true
	p.mu.Unlock()
	if was {
		c.mu.Unlock()
		return
	}
	if c.peers[p.id] == p {
		delete(c.peers, p.id)
	}
	// Nothing more is written for it.
	p.mu.Lock()
	for _, o := range p.audioOut {
		o.src.mu.Lock()
		delete(o.src.outs, p)
		o.src.mu.Unlock()
	}
	p.mu.Unlock()
	var gone []string
	for key := range c.audio {
		if key[:32] == p.id {
			gone = append(gone, key)
		}
	}
	for key, vs := range c.video {
		if vs.source == p.id {
			gone = append(gone, key)
		}
	}
	for _, key := range gone {
		delete(c.audio, key)
		delete(c.video, key)
		for _, other := range c.peers {
			other.detachLocked(key)
		}
	}
	empty := len(c.peers) == 0
	c.mu.Unlock()
	// Released outside the call lock: the SFU lock is taken before it.
	if p.release != nil {
		p.release()
	}
	_ = p.pc.Close()
	if empty {
		c.sfu.dropIfIdle(c)
	} else {
		c.broadcastState()
		c.notifyWatchers()
	}
}

func (p *Peer) detachLocked(key string) {
	p.mu.Lock()
	a := p.audioOut[key]
	f := p.videoOut[key]
	delete(p.audioOut, key)
	delete(p.videoOut, key)
	p.mu.Unlock()
	var s *webrtc.RTPSender
	if a != nil {
		a.src.mu.Lock()
		delete(a.src.outs, p)
		a.src.mu.Unlock()
		s = a.sender
	}
	if f != nil {
		f.vs.unsubscribe(f)
		s = f.sender
	}
	if s != nil {
		_ = p.pc.RemoveTrack(s)
		p.renegotiate()
	}
}

func (c *call) applyControl(ctl *wire.CallControl) {
	c.mu.Lock()
	if ctl.Seq <= c.control.Seq && c.control.Seq != 0 {
		c.mu.Unlock()
		return
	}
	c.control, c.sets = ctl, setsOf(ctl)
	for key, src := range c.audio {
		source, kind, _ := strings.Cut(key, "/")
		src.muted.Store(c.sets.audioMuted(source, kind))
	}
	for _, vs := range c.video {
		vs.blocked.Store(c.sets.videoBlocked(vs.source, vs.kind))
	}
	var kick []*Peer
	for _, r := range ctl.Removed {
		c.removeLocked(pid(r))
		if p := c.peers[pid(r)]; p != nil {
			kick = append(kick, p)
		}
	}
	for _, p := range c.peers {
		p.applyAudioLocked()
	}
	c.mu.Unlock()
	// Remembered outside the call lock: the SFU lock is taken before it.
	c.sfu.rememberRemoved(c.id, ctl.Removed)
	for _, p := range kick {
		p.sig.CallState(&wire.CallState{CallId: c.id, Ended: "A moderator disconnected you from this call."})
		p.Leave()
	}
	c.broadcastState()
	c.notifyWatchers()
}

// removeLocked notes a removed participant, forgetting the oldest once
// maxRemoved are kept (c.mu held).
func (c *call) removeLocked(id string) {
	if c.removed[id] {
		return
	}
	c.removed[id] = true
	c.removedOrder = append(c.removedOrder, id)
	if len(c.removedOrder) > maxRemoved {
		delete(c.removed, c.removedOrder[0])
		c.removedOrder = c.removedOrder[1:]
	}
}

func (c *call) broadcastState() {
	c.mu.Lock()
	st := &wire.CallState{CallId: c.id}
	peers := make([]*Peer, 0, len(c.peers))
	for _, p := range c.peers {
		peers = append(peers, p)
		p.mu.Lock()
		st.Members = append(st.Members, &wire.CallMember{
			Participant: p.grant.Participant,
			Kid:         p.grant.Kid,
			Audio:       p.sends[KindAudio],
			Camera:      p.sends[KindCamera],
			Screen:      p.sends[KindScreen],
			Muted:       c.sets.muted[p.id],
			Deafened:    c.sets.deafened[p.id],
			Priority:    p.grant.Priority,
		})
		p.mu.Unlock()
	}
	c.mu.Unlock()
	for _, p := range peers {
		p.sig.CallState(st)
	}
}

func (c *call) end(reason string) {
	c.mu.Lock()
	c.ended = true
	peers := make([]*Peer, 0, len(c.peers))
	for _, p := range c.peers {
		peers = append(peers, p)
	}
	c.mu.Unlock()
	for _, p := range peers {
		p.sig.CallState(&wire.CallState{CallId: c.id, Ended: reason})
		p.Leave()
	}
	c.sfu.dropCall(c)
}

// ---------------------------------------------------------------- video

type videoSource struct {
	call   *call
	owner  *Peer
	source string
	kind   string
	codec  webrtc.RTPCodecCapability
	pc     *webrtc.PeerConnection

	// The server took this participant's Video or Screen Share away.
	blocked atomic.Bool

	mu     sync.Mutex
	layers map[string]*webrtc.TrackRemote
	subs   map[*forwarder]bool
}

func (vs *videoSource) subscribe(f *forwarder) {
	vs.mu.Lock()
	if vs.subs == nil {
		vs.subs = map[*forwarder]bool{}
	}
	vs.subs[f] = true
	vs.mu.Unlock()
	if want := f.target(); want != "" {
		vs.requestKeyframe(want)
	}
}

func (vs *videoSource) unsubscribe(f *forwarder) {
	vs.mu.Lock()
	delete(vs.subs, f)
	vs.mu.Unlock()
}

// requestKeyframe asks the sender for a keyframe on one layer.
func (vs *videoSource) requestKeyframe(rid string) {
	vs.mu.Lock()
	t := vs.layers[rid]
	if t == nil {
		for _, l := range vs.layers {
			t = l
			break
		}
	}
	vs.mu.Unlock()
	if t != nil {
		_ = vs.pc.WriteRTCP(pli(t.SSRC()))
	}
}

func (vs *videoSource) read(rid string, t *webrtc.TrackRemote) {
	for {
		pkt, _, err := t.ReadRTP()
		if err != nil {
			return
		}
		if vs.blocked.Load() || !vs.owner.inbound.allow(len(pkt.Payload)) {
			continue
		}
		key := isVP8Keyframe(pkt)
		vs.mu.Lock()
		subs := make([]*forwarder, 0, len(vs.subs))
		for f := range vs.subs {
			subs = append(subs, f)
		}
		vs.mu.Unlock()
		for _, f := range subs {
			f.write(rid, pkt, key)
		}
	}
}

// forwarder carries one source's video to one subscriber, following the
// layer they want and switching only at keyframes.
type forwarder struct {
	vs     *videoSource
	peer   *Peer
	local  *webrtc.TrackLocalStaticRTP
	sender *webrtc.RTPSender

	mu        sync.Mutex
	want, cur string
	started   bool
	lastSeq   uint16
	lastTS    uint32
	seqOff    uint16
	tsOff     uint32
	lastWrite time.Time
}

func (f *forwarder) target() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.want
}

func (f *forwarder) choose(rid string) {
	f.mu.Lock()
	changed := rid != f.want
	f.want = rid
	f.mu.Unlock()
	if changed && rid != "" {
		f.vs.requestKeyframe(rid)
	}
}

func (f *forwarder) write(rid string, pkt *rtp.Packet, keyframe bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.want == "" {
		return
	}
	if rid != f.cur {
		// Switch to the wanted layer (or any layer if the wanted one isn't
		// being sent) only at a keyframe.
		if !keyframe || (rid != f.want && f.vs.hasLayer(f.want)) {
			return
		}
		if f.started {
			// Continue the outgoing sequence and timeline where the old layer
			// stopped, so the receiver sees one stream.
			f.seqOff = f.lastSeq + 1 - pkt.SequenceNumber
			f.tsOff = f.lastTS + 3000 - pkt.Timestamp
		}
		f.cur = rid
		f.started = true
	}
	out := *pkt
	out.SequenceNumber = pkt.SequenceNumber + f.seqOff
	out.Timestamp = pkt.Timestamp + f.tsOff
	f.lastSeq = out.SequenceNumber
	f.lastTS = out.Timestamp
	f.lastWrite = time.Now()
	stripExtensions(&out)
	_ = f.local.WriteRTP(&out)
}

func (vs *videoSource) hasLayer(rid string) bool {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	return vs.layers[rid] != nil
}

// readRTCP turns the subscriber's keyframe requests into requests to the sender.
func (f *forwarder) readRTCP() {
	for {
		pkts, _, err := f.sender.ReadRTCP()
		if err != nil {
			return
		}
		for _, p := range pkts {
			switch p.(type) {
			case *rtcpPLI, *rtcpFIR:
				f.vs.requestKeyframe(f.target())
			}
		}
	}
}

// budget is a token bucket of bytes per second (burst: one second's worth).
type budget struct {
	mu     sync.Mutex
	rate   float64
	tokens float64
	last   time.Time
}

func newBudget(bytesPerSecond int) *budget {
	r := float64(bytesPerSecond)
	return &budget{rate: r, tokens: r, last: time.Now()}
}

// allow takes n bytes if there are enough; a participant sending more
// than its share has the excess dropped instead of fanned out to everyone.
func (b *budget) allow(n int) bool {
	if b == nil || b.rate <= 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tokens = min(b.rate, b.tokens+now.Sub(b.last).Seconds()*b.rate)
	b.last = now
	if b.tokens < float64(n) {
		return false
	}
	b.tokens -= float64(n)
	return true
}
