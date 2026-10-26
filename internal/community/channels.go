package community

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Channels, categories, ordering, topics, slowmode and per-channel
// overrides (docs/admin-guide.md "Channels and categories").

const (
	// MaxChannels and MaxCategories cap a community's channels (text and voice
	// together) and categories (docs/operations.md "Quotas").
	MaxChannels        = 500
	MaxCategories      = 50
	maxDescription     = 1024
	maxSlowmodeSeconds = 6 * 3600
	maxOverrides       = 100
)

var channelNameOK = func(n string) bool {
	if n == "" || utf8.RuneCountInString(n) > 64 {
		return false
	}
	for _, r := range n {
		if r == ' ' || r == '#' || r < 0x20 {
			return false
		}
	}
	return true
}

// voiceChannelName checks a voice channel's name: like Discord, voice
// channels keep their capitals and spaces (1 to 100 characters, no control
// characters).
func voiceChannelName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if n == "" || utf8.RuneCountInString(n) > 100 {
		return "", errInvalid("voice channel names are 1 to 100 characters")
	}
	for _, r := range n {
		if r < 0x20 || r == 0x7f {
			return "", errInvalid("voice channel names can't contain control characters")
		}
	}
	return n, nil
}

// channelName checks and normalizes a name for a channel of kind.
func channelName(kind wire.ChannelKind, n string) (string, error) {
	if kind == wire.ChannelKind_CHANNEL_KIND_VOICE {
		return voiceChannelName(n)
	}
	name := normChannelName(n)
	if !channelNameOK(name) {
		return "", errInvalid("channel names are 1 to 64 characters without spaces or #")
	}
	return name, nil
}

func normChannelName(n string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(n), "#")), " ", "-"))
}

func validCategoryName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if n == "" || utf8.RuneCountInString(n) > 64 {
		return "", errInvalid("category names are 1 to 64 characters")
	}
	for _, r := range n {
		if r < 0x20 || r == 0x7f {
			return "", errInvalid("category names can't contain control characters")
		}
	}
	return n, nil
}

func validDescription(d string) (string, error) {
	d = strings.TrimSpace(d)
	if utf8.RuneCountInString(d) > maxDescription {
		return "", errInvalid(fmt.Sprintf("topics may be at most %d characters", maxDescription))
	}
	for _, r := range d {
		if r < 0x20 && r != '\n' {
			return "", errInvalid("topics can't contain control characters")
		}
	}
	return d, nil
}

func validToggle(t wire.ChannelToggle) error {
	if t != wire.ChannelToggle_CHANNEL_TOGGLE_INHERIT && t != wire.ChannelToggle_CHANNEL_TOGGLE_OFF {
		return errInvalid("channel settings are inherit or off")
	}
	return nil
}

// channelsIn lists the channels of one category (0 = uncategorized) in
// order; callers hold s.mu.
func (s *Server) channelsIn(category int64) []*channelRec {
	var out []*channelRec
	for _, c := range s.channels {
		if c.CategoryID == category {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Position != out[j].Position {
			return out[i].Position < out[j].Position
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (s *Server) sortedCategories() []*categoryRec {
	out := make([]*categoryRec, 0, len(s.categories))
	for _, c := range s.categories {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Position != out[j].Position {
			return out[i].Position < out[j].Position
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// endPosition is the position after the last channel of a category.
func (s *Server) endPosition(category int64) int32 {
	var pos int32
	for _, c := range s.channelsIn(category) {
		if c.Position >= pos {
			pos = c.Position + 1
		}
	}
	return pos
}

func (s *Server) opCreateChannel(ctx context.Context, actor *memberRec, c *wire.CreateChannel, out *adminOutcome) error {
	if err := s.need(actor, PermManageChannels); err != nil {
		return err
	}
	kind := c.Kind
	if kind == wire.ChannelKind_CHANNEL_KIND_UNSPECIFIED {
		kind = wire.ChannelKind_CHANNEL_KIND_TEXT
	}
	name, err := channelName(kind, c.Name)
	if err != nil {
		return err
	}
	if kind != wire.ChannelKind_CHANNEL_KIND_TEXT && kind != wire.ChannelKind_CHANNEL_KIND_VOICE {
		return errInvalid("unknown channel kind")
	}
	if kind == wire.ChannelKind_CHANNEL_KIND_VOICE {
		if err := s.allowed(FeatureVoice, nil, actor, 0); err != nil {
			return err
		}
	}
	desc, err := validDescription(c.Description)
	if err != nil {
		return err
	}
	var cat *categoryRec
	if c.CategoryId != 0 {
		if cat = s.categories[c.CategoryId]; cat == nil {
			return errNotFound("no such category")
		}
	}
	if len(s.channels) >= MaxChannels {
		return errConflict(fmt.Sprintf("a community has at most %d channels; delete one first", MaxChannels))
	}
	id, err := s.nextID(ctx, s.db, "channels")
	if err != nil {
		return err
	}
	ch := &channelRec{ID: id, Name: name, Kind: kind, Position: s.endPosition(c.CategoryId), Description: desc,
		Topic: s.newTopic(), Key: randomBytes(32), Epoch: 1}
	if cat != nil {
		// A new channel in a category starts synced with it.
		ch.CategoryID, ch.Synced = cat.ID, true
		ch.Overrides = append([]Override(nil), cat.Overrides...)
	}
	if err := s.saveChannel(ctx, s.db, ch); err != nil {
		return err
	}
	s.channels[id] = ch
	out.result.Id = id
	prefix := "#"
	if kind == wire.ChannelKind_CHANNEL_KIND_VOICE {
		prefix = "voice channel "
	}
	out.summary = fmt.Sprintf("created %s%s", prefix, name)
	if cat != nil {
		out.summary += " in " + cat.Name
	}
	out.broadcast = true
	return nil
}

func toggleWord(t wire.ChannelToggle) string {
	if t == wire.ChannelToggle_CHANNEL_TOGGLE_OFF {
		return "off"
	}
	return "inherited"
}

func (s *Server) opEditChannel(ctx context.Context, actor *memberRec, e *wire.EditChannel, out *adminOutcome) error {
	if err := s.need(actor, PermManageChannels); err != nil {
		return err
	}
	c := s.channels[e.Id]
	if c == nil {
		return errNotFound("no such channel")
	}
	if err := s.needInChannel(actor, c, PermManageChannels); err != nil {
		return err
	}
	next := c.clone()
	var changes []string
	if e.Name != "" {
		name, err := channelName(c.Kind, e.Name)
		if err != nil {
			return err
		}
		if name != c.Name {
			changes = append(changes, fmt.Sprintf("renamed to #%s", name))
		}
		next.Name = name
	}
	if e.Position != c.Position {
		next.Position = e.Position
		changes = append(changes, "moved")
	}
	if e.ClearRetention {
		next.MsgRet, next.FileRet = nil, nil
		changes = append(changes, "retention back to community default")
	}
	if e.MessageRetentionDays != nil {
		if *e.MessageRetentionDays > MaxRetentionDays {
			return errInvalid("retention is too long")
		}
		v := *e.MessageRetentionDays
		next.MsgRet = &v
		changes = append(changes, fmt.Sprintf("message retention %d days", v))
	}
	if e.FileRetentionDays != nil {
		if *e.FileRetentionDays > MaxRetentionDays {
			return errInvalid("retention is too long")
		}
		v := *e.FileRetentionDays
		next.FileRet = &v
		changes = append(changes, fmt.Sprintf("file retention %d days", v))
	}
	if e.Description != nil {
		d, err := validDescription(*e.Description)
		if err != nil {
			return err
		}
		if d != c.Description {
			changes = append(changes, "topic changed")
		}
		next.Description = d
	}
	if e.SlowmodeSeconds != nil {
		if *e.SlowmodeSeconds > maxSlowmodeSeconds {
			return errInvalid(fmt.Sprintf("slowmode may be at most %d seconds", maxSlowmodeSeconds))
		}
		if *e.SlowmodeSeconds != c.Slowmode {
			if *e.SlowmodeSeconds == 0 {
				changes = append(changes, "slowmode off")
			} else {
				changes = append(changes, fmt.Sprintf("slowmode %d seconds", *e.SlowmodeSeconds))
			}
		}
		next.Slowmode = *e.SlowmodeSeconds
	}
	for _, t := range []struct {
		in    *wire.ChannelToggle
		field *wire.ChannelToggle
		f     Feature
	}{
		{e.Attachments, &next.Attachments, FeatureAttachments},
		{e.LinkPreviews, &next.LinkPreviews, FeatureLinkPreviews},
		{e.Reactions, &next.Reactions, FeatureReactions},
		{e.HistoryForNewMembers, &next.HistoryForNew, FeatureHistoryForNew},
	} {
		if t.in == nil {
			continue
		}
		if err := validToggle(*t.in); err != nil {
			return err
		}
		if *t.in != *t.field {
			changes = append(changes, fmt.Sprintf("%s %s in this channel", features[t.f].label, toggleWord(*t.in)))
		}
		*t.field = *t.in
	}
	if e.CategoryId != nil && *e.CategoryId != c.CategoryID {
		to := *e.CategoryId
		var cat *categoryRec
		if to != 0 {
			if cat = s.categories[to]; cat == nil {
				return errNotFound("no such category")
			}
		}
		next.CategoryID = to
		next.Position = s.endPosition(to)
		if cat == nil {
			next.Synced = false
			changes = append(changes, "moved out of its category")
		} else {
			changes = append(changes, "moved to "+cat.Name)
			if c.Synced {
				next.Overrides = append([]Override(nil), cat.Overrides...)
			}
		}
	}
	if e.SyncPermissions {
		cat := s.categories[next.CategoryID]
		if cat == nil {
			return errInvalid("only a channel in a category can sync its permissions")
		}
		next.Synced = true
		next.Overrides = append([]Override(nil), cat.Overrides...)
		changes = append(changes, "permissions synced with "+cat.Name)
	}
	// Syncing, or moving a synced channel into a category, replaces the
	// channel's overrides: every override that changes passes the checks
	// of SetChannelOverride.
	if err := s.checkOverrideEdit(actor, c.Overrides, next.Overrides); err != nil {
		return err
	}
	before := s.viewSets()
	if err := s.saveChannel(ctx, s.db, next); err != nil {
		return err
	}
	s.channels[c.ID] = next
	if err := s.rotateChannels(ctx, lostAccess(before, s.viewSets())); err != nil {
		return err
	}
	if len(changes) == 0 {
		changes = append(changes, "no changes")
	}
	out.summary = fmt.Sprintf("edited #%s: %s", c.Name, strings.Join(changes, ", "))
	out.broadcast = true
	return nil
}

func (s *Server) opDeleteChannel(ctx context.Context, actor *memberRec, d *wire.DeleteChannel, out *adminOutcome) error {
	if err := s.need(actor, PermManageChannels); err != nil {
		return err
	}
	c := s.channels[d.Id]
	if c == nil {
		return errNotFound("no such channel")
	}
	if err := s.needInChannel(actor, c, PermManageChannels); err != nil {
		return err
	}
	textChannels := 0
	for _, cc := range s.channels {
		if cc.Kind == wire.ChannelKind_CHANNEL_KIND_TEXT {
			textChannels++
		}
	}
	if c.Kind == wire.ChannelKind_CHANNEL_KIND_TEXT && textChannels == 1 {
		return errConflict("a community needs at least one text channel")
	}
	// What the server keeps in memory about the channel's messages: its
	// pins, and its recent messages (ban history deletion).
	pinned, err := s.idsOf(ctx, `SELECT message_id FROM pins WHERE channel_id = ?`, c.ID)
	if err != nil {
		return err
	}
	recent, err := s.idsOf(ctx, `SELECT id FROM messages WHERE channel_id = ? AND created_at >= ?`, c.ID, s.now().Add(-recentWindow).UnixMilli())
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM channels WHERE id = ?`, c.ID); err != nil {
		return err
	}
	s.bestEffort(ctx, `DELETE FROM messages WHERE channel_id = ?`, c.ID)
	s.bestEffort(ctx, `DELETE FROM files WHERE channel_id = ?`, c.ID)
	delete(s.channels, c.ID)
	for k := range s.lastPost {
		if k.channel == c.ID {
			delete(s.lastPost, k)
		}
	}
	for _, id := range pinned {
		delete(s.pinned, id)
	}
	s.recent.drop(recent)
	s.unindexChannel(c.ID)
	// After the action, outside s.mu (it tells the relay).
	deleted, prev := c.ID, out.after
	out.after = func() {
		if prev != nil {
			prev()
		}
		s.voiceEndChannel(deleted, "This voice channel was deleted.")
	}
	out.summary = fmt.Sprintf("deleted channel #%s", c.Name)
	out.broadcast = true
	return nil
}

// ---------------------------------------------------------------- categories

func (s *Server) opCreateCategory(ctx context.Context, actor *memberRec, c *wire.CreateCategory, out *adminOutcome) error {
	if err := s.need(actor, PermManageChannels); err != nil {
		return err
	}
	name, err := validCategoryName(c.Name)
	if err != nil {
		return err
	}
	if len(s.categories) >= MaxCategories {
		return errConflict(fmt.Sprintf("a community has at most %d categories; delete one first", MaxCategories))
	}
	id, err := s.nextID(ctx, s.db, "categories")
	if err != nil {
		return err
	}
	var pos int32
	for _, cc := range s.categories {
		if cc.Position >= pos {
			pos = cc.Position + 1
		}
	}
	cat := &categoryRec{ID: id, Name: name, Position: pos}
	if err := s.saveCategory(ctx, s.db, cat); err != nil {
		return err
	}
	s.categories[id] = cat
	out.result.Id = id
	out.summary = "created category " + name
	out.broadcast = true
	return nil
}

func (s *Server) opEditCategory(ctx context.Context, actor *memberRec, e *wire.EditCategory, out *adminOutcome) error {
	if err := s.need(actor, PermManageChannels); err != nil {
		return err
	}
	cat := s.categories[e.Id]
	if cat == nil {
		return errNotFound("no such category")
	}
	name, err := validCategoryName(e.Name)
	if err != nil {
		return err
	}
	next := cat.clone()
	next.Name = name
	if err := s.saveCategory(ctx, s.db, next); err != nil {
		return err
	}
	s.categories[cat.ID] = next
	out.summary = fmt.Sprintf("renamed category %s to %s", cat.Name, name)
	out.broadcast = true
	return nil
}

// opDeleteCategory keeps the category's channels: they move to the end of
// the uncategorized list, unsynced, with their overrides as they are.
func (s *Server) opDeleteCategory(ctx context.Context, actor *memberRec, d *wire.DeleteCategory, out *adminOutcome) error {
	if err := s.need(actor, PermManageChannels); err != nil {
		return err
	}
	cat := s.categories[d.Id]
	if cat == nil {
		return errNotFound("no such category")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM categories WHERE id = ?`, cat.ID); err != nil {
		return err
	}
	pos := s.endPosition(0)
	moved := map[int64]*channelRec{}
	for _, c := range s.channelsIn(cat.ID) {
		next := c.clone()
		next.CategoryID, next.Synced, next.Position = 0, false, pos
		pos++
		if err := s.saveChannel(ctx, tx, next); err != nil {
			return err
		}
		moved[c.ID] = next
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	delete(s.categories, cat.ID)
	for id, c := range moved {
		s.channels[id] = c
	}
	out.summary = "deleted category " + cat.Name
	out.broadcast = true
	return nil
}

func (s *Server) opReorderCategories(ctx context.Context, actor *memberRec, r *wire.ReorderCategories, out *adminOutcome) error {
	if err := s.need(actor, PermManageChannels); err != nil {
		return err
	}
	if len(r.CategoryIds) != len(s.categories) {
		return errInvalid("list every category, top first")
	}
	seen := map[int64]bool{}
	for _, id := range r.CategoryIds {
		if s.categories[id] == nil || seen[id] {
			return errInvalid("category list has unknown or repeated categories")
		}
		seen[id] = true
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	next := map[int64]*categoryRec{}
	for i, id := range r.CategoryIds {
		c := s.categories[id].clone()
		c.Position = int32(i)
		if err := s.saveCategory(ctx, tx, c); err != nil {
			return err
		}
		next[id] = c
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for id, c := range next {
		s.categories[id] = c
	}
	out.summary = "reordered categories"
	out.broadcast = true
	return nil
}

func (s *Server) opReorderChannels(ctx context.Context, actor *memberRec, r *wire.ReorderChannels, out *adminOutcome) error {
	if err := s.need(actor, PermManageChannels); err != nil {
		return err
	}
	if r.CategoryId != 0 && s.categories[r.CategoryId] == nil {
		return errNotFound("no such category")
	}
	current := s.channelsIn(r.CategoryId)
	if len(r.ChannelIds) != len(current) {
		return errInvalid("list every channel in the category, top first")
	}
	seen := map[int64]bool{}
	for _, id := range r.ChannelIds {
		c := s.channels[id]
		if c == nil || c.CategoryID != r.CategoryId || seen[id] {
			return errInvalid("channel list has unknown, repeated or misplaced channels")
		}
		seen[id] = true
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	next := map[int64]*channelRec{}
	for i, id := range r.ChannelIds {
		c := s.channels[id].clone()
		c.Position = int32(i)
		if err := s.saveChannel(ctx, tx, c); err != nil {
			return err
		}
		next[id] = c
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for id, c := range next {
		s.channels[id] = c
	}
	where := "uncategorized channels"
	if r.CategoryId != 0 {
		where = "channels in " + s.categories[r.CategoryId].Name
	}
	out.summary = "reordered " + where
	out.broadcast = true
	return nil
}

// ---------------------------------------------------------------- overrides

// needInChannel checks that the actor holds perm in channel c, which
// includes View Channel there: Manage Channels doesn't reach channels the
// actor can't see. Callers hold s.mu.
func (s *Server) needInChannel(actor *memberRec, c *channelRec, perm uint64) error {
	if s.channelPerms(actor, c)&perm != perm {
		return errDenied("you don't have permission to do that in this channel")
	}
	return nil
}

// permsUnder is the actor's permissions under a list of overrides: in a
// channel with them, or in a category's channels when they are the
// category's. Callers hold s.mu.
func (s *Server) permsUnder(actor *memberRec, overrides []Override) uint64 {
	return s.channelPerms(actor, &channelRec{Overrides: overrides})
}

type overrideKey struct {
	role   int64
	member string
}

func overrideIndex(list []Override) map[overrideKey]Override {
	out := make(map[overrideKey]Override, len(list))
	for _, o := range list {
		k := overrideKey{role: o.RoleID}
		if o.Member != nil {
			k = overrideKey{member: string(o.Member)}
		}
		out[k] = o
	}
	return out
}

// checkOverrideEdit is the rule for every change to a channel's or a
// category's overrides (SetChannelOverride, syncing with a category, moving
// a synced channel): for each override that is added, removed or changed
// between old and next, the actor needs Manage Channels and Manage Roles
// where the overrides apply (Discord's Manage Permissions is per channel,
// and needs View Channel there), may only touch roles below their highest
// role (or @everyone) and members they outrank, and may only change bits
// they hold there. Callers hold s.mu.
func (s *Server) checkOverrideEdit(actor *memberRec, old, next []Override) error {
	before, after := overrideIndex(old), overrideIndex(next)
	changed := map[overrideKey]uint64{}
	for k, o := range before {
		n := after[k]
		if d := (o.Allow ^ n.Allow) | (o.Deny ^ n.Deny); d != 0 {
			changed[k] = d
		}
	}
	for k, n := range after {
		if _, ok := before[k]; !ok && (n.Allow|n.Deny) != 0 {
			changed[k] = n.Allow | n.Deny
		}
	}
	if len(changed) == 0 {
		return nil
	}
	held := s.permsUnder(actor, old)
	if held&(PermManageChannels|PermManageRoles) != PermManageChannels|PermManageRoles {
		return errDenied("changing permission overrides needs Manage Channels and Manage Roles in that channel")
	}
	actorSub := s.subject(actor)
	_, roles := s.roleInfos()
	for k, bits := range changed {
		if k.member == "" {
			if r := s.roles[k.role]; r != nil && !actorSub.Owner && r.ID != s.everyoneID && r.Position >= TopPosition(actorSub, roles) {
				return errDenied("you can only set overrides for roles below your highest role")
			}
		} else if m := s.members[k.member]; m != nil && !Outranks(actorSub, s.subject(m), roles) {
			return errDenied("you can only set overrides for members below you")
		}
		if !actorSub.Owner && bits&^held != 0 {
			return errDenied("you can't change permissions you don't have")
		}
	}
	return nil
}

// opOverride sets one override on a channel or a category, for a role or a
// member, under checkOverrideEdit's rules.
func (s *Server) opOverride(ctx context.Context, actor *memberRec, o *wire.SetChannelOverride, out *adminOutcome) error {
	if err := s.need(actor, PermManageChannels|PermManageRoles); err != nil {
		return err
	}
	if (o.ChannelId == 0) == (o.CategoryId == 0) {
		return errInvalid("set an override on a channel or on a category")
	}
	if (o.RoleId == 0) == (len(o.Member) == 0) {
		return errInvalid("an override is for a role or for one member")
	}
	if o.Allow&^PermChannelMask != 0 || o.Deny&^PermChannelMask != 0 {
		return errInvalid("overrides may only change channel permissions")
	}
	if o.Allow&o.Deny != 0 {
		return errInvalid("a permission can't be both allowed and denied")
	}
	var who string
	if o.RoleId != 0 {
		r := s.roles[o.RoleId]
		if r == nil {
			return errNotFound("no such role")
		}
		who = r.Name
	} else {
		m := s.members[string(o.Member)]
		if m == nil || m.Status != wire.MemberStatus_MEMBER_STATUS_ACTIVE {
			return errNotFound("no such member")
		}
		who = m.Name
	}
	matches := func(ov Override) bool {
		if o.RoleId != 0 {
			return ov.Member == nil && ov.RoleID == o.RoleId
		}
		return bytes.Equal(ov.Member, o.Member)
	}
	apply := func(list []Override) ([]Override, error) {
		var kept []Override
		for _, ov := range list {
			if matches(ov) {
				continue
			}
			kept = append(kept, ov)
		}
		if o.Allow != 0 || o.Deny != 0 {
			ov := Override{RoleID: o.RoleId, Allow: o.Allow, Deny: o.Deny}
			if o.RoleId == 0 {
				ov.Member = append([]byte(nil), o.Member...)
			}
			kept = append(kept, ov)
		}
		if len(kept) > maxOverrides {
			return nil, errConflict(fmt.Sprintf("a channel or category has at most %d permission overrides", maxOverrides))
		}
		if err := s.checkOverrideEdit(actor, list, kept); err != nil {
			return nil, err
		}
		return kept, nil
	}
	before := s.viewSets()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	channels := map[int64]*channelRec{}
	var cat *categoryRec
	var where string
	if o.ChannelId != 0 {
		c := s.channels[o.ChannelId]
		if c == nil {
			return errNotFound("no such channel")
		}
		next := c.clone()
		if next.Overrides, err = apply(next.Overrides); err != nil {
			return err
		}
		// Editing a channel's own overrides unsyncs it from its category.
		next.Synced = false
		if err := s.saveChannel(ctx, tx, next); err != nil {
			return err
		}
		channels[c.ID] = next
		where = "#" + c.Name
	} else {
		cur := s.categories[o.CategoryId]
		if cur == nil {
			return errNotFound("no such category")
		}
		cat = cur.clone()
		if cat.Overrides, err = apply(cat.Overrides); err != nil {
			return err
		}
		if err := s.saveCategory(ctx, tx, cat); err != nil {
			return err
		}
		for _, c := range s.channelsIn(cat.ID) {
			if !c.Synced {
				continue
			}
			if err := s.checkOverrideEdit(actor, c.Overrides, cat.Overrides); err != nil {
				return err
			}
			next := c.clone()
			next.Overrides = append([]Override(nil), cat.Overrides...)
			if err := s.saveChannel(ctx, tx, next); err != nil {
				return err
			}
			channels[c.ID] = next
		}
		where = "category " + cat.Name
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if cat != nil {
		s.categories[cat.ID] = cat
	}
	for id, c := range channels {
		s.channels[id] = c
	}
	if err := s.rotateChannels(ctx, lostAccess(before, s.viewSets())); err != nil {
		return err
	}
	verb := "set"
	if o.Allow == 0 && o.Deny == 0 {
		verb = "removed"
	}
	if o.RoleId == 0 {
		who += " (" + identity.Fingerprint(o.Member) + ")"
	}
	out.summary = fmt.Sprintf("%s %s override on %s", verb, who, where)
	out.broadcast = true
	return nil
}

// ---------------------------------------------------------------- slowmode

type slowKey struct {
	channel int64
	member  string
}

// maxSlowEntries bounds the slowmode table; older entries than the
// longest slowmode are pruned when it fills.
const maxSlowEntries = 100_000

// checkSlowmode refuses a post within the channel's slowmode window.
// Members who can manage messages or channels there are exempt. Callers
// hold s.mu.
func (s *Server) checkSlowmode(c *channelRec, m *memberRec) error {
	if c.Slowmode == 0 || m == nil {
		return nil
	}
	if s.channelPerms(m, c)&(PermManageMessages|PermManageChannels) != 0 {
		return nil
	}
	last, ok := s.lastPost[slowKey{c.ID, string(m.Key)}]
	if !ok {
		return nil
	}
	wait := last + int64(c.Slowmode)*1000 - s.now().UnixMilli()
	if wait > 0 {
		return errRateLimited(fmt.Sprintf("slowmode is on: wait %d more seconds", (wait+999)/1000))
	}
	return nil
}

// recordPost notes a member's post for slowmode; callers hold s.mu for
// writing.
func (s *Server) recordPost(c *channelRec, author []byte, at int64) {
	if c.Slowmode == 0 {
		return
	}
	if len(s.lastPost) >= maxSlowEntries {
		cutoff := at - maxSlowmodeSeconds*1000
		for k, t := range s.lastPost {
			if t < cutoff {
				delete(s.lastPost, k)
			}
		}
	}
	s.lastPost[slowKey{c.ID, string(author)}] = at
}
