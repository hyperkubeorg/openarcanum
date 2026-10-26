package community

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Reports (docs/protocol.md §4.9): any member can report a message into
// the moderator queue; AutoMod alerts land in the
// same queue. Members with Manage Messages see the queue (reporter,
// message, reason) and resolve or dismiss reports.

const (
	MaxReportReason = 500
	// Per member: at most reportBurst reports per reportWindow.
	reportBurst  = 5
	reportWindow = 10 * time.Minute
	// Per message author: at most alertBurst AutoMod alert reports per
	// reportWindow. Their messages are still blocked or timed out; only
	// the extra reports are skipped.
	alertBurst = 5
)

// reportRec is a report's sealed record (storage key). The reporter's
// reason is sealed separately, under the month key of created_at.
type reportRec struct {
	Kind          wire.ReportKind `json:"kind"`
	CreatedAt     int64           `json:"created_at"`
	Reporter      []byte          `json:"reporter,omitempty"`
	MessageID     int64           `json:"message_id,omitempty"`
	ChannelID     int64           `json:"channel_id"`
	Author        []byte          `json:"author"`
	RuleID        int64           `json:"rule_id,omitempty"`
	RuleName      string          `json:"rule_name,omitempty"`
	AutoModAction string          `json:"automod_action,omitempty"`
	ResolvedBy    []byte          `json:"resolved_by,omitempty"`
	ResolvedAt    int64           `json:"resolved_at,omitempty"`
	// Status and Month bind the plaintext reports.status and reports.month
	// columns (loadReport checks them, as it checks reports.dedupe against
	// the reporter and message): a closed report can't be reopened, or an
	// open one hidden, on disk.
	Status wire.ReportStatus `json:"status"`
	Month  int               `json:"month"`
}

// maxOpenReports: the queue stops taking member reports and AutoMod alerts
// at this many open ones (a variable only so tests can lower it).
var maxOpenReports = 5000

// MaxOpenReportsPerReporter is how many of one member's reports may be open
// at once, so a few members can't fill the queue (a variable for tests).
var MaxOpenReportsPerReporter = 20

// openReportsBy counts the open reports member filed. The reporter is
// sealed in each record, so this reads the open queue (at most
// maxOpenReports rows; reporting is rate limited). Callers hold s.mu.
func (s *Server) openReportsBy(ctx context.Context, member []byte) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, data FROM reports WHERE status = ?`, int(wire.ReportStatus_REPORT_STATUS_OPEN))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id int64
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			return 0, err
		}
		var rec reportRec
		if err := s.openJSON("reports", "data", rowID(id), data, &rec); err != nil {
			return 0, err
		}
		if bytes.Equal(rec.Reporter, member) {
			n++
		}
	}
	return n, rows.Err()
}

// rateLimiter remembers recent event times per member, in memory only
// (reports, AutoMod alerts, profile changes).
type rateLimiter struct {
	mu   sync.Mutex
	seen map[string][]time.Time
}

func (l *rateLimiter) allow(member []byte, now time.Time) bool {
	return l.allowN(member, now, reportBurst, reportWindow)
}

// allowN allows at most burst events per member in any window.
func (l *rateLimiter) allowN(member []byte, now time.Time, burst int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen == nil {
		l.seen = map[string][]time.Time{}
	}
	k := string(member)
	var keep []time.Time
	for _, t := range l.seen[k] {
		if now.Sub(t) < window {
			keep = append(keep, t)
		}
	}
	if len(keep) >= burst {
		l.seen[k] = keep
		return false
	}
	l.seen[k] = append(keep, now)
	return true
}

// reportDedupe is the keyed lookup hash that stops a member reporting the
// same message twice.
func (s *Server) reportDedupe(reporter []byte, messageID int64) []byte {
	m := hmac.New(sha256.New, s.lookupKey)
	m.Write([]byte("report\x00"))
	m.Write(reporter)
	m.Write(be64(messageID))
	return m.Sum(nil)
}

// createReport stores a report and returns it as moderators see it.
// Callers hold s.mu for writing.
func (s *Server) createReport(ctx context.Context, rec *reportRec, reason string, dedupe []byte) (*wire.Report, error) {
	month := monthOf(rec.CreatedAt)
	rec.Status, rec.Month = wire.ReportStatus_REPORT_STATUS_OPEN, month
	if _, err := s.monthKey(ctx, month, true); err != nil {
		return nil, err
	}
	id, err := s.nextID(ctx, s.db, "reports")
	if err != nil {
		return nil, err
	}
	var reasonEnc []byte
	if reason != "" {
		k, err := s.monthKey(ctx, month, false)
		if err != nil {
			return nil, err
		}
		reasonEnc = sealWith(k, "reports", "reason", rowID(id), []byte(reason))
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO reports (id, status, month, dedupe, data, reason) VALUES (?, ?, ?, ?, ?, ?)`,
		id, int(wire.ReportStatus_REPORT_STATUS_OPEN), month, dedupe, s.sealJSON("reports", "data", rowID(id), rec), reasonEnc); err != nil {
		if dedupe != nil && strings.Contains(err.Error(), "UNIQUE") {
			return nil, errConflict("you already reported this message")
		}
		return nil, err
	}
	return s.reportProto(id, wire.ReportStatus_REPORT_STATUS_OPEN, rec, reason), nil
}

// reportProto fills in display names; callers hold s.mu.
func (s *Server) reportProto(id int64, status wire.ReportStatus, rec *reportRec, reason string) *wire.Report {
	r := &wire.Report{
		Id: id, Kind: rec.Kind, Status: status, CreatedAt: rec.CreatedAt, Reporter: rec.Reporter,
		MessageId: rec.MessageID, ChannelId: rec.ChannelID, Author: rec.Author, AuthorName: s.nameOf(rec.Author),
		Reason: reason, RuleId: rec.RuleID, RuleName: rec.RuleName, AutomodAction: rec.AutoModAction,
		ResolvedBy: rec.ResolvedBy, ResolvedAt: rec.ResolvedAt,
	}
	if len(rec.Reporter) > 0 {
		r.ReporterName = s.nameOf(rec.Reporter)
	}
	if len(rec.ResolvedBy) > 0 {
		r.ResolvedByName = s.nameOf(rec.ResolvedBy)
	}
	return r
}

// modAlertSender returns a func that pushes rep to every session of a
// member who can see the report queue, then everyone's new queue counts.
// Callers build it under s.mu and run it after releasing it.
func (s *Server) modAlertSender(rep *wire.Report) func() {
	return func() {
		msg := &wire.ServerMessage{Body: &wire.ServerMessage_ModAlert{ModAlert: &wire.ModAlert{Report: rep}}}
		for _, ss := range s.allSessions() {
			if ss.getStatus() != wire.MemberStatus_MEMBER_STATUS_ACTIVE {
				continue
			}
			s.mu.RLock()
			ok := s.basePerms(s.members[string(ss.member)])&PermManageMessages != 0
			s.mu.RUnlock()
			if ok {
				ss.send(msg)
			}
		}
		s.pushModQueue()
	}
}

// modQueueLocked is what waits for m in the moderator queues: open reports
// and AutoMod alerts with Manage Messages, join requests with Approve
// Members; nil for a member who can see neither queue. Callers hold s.mu.
func (s *Server) modQueueLocked(ctx context.Context, m *memberRec) *wire.ModQueue {
	if m == nil || m.Status != wire.MemberStatus_MEMBER_STATUS_ACTIVE {
		return nil
	}
	base := s.basePerms(m)
	reports, pending := base&PermManageMessages != 0, base&PermApproveMembers != 0
	if !reports && !pending {
		return nil
	}
	q := &wire.ModQueue{}
	if reports {
		n, err := s.count(ctx, `SELECT count(*) FROM reports WHERE status = ?`, int(wire.ReportStatus_REPORT_STATUS_OPEN))
		if err != nil {
			s.log.Warn("counting open reports failed", "err", err)
		}
		q.OpenReports = uint32(n)
	}
	if pending {
		for _, x := range s.members {
			if x.Status == wire.MemberStatus_MEMBER_STATUS_PENDING {
				q.PendingMembers++
			}
		}
	}
	return q
}

// pushModQueue sends every session of a member who can see a moderator
// queue its current counts (after a report, an alert, a resolved report,
// a join request or a decision on one). Callers don't hold s.mu.
func (s *Server) pushModQueue() {
	ctx := context.Background()
	for _, ss := range s.allSessions() {
		if ss.getStatus() != wire.MemberStatus_MEMBER_STATUS_ACTIVE {
			continue
		}
		s.mu.RLock()
		q := s.modQueueLocked(ctx, s.members[string(ss.member)])
		s.mu.RUnlock()
		if q != nil {
			ss.send(&wire.ServerMessage{Body: &wire.ServerMessage_ModQueue{ModQueue: q}})
		}
	}
}

func (s *Server) reportMessage(ctx context.Context, ss *sess, r *wire.ReportMessage) (*wire.ServerMessage, error) {
	reason := strings.TrimSpace(r.Reason)
	if utf8.RuneCountInString(reason) > MaxReportReason {
		return nil, errTooLarge(fmt.Sprintf("a report reason may be at most %d characters", MaxReportReason))
	}
	if !utf8.ValidString(reason) {
		return nil, errInvalid("the reason isn't valid text")
	}
	s.mu.Lock()
	stored, body, err := s.loadForUpdate(ctx, r.MessageId)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	// Only a message the member may read (the same gate as history).
	if _, err := s.readableMessage(ss, stored); err != nil {
		s.mu.Unlock()
		return nil, errNotFound("no such message")
	}
	if stored.deleted {
		s.mu.Unlock()
		return nil, errNotFound("that message was deleted")
	}
	if string(body.Author) == string(ss.member) {
		s.mu.Unlock()
		return nil, errInvalid("you can't report your own message")
	}
	open, err := s.count(ctx, `SELECT count(*) FROM reports WHERE status = ?`, int(wire.ReportStatus_REPORT_STATUS_OPEN))
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if open >= maxOpenReports {
		s.mu.Unlock()
		return nil, errRateLimited("the moderators' queue is full right now; try again later")
	}
	mine, err := s.openReportsBy(ctx, ss.member)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if mine >= MaxOpenReportsPerReporter {
		s.mu.Unlock()
		return nil, errRateLimited(fmt.Sprintf("you have %d reports waiting for the moderators; wait until they've looked at them", mine))
	}
	if !s.reports.allow(ss.member, s.now()) {
		s.mu.Unlock()
		return nil, errRateLimited(fmt.Sprintf("you can report at most %d messages every %d minutes", reportBurst, int(reportWindow/time.Minute)))
	}
	rep, err := s.createReport(ctx, &reportRec{
		Kind: wire.ReportKind_REPORT_KIND_MEMBER, CreatedAt: s.now().UnixMilli(), Reporter: ss.member,
		MessageID: stored.id, ChannelID: stored.channelID, Author: body.Author,
	}, reason, s.reportDedupe(ss.member, stored.id))
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	send := s.modAlertSender(rep)
	s.mu.Unlock()
	send()
	return okBody(), nil
}

// loadReport reads and decrypts one report; callers hold s.mu.
func (s *Server) loadReport(ctx context.Context, x dbtx, id int64) (wire.ReportStatus, *reportRec, string, error) {
	var status, month int
	var dedupe, data, reasonEnc []byte
	err := x.QueryRowContext(ctx, `SELECT status, month, dedupe, data, reason FROM reports WHERE id = ?`, id).Scan(&status, &month, &dedupe, &data, &reasonEnc)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, "", errNotFound("no such report")
	}
	if err != nil {
		return 0, nil, "", err
	}
	rec := &reportRec{}
	if err := s.openJSON("reports", "data", rowID(id), data, rec); err != nil {
		return 0, nil, "", err
	}
	if err := s.checkReportBinding(id, wire.ReportStatus(status), month, dedupe, rec); err != nil {
		return 0, nil, "", err
	}
	reason := ""
	if reasonEnc != nil {
		pt, err := s.openMonth(ctx, month, "reports", "reason", rowID(id), reasonEnc)
		if err != nil {
			return 0, nil, "", err
		}
		reason = string(pt)
	}
	return wire.ReportStatus(status), rec, reason, nil
}

func (s *Server) listReports(ctx context.Context, ss *sess, q *wire.ListReports) (*wire.ServerMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.basePerms(s.members[string(ss.member)])&PermManageMessages == 0 {
		return nil, errDenied("only moderators (Manage Messages) can see reports")
	}
	limit := int(q.Limit)
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	before := q.BeforeId
	if before <= 0 {
		before = 1 << 62
	}
	where := `WHERE id < ? AND status = ?`
	args := []any{before, int(wire.ReportStatus_REPORT_STATUS_OPEN)}
	if q.IncludeClosed {
		where = `WHERE id < ?`
		args = args[:1]
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM reports `+where+` ORDER BY id DESC LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := &wire.ReportList{}
	if len(ids) > limit {
		out.More = true
		ids = ids[:limit]
	}
	for _, id := range ids {
		status, rec, reason, err := s.loadReport(ctx, s.db, id)
		if err != nil {
			return nil, err
		}
		out.Reports = append(out.Reports, s.reportProto(id, status, rec, reason))
	}
	return &wire.ServerMessage{Body: &wire.ServerMessage_Reports{Reports: out}}, nil
}

func (s *Server) opResolveReport(ctx context.Context, actor *memberRec, r *wire.ResolveReport, out *adminOutcome) error {
	if err := s.need(actor, PermManageMessages); err != nil {
		return err
	}
	status, rec, _, err := s.loadReport(ctx, s.db, r.Id)
	if err != nil {
		return err
	}
	if status != wire.ReportStatus_REPORT_STATUS_OPEN {
		return errConflict("that report was already closed")
	}
	next, verb := wire.ReportStatus_REPORT_STATUS_RESOLVED, "resolved"
	if r.Dismiss {
		next, verb = wire.ReportStatus_REPORT_STATUS_DISMISSED, "dismissed"
	}
	rec.ResolvedBy, rec.ResolvedAt, rec.Status = actor.Key, s.now().UnixMilli(), next
	if _, err := s.db.ExecContext(ctx, `UPDATE reports SET status = ?, data = ? WHERE id = ?`,
		int(next), s.sealJSON("reports", "data", rowID(r.Id), rec), r.Id); err != nil {
		return err
	}
	what := "report"
	if rec.Kind == wire.ReportKind_REPORT_KIND_AUTOMOD {
		what = "AutoMod alert"
	}
	out.summary = fmt.Sprintf("%s %s #%d about %s", verb, what, r.Id, s.nameOf(rec.Author))
	out.modQueue = true
	return nil
}

// checkReportBinding compares a report's plaintext columns with its sealed
// record (atrest_bind.go).
func (s *Server) checkReportBinding(id int64, status wire.ReportStatus, month int, dedupe []byte, rec *reportRec) error {
	if rec.Status != status {
		return &TamperError{"reports", "status", rowID(id)}
	}
	if rec.Month != month {
		return &TamperError{"reports", "month", rowID(id)}
	}
	var want []byte
	if rec.Kind == wire.ReportKind_REPORT_KIND_MEMBER {
		want = s.reportDedupe(rec.Reporter, rec.MessageID)
	}
	if !bytes.Equal(dedupe, want) {
		return &TamperError{"reports", "dedupe", rowID(id)}
	}
	return nil
}
