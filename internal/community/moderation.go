package community

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Moderation: timeouts, member screening, bulk delete and a ban's history
// deletion (docs/protocol.md §4.7).

// Timeout bounds (Discord's: 60 seconds to 28 days).
const (
	MinTimeoutSeconds = 60
	MaxTimeoutSeconds = 28 * 24 * 3600
	// MaxBulkDelete is the most messages one bulk delete removes.
	MaxBulkDelete = 100
)

// banDeleteWindows are the history windows a ban may delete (none, 1 h, 6 h,
// 24 h, 3 days, 7 days).
var banDeleteWindows = map[uint32]string{
	0: "", 3600: "the last hour", 21600: "the last 6 hours", 86400: "the last 24 hours",
	259200: "the last 3 days", 604800: "the last 7 days",
}

// timedOut reports whether m is timed out right now. Owners and
// administrators never are (as in Discord; opTimeout refuses to time them
// out, and a timeout stops applying when the member becomes an
// administrator). Callers hold s.mu. Other server code (voice Speak,
// reactions, typing) calls this, or uses channelPerms, which already
// removes PermTimeoutMask from a timed-out member.
func (s *Server) timedOut(m *memberRec) bool {
	if m == nil || m.TimeoutUntil == 0 || m.TimeoutUntil <= s.now().UnixMilli() || s.isOwner(m) {
		return false
	}
	ev, roles := s.roleInfos()
	return BasePerms(s.subject(m), ev, roles)&PermAdministrator == 0
}

// screenedUntil returns when member screening lets m post, react and start
// DMs, or 0 if it already does. Owners and members holding any role are
// exempt (a role means someone vetted them). Callers hold s.mu.
func (s *Server) screenedUntil(m *memberRec) int64 {
	mins := s.settings.ScreeningMinutes
	if m == nil || mins == 0 || len(m.Roles) > 0 || s.isOwner(m) {
		return 0
	}
	at := m.JoinedAt + int64(mins)*60_000
	if at <= s.now().UnixMilli() {
		return 0
	}
	return at
}

// newMemberUntil is when m's new-member restrictions end (no links, files
// or mentions; settings.new_member_minutes), or 0 when they don't apply:
// off, already over, or m has a role or is an owner (as for screening).
// Callers hold s.mu.
func (s *Server) newMemberUntil(m *memberRec) int64 {
	mins := s.settings.NewMemberMinutes
	if m == nil || mins == 0 || len(m.Roles) > 0 || s.isOwner(m) {
		return 0
	}
	at := m.JoinedAt + int64(mins)*60_000
	if at <= s.now().UnixMilli() {
		return 0
	}
	return at
}

// newMemberTextError refuses text with a link or a mention from a member
// under new-member restrictions. Callers hold s.mu.
func (s *Server) newMemberTextError(m *memberRec, text string) error {
	at := s.newMemberUntil(m)
	if at == 0 || text == "" {
		return nil
	}
	if linkRE.MatchString(text) || mentionRE.MatchString(codeBlockRE.ReplaceAllString(text, " ")) {
		return errDenied(fmt.Sprintf("New members can post links and mentions from %s.", fmtUTC(at)))
	}
	return nil
}

// restrictedPerms are the permission bits a timeout, member screening or
// new-member restrictions currently take away from m. Callers hold s.mu.
func (s *Server) restrictedPerms(m *memberRec) uint64 {
	var r uint64
	if s.timedOut(m) {
		r |= PermTimeoutMask
	}
	if s.screenedUntil(m) != 0 {
		r |= PermScreeningMask
	}
	if s.newMemberUntil(m) != 0 {
		r |= PermNewMemberMask
	}
	return r
}

func fmtUTC(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04 UTC")
}

// participationError explains why m can't add to a conversation right now
// (post, edit, react, attach, start typing), or returns nil. Callers hold
// s.mu. Its messages name the time the restriction ends.
func (s *Server) participationError(m *memberRec) error {
	if s.timedOut(m) {
		return errDenied(fmt.Sprintf("You're timed out until %s, so you can read but not post.", fmtUTC(m.TimeoutUntil)))
	}
	if at := s.screenedUntil(m); at != 0 {
		return errDenied(fmt.Sprintf("New members can post from %s (member screening).", fmtUTC(at)))
	}
	return nil
}

// humanSeconds renders a duration for audit summaries.
func humanSeconds(sec uint32) string {
	switch {
	case sec%86400 == 0:
		return plural(int(sec/86400), "day")
	case sec%3600 == 0:
		return plural(int(sec/3600), "hour")
	case sec%60 == 0:
		return plural(int(sec/60), "minute")
	}
	return plural(int(sec), "second")
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// opTimeout times a member out, or lifts a timeout (seconds = 0).
func (s *Server) opTimeout(ctx context.Context, actor *memberRec, t *wire.TimeoutMember, out *adminOutcome) error {
	if err := s.need(actor, PermTimeoutMembers); err != nil {
		return err
	}
	if utf8.RuneCountInString(t.Reason) > 512 {
		return errInvalid("reason may be at most 512 characters")
	}
	if t.Seconds != 0 && (t.Seconds < MinTimeoutSeconds || t.Seconds > MaxTimeoutSeconds) {
		return errInvalid("a timeout lasts from 60 seconds to 28 days")
	}
	m := s.members[string(t.Member)]
	if m == nil || m.Status != wire.MemberStatus_MEMBER_STATUS_ACTIVE {
		return errNotFound("no such member")
	}
	if string(m.Key) == string(actor.Key) {
		return errInvalid("you can't time yourself out")
	}
	if s.isOwner(m) {
		return errDenied("owners can't be timed out")
	}
	ev, roles := s.roleInfos()
	if !Outranks(s.subject(actor), s.subject(m), roles) {
		return errDenied("you can only time out members below you")
	}
	if BasePerms(s.subject(m), ev, roles)&PermAdministrator != 0 {
		return errDenied("administrators can't be timed out")
	}
	until := int64(0)
	if t.Seconds > 0 {
		until = s.now().UnixMilli() + int64(t.Seconds)*1000
	}
	next := *m
	next.TimeoutUntil = until
	if err := s.saveMember(ctx, s.db, &next); err != nil {
		return err
	}
	*m = next
	if until == 0 {
		out.summary = fmt.Sprintf("removed the timeout of %s", s.nameOf(m.Key))
	} else {
		out.summary = fmt.Sprintf("timed out %s for %s", s.nameOf(m.Key), humanSeconds(t.Seconds))
		out.after = func() { s.scheduleTimeoutEnd(until) }
	}
	if t.Reason != "" {
		out.summary += ": " + t.Reason
	}
	out.broadcast = true
	return nil
}

// scheduleTimeoutEnd pushes fresh state when a timeout ends, so the member's
// composer unlocks and moderators' lists update without a reconnect.
func (s *Server) scheduleTimeoutEnd(until int64) {
	d := time.Until(time.UnixMilli(until)) + 500*time.Millisecond
	if d < 0 {
		d = 0
	}
	time.AfterFunc(d, s.broadcastState)
}

// scheduleTimeoutEnds re-arms the timers for timeouts still running after
// a restart.
func (s *Server) scheduleTimeoutEnds() {
	s.mu.RLock()
	now := s.now().UnixMilli()
	var ends []int64
	for _, m := range s.members {
		if m.TimeoutUntil > now {
			ends = append(ends, m.TimeoutUntil)
		}
	}
	s.mu.RUnlock()
	for _, e := range ends {
		s.scheduleTimeoutEnd(e)
	}
}

// deleteStored marks messages deleted (keeping only their author, as a
// single delete does), drops their attachments and search entries, and
// publishes one DeletedMessages event per channel. Callers hold s.mu.
func (s *Server) deleteStored(ctx context.Context, rows []storedMessage, authors [][]byte) (int, error) {
	byChannel := map[int64][]int64{}
	for i, r := range rows {
		body, err := s.sealBody(ctx, r.id, r.month, &wire.Message{ChannelId: r.channelID, CreatedAt: r.created, Author: authors[i]}, false)
		if err != nil {
			return 0, err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE messages SET deleted = 1, body = ? WHERE id = ?`, body, r.id); err != nil {
			return 0, err
		}
		// Its attachments and link preview image go with it, and it's unpinned.
		s.bestEffort(ctx, `DELETE FROM files WHERE message_id = ?`, r.id)
		s.bestEffort(ctx, `DELETE FROM pins WHERE message_id = ?`, r.id)
		delete(s.pinned, r.id)
		s.unindexMessage(r.id)
		byChannel[r.channelID] = append(byChannel[r.channelID], r.id)
	}
	for cid, ids := range byChannel {
		if c := s.channels[cid]; c != nil {
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			s.publishEvent(c, &wire.ChannelEvent{Kind: &wire.ChannelEvent_DeletedMany{DeletedMany: &wire.DeletedMessages{Ids: ids}}})
		}
	}
	return len(rows), nil
}

// recentAuthors indexes, in memory only, which messages each member posted
// in the last 7 days (the longest window a ban can delete). Authors are
// sealed in the database, so without it a ban would decrypt a week of the
// whole community's messages; with it, only the banned member's.
type recentAuthors struct {
	mu   sync.Mutex
	byAu map[string][]recentMsg
}

type recentMsg struct{ id, created int64 }

const recentWindow = 7 * 24 * time.Hour

func (r *recentAuthors) add(author []byte, id, created int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byAu == nil {
		r.byAu = map[string][]recentMsg{}
	}
	r.byAu[string(author)] = append(r.byAu[string(author)], recentMsg{id, created})
}

// since lists the ids author posted at or after `since`.
func (r *recentAuthors) since(author []byte, since int64) []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []int64
	for _, m := range r.byAu[string(author)] {
		if m.created >= since {
			out = append(out, m.id)
		}
	}
	return out
}

// drop forgets messages (deleted with their channel).
func (r *recentAuthors) drop(ids []int64) {
	gone := make(map[int64]bool, len(ids))
	for _, id := range ids {
		gone[id] = true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, list := range r.byAu {
		keep := list[:0]
		for _, m := range list {
			if !gone[m.id] {
				keep = append(keep, m)
			}
		}
		if len(keep) == 0 {
			delete(r.byAu, k)
		} else {
			r.byAu[k] = keep
		}
	}
}

// prune drops entries older than the window.
func (r *recentAuthors) prune(cutoff int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, list := range r.byAu {
		keep := list[:0]
		for _, m := range list {
			if m.created >= cutoff {
				keep = append(keep, m)
			}
		}
		if len(keep) == 0 {
			delete(r.byAu, k)
		} else {
			r.byAu[k] = keep
		}
	}
}

// loadRecentAuthors builds the index from the last 7 days of messages at
// startup. A message that doesn't decrypt is left out (and reported by the
// search rebuild and by history).
func (s *Server) loadRecentAuthors(ctx context.Context) error {
	since := s.now().Add(-recentWindow).UnixMilli()
	rows, err := s.db.QueryContext(ctx, `SELECT `+msgCols+` FROM messages WHERE created_at >= ? AND deleted = 0`, since)
	if err != nil {
		return err
	}
	var all []storedMessage
	for rows.Next() {
		r, err := scanStored(rows)
		if err != nil {
			rows.Close()
			return err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range all {
		if body, err := s.openBody(ctx, r); err == nil && body.System == nil {
			s.recent.add(body.Author, r.id, r.created)
		}
	}
	return nil
}

// purgeAuthor deletes every message `author` posted since `since` (a ban's
// "delete message history", at most 7 days back). Callers hold s.mu.
func (s *Server) purgeAuthor(ctx context.Context, author []byte, since int64) (int, error) {
	var hit []storedMessage
	var authors [][]byte
	for _, id := range s.recent.since(author, since) {
		r, body, err := s.loadForUpdate(ctx, id)
		var re *rpcError
		if errors.As(err, &re) && re.code == wire.RpcErrorCode_RPC_ERROR_CODE_NOT_FOUND {
			continue // purged by retention or with its channel
		}
		if err != nil {
			return 0, err
		}
		if r.deleted || string(body.Author) != string(author) || r.created < since {
			continue
		}
		hit = append(hit, r)
		authors = append(authors, body.Author)
	}
	return s.deleteStored(ctx, hit, authors)
}

// opBulkDelete deletes up to MaxBulkDelete messages of one channel.
func (s *Server) opBulkDelete(ctx context.Context, actor *memberRec, b *wire.BulkDelete, out *adminOutcome) error {
	c := s.channels[b.ChannelId]
	if c == nil {
		return errNotFound("no such channel")
	}
	if s.channelPerms(actor, c)&(PermViewChannel|PermManageMessages) != PermViewChannel|PermManageMessages {
		return errDenied("you can't delete other people's messages in this channel")
	}
	// Voice channels' text chat too, even with voice channels turned off
	// (cleaning up stays possible).
	if c.Kind != wire.ChannelKind_CHANNEL_KIND_TEXT && c.Kind != wire.ChannelKind_CHANNEL_KIND_VOICE {
		return errInvalid("this channel doesn't take messages")
	}
	var ids []int64
	switch {
	case len(b.MessageIds) > 0 && b.Last > 0:
		return errInvalid("give either message ids or a count, not both")
	case len(b.MessageIds) > MaxBulkDelete || b.Last > MaxBulkDelete:
		return errInvalid(fmt.Sprintf("a bulk delete removes at most %d messages", MaxBulkDelete))
	case len(b.MessageIds) > 0:
		seen := map[int64]bool{}
		for _, id := range b.MessageIds {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	case b.Last > 0:
		rows, err := s.db.QueryContext(ctx, `SELECT id FROM messages WHERE channel_id = ? AND deleted = 0 ORDER BY id DESC LIMIT ?`, c.ID, b.Last)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	default:
		return errInvalid("choose messages to delete")
	}
	var hit []storedMessage
	var authors [][]byte
	for _, id := range ids {
		r, body, err := s.loadForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if r.channelID != c.ID {
			return errInvalid(fmt.Sprintf("message %d isn't in #%s", id, c.Name))
		}
		if r.deleted {
			continue
		}
		hit = append(hit, r)
		authors = append(authors, body.Author)
	}
	n, err := s.deleteStored(ctx, hit, authors)
	if err != nil {
		return err
	}
	out.result.Deleted = uint32(n)
	out.summary = fmt.Sprintf("bulk deleted %s in #%s", plural(n, "message"), c.Name)
	return nil
}

// targetOf is the member an admin action acts on, for audit filters.
func targetOf(a *wire.AdminAction) []byte {
	switch op := a.Op.(type) {
	case *wire.AdminAction_Kick:
		return op.Kick.Member
	case *wire.AdminAction_Ban:
		return op.Ban.Member
	case *wire.AdminAction_Unban:
		return op.Unban.Member
	case *wire.AdminAction_Approve:
		return op.Approve.Member
	case *wire.AdminAction_Deny:
		return op.Deny.Member
	case *wire.AdminAction_AssignRoles:
		return op.AssignRoles.Member
	case *wire.AdminAction_ReplaceMember:
		return op.ReplaceMember.OldKey
	case *wire.AdminAction_InviteOwner:
		return op.InviteOwner.Member
	case *wire.AdminAction_Timeout:
		return op.Timeout.Member
	case *wire.AdminAction_SetNickname:
		return op.SetNickname.Member
	case *wire.AdminAction_VoiceMute:
		return op.VoiceMute.Member
	case *wire.AdminAction_VoiceDeafen:
		return op.VoiceDeafen.Member
	case *wire.AdminAction_VoiceMove:
		return op.VoiceMove.Member
	case *wire.AdminAction_SetChannelOverride:
		return op.SetChannelOverride.Member
	}
	return nil
}

// systemAudit writes an audit entry the server makes on its own (AutoMod).
// Callers hold s.mu.
func (s *Server) systemAudit(ctx context.Context, op string, target []byte, summary string) {
	s.recordAudit(ctx, nil, op, target, 0, summary)
}

// recordAudit writes an audit entry for something that isn't a signed admin
// action: AutoMod (no actor), or a moderator's message action (deleting
// someone else's message, pinning) in channel (0 = none). The summary never
// quotes message text. Callers hold s.mu.
func (s *Server) recordAudit(ctx context.Context, actor []byte, op string, target []byte, channel int64, summary string) {
	if err := s.writeAudit(ctx, &auditRec{At: s.now().UnixMilli(), Actor: actor, Summary: summary, Op: op, Target: target, Channel: channel}); err != nil {
		s.log.Error("audit write failed", "err", err)
	}
}
