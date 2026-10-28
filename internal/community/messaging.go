package community

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Messaging limits (docs/protocol.md §4.3).
const (
	MaxAttachments      = 10
	MaxReactionEmoji    = 20
	MaxPinsPerChannel   = 50
	MaxMentionedMembers = 100
	MaxMentionedRoles   = 20
	MaxNicknameChars    = 32
	MaxAboutChars       = 190
	MaxPronounsChars    = 40
	MaxEmojiChars       = 32
	// A staged upload must be posted within this time or it's deleted.
	StagedFileTTL = time.Hour
	// Minimum time between two typing events from one session in one channel.
	TypingInterval = 2 * time.Second
	// Profile changes (SetProfile, UpdateProfile) rebuild every session's
	// state: at most ProfileChangeBurst per member in any
	// ProfileChangeWindow, one every 2 s on average.
	ProfileChangeBurst  = 3
	ProfileChangeWindow = 6 * time.Second
)

// profileRateError limits profile changes per member; callers hold s.mu.
func (s *Server) profileRateError(member []byte) error {
	if !s.profileLimit.allowN(member, s.now(), ProfileChangeBurst, ProfileChangeWindow) {
		return errRateLimited("you're changing your profile too often; wait a few seconds")
	}
	return nil
}

// ---------------------------------------------------------------- mentions

// Mention tokens in message text: <@member> (base64url member key),
// <@&role> (role id), @everyone, @here and @channel. Tokens inside code spans and
// code blocks don't count.
var (
	mentionRE   = regexp.MustCompile(`<@(&?)([A-Za-z0-9_-]{1,64})>|@(everyone|here|channel)\b`)
	codeBlockRE = regexp.MustCompile("(?s)```.*?```|`[^`\n]*`")
)

type mentionSet struct {
	members                 [][]byte
	roles                   []int64
	everyone, here, channel bool
}

func (ms mentionSet) apply(m *wire.Message) {
	m.Mentions, m.MentionRoles, m.MentionEveryone, m.MentionHere = ms.members, ms.roles, ms.everyone, ms.here
	m.MentionChannel = ms.channel
}

// parseMentions finds the mentions in text that the author may make in c.
// Mentions the author isn't allowed are left in the text but not marked, so
// they notify nobody (the message itself is still sent, as Discord does).
// @everyone, @here, @channel and roles that aren't mentionable need Mention
// @everyone/@here and All Roles; a member mention counts only if that member
// can read the channel. Callers hold s.mu.
func (s *Server) parseMentions(author *memberRec, c *channelRec, text string) mentionSet {
	var out mentionSet
	if text == "" {
		return out
	}
	canAll := s.channelPerms(author, c)&PermMentionEveryone != 0
	seenM := map[string]bool{}
	seenR := map[int64]bool{}
	for _, m := range mentionRE.FindAllStringSubmatch(codeBlockRE.ReplaceAllString(text, " "), -1) {
		switch {
		case m[3] == "everyone":
			out.everyone = out.everyone || canAll
		case m[3] == "here":
			out.here = out.here || canAll
		case m[3] == "channel":
			out.channel = out.channel || canAll
		case m[1] == "&":
			id, err := strconv.ParseInt(m[2], 10, 64)
			r := s.roles[id]
			if err != nil || r == nil || id == s.everyoneID || seenR[id] || len(out.roles) >= MaxMentionedRoles {
				continue
			}
			if canAll || r.Mentionable {
				seenR[id] = true
				out.roles = append(out.roles, id)
			}
		default:
			key, err := identity.Decode(m[2])
			if err != nil || seenM[string(key)] || len(out.members) >= MaxMentionedMembers {
				continue
			}
			target := s.members[string(key)]
			if target == nil || s.channelPerms(target, c)&PermViewChannel == 0 {
				continue
			}
			seenM[string(key)] = true
			out.members = append(out.members, key)
		}
	}
	return out
}

// ---------------------------------------------------------------- reactions

// Custom emoji references are ":name:id" (docs/protocol.md §4.3).
var customEmojiRE = regexp.MustCompile(`^:[A-Za-z0-9_]{2,32}:[1-9][0-9]{0,18}$`)

// validEmoji accepts a custom emoji reference, or a short Unicode string that
// isn't plain ASCII text (one emoji, possibly with modifiers and joiners).
func validEmoji(e string) error {
	if customEmojiRE.MatchString(e) {
		return nil
	}
	n := utf8.RuneCountInString(e)
	if !utf8.ValidString(e) || n == 0 || n > MaxEmojiChars {
		return errInvalid("that isn't an emoji")
	}
	ascii := true
	for _, r := range e {
		if unicode.IsSpace(r) || unicode.IsControl(r) || bidiControl(r) {
			return errInvalid("that isn't an emoji")
		}
		if r > unicode.MaxASCII {
			ascii = false
		}
	}
	if ascii {
		return errInvalid("that isn't an emoji")
	}
	return nil
}

// bidiControl reports the directional marks, embeddings, overrides and
// isolates (U+061C, U+200E, U+200F, U+202A to U+202E, U+2066 to U+2069), which
// could reorder the text shown around a reaction. The zero-width joiner and
// variation selectors stay allowed: emoji sequences need them.
func bidiControl(r rune) bool {
	return r == 0x061c || r == 0x200e || r == 0x200f || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
}

// MaxReactors bounds the reactions on one message in all (members times
// emoji): each is a 32-byte key in the sealed body, which is re-sent to
// every member on each change. A variable for tests.
var MaxReactors = 1000

// reactorCount is the number of (member, emoji) reactions on a message.
func reactorCount(rs []*wire.Reaction) int {
	n := 0
	for _, rx := range rs {
		n += len(rx.Members)
	}
	return n
}

func (s *Server) react(ctx context.Context, ss *sess, r *wire.React) (*wire.ServerMessage, error) {
	if err := validEmoji(r.Emoji); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	row, body, err := s.loadForUpdate(ctx, r.MessageId)
	if err != nil {
		return nil, err
	}
	// Only a message the member may read (the same gate as history).
	c, err := s.readableMessage(ss, row)
	if err != nil {
		return nil, err
	}
	if row.deleted {
		return nil, errNotFound("message was deleted")
	}
	if err := s.allowed(FeatureReactions, c, nil, 0); err != nil {
		return nil, err
	}
	me := s.members[string(ss.member)]
	if !r.Remove {
		// A timeout or member screening holds reactions back too.
		if err := s.participationError(me); err != nil {
			return nil, err
		}
		if s.channelPerms(me, c)&PermAddReactions == 0 {
			return nil, errDenied("you can't add reactions in this channel")
		}
		// A custom emoji must exist, the toggle be on, and the member have
		// Use Custom Emoji here (emoji.go).
		if customEmojiRE.MatchString(r.Emoji) {
			if _, err := s.useCustomEmoji(me, c, r.Emoji); err != nil {
				return nil, err
			}
		}
	}
	idx := -1
	for i, rx := range body.Reactions {
		if rx.Emoji == r.Emoji {
			idx = i
		}
	}
	changed := false
	switch {
	case r.Remove && idx >= 0:
		rx := body.Reactions[idx]
		for i, k := range rx.Members {
			if string(k) == string(ss.member) {
				rx.Members = append(rx.Members[:i], rx.Members[i+1:]...)
				changed = true
				break
			}
		}
		if len(rx.Members) == 0 {
			body.Reactions = append(body.Reactions[:idx], body.Reactions[idx+1:]...)
		}
	case !r.Remove && idx >= 0:
		rx := body.Reactions[idx]
		found := false
		for _, k := range rx.Members {
			found = found || string(k) == string(ss.member)
		}
		if !found {
			if reactorCount(body.Reactions) >= MaxReactors {
				return nil, errConflict(fmt.Sprintf("a message can have at most %d reactions", MaxReactors))
			}
			rx.Members = append(rx.Members, ss.member)
			changed = true
		}
	case !r.Remove:
		if len(body.Reactions) >= MaxReactionEmoji {
			return nil, errConflict(fmt.Sprintf("a message can have at most %d different reactions", MaxReactionEmoji))
		}
		if reactorCount(body.Reactions) >= MaxReactors {
			return nil, errConflict(fmt.Sprintf("a message can have at most %d reactions", MaxReactors))
		}
		body.Reactions = append(body.Reactions, &wire.Reaction{Emoji: r.Emoji, Members: [][]byte{ss.member}})
		changed = true
	}
	if changed {
		if err := s.rewriteBody(ctx, row, body); err != nil {
			return nil, err
		}
	}
	// The reply and the published update carry the reactions only, never
	// the message body.
	u := &wire.MessageUpdate{MessageId: row.id, ChannelId: c.ID, Change: &wire.MessageUpdate_Reactions{Reactions: &wire.ReactionList{Reactions: body.Reactions}}}
	if changed {
		s.publishUpdate(c, u)
	}
	return &wire.ServerMessage{Body: &wire.ServerMessage_MessageUpdate{MessageUpdate: u}}, nil
}

// readableMessage checks that the member may read message r: View Channel
// and Read Message History in its channel, and with "new members see
// history" off, only messages from the member's join time on (the gate
// history applies). It returns the channel. Callers hold s.mu.
func (s *Server) readableMessage(ss *sess, r storedMessage) (*channelRec, error) {
	c, err := s.readableChannel(ss, r.channelID, PermViewChannel)
	if err != nil {
		return nil, err
	}
	m := s.members[string(ss.member)]
	if s.channelPerms(m, c)&PermReadMessageHistory == 0 {
		return nil, errDenied("you can't read the message history of this channel")
	}
	if !s.historyVisible(m, c, r.created) {
		return nil, errNotFound("no such message")
	}
	return c, nil
}

// publishUpdate sends a MessageUpdate (reactions, pin state or preview;
// never the message text) to the channel topic.
func (s *Server) publishUpdate(c *channelRec, u *wire.MessageUpdate) {
	s.publishEvent(c, &wire.ChannelEvent{Kind: &wire.ChannelEvent_Update{Update: u}})
}

// ---------------------------------------------------------------- pins

// loadPins reads the pinned message ids at startup.
func (s *Server) loadPins(ctx context.Context) error {
	s.pinned = map[int64]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT message_id FROM pins`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		s.pinned[id] = true
	}
	return rows.Err()
}

func (s *Server) pin(ctx context.Context, ss *sess, p *wire.Pin) (*wire.ServerMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, body, err := s.loadForUpdate(ctx, p.MessageId)
	if err != nil {
		return nil, err
	}
	c, err := s.readableMessage(ss, row)
	if err != nil {
		return nil, err
	}
	if row.deleted {
		return nil, errNotFound("message was deleted")
	}
	if s.channelPerms(s.members[string(ss.member)], c)&PermManageMessages == 0 {
		return nil, errDenied("pinning needs the Manage Messages permission")
	}
	changed := false
	if p.Unpin {
		if s.pinned[row.id] {
			if _, err := s.db.ExecContext(ctx, `DELETE FROM pins WHERE message_id = ?`, row.id); err != nil {
				return nil, err
			}
			delete(s.pinned, row.id)
			changed = true
		}
	} else if !s.pinned[row.id] {
		n, err := s.count(ctx, `SELECT count(*) FROM pins WHERE channel_id = ?`, c.ID)
		if err != nil {
			return nil, err
		}
		if n >= MaxPinsPerChannel {
			return nil, errConflict(fmt.Sprintf("a channel can have at most %d pinned messages", MaxPinsPerChannel))
		}
		rec := &pinRec{By: ss.member, At: s.now().UnixMilli(), ChannelID: c.ID}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO pins (message_id, channel_id, data) VALUES (?, ?, ?)`,
			row.id, c.ID, s.sealJSON("pins", "data", rowID(row.id), rec)); err != nil {
			return nil, err
		}
		s.pinned[row.id] = true
		changed = true
	}
	u := &wire.MessageUpdate{MessageId: row.id, ChannelId: c.ID, Change: &wire.MessageUpdate_Pinned{Pinned: s.pinned[row.id]}}
	if changed {
		op, verb := "pin", "pinned"
		if p.Unpin {
			op, verb = "unpin", "unpinned"
		}
		s.recordAudit(ctx, ss.member, op, body.Author, c.ID, fmt.Sprintf("%s a message by %s in #%s", verb, s.nameOf(body.Author), c.Name))
		s.publishUpdate(c, u)
	}
	return &wire.ServerMessage{Body: &wire.ServerMessage_MessageUpdate{MessageUpdate: u}}, nil
}

func (s *Server) listPins(ctx context.Context, ss *sess, l *wire.ListPins) (*wire.ServerMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, err := s.readableChannel(ss, l.ChannelId, PermViewChannel)
	if err != nil {
		return nil, err
	}
	// Pins are history: the same gate as history (Read Message History, and
	// below, nothing from before the member joined when new members don't
	// see history).
	me := s.members[string(ss.member)]
	if s.channelPerms(me, c)&PermReadMessageHistory == 0 {
		return nil, errDenied("you can't read the message history of this channel")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT message_id, data FROM pins WHERE channel_id = ?`, c.ID)
	if err != nil {
		return nil, err
	}
	type pinned struct {
		id int64
		at int64
	}
	var list []pinned
	for rows.Next() {
		var id int64
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			rows.Close()
			return nil, err
		}
		var rec pinRec
		if err := s.openJSON("pins", "data", rowID(id), data, &rec); err != nil {
			rows.Close()
			return nil, err
		}
		if rec.ChannelID != c.ID {
			rows.Close()
			return nil, &TamperError{"pins", "channel_id", rowID(id)}
		}
		list = append(list, pinned{id, rec.At})
	}
	rows.Close()
	sort.Slice(list, func(i, j int) bool {
		if list[i].at != list[j].at {
			return list[i].at > list[j].at
		}
		return list[i].id > list[j].id
	})
	out := &wire.PinList{}
	for _, p := range list {
		r, err := scanStored(s.db.QueryRowContext(ctx, `SELECT `+msgCols+` FROM messages WHERE id = ?`, p.id))
		if err != nil || r.deleted {
			continue
		}
		if r.channelID != c.ID {
			return nil, &TamperError{"pins", "channel_id", rowID(p.id)}
		}
		if !s.historyVisible(me, c, r.created) {
			continue
		}
		m, err := s.scanMessage(ctx, r)
		if err != nil {
			return nil, err
		}
		out.Messages = append(out.Messages, m)
	}
	return &wire.ServerMessage{Body: &wire.ServerMessage_Pins{Pins: out}}, nil
}

// ---------------------------------------------------------------- where messages go

// messagesAllowed says whether a channel takes messages: text channels do,
// and so do voice channels (their text chat, docs/protocol.md §4.3) while
// the community has voice channels on. Permissions, slowmode and the
// channel's feature settings apply to both alike. Callers hold s.mu.
func (s *Server) messagesAllowed(c *channelRec) error {
	switch c.Kind {
	case wire.ChannelKind_CHANNEL_KIND_TEXT:
		return nil
	case wire.ChannelKind_CHANNEL_KIND_VOICE:
		return s.allowed(FeatureVoice, nil, nil, 0)
	}
	return errInvalid("this channel doesn't take messages")
}

// ---------------------------------------------------------------- typing

// typing publishes a live TypingEvent to the channel topic. Nothing is
// stored; one session may send one per channel every TypingInterval.
func (s *Server) typing(ss *sess, t *wire.Typing) (*wire.ServerMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// A timed-out or screened member can't start typing (their Send
	// Messages is held back too; this says why and until when).
	if err := s.participationError(s.members[string(ss.member)]); err != nil {
		return nil, err
	}
	c, err := s.readableChannel(ss, t.ChannelId, PermViewChannel|PermSendMessages)
	if err != nil {
		return nil, err
	}
	if err := s.messagesAllowed(c); err != nil {
		return nil, err
	}
	if err := s.allowed(FeatureTyping, c, nil, 0); err != nil {
		return nil, err
	}
	now := s.now()
	ss.mu.Lock()
	if ss.typing == nil {
		ss.typing = map[int64]time.Time{}
	}
	last := ss.typing[c.ID]
	if now.Sub(last) < TypingInterval {
		ss.mu.Unlock()
		return nil, errRateLimited("typing events are limited to one every 2 seconds")
	}
	ss.typing[c.ID] = now
	ss.mu.Unlock()
	s.publishEvent(c, &wire.ChannelEvent{Kind: &wire.ChannelEvent_Typing{Typing: &wire.TypingEvent{Member: ss.member, Name: s.shownName(ss.member)}}})
	return okBody(), nil
}

// ---------------------------------------------------------------- presence

// isOnline reports whether a member has an admitted session.
func (s *Server) isOnline(member []byte) bool {
	s.smu.Lock()
	defer s.smu.Unlock()
	return s.onlineLocked(member)
}

func (s *Server) onlineLocked(member []byte) bool {
	for ss := range s.sessions {
		if string(ss.member) == string(member) && ss.getStatus() == wire.MemberStatus_MEMBER_STATUS_ACTIVE {
			return true
		}
	}
	return false
}

// presenceInterval is how often presence changes go out: one batch per
// session per interval, however many members came and went. Only
// transitions count (first session opened, last one closed), so a member
// reconnecting on several devices costs nothing extra. Sending each change
// on its own would make a burst of joins or reconnects cost (changes ×
// online sessions) frames on the server's relay connection
// (docs/load-tests.md).
var presenceInterval = 500 * time.Millisecond

// presenceChanged records a member's online state for the next batch. An
// invisible member counts as offline (status.go): their sessions work as
// usual, but nobody is told they're there.
func (s *Server) presenceChanged(member []byte) {
	s.smu.Lock()
	defer s.smu.Unlock()
	now := s.onlineLocked(member) && !s.invisible[string(member)]
	if s.presence == nil {
		s.presence = map[string]bool{}
	}
	if s.presence[string(member)] == now {
		return
	}
	if now {
		s.presence[string(member)] = true
	} else {
		delete(s.presence, string(member))
	}
	if s.presencePending == nil {
		s.presencePending = map[string]bool{}
	}
	s.presencePending[string(member)] = now
}

func (s *Server) presenceLoop(ctx context.Context) {
	t := time.NewTicker(presenceInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.flushPresence()
		}
	}
}

// flushPresence sends the changes since the last batch to every active
// session, as one message each: presence, and the status entries
// (status.go) that changed.
func (s *Server) flushPresence() {
	s.smu.Lock()
	if len(s.presencePending) == 0 && len(s.statusPending) == 0 {
		s.smu.Unlock()
		return
	}
	batch := &wire.PresenceBatch{Updates: make([]*wire.PresenceUpdate, 0, len(s.presencePending))}
	for m, online := range s.presencePending {
		batch.Updates = append(batch.Updates, &wire.PresenceUpdate{Member: []byte(m), Online: online})
	}
	s.presencePending = nil
	pendingStatus := make([]string, 0, len(s.statusPending))
	for m := range s.statusPending {
		pendingStatus = append(pendingStatus, m)
	}
	s.statusPending = nil
	var targets []*sess
	for ss := range s.sessions {
		if ss.getStatus() == wire.MemberStatus_MEMBER_STATUS_ACTIVE {
			targets = append(targets, ss)
		}
	}
	s.smu.Unlock()
	if len(pendingStatus) > 0 {
		var rest []string
		batch.Statuses, rest = s.statusEntries(pendingStatus)
		for _, m := range rest {
			s.statusChanged([]byte(m))
		}
	}
	msg := &wire.ServerMessage{Body: &wire.ServerMessage_PresenceBatch{PresenceBatch: batch}}
	for _, ss := range targets {
		ss.send(msg)
	}
}

// ---------------------------------------------------------------- nicknames and profiles

func validProfileText(v string, max int, what string, multiline bool) (string, error) {
	v = strings.TrimSpace(v)
	if utf8.RuneCountInString(v) > max {
		return "", errInvalid(fmt.Sprintf("%s may be at most %d characters", what, max))
	}
	for _, r := range v {
		if (r < 0x20 && (!multiline || r != '\n')) || r == 0x7f {
			return "", errInvalid(what + " contains control characters")
		}
	}
	return v, nil
}

func (s *Server) opSetNickname(ctx context.Context, actor *memberRec, n *wire.SetNickname, out *adminOutcome) error {
	target := s.members[string(n.Member)]
	if target == nil || target.Status != wire.MemberStatus_MEMBER_STATUS_ACTIVE {
		return errNotFound("no such member")
	}
	nick, err := validProfileText(n.Nickname, MaxNicknameChars, "nicknames", false)
	if err != nil {
		return err
	}
	self := string(target.Key) == string(actor.Key)
	if self {
		if s.timedOut(actor) {
			return errDenied(fmt.Sprintf("You're timed out until %s and can't change your nickname.", fmtUTC(actor.TimeoutUntil)))
		}
		if err := s.need(actor, PermChangeNickname); err != nil {
			return errDenied("changing your nickname needs the Change Nickname permission")
		}
	} else {
		if err := s.need(actor, PermManageNicknames); err != nil {
			return errDenied("changing others' nicknames needs the Manage Nicknames permission")
		}
		_, roles := s.roleInfos()
		if !Outranks(s.subject(actor), s.subject(target), roles) {
			return errDenied("you can only change the nicknames of members below your highest role")
		}
	}
	if nick == target.Nick {
		// Nothing changes: no save, audit entry or state rebuild.
		return nil
	}
	if self {
		// Like SetProfile and UpdateProfile: each change rebuilds every
		// session's state, and Change Nickname is an @everyone default.
		if err := s.profileRateError(actor.Key); err != nil {
			return err
		}
	}
	next := *target
	next.Nick = nick
	if err := s.saveMember(ctx, s.db, &next); err != nil {
		return err
	}
	*target = next
	switch {
	case self && nick == "":
		out.summary = "cleared their nickname"
	case self:
		out.summary = fmt.Sprintf("changed their nickname to %q", nick)
	case nick == "":
		out.summary = fmt.Sprintf("cleared %s's nickname", target.Name)
	default:
		out.summary = fmt.Sprintf("changed %s's nickname to %q", target.Name, nick)
	}
	out.broadcast = true
	return nil
}

func (s *Server) updateProfile(ctx context.Context, ss *sess, p *wire.UpdateProfile) (*wire.ServerMessage, error) {
	about, err := validProfileText(p.About, MaxAboutChars, "about text", true)
	if err != nil {
		return nil, err
	}
	pronouns, err := validProfileText(p.Pronouns, MaxPronounsChars, "pronouns", false)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	m := s.members[string(ss.member)]
	if m == nil {
		s.mu.Unlock()
		return nil, errNotMember()
	}
	if s.timedOut(m) && (about != m.About || pronouns != m.Pronouns) {
		s.mu.Unlock()
		return nil, errDenied(fmt.Sprintf("You're timed out until %s and can't change your profile.", fmtUTC(m.TimeoutUntil)))
	}
	if err := s.profileRateError(ss.member); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	next := *m
	next.About, next.Pronouns = about, pronouns
	if err := s.saveMember(ctx, s.db, &next); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	*m = next
	s.mu.Unlock()
	s.broadcastState()
	return okBody(), nil
}

// ---------------------------------------------------------------- system messages

// postSystem posts a join or leave message in the join/leave channel when
// that toggle is on. Failures are logged: membership changes never fail
// because of them. Callers hold s.mu.
func (s *Server) postSystem(ctx context.Context, kind wire.SystemEventKind, member []byte) {
	if !featureOn(s.settings, FeatureJoinLeave) {
		return
	}
	c := s.joinLeaveChannel()
	if c == nil {
		return
	}
	body := &wire.Message{Author: member, System: &wire.SystemEvent{Kind: kind, Member: member}}
	if _, err := s.insertMessage(ctx, c, body, nil); err != nil {
		s.log.Error("posting a join/leave message failed", "channel", c.ID, "err", err)
	}
}

// ---------------------------------------------------------------- attachments

// claimStaged checks that every file id is a staged upload of this member in
// this channel, and that attachments are allowed here. Callers hold s.mu.
func (s *Server) claimStaged(ctx context.Context, ss *sess, c *channelRec, ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > MaxAttachments {
		return nil, errTooLarge(fmt.Sprintf("a message can carry at most %d files", MaxAttachments))
	}
	if err := s.allowed(FeatureAttachments, c, nil, 0); err != nil {
		return nil, err
	}
	if s.channelPerms(s.members[string(ss.member)], c)&PermAttachFiles == 0 {
		return nil, errDenied("you can't attach files in this channel")
	}
	seen := map[int64]bool{}
	var out []int64
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		var channelID, messageID int64
		err := s.db.QueryRowContext(ctx, `SELECT channel_id, message_id FROM files WHERE id = ? AND complete = 1`, id).Scan(&channelID, &messageID)
		if err != nil || channelID != c.ID || messageID != 0 {
			return nil, errNotFound("an attachment is missing, was already posted, or belongs to another channel")
		}
		_, meta, err := s.loadFileMeta(ctx, id)
		if err != nil {
			return nil, err
		}
		if string(meta.Uploader) != string(ss.member) {
			return nil, errNotFound("an attachment is missing, was already posted, or belongs to another channel")
		}
		if err := s.attachmentKindAllowed(meta.Mime, nil); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// expireStaged deletes staged uploads nobody posted within StagedFileTTL.
func (s *Server) expireStaged(ctx context.Context, now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, `DELETE FROM files WHERE message_id = 0 AND created_at < ?`, now.Add(-StagedFileTTL).UnixMilli())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

var errNoMessage = errors.New("message is gone")

// updatePreview reloads a message, lets f set its link preview and, if f
// reports a change, reseals it and publishes the preview as a MessageUpdate
// (without the message text). Callers hold s.mu.
func (s *Server) updatePreview(ctx context.Context, id int64, f func(body *wire.Message) bool) error {
	r, body, err := s.loadForUpdate(ctx, id)
	if err != nil {
		return err
	}
	if r.deleted {
		return errNoMessage
	}
	c := s.channels[r.channelID]
	if c == nil {
		return errNoMessage
	}
	body = proto.Clone(body).(*wire.Message)
	if !f(body) {
		return nil
	}
	if err := s.rewriteBody(ctx, r, body); err != nil {
		return err
	}
	view := s.messageView(ctx, r, body)
	s.publishUpdate(c, &wire.MessageUpdate{MessageId: r.id, ChannelId: c.ID, Change: &wire.MessageUpdate_Preview{Preview: view.Preview}})
	return nil
}
