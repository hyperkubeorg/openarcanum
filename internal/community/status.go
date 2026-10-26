package community

import (
	"bytes"
	"context"
	"database/sql"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Statuses (docs/protocol.md §4.11). A member's status (online, idle, do not
// disturb, custom text, expiry) is end-to-end encrypted by their client:
// one vodozemac Megolm session per audience, whose key reaches each
// recipient device over Olm. This server only stores and forwards opaque
// bytes:
//
//   - status devices: one signed entry per member device (public keys the
//     member key vouches for), so senders know where to send keys;
//   - status keys: Olm messages for one recipient device each, kept until
//     that device fetches and acknowledges them (or for statusKeyTTL);
//   - one status ciphertext ("blob") per member, the Megolm message for this
//     community's audience, pushed to every member with the presence batch.
//
// The one thing it reads is the member's Invisible flag (kept in their
// member record), which it must honour: an invisible member's sessions
// don't count as online for anyone else (presenceChanged, memberProto).
//
// At rest everything is sealed like the rest of the database: statuses rows
// are keyed by the member's lookup hash, status_keys rows by the recipient's
// and by a lookup hash of the recipient device and of the (recipient,
// sender, device) triple, which replaces an older key from the same sender.

const (
	maxStatusDevices    = 10
	maxStatusDeviceID   = 64
	maxStatusEntryBytes = 2 << 10
	maxStatusBlobBytes  = 4 << 10
	maxStatusDropBytes  = 4 << 10
	// Per SendStatusKeys request; a sender splits a rotation in a large
	// community over several.
	maxStatusDropsPerCall = 500
	// Per StatusKeys reply and per StatusList page.
	maxStatusItemsPerReply = 500
	maxStatusReplyBytes    = 4 << 20
	// Waiting key drops in all (SendStatusKeys is refused beyond it). Per
	// recipient there is no separate bound to trim to: a drop goes only to a
	// device the recipient registered (at most maxStatusDevices), and a
	// sender's newer drop for that device replaces their older one, so a
	// recipient holds at most one drop per sender and registered device,
	// and one sender can never push out what others sent.
	maxStatusKeysTotal = 250000
	statusKeyTTL       = 7 * 24 * time.Hour
	// Rate limits per member.
	statusSetBurst       = 10
	statusSetWindow      = 20 * time.Second
	statusDeviceBurst    = 20
	statusDeviceWindow   = time.Minute
	statusSendBurst      = 120
	statusSendWindow     = time.Minute
	statusRequestBurst   = 30
	statusRequestWindow  = time.Minute
	statusPushBatchBytes = 4 << 20
)

// Lookup-hash domains for status keys (atrest.go lookupID).
const (
	lookupStatusDevice = "status-device"
	lookupStatusPair   = "status-pair"
)

// CREATE statements for schema 10 (schema_v2.go migrations).
const schemaV10Tables = `
CREATE TABLE statuses (kid BLOB PRIMARY KEY, data BLOB NOT NULL);
CREATE TABLE status_keys (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	kid BLOB NOT NULL,
	dev BLOB NOT NULL,
	pair BLOB NOT NULL UNIQUE,
	at INTEGER NOT NULL,
	data BLOB NOT NULL
);
CREATE INDEX status_keys_kid ON status_keys (kid, dev, id);
CREATE INDEX status_keys_at ON status_keys (at);
`

// statusRec is a member's status record, sealed in statuses.data.
type statusRec struct {
	Member  []byte            `json:"member"`
	Devices []statusDeviceRec `json:"devices,omitempty"`
	Blob    []byte            `json:"blob,omitempty"`
}

type statusDeviceRec struct {
	// The device's status Olm key (curve25519).
	Device []byte `json:"device"`
	// Serialized SignedStatusDevice, opaque here.
	Entry []byte `json:"entry"`
	// The session device key (Noise static) that registered it, so revoking
	// that device removes it.
	By []byte `json:"by"`
}

// statusKeyRec is one waiting key drop, sealed in status_keys.data.
type statusKeyRec struct {
	Recipient []byte `json:"recipient"`
	Sender    []byte `json:"sender"`
	Device    []byte `json:"device"`
	Envelope  []byte `json:"envelope"`
	At        int64  `json:"at"`
}

// statusState is the in-memory copy of the statuses table (guarded by its
// own mutex: lock order is s.mu, then statusState.mu, then s.smu).
type statusState struct {
	mu   sync.Mutex
	recs map[string]*statusRec
	// Rate limiters per member.
	setLimit, deviceLimit, sendLimit, requestLimit rateLimiter
}

func (s *Server) statusDev(device []byte) []byte {
	return lookupID(s.lookupKey, lookupStatusDevice, device)
}

func (s *Server) statusPair(recipient, sender, device []byte) []byte {
	b := make([]byte, 0, 1+len(recipient)+1+len(sender)+len(device))
	b = append(b, byte(len(recipient)))
	b = append(b, recipient...)
	b = append(b, byte(len(sender)))
	b = append(b, sender...)
	b = append(b, device...)
	return lookupID(s.lookupKey, lookupStatusPair, b)
}

// loadStatuses reads the statuses table into memory (Open).
func (s *Server) loadStatuses(ctx context.Context) error {
	s.status.recs = map[string]*statusRec{}
	return queryAll(ctx, s.db, `SELECT kid, data FROM statuses`, func(r *sql.Rows) error {
		var kid, data []byte
		if err := r.Scan(&kid, &data); err != nil {
			return err
		}
		var rec statusRec
		if err := s.openJSON("statuses", "data", rowKey(kid), data, &rec); err != nil {
			return err
		}
		if !bytes.Equal(s.kid(rec.Member), kid) {
			return &TamperError{"statuses", "kid", rowKey(kid)}
		}
		s.status.recs[string(rec.Member)] = &rec
		return nil
	})
}

// saveStatusLocked writes rec (or deletes it when it holds nothing).
// Callers hold s.status.mu.
func (s *Server) saveStatusLocked(ctx context.Context, rec *statusRec) error {
	kid := s.kid(rec.Member)
	if len(rec.Devices) == 0 && len(rec.Blob) == 0 {
		delete(s.status.recs, string(rec.Member))
		_, err := s.db.ExecContext(ctx, `DELETE FROM statuses WHERE kid = ?`, kid)
		return err
	}
	s.status.recs[string(rec.Member)] = rec
	_, err := s.db.ExecContext(ctx, `INSERT INTO statuses (kid, data) VALUES (?, ?) ON CONFLICT (kid) DO UPDATE SET data = excluded.data`,
		kid, s.sealJSON("statuses", "data", rowKey(kid), rec))
	return err
}

func (s *Server) statusRecLocked(member []byte) *statusRec {
	if rec := s.status.recs[string(member)]; rec != nil {
		return rec
	}
	return &statusRec{Member: append([]byte(nil), member...)}
}

// activeMember reports whether key is an admitted member.
func (s *Server) activeMember(key []byte) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m := s.members[string(key)]
	return m != nil && m.Status == wire.MemberStatus_MEMBER_STATUS_ACTIVE
}

// statusChanged queues member's entry for the next presence batch.
func (s *Server) statusChanged(member []byte) {
	s.smu.Lock()
	defer s.smu.Unlock()
	if s.statusPending == nil {
		s.statusPending = map[string]bool{}
	}
	s.statusPending[string(member)] = true
}

func (s *Server) statusEntryLocked(member []byte) *wire.StatusEntry {
	e := &wire.StatusEntry{Member: member}
	if rec := s.status.recs[string(member)]; rec != nil {
		for _, d := range rec.Devices {
			e.Devices = append(e.Devices, d.Entry)
		}
		e.Blob = rec.Blob
	}
	return e
}

// ---------------------------------------------------------------- requests

// checkStatusDevice refuses an entry that isn't the caller's own, signed
// with their member key, for the device it is stored under. Clients check
// the same before sending anyone a key; checking here keeps junk out.
func checkStatusDevice(entry, member, device []byte) error {
	var signed wire.SignedStatusDevice
	var d wire.StatusDevice
	if proto.Unmarshal(entry, &signed) != nil || proto.Unmarshal(signed.Device, &d) != nil {
		return errInvalid("malformed status device entry")
	}
	if !bytes.Equal(d.Member, member) || !bytes.Equal(d.Curve25519, device) {
		return errInvalid("status device entry is for another member or device")
	}
	if identity.Verify(member, identity.CtxStatusDevice, signed.Device, signed.Signature) != nil {
		return errDenied("bad status device signature")
	}
	return nil
}

func (s *Server) setStatusDevice(ctx context.Context, ss *sess, p *wire.SetStatusDevice) (*wire.ServerMessage, error) {
	if len(p.Device) == 0 || len(p.Device) > maxStatusDeviceID {
		return nil, errInvalid("bad status device")
	}
	if len(p.Entry) > maxStatusEntryBytes {
		return nil, errTooLarge("status device entry is too large")
	}
	if len(p.Entry) > 0 {
		if err := checkStatusDevice(p.Entry, ss.member, p.Device); err != nil {
			return nil, err
		}
	}
	if !s.status.deviceLimit.allowN(ss.member, s.now(), statusDeviceBurst, statusDeviceWindow) {
		return nil, errRateLimited("too many status device changes; try again in a minute")
	}
	s.status.mu.Lock()
	rec := s.statusRecLocked(ss.member)
	at := -1
	for i, d := range rec.Devices {
		if bytes.Equal(d.Device, p.Device) {
			at = i
		}
	}
	switch {
	case len(p.Entry) == 0 && at < 0:
		s.status.mu.Unlock()
		return okBody(), nil
	case len(p.Entry) == 0:
		rec.Devices = append(rec.Devices[:at], rec.Devices[at+1:]...)
		// Keys waiting for a device that no longer receives statuses go.
		s.bestEffort(ctx, `DELETE FROM status_keys WHERE kid = ? AND dev = ?`, s.kid(ss.member), s.statusDev(p.Device))
	case at >= 0 && bytes.Equal(rec.Devices[at].Entry, p.Entry) && bytes.Equal(rec.Devices[at].By, ss.device):
		// Unchanged: nothing to save or announce (a device registers on
		// every connect).
		s.status.mu.Unlock()
		return okBody(), nil
	case at >= 0:
		rec.Devices[at] = statusDeviceRec{Device: p.Device, Entry: p.Entry, By: ss.device}
	default:
		if len(rec.Devices) >= maxStatusDevices {
			s.status.mu.Unlock()
			return nil, errInvalid("at most 10 devices can receive statuses; remove one first")
		}
		rec.Devices = append(rec.Devices, statusDeviceRec{Device: p.Device, Entry: p.Entry, By: ss.device})
	}
	err := s.saveStatusLocked(ctx, rec)
	s.status.mu.Unlock()
	if err != nil {
		return nil, err
	}
	s.statusChanged(ss.member)
	return okBody(), nil
}

func (s *Server) setStatus(ctx context.Context, ss *sess, p *wire.SetStatus) (*wire.ServerMessage, error) {
	if len(p.Blob) > maxStatusBlobBytes {
		return nil, errTooLarge("status is too large")
	}
	if !s.status.setLimit.allowN(ss.member, s.now(), statusSetBurst, statusSetWindow) {
		return nil, errRateLimited("statuses change at most 10 times in 20 seconds")
	}
	if err := s.setInvisible(ctx, ss.member, p.Invisible); err != nil {
		return nil, err
	}
	if p.KeepBlob {
		return okBody(), nil
	}
	s.status.mu.Lock()
	rec := s.statusRecLocked(ss.member)
	if bytes.Equal(rec.Blob, p.Blob) {
		s.status.mu.Unlock()
		return okBody(), nil
	}
	rec.Blob = append([]byte(nil), p.Blob...)
	err := s.saveStatusLocked(ctx, rec)
	s.status.mu.Unlock()
	if err != nil {
		return nil, err
	}
	s.statusChanged(ss.member)
	return okBody(), nil
}

// setInvisible records the member's Invisible flag (in their member record,
// so it holds from the first moment of their next session) and re-announces
// their presence when it changes.
func (s *Server) setInvisible(ctx context.Context, member []byte, invisible bool) error {
	s.mu.Lock()
	m := s.members[string(member)]
	if m == nil {
		s.mu.Unlock()
		return errNotMember()
	}
	if m.Invisible != invisible {
		m.Invisible = invisible
		if err := s.saveMember(ctx, s.db, m); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	s.mu.Unlock()
	s.smu.Lock()
	if s.invisible == nil {
		s.invisible = map[string]bool{}
	}
	changed := s.invisible[string(member)] != invisible
	if invisible {
		s.invisible[string(member)] = true
	} else {
		delete(s.invisible, string(member))
	}
	s.smu.Unlock()
	if changed {
		s.presenceChanged(member)
	}
	return nil
}

// noteInvisible copies a member's Invisible flag into the presence state
// before their session counts as online (handleSession).
func (s *Server) noteInvisible(member []byte) {
	s.mu.RLock()
	m := s.members[string(member)]
	inv := m != nil && m.Invisible
	s.mu.RUnlock()
	s.smu.Lock()
	if s.invisible == nil {
		s.invisible = map[string]bool{}
	}
	if inv {
		s.invisible[string(member)] = true
	} else {
		delete(s.invisible, string(member))
	}
	s.smu.Unlock()
}

func (s *Server) listStatuses(ss *sess, p *wire.ListStatuses) (*wire.ServerMessage, error) {
	s.mu.RLock()
	active := map[string]bool{}
	for k, m := range s.members {
		if m.Status == wire.MemberStatus_MEMBER_STATUS_ACTIVE {
			active[k] = true
		}
	}
	self := s.members[string(ss.member)]
	invisible := self != nil && self.Invisible
	s.mu.RUnlock()
	s.status.mu.Lock()
	keys := make([]string, 0, len(s.status.recs))
	for k := range s.status.recs {
		if active[k] && (len(p.After) == 0 || k > string(p.After)) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := &wire.StatusList{Invisible: invisible}
	size := 0
	for _, k := range keys {
		if len(out.Entries) >= maxStatusItemsPerReply || size >= maxStatusReplyBytes {
			out.More = true
			break
		}
		e := s.statusEntryLocked([]byte(k))
		size += len(e.Blob) + 64
		for _, d := range e.Devices {
			size += len(d)
		}
		out.Entries = append(out.Entries, e)
	}
	s.status.mu.Unlock()
	return &wire.ServerMessage{Body: &wire.ServerMessage_Statuses{Statuses: out}}, nil
}

func (s *Server) sendStatusKeys(ctx context.Context, ss *sess, p *wire.SendStatusKeys) (*wire.ServerMessage, error) {
	if len(p.Drops) == 0 {
		return okBody(), nil
	}
	if len(p.Drops) > maxStatusDropsPerCall {
		return nil, errInvalid("at most 500 status keys per request")
	}
	for _, d := range p.Drops {
		if len(d.Member) == 0 || len(d.Member) > 64 || len(d.Device) == 0 || len(d.Device) > maxStatusDeviceID || len(d.Envelope) == 0 {
			return nil, errInvalid("bad status key")
		}
		if len(d.Envelope) > maxStatusDropBytes {
			return nil, errTooLarge("status key is too large")
		}
	}
	if !s.status.sendLimit.allowN(ss.member, s.now(), statusSendBurst, statusSendWindow) {
		return nil, errRateLimited("too many status keys; try again in a minute")
	}
	s.mu.RLock()
	active := map[string]bool{}
	for _, d := range p.Drops {
		m := s.members[string(d.Member)]
		active[string(d.Member)] = m != nil && m.Status == wire.MemberStatus_MEMBER_STATUS_ACTIVE
	}
	s.mu.RUnlock()
	// Only devices the recipient registered (signed by their member key)
	// receive keys; a drop for any other device id is discarded, as for a
	// non-member. Without this, made-up device ids let one member fill a
	// recipient's mailbox and the server's.
	s.status.mu.Lock()
	registered := map[string]bool{}
	for _, d := range p.Drops {
		if rec := s.status.recs[string(d.Member)]; rec != nil && active[string(d.Member)] {
			for _, dev := range rec.Devices {
				if bytes.Equal(dev.Device, d.Device) {
					registered[string(d.Member)+"\x00"+string(d.Device)] = true
				}
			}
		}
	}
	s.status.mu.Unlock()
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM status_keys`).Scan(&total); err != nil {
		return nil, err
	}
	if total+int64(len(p.Drops)) > maxStatusKeysTotal {
		return nil, errRateLimited("this server holds too many undelivered status keys; try again later")
	}
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	pushes := map[string][]*wire.StatusKeyItem{}
	for _, d := range p.Drops {
		if !active[string(d.Member)] || !registered[string(d.Member)+"\x00"+string(d.Device)] {
			// Not (or no longer) a member, or not (or no longer) one of their
			// status devices: dropped quietly, as a sender's view of the
			// roll can be a moment behind.
			continue
		}
		kid := s.kid(d.Member)
		pair := s.statusPair(d.Member, ss.member, d.Device)
		rec := statusKeyRec{Recipient: d.Member, Sender: ss.member, Device: d.Device, Envelope: d.Envelope, At: now}
		if _, err := tx.ExecContext(ctx, `DELETE FROM status_keys WHERE pair = ?`, pair); err != nil {
			return nil, err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO status_keys (kid, dev, pair, at, data) VALUES (?, ?, ?, ?, ?)`,
			kid, s.statusDev(d.Device), pair, now, s.sealJSON("status_keys", "data", rowKey(pair), &rec))
		if err != nil {
			return nil, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, err
		}
		pushes[string(d.Member)] = append(pushes[string(d.Member)], &wire.StatusKeyItem{Id: uint64(id), Sender: ss.member, Device: d.Device, Envelope: d.Envelope})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// Recipients online now get theirs at once (each device keeps what is
	// for it and acknowledges it with FetchStatusKeys).
	for member, items := range pushes {
		msg := &wire.ServerMessage{Body: &wire.ServerMessage_StatusKeys{StatusKeys: &wire.StatusKeys{Items: items}}}
		for _, rs := range s.sessionsFor([]byte(member)) {
			if rs.getStatus() == wire.MemberStatus_MEMBER_STATUS_ACTIVE {
				rs.send(msg)
			}
		}
	}
	return okBody(), nil
}

func (s *Server) fetchStatusKeys(ctx context.Context, ss *sess, p *wire.FetchStatusKeys) (*wire.ServerMessage, error) {
	if len(p.Ack) > 2*maxStatusItemsPerReply {
		return nil, errInvalid("acknowledge at most 1000 status keys at a time")
	}
	if len(p.Device) == 0 || len(p.Device) > maxStatusDeviceID {
		return nil, errInvalid("bad status device")
	}
	kid := s.kid(ss.member)
	if len(p.Ack) > 0 {
		args := []any{kid}
		marks := make([]string, 0, len(p.Ack))
		for _, id := range p.Ack {
			args = append(args, int64(id))
			marks = append(marks, "?")
		}
		// Only the caller's own drops can be acknowledged.
		if _, err := s.db.ExecContext(ctx, `DELETE FROM status_keys WHERE kid = ? AND id IN (`+strings.Join(marks, ",")+`)`, args...); err != nil {
			return nil, err
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, pair, at, data FROM status_keys WHERE kid = ? AND dev = ? ORDER BY id LIMIT ?`,
		kid, s.statusDev(p.Device), maxStatusItemsPerReply+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &wire.StatusKeys{}
	size := 0
	for rows.Next() {
		var id, at int64
		var pair, data []byte
		if err := rows.Scan(&id, &pair, &at, &data); err != nil {
			return nil, err
		}
		if len(out.Items) >= maxStatusItemsPerReply || size >= maxStatusReplyBytes {
			out.More = true
			break
		}
		var rec statusKeyRec
		if err := s.openJSON("status_keys", "data", rowKey(pair), data, &rec); err != nil {
			return nil, err
		}
		if !bytes.Equal(rec.Recipient, ss.member) || rec.At != at || !bytes.Equal(s.statusDev(rec.Device), s.statusDev(p.Device)) {
			return nil, &TamperError{"status_keys", "kid", rowID(id)}
		}
		size += len(rec.Envelope) + 64
		out.Items = append(out.Items, &wire.StatusKeyItem{Id: uint64(id), Sender: rec.Sender, Device: rec.Device, Envelope: rec.Envelope})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &wire.ServerMessage{Body: &wire.ServerMessage_StatusKeys{StatusKeys: out}}, nil
}

func (s *Server) requestStatusKey(ss *sess, p *wire.RequestStatusKey) (*wire.ServerMessage, error) {
	if len(p.Device) == 0 || len(p.Device) > maxStatusDeviceID || len(p.Member) == 0 {
		return nil, errInvalid("bad status key request")
	}
	if !s.activeMember(p.Member) {
		return nil, errNotFound("no such member")
	}
	if !s.status.requestLimit.allowN(ss.member, s.now(), statusRequestBurst, statusRequestWindow) {
		return nil, errRateLimited("too many status key requests; try again in a minute")
	}
	msg := &wire.ServerMessage{Body: &wire.ServerMessage_StatusKeyRequest{StatusKeyRequest: &wire.StatusKeyRequest{Member: ss.member, Device: p.Device}}}
	for _, rs := range s.sessionsFor(p.Member) {
		if rs.getStatus() == wire.MemberStatus_MEMBER_STATUS_ACTIVE {
			rs.send(msg)
		}
	}
	return okBody(), nil
}

// ---------------------------------------------------------------- lifecycle

// statusMemberGone forgets a member who left or was removed: their status
// devices, ciphertext and the keys waiting for them. Members' clients see
// their entry emptied and stop sharing with them (the next status they post
// uses a new session key).
func (s *Server) statusMemberGone(ctx context.Context, member []byte) {
	s.status.mu.Lock()
	_, had := s.status.recs[string(member)]
	delete(s.status.recs, string(member))
	s.status.mu.Unlock()
	s.bestEffort(ctx, `DELETE FROM statuses WHERE kid = ?`, s.kid(member))
	s.bestEffort(ctx, `DELETE FROM status_keys WHERE kid = ?`, s.kid(member))
	if had {
		s.statusChanged(member)
	}
}

// statusDeviceRevoked drops the status devices a revoked session device
// registered.
func (s *Server) statusDeviceRevoked(ctx context.Context, member, deviceKey []byte) {
	s.status.mu.Lock()
	rec := s.status.recs[string(member)]
	if rec == nil {
		s.status.mu.Unlock()
		return
	}
	keep := rec.Devices[:0]
	removed := false
	for _, d := range rec.Devices {
		if bytes.Equal(d.By, deviceKey) {
			removed = true
			s.bestEffort(ctx, `DELETE FROM status_keys WHERE kid = ? AND dev = ?`, s.kid(member), s.statusDev(d.Device))
			continue
		}
		keep = append(keep, d)
	}
	rec.Devices = keep
	var err error
	if removed {
		err = s.saveStatusLocked(ctx, rec)
	}
	s.status.mu.Unlock()
	if err != nil {
		s.log.Warn("removing a revoked device's status entry", "member", identity.Fingerprint(member), "err", err)
	}
	if removed {
		s.statusChanged(member)
	}
}

// expireStatusKeys deletes key drops nobody fetched within statusKeyTTL
// (gcLoop).
func (s *Server) expireStatusKeys(ctx context.Context, now time.Time) {
	s.bestEffort(ctx, `DELETE FROM status_keys WHERE at < ?`, now.Add(-statusKeyTTL).UnixMilli())
}

// statusEntries builds the entries for members whose status changed, up to
// about statusPushBatchBytes; the rest stay queued for the next batch.
func (s *Server) statusEntries(pending []string) (entries []*wire.StatusEntry, rest []string) {
	s.status.mu.Lock()
	defer s.status.mu.Unlock()
	size := 0
	for i, m := range pending {
		if size >= statusPushBatchBytes {
			return entries, pending[i:]
		}
		e := s.statusEntryLocked([]byte(m))
		size += len(e.Blob) + 64
		for _, d := range e.Devices {
			size += len(d)
		}
		entries = append(entries, e)
	}
	return entries, nil
}
