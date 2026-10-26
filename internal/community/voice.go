package community

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/callauth"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Calls in voice channels (docs/protocol.md §12). Media goes between each
// participant and the call's relay, whose SFU forwards it without keys. The
// server:
//   - picks a relay that carries calls, and moves the call when that relay
//     fails;
//   - admits each participant with a grant signed by the server key, saying
//     what they may send (Speak, Video, Screen Share);
//   - holds the call key, which changes on every join and leave, and hands
//     it to the participants;
//   - sends signed controls to the relay for server mute and deafen,
//     permissions lost mid-call, and participants who left or were removed.

const (
	// Discord's largest voice channel user limit.
	maxCallParticipants = 99
	// A relay is given up on when half the participants (or the server
	// itself) can't reach it; reports older than this don't count.
	relayFailWindow = 30 * time.Second
	// The least time between two relay moves of one call.
	relayMoveGap = 10 * time.Second
	// How often participants' permissions are checked again (timeouts end,
	// roles change outside admin actions).
	voiceRecheckEvery = 10 * time.Second
	// Departed participants listed in a control (the newest are kept; older
	// ones' grants are nearly expired anyway).
	maxGone = 500
	// Each join (and rejoin) rotates everyone's key, so a session gets an
	// allowance: voiceJoinBurst at once, then one more every voiceJoinEvery.
	voiceJoinBurst = 5
	voiceJoinEvery = 3 * time.Second
)

type participant struct {
	sess   *sess
	member []byte
	id     []byte // random per join; the relay's name for them
	slot   uint32 // 1..255, the low byte of their SFrame key ids
	perms  callPerms

	selfMute, selfDeaf, camera, screen bool
	// priorityOn: the participant switched priority speaker on (VoiceState).
	priorityOn bool
	failedAt   time.Time // reported the relay unreachable
}

// prioritySpeaking: priority speaker is on for everyone's participant list
// only while the participant both switched it on and has the permission (a
// role change can take the permission away mid-call).
func (p *participant) prioritySpeaking() bool { return p.priorityOn && p.perms.priority }

// callPerms is what a participant may do in the call's channel.
type callPerms struct {
	speak, video, screen, vad, priority bool
}

type call struct {
	channelID int64
	nonce     []byte
	id        []byte
	key       []byte
	epoch     uint32
	relay     string
	movedAt   time.Time
	seq       uint64
	parts     map[*sess]*participant
	// Participants who left or were removed, until their grants expire:
	// the relay refuses them.
	gone map[string]time.Time
	// The newest signed control. Every session carries it, and clients hand
	// it to the relay when they join, so a relay starting the call (first
	// join, after a move or a relay restart) enforces it from the start.
	signed *wire.SignedCallControl
}

type voiceState struct {
	mu       sync.Mutex
	calls    map[int64]*call
	lastJoin map[*sess]joinAllowance // forgotten when the session ends
}

func newVoiceState() *voiceState {
	return &voiceState{calls: map[int64]*call{}, lastJoin: map[*sess]joinAllowance{}}
}

// callPermsFor works out m's call permissions in c (callers hold s.mu).
func (s *Server) callPermsFor(m *memberRec, c *channelRec) callPerms {
	have := s.channelPerms(m, c)
	return callPerms{
		speak:    have&PermSpeak != 0,
		video:    have&PermVideo != 0 && s.allowed(FeatureVideo, c, m, 0) == nil,
		screen:   have&PermScreenShare != 0 && s.allowed(FeatureScreenShare, c, m, 0) == nil,
		vad:      have&PermUseVoiceActivity != 0,
		priority: have&PermPrioritySpeaker != 0,
	}
}

// callRelayLocked picks the attached relay that carries calls with the
// fewest calls on it, other than avoid (v.mu held).
func (s *Server) callRelayLocked(avoid string) string {
	s.rmu.RLock()
	var urls []string
	for u, c := range s.relays {
		if c.Calls() && u != avoid {
			urls = append(urls, u)
		}
	}
	s.rmu.RUnlock()
	if len(urls) == 0 {
		return ""
	}
	load := map[string]int{}
	for _, cl := range s.voice.calls {
		load[cl.relay]++
	}
	sort.Slice(urls, func(i, j int) bool {
		if load[urls[i]] != load[urls[j]] {
			return load[urls[i]] < load[urls[j]]
		}
		return urls[i] < urls[j]
	})
	return urls[0]
}

func (s *Server) relayAttached(url string) bool {
	s.rmu.RLock()
	defer s.rmu.RUnlock()
	_, ok := s.relays[url]
	return ok
}

func (s *Server) voiceJoin(ss *sess, j *wire.VoiceJoin) (*wire.ServerMessage, error) {
	s.mu.RLock()
	c := s.channels[j.ChannelId]
	if c == nil || c.Kind != wire.ChannelKind_CHANNEL_KIND_VOICE {
		s.mu.RUnlock()
		return nil, errNotFound("no such voice channel")
	}
	m := s.members[string(ss.member)]
	err := s.allowed(FeatureVoice, c, m, PermViewChannel|PermConnectVoice)
	var perms callPerms
	if err == nil {
		perms = s.callPermsFor(m, c)
	}
	s.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	v := s.voice
	if j.Rejoin || j.FailedRelay != "" {
		v.mu.Lock()
		cl := v.calls[j.ChannelId]
		var p *participant
		if cl != nil {
			p = cl.parts[ss]
		}
		if p == nil {
			v.mu.Unlock()
			return nil, errNotFound("you're not in this call")
		}
		if wait := s.joinTooSoonLocked(p); wait {
			v.mu.Unlock()
			return nil, errRateLimited("reconnecting too often; try again in a moment")
		}
		moved := false
		if j.FailedRelay != "" && j.FailedRelay == cl.relay {
			p.failedAt = s.now()
			moved = s.maybeMoveLocked(cl)
		}
		// Reconnecting starts the client's SFrame counters from zero again;
		// a new key makes sure no nonce is ever used twice (moves rekey
		// already).
		if !moved {
			cl.key, cl.epoch = randomBytes(32), cl.epoch+1
		}
		out := s.changedLocked(cl)
		v.mu.Unlock()
		s.deliver(out, ss)
		return &wire.ServerMessage{Body: &wire.ServerMessage_VoiceSession{VoiceSession: out.updates[ss]}}, nil
	}

	v.mu.Lock()
	// One call per session at a time.
	var left []*call
	for id, other := range v.calls {
		if _, in := other.parts[ss]; in && id != j.ChannelId {
			s.removeLocked(other, ss)
			left = append(left, other)
		}
	}
	cl := v.calls[j.ChannelId]
	if cl == nil {
		relay := s.callRelayLocked("")
		if relay == "" {
			v.mu.Unlock()
			s.afterVoiceChange(left...)
			return nil, errDisabled("the server isn't connected to a relay that carries calls")
		}
		nonce := randomBytes(16)
		cl = &call{
			channelID: j.ChannelId, nonce: nonce, id: callauth.CallID(s.key.Public, nonce), relay: relay,
			parts: map[*sess]*participant{}, gone: map[string]time.Time{},
		}
		v.calls[j.ChannelId] = cl
	}
	if _, in := cl.parts[ss]; !in {
		if len(cl.parts) >= maxCallParticipants {
			v.mu.Unlock()
			s.afterVoiceChange(left...)
			return nil, errConflict("this voice channel is full")
		}
		p := &participant{sess: ss, member: ss.member, id: randomBytes(16), slot: freeSlot(cl), perms: perms}
		if s.joinTooSoonLocked(p) {
			v.mu.Unlock()
			s.afterVoiceChange(left...)
			return nil, errRateLimited("joining too often; try again in a moment")
		}
		cl.parts[ss] = p
		cl.key, cl.epoch = randomBytes(32), cl.epoch+1
	}
	out := s.changedLocked(cl)
	v.mu.Unlock()
	s.afterVoiceChange(left...)
	s.deliver(out, ss)
	return &wire.ServerMessage{Body: &wire.ServerMessage_VoiceSession{VoiceSession: out.updates[ss]}}, nil
}

// joinTooSoonLocked reports whether this session has used up its join
// allowance, and otherwise takes one join from it (v.mu held).
func (s *Server) joinTooSoonLocked(p *participant) bool {
	now := s.now()
	v := s.voice
	a, ok := v.lastJoin[p.sess]
	if !ok {
		a = joinAllowance{left: voiceJoinBurst, at: now}
	}
	a.left = min(float64(voiceJoinBurst), a.left+now.Sub(a.at).Seconds()/voiceJoinEvery.Seconds())
	a.at = now
	if a.left < 1 {
		v.lastJoin[p.sess] = a
		return true
	}
	a.left--
	v.lastJoin[p.sess] = a
	return false
}

type joinAllowance struct {
	left float64
	at   time.Time
}

// freeSlot is the lowest slot no participant holds (v.mu held; at most
// maxCallParticipants are ever in a call, so one is always free).
func freeSlot(cl *call) uint32 {
	used := map[uint32]bool{}
	for _, p := range cl.parts {
		used[p.slot] = true
	}
	for i := uint32(1); ; i++ {
		if !used[i] {
			return i
		}
	}
}

// maybeMoveLocked moves the call to another relay if the server has lost
// its own connection to the current one or half the participants reported
// it unreachable recently (v.mu held). It reports whether it moved.
func (s *Server) maybeMoveLocked(cl *call) bool {
	now := s.now()
	if now.Sub(cl.movedAt) < relayMoveGap {
		return false
	}
	failed := 0
	for _, p := range cl.parts {
		if now.Sub(p.failedAt) < relayFailWindow {
			failed++
		}
	}
	if s.relayAttached(cl.relay) && failed*2 < len(cl.parts) {
		return false
	}
	next := s.callRelayLocked(cl.relay)
	if next == "" {
		return false
	}
	s.log.Info("call moved to another relay", "channel", cl.channelID)
	cl.relay, cl.movedAt = next, now
	for _, p := range cl.parts {
		p.failedAt = time.Time{}
	}
	cl.key, cl.epoch = randomBytes(32), cl.epoch+1
	return true
}

// voiceRelayLost moves the calls on a relay the server lost.
func (s *Server) voiceRelayLost(url string) {
	v := s.voice
	v.mu.Lock()
	var outs []voiceOut
	for _, cl := range v.calls {
		if cl.relay == url && s.maybeMoveLocked(cl) {
			outs = append(outs, s.changedLocked(cl))
		}
	}
	v.mu.Unlock()
	for _, out := range outs {
		s.deliver(out, nil)
	}
}

func (s *Server) voiceLeave(ss *sess, l *wire.VoiceLeave) (*wire.ServerMessage, error) {
	v := s.voice
	v.mu.Lock()
	cl := v.calls[l.ChannelId]
	if cl != nil {
		s.removeLocked(cl, ss)
	}
	v.mu.Unlock()
	if cl != nil {
		s.afterVoiceChange(cl)
	}
	return okBody(), nil
}

// voiceSetState: a participant's own mute, deafen, camera and screen.
func (s *Server) voiceSetState(ss *sess, st *wire.VoiceState) (*wire.ServerMessage, error) {
	v := s.voice
	v.mu.Lock()
	cl := v.calls[st.ChannelId]
	var p *participant
	if cl != nil {
		p = cl.parts[ss]
	}
	if p == nil {
		v.mu.Unlock()
		return nil, errNotFound("you're not in this call")
	}
	p.selfMute, p.selfDeaf = st.SelfMute || st.SelfDeaf, st.SelfDeaf
	p.camera, p.screen = st.Camera && p.perms.video, st.Screen && p.perms.screen
	p.priorityOn = st.Priority
	updates := s.snapshotsLocked(cl)
	v.mu.Unlock()
	s.sendVoiceUpdates(updates, nil)
	s.broadcastVoiceRooms()
	return okBody(), nil
}

func (s *Server) voiceDisconnect(ss *sess) {
	if s.voice == nil {
		return
	}
	v := s.voice
	v.mu.Lock()
	var changed []*call
	for _, cl := range v.calls {
		if _, in := cl.parts[ss]; in {
			s.removeLocked(cl, ss)
			changed = append(changed, cl)
		}
	}
	delete(v.lastJoin, ss)
	v.mu.Unlock()
	s.afterVoiceChange(changed...)
}

// voiceEndChannel ends a channel's call (deleted, voice turned off).
func (s *Server) voiceEndChannel(channelID int64, reason string) {
	v := s.voice
	if v == nil {
		return
	}
	v.mu.Lock()
	cl := v.calls[channelID]
	var sessions []*sess
	if cl != nil {
		for ss, p := range cl.parts {
			sessions = append(sessions, ss)
			cl.gone[string(p.id)] = s.now()
		}
		cl.parts = map[*sess]*participant{}
		delete(v.calls, channelID)
	}
	var out voiceOut
	if cl != nil {
		out = s.changedLocked(cl)
	}
	v.mu.Unlock()
	if cl == nil {
		return
	}
	s.deliverControl(out.relay, out.control)
	for _, ss := range sessions {
		ss.send(voiceEnded(channelID, reason, 0))
	}
	s.broadcastVoiceRooms()
}

func voiceEnded(channelID int64, reason string, movedTo int64) *wire.ServerMessage {
	return &wire.ServerMessage{Body: &wire.ServerMessage_VoiceUpdate{VoiceUpdate: &wire.VoiceUpdate{
		Session: &wire.VoiceSession{ChannelId: channelID, Ended: reason, MovedTo: movedTo},
	}}}
}

// removeLocked drops ss from the call and rekeys it for whoever remains
// (v.mu held). The relay is told by afterVoiceChange.
func (s *Server) removeLocked(cl *call, ss *sess) {
	p := cl.parts[ss]
	if p == nil {
		return
	}
	delete(cl.parts, ss)
	cl.gone[string(p.id)] = s.now()
	if len(cl.parts) == 0 {
		// Kept until the relay has been told, then dropped (afterVoiceChange).
		return
	}
	cl.key, cl.epoch = randomBytes(32), cl.epoch+1
}

// afterVoiceChange tells the relay and the remaining participants about
// calls whose membership changed, and forgets calls nobody is in.
func (s *Server) afterVoiceChange(calls ...*call) {
	for _, cl := range calls {
		s.voice.mu.Lock()
		out := s.changedLocked(cl)
		if len(cl.parts) == 0 && s.voice.calls[cl.channelID] == cl {
			delete(s.voice.calls, cl.channelID)
		}
		s.voice.mu.Unlock()
		s.deliverControl(out.relay, out.control)
		s.sendVoiceUpdates(out.updates, nil)
	}
	if len(calls) > 0 {
		s.broadcastVoiceRooms()
	}
}

// voiceOut is what a change sends, worked out under v.mu.
type voiceOut struct {
	relay   string
	control *wire.SignedCallControl
	updates map[*sess]*wire.VoiceSession
}

// changedLocked builds the call's new control, then everyone's session
// (which carries that control) (v.mu held).
func (s *Server) changedLocked(cl *call) voiceOut {
	ctl := s.controlLocked(cl)
	return voiceOut{relay: cl.relay, control: ctl, updates: s.snapshotsLocked(cl)}
}

// deliver sends a change: the control to the relay, sessions to everyone
// but skip (who gets theirs as a reply), and the rooms to everyone.
func (s *Server) deliver(out voiceOut, skip *sess) {
	s.deliverControl(out.relay, out.control)
	s.sendVoiceUpdates(out.updates, skip)
	s.broadcastVoiceRooms()
}

// controlLocked builds and signs the call's current control and keeps it
// (v.mu held).
func (s *Server) controlLocked(cl *call) *wire.SignedCallControl {
	now := s.now()
	cl.seq++
	ctl := &wire.CallControl{CallId: cl.id, Seq: cl.seq, At: now.UnixMilli()}
	type goneEntry struct {
		id string
		at time.Time
	}
	var gone []goneEntry
	for id, at := range cl.gone {
		if now.Sub(at) > callauth.MaxGrantLifetime {
			delete(cl.gone, id)
			continue
		}
		gone = append(gone, goneEntry{id, at})
	}
	if len(gone) > maxGone {
		sort.Slice(gone, func(i, j int) bool { return gone[i].at.After(gone[j].at) })
		for _, g := range gone[maxGone:] {
			delete(cl.gone, g.id)
		}
		gone = gone[:maxGone]
	}
	for _, g := range gone {
		ctl.Removed = append(ctl.Removed, []byte(g.id))
	}
	s.mu.RLock()
	for _, p := range cl.parts {
		m := s.members[string(p.member)]
		if !p.perms.speak || (m != nil && m.VoiceMuted) {
			ctl.Muted = append(ctl.Muted, p.id)
		}
		if m != nil && m.VoiceDeafened {
			ctl.Deafened = append(ctl.Deafened, p.id)
		}
		if !p.perms.video {
			ctl.NoVideo = append(ctl.NoVideo, p.id)
		}
		if !p.perms.screen {
			ctl.NoScreen = append(ctl.NoScreen, p.id)
		}
	}
	s.mu.RUnlock()
	signed, err := callauth.SignControl(s.key, ctl)
	if err != nil {
		s.log.Error("call control", "err", err)
		return cl.signed
	}
	cl.signed = signed
	return signed
}

// deliverControl sends a control to the call's relay. If the relay doesn't
// have the call (nobody connected yet) or isn't reachable, the next
// participant to join brings the control along (CallJoin.control).
func (s *Server) deliverControl(relay string, signed *wire.SignedCallControl) {
	if signed == nil {
		return
	}
	s.rmu.RLock()
	rc := s.relays[relay]
	s.rmu.RUnlock()
	if rc == nil {
		return
	}
	if err := rc.CallUpdate(signed); err != nil {
		s.log.Warn("call control not delivered", "err", err)
	}
}

func (s *Server) sendVoiceUpdates(u map[*sess]*wire.VoiceSession, skip *sess) {
	for ss, st := range u {
		if ss != skip {
			ss.send(&wire.ServerMessage{Body: &wire.ServerMessage_VoiceUpdate{VoiceUpdate: &wire.VoiceUpdate{Session: st}}})
		}
	}
}

// snapshotsLocked builds each participant's view of the call, with a fresh
// grant (v.mu held).
func (s *Server) snapshotsLocked(cl *call) map[*sess]*wire.VoiceSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var parts []*wire.VoiceParticipant
	for _, p := range cl.parts {
		vp := &wire.VoiceParticipant{
			Member: p.member, Participant: p.id, Slot: p.slot, ListenOnly: !p.perms.speak,
			SelfMute: p.selfMute, SelfDeaf: p.selfDeaf, Camera: p.camera, Screen: p.screen, Priority: p.prioritySpeaking(),
		}
		if m := s.members[string(p.member)]; m != nil {
			vp.DisplayName = m.Name
			vp.ServerMute, vp.ServerDeaf = m.VoiceMuted, m.VoiceDeafened
		}
		parts = append(parts, vp)
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].Slot < parts[j].Slot })
	out := map[*sess]*wire.VoiceSession{}
	expires := s.now().Add(callauth.MaxGrantLifetime).UnixMilli()
	for ss, p := range cl.parts {
		m := s.members[string(p.member)]
		grant, err := callauth.SignGrant(s.key, &wire.CallGrant{
			CallId: cl.id, ServerKey: s.key.Public, CallNonce: cl.nonce, Participant: p.id, Kid: p.slot,
			Audio: p.perms.speak, Video: p.perms.video, Screen: p.perms.screen, Priority: p.perms.priority,
			ExpiresAt: expires, MaxParticipants: maxCallParticipants,
		})
		if err != nil {
			s.log.Error("call grant", "err", err)
			continue
		}
		vs := &wire.VoiceSession{
			ChannelId: cl.channelID, CallKey: cl.key, KeyEpoch: cl.epoch, Participants: parts, Relay: cl.relay, Control: cl.signed,
			ListenOnly: !p.perms.speak, Grant: grant, Participant: p.id, Slot: p.slot,
			CanVideo: p.perms.video, CanScreen: p.perms.screen, CanVoiceActivity: p.perms.vad, Priority: p.perms.priority,
		}
		if m != nil {
			vs.ServerMute, vs.ServerDeaf = m.VoiceMuted, m.VoiceDeafened
		}
		out[ss] = vs
	}
	return out
}

// voiceRecheck works out every participant's call permissions again (after
// admin actions, and periodically for timeouts that end). Anyone who lost
// Connect is disconnected; other changes reach the relay and the
// participants.
func (s *Server) voiceRecheck() {
	v := s.voice
	v.mu.Lock()
	var changed []*call
	var kicked []*sess
	var kickedFrom []int64
	s.mu.RLock()
	for _, cl := range v.calls {
		c := s.channels[cl.channelID]
		dirty := false
		for ss, p := range cl.parts {
			m := s.members[string(p.member)]
			if c == nil || s.allowed(FeatureVoice, c, m, PermViewChannel|PermConnectVoice) != nil {
				kicked, kickedFrom = append(kicked, ss), append(kickedFrom, cl.channelID)
				continue
			}
			if np := s.callPermsFor(m, c); np != p.perms {
				p.perms, dirty = np, true
				p.camera, p.screen = p.camera && np.video, p.screen && np.screen
			}
		}
		if dirty {
			changed = append(changed, cl)
		}
	}
	s.mu.RUnlock()
	for i, ss := range kicked {
		if cl := v.calls[kickedFrom[i]]; cl != nil {
			s.removeLocked(cl, ss)
			changed = append(changed, cl)
		}
	}
	v.mu.Unlock()
	for i, ss := range kicked {
		ss.send(voiceEnded(kickedFrom[i], "You can no longer connect to this voice channel.", 0))
	}
	s.afterVoiceChange(dedupCalls(changed)...)
}

func dedupCalls(calls []*call) []*call {
	seen := map[*call]bool{}
	out := calls[:0]
	for _, c := range calls {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

func (s *Server) voiceRecheckLoop(ctx context.Context) {
	t := time.NewTicker(voiceRecheckEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.voiceRecheck()
		}
	}
}

// voiceMemberChanged: a member's server mute or deafen changed.
func (s *Server) voiceMemberChanged(member []byte) {
	v := s.voice
	v.mu.Lock()
	var calls []*call
	for _, cl := range v.calls {
		for _, p := range cl.parts {
			if bytes.Equal(p.member, member) {
				calls = append(calls, cl)
				break
			}
		}
	}
	v.mu.Unlock()
	s.afterVoiceChange(calls...)
}

// ---------------------------------------------------------------- moderation (admin actions)

// voiceTarget checks a voice moderation action on target (s.mu held).
func (s *Server) voiceTarget(actor *memberRec, target []byte, perm uint64) (*memberRec, error) {
	if err := s.need(actor, perm); err != nil {
		return nil, err
	}
	m := s.members[string(target)]
	if m == nil || m.Status != wire.MemberStatus_MEMBER_STATUS_ACTIVE {
		return nil, errNotFound("no such member")
	}
	_, roles := s.roleInfos()
	if !bytes.Equal(target, actor.Key) && !Outranks(s.subject(actor), s.subject(m), roles) {
		return nil, errDenied("you can only do that to members below you")
	}
	return m, nil
}

func (s *Server) opVoiceMute(ctx context.Context, actor *memberRec, op *wire.VoiceMute, out *adminOutcome) error {
	m, err := s.voiceTarget(actor, op.Member, PermMuteMembers)
	if err != nil {
		return err
	}
	m.VoiceMuted = op.Muted
	if err := s.saveMember(ctx, s.db, m); err != nil {
		return err
	}
	verb := "server muted"
	if !op.Muted {
		verb = "server unmuted"
	}
	out.summary = fmt.Sprintf("%s %s", verb, m.Name)
	out.after = func() { s.voiceMemberChanged(op.Member) }
	return nil
}

func (s *Server) opVoiceDeafen(ctx context.Context, actor *memberRec, op *wire.VoiceDeafen, out *adminOutcome) error {
	m, err := s.voiceTarget(actor, op.Member, PermDeafenMembers)
	if err != nil {
		return err
	}
	m.VoiceDeafened = op.Deafened
	if err := s.saveMember(ctx, s.db, m); err != nil {
		return err
	}
	verb := "server deafened"
	if !op.Deafened {
		verb = "server undeafened"
	}
	out.summary = fmt.Sprintf("%s %s", verb, m.Name)
	out.after = func() { s.voiceMemberChanged(op.Member) }
	return nil
}

// opVoiceMove moves a member's call to another voice channel (or, with
// channel 0, disconnects them). Their client joins the new channel itself.
func (s *Server) opVoiceMove(_ context.Context, actor *memberRec, op *wire.VoiceMove, out *adminOutcome) error {
	m, err := s.voiceTarget(actor, op.Member, PermMoveMembers)
	if err != nil {
		return err
	}
	var to *channelRec
	if op.ChannelId != 0 {
		to = s.channels[op.ChannelId]
		if to == nil || to.Kind != wire.ChannelKind_CHANNEL_KIND_VOICE {
			return errNotFound("no such voice channel")
		}
		// The mover must be able to connect there, and so must the member.
		if err := s.allowed(FeatureVoice, to, actor, PermViewChannel|PermConnectVoice); err != nil {
			return err
		}
		if s.allowed(FeatureVoice, to, m, PermViewChannel|PermConnectVoice) != nil {
			return errDenied("they can't connect to that channel")
		}
	}
	// Who's in a call is the voice state's; it's checked after the action,
	// outside s.mu.
	if to != nil {
		out.summary = fmt.Sprintf("moved %s to %s", m.Name, to.Name)
	} else {
		out.summary = fmt.Sprintf("disconnected %s from voice", m.Name)
	}
	target, channel := op.Member, op.ChannelId
	out.after = func() { s.voiceMove(target, channel) }
	return nil
}

func (s *Server) voiceMove(member []byte, to int64) {
	v := s.voice
	v.mu.Lock()
	var changed []*call
	type moved struct {
		ss   *sess
		from int64
	}
	var ms []moved
	for _, cl := range v.calls {
		if cl.channelID == to {
			continue
		}
		for ss, p := range cl.parts {
			if bytes.Equal(p.member, member) {
				s.removeLocked(cl, ss)
				changed = append(changed, cl)
				ms = append(ms, moved{ss, cl.channelID})
			}
		}
	}
	v.mu.Unlock()
	reason := "A moderator disconnected you from voice."
	if to != 0 {
		reason = "A moderator moved you to another channel."
	}
	for _, x := range ms {
		x.ss.send(voiceEnded(x.from, reason, to))
	}
	s.afterVoiceChange(dedupCalls(changed)...)
	if len(ms) == 0 {
		s.log.Debug("voice move: member not in a call", "member", identity.Fingerprint(member))
	}
}

// ---------------------------------------------------------------- who's in which voice channel

type roomEntry struct {
	channel int64
	p       *wire.VoiceParticipant
}

// voiceRoomEntries lists everyone in a call, without names (v.mu taken here,
// never s.mu, so callers may hold neither).
func (s *Server) voiceRoomEntries() []roomEntry {
	v := s.voice
	v.mu.Lock()
	defer v.mu.Unlock()
	var out []roomEntry
	for _, cl := range v.calls {
		for _, p := range cl.parts {
			out = append(out, roomEntry{cl.channelID, &wire.VoiceParticipant{
				Member: p.member, Slot: p.slot, ListenOnly: !p.perms.speak, SelfMute: p.selfMute, SelfDeaf: p.selfDeaf,
				Camera: p.camera, Screen: p.screen, Priority: p.prioritySpeaking(),
			}})
		}
	}
	return out
}

// voiceRoomsFor is the rooms member may see (s.mu held for reading).
func (s *Server) voiceRoomsFor(member []byte, entries []roomEntry) *wire.VoiceRooms {
	viewer := s.members[string(member)]
	rooms := map[int64]*wire.VoiceRoom{}
	var order []int64
	for i := range entries {
		e := &entries[i]
		c := s.channels[e.channel]
		if c == nil || viewer == nil || s.channelPerms(viewer, c)&PermViewChannel == 0 {
			continue
		}
		r := rooms[e.channel]
		if r == nil {
			r = &wire.VoiceRoom{ChannelId: e.channel}
			rooms[e.channel] = r
			order = append(order, e.channel)
		}
		p := e.p
		vp := &wire.VoiceParticipant{
			Member: p.Member, Slot: p.Slot, ListenOnly: p.ListenOnly, SelfMute: p.SelfMute, SelfDeaf: p.SelfDeaf,
			Camera: p.Camera, Screen: p.Screen, Priority: p.Priority,
		}
		if m := s.members[string(p.Member)]; m != nil {
			vp.DisplayName = m.Name
			vp.ServerMute, vp.ServerDeaf = m.VoiceMuted, m.VoiceDeafened
		}
		r.Participants = append(r.Participants, vp)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	out := &wire.VoiceRooms{}
	for _, id := range order {
		r := rooms[id]
		sort.Slice(r.Participants, func(i, j int) bool { return r.Participants[i].Slot < r.Participants[j].Slot })
		out.Rooms = append(out.Rooms, r)
	}
	return out
}

// sendVoiceRooms sends the rooms to one session (after it syncs).
func (s *Server) sendVoiceRooms(ss *sess) {
	entries := s.voiceRoomEntries()
	s.mu.RLock()
	rooms := s.voiceRoomsFor(ss.member, entries)
	s.mu.RUnlock()
	ss.send(&wire.ServerMessage{Body: &wire.ServerMessage_VoiceRooms{VoiceRooms: rooms}})
}

// broadcastVoiceRooms sends every active session its view of the rooms.
func (s *Server) broadcastVoiceRooms() {
	entries := s.voiceRoomEntries()
	sessions := s.allSessions()
	s.mu.RLock()
	msgs := make([]*wire.ServerMessage, len(sessions))
	for i, ss := range sessions {
		if ss.getStatus() == wire.MemberStatus_MEMBER_STATUS_ACTIVE {
			msgs[i] = &wire.ServerMessage{Body: &wire.ServerMessage_VoiceRooms{VoiceRooms: s.voiceRoomsFor(ss.member, entries)}}
		}
	}
	s.mu.RUnlock()
	for i, ss := range sessions {
		if msgs[i] != nil {
			ss.send(msgs[i])
		}
	}
}
