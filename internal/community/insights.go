package community

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Insights (docs/protocol.md §4.12): the community's activity per UTC day,
// for its admins. Counts only, from what the server sees anyway: joins,
// leaves, messages, members who connected or posted, voice minutes and the
// most members online at once. Who connected or posted is kept for the
// current day only, in memory, to count each member once; the day's row
// stores the counts, never who. A restart forgets the day's sets, so a
// member seen before and after it may count once more at most: the stored
// count is the larger of the two.

const (
	// insightsKeepDays bounds the rows kept (a year and a bit).
	insightsKeepDays = 400
	// insightsDefaultDays is what GetInsights returns without a count.
	insightsDefaultDays = 30
	// insightsChannelDays is the window of the per-channel message counts.
	insightsChannelDays = 30
)

const schemaV12Tables = `CREATE TABLE insights (day TEXT PRIMARY KEY, data BLOB NOT NULL);`

type insightDay struct {
	Members      int64 `json:"members"`
	Joined       int64 `json:"joined"`
	Left         int64 `json:"left"`
	Messages     int64 `json:"messages"`
	Active       int64 `json:"active"`
	Posters      int64 `json:"posters"`
	VoiceMinutes int64 `json:"voice_minutes"`
	PeakOnline   int64 `json:"peak_online"`
}

type insightsState struct {
	mu      sync.Mutex
	day     string
	cur     insightDay
	active  map[string]struct{}
	posters map[string]struct{}
	// done: days that ended and aren't saved yet (saveInsights).
	done map[string]insightDay
}

func insightsDay(t time.Time) string { return t.UTC().Format(time.DateOnly) }

// rollInsights moves to now's day (st.mu held): the day that ended waits
// in done for the next save.
func (st *insightsState) roll(now time.Time) {
	day := insightsDay(now)
	if st.day == day {
		return
	}
	if st.day != "" {
		if st.done == nil {
			st.done = map[string]insightDay{}
		}
		st.done[st.day] = st.counts()
	}
	st.day, st.cur = day, insightDay{}
	st.active, st.posters = map[string]struct{}{}, map[string]struct{}{}
}

// counts is the current day's row (st.mu held).
func (st *insightsState) counts() insightDay {
	d := st.cur
	d.Active = max(d.Active, int64(len(st.active)))
	d.Posters = max(d.Posters, int64(len(st.posters)))
	return d
}

func (s *Server) noteInsight(f func(st *insightsState)) {
	st := &s.insights
	st.mu.Lock()
	defer st.mu.Unlock()
	st.roll(s.now())
	f(st)
}

// insightSeen: a member connected (an admitted session).
func (s *Server) insightSeen(member []byte) {
	s.noteInsight(func(st *insightsState) { st.active[string(member)] = struct{}{} })
}

// insightPost: a member posted a message (system notices count as
// messages, not as posters).
func (s *Server) insightPost(author []byte, system bool) {
	s.noteInsight(func(st *insightsState) {
		st.cur.Messages++
		if !system {
			st.posters[string(author)] = struct{}{}
			st.active[string(author)] = struct{}{}
		}
	})
}

func (s *Server) insightJoined() { s.noteInsight(func(st *insightsState) { st.cur.Joined++ }) }
func (s *Server) insightLeft()   { s.noteInsight(func(st *insightsState) { st.cur.Left++ }) }

// loadInsights picks the current day up where the last run left it.
func (s *Server) loadInsights(ctx context.Context) error {
	day := insightsDay(s.now())
	var sealed []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM insights WHERE day = ?`, day).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var d insightDay
	if err := s.openJSON("insights", "data", day, sealed, &d); err != nil {
		return err
	}
	st := &s.insights
	st.mu.Lock()
	defer st.mu.Unlock()
	st.roll(s.now())
	st.cur = d
	return nil
}

// insightsTick runs each minute (gcLoop): it samples who is online and in
// calls, and saves the day's row (and any day that ended).
func (s *Server) insightsTick(ctx context.Context) {
	online, members := s.onlineAndMembers()
	inCalls := s.callParticipants()
	st := &s.insights
	st.mu.Lock()
	st.roll(s.now())
	st.cur.Members = members
	st.cur.PeakOnline = max(st.cur.PeakOnline, online)
	st.cur.VoiceMinutes += inCalls
	rows := map[string]insightDay{st.day: st.counts()}
	for day, d := range st.done {
		rows[day] = d
	}
	st.done = nil
	st.mu.Unlock()
	for day, d := range rows {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO insights (day, data) VALUES (?, ?) ON CONFLICT (day) DO UPDATE SET data = excluded.data`,
			day, s.sealJSON("insights", "data", day, &d)); err != nil {
			s.log.Error("saving insights failed", "err", err)
		}
	}
	cutoff := insightsDay(s.now().AddDate(0, 0, -insightsKeepDays))
	s.bestEffort(ctx, `DELETE FROM insights WHERE day < ?`, cutoff)
}

func (s *Server) onlineAndMembers() (online, members int64) {
	s.smu.Lock()
	seen := map[string]bool{}
	for ss := range s.sessions {
		if ss.getStatus() == wire.MemberStatus_MEMBER_STATUS_ACTIVE {
			seen[string(ss.member)] = true
		}
	}
	s.smu.Unlock()
	s.mu.RLock()
	for _, m := range s.members {
		if m.Status == wire.MemberStatus_MEMBER_STATUS_ACTIVE {
			members++
		}
	}
	s.mu.RUnlock()
	return int64(len(seen)), members
}

func (s *Server) callParticipants() int64 {
	s.voice.mu.Lock()
	defer s.voice.mu.Unlock()
	var n int64
	for _, cl := range s.voice.calls {
		n += int64(len(cl.parts))
	}
	return n
}

func (s *Server) getInsights(ctx context.Context, ss *sess, q *wire.GetInsights) (*wire.ServerMessage, error) {
	s.mu.RLock()
	allowed := s.basePerms(s.members[string(ss.member)])&(PermManageCommunity|PermViewAuditLog) != 0
	s.mu.RUnlock()
	if !allowed {
		return nil, errDenied("insights are for admins (Manage community or View audit log)")
	}
	days := int(q.Days)
	if days <= 0 {
		days = insightsDefaultDays
	}
	if days > insightsKeepDays {
		return nil, errInvalid("insights go back 400 days at most")
	}
	s.insightsTick(ctx) // the current day as of now
	since := insightsDay(s.now().AddDate(0, 0, -(days - 1)))
	rows, err := s.db.QueryContext(ctx, `SELECT day, data FROM insights WHERE day >= ? ORDER BY day`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &wire.Insights{}
	for rows.Next() {
		var day string
		var sealed []byte
		if err := rows.Scan(&day, &sealed); err != nil {
			return nil, err
		}
		var d insightDay
		if err := s.openJSON("insights", "data", day, sealed, &d); err != nil {
			return nil, err
		}
		out.Days = append(out.Days, &wire.InsightDay{
			Day: day, Members: u32(d.Members), Joined: u32(d.Joined), Left: u32(d.Left), Messages: u32(d.Messages),
			Active: u32(d.Active), Posters: u32(d.Posters), VoiceMinutes: u32(d.VoiceMinutes), PeakOnline: u32(d.PeakOnline),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	crows, err := s.db.QueryContext(ctx, `SELECT channel_id, count(*) FROM messages WHERE deleted = 0 AND created_at >= ? GROUP BY channel_id`,
		s.now().AddDate(0, 0, -insightsChannelDays).UnixMilli())
	if err != nil {
		return nil, err
	}
	defer crows.Close()
	s.mu.RLock()
	for crows.Next() {
		var ch int64
		var n int64
		if err := crows.Scan(&ch, &n); err != nil {
			s.mu.RUnlock()
			return nil, err
		}
		if s.channels[ch] != nil {
			out.Channels = append(out.Channels, &wire.ChannelActivity{ChannelId: ch, Messages: u32(n)})
		}
	}
	s.mu.RUnlock()
	sort.Slice(out.Channels, func(i, j int) bool { return out.Channels[i].Messages > out.Channels[j].Messages })
	online, members := s.onlineAndMembers()
	out.Members, out.Online = u32(members), u32(online)
	return &wire.ServerMessage{Body: &wire.ServerMessage_Insights{Insights: out}}, nil
}

func u32(n int64) uint32 {
	if n < 0 {
		return 0
	}
	if n > 1<<32-1 {
		return 1<<32 - 1
	}
	return uint32(n)
}
