package community

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/relayurl"
	"github.com/hyperkubeorg/openarcanum/internal/version"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// ---------------------------------------------------------------- retention

const dayMillis = int64(24 * time.Hour / time.Millisecond)

// Purge deletes messages and files past their retention (per channel, else
// the community setting), then destroys the month keys of months that are
// entirely past the longest retention in effect. Something exactly
// `retention` old is purged.
func (s *Server) Purge(ctx context.Context, now time.Time) (msgs, files int, err error) {
	r, err := s.purge(ctx, now)
	return r.msgs, r.files, err
}

type purgeResult struct {
	msgs, files int
	months      []int
}

func (s *Server) purge(ctx context.Context, now time.Time) (purgeResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// The purge deletes files; the next upload recounts the file store.
	defer func() { s.quota.known = false }()
	var out purgeResult
	for _, c := range s.channels {
		msgDays, fileDays := s.retention(c)
		if msgDays > 0 {
			cutoff := now.UnixMilli() - int64(msgDays)*dayMillis
			rows, err := s.db.QueryContext(ctx, `SELECT id FROM messages WHERE channel_id = ? AND created_at <= ?`, c.ID, cutoff)
			if err != nil {
				return out, err
			}
			var ids []int64
			for rows.Next() {
				var id int64
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return out, err
				}
				ids = append(ids, id)
			}
			rows.Close()
			var fileIDs []int64
			for _, id := range ids {
				frows, err := s.db.QueryContext(ctx, `SELECT id FROM files WHERE message_id = ?`, id)
				if err != nil {
					return out, err
				}
				for frows.Next() {
					var fid int64
					if err := frows.Scan(&fid); err != nil {
						frows.Close()
						return out, err
					}
					fileIDs = append(fileIDs, fid)
				}
				frows.Close()
			}
			if len(ids) > 0 {
				if _, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE channel_id = ? AND created_at <= ?`, c.ID, cutoff); err != nil {
					return out, err
				}
				for _, id := range ids {
					s.unindexMessage(id)
					delete(s.pinned, id)
				}
				out.msgs += len(ids)
			}
			// A message's attachments (and link preview image) go with it.
			for _, fid := range fileIDs {
				if res, err := s.db.ExecContext(ctx, `DELETE FROM files WHERE id = ?`, fid); err == nil {
					if n, _ := res.RowsAffected(); n > 0 {
						out.files++
					}
				}
			}
		}
		if fileDays > 0 {
			cutoff := now.UnixMilli() - int64(fileDays)*dayMillis
			res, err := s.db.ExecContext(ctx, `DELETE FROM files WHERE channel_id = ? AND created_at <= ?`, c.ID, cutoff)
			if err != nil {
				return out, err
			}
			n, _ := res.RowsAffected()
			out.files += int(n)
		}
	}
	months, m, f, err := s.expireMonthKeys(ctx, now)
	out.months, out.msgs, out.files = months, out.msgs+m, out.files+f
	return out, err
}

// retention returns a channel's effective message and file retention in
// days (0 keeps forever).
func (s *Server) retention(c *channelRec) (msgDays, fileDays uint32) {
	msgDays, fileDays = s.settings.MessageRetentionDays, s.settings.FileRetentionDays
	if c.MsgRet != nil {
		msgDays = *c.MsgRet
	}
	if c.FileRet != nil {
		fileDays = *c.FileRet
	}
	return msgDays, fileDays
}

// longestRetention is the longest retention in effect over every channel's
// messages and files; keep is true if month keys must be kept (something is
// kept forever, or there are no channels yet). Every stored message and file
// belongs to an existing channel (deleting a channel deletes its content),
// so this bounds the age of everything kept.
func (s *Server) longestRetention() (days uint32, keep bool) {
	for _, c := range s.channels {
		m, f := s.retention(c)
		if m == 0 || f == 0 {
			return 0, true
		}
		days = max(days, m, f)
	}
	return days, len(s.channels) == 0
}

// expireMonthKeys destroys the key of every month whose last millisecond is
// past the longest retention in effect, deleting any rows still in that
// month first (they're past every retention by then). Afterwards the WAL is
// checkpointed and truncated so the deleted key doesn't linger there.
// Callers hold s.mu.
func (s *Server) expireMonthKeys(ctx context.Context, now time.Time) (months []int, msgs, files int, err error) {
	longest, keep := s.longestRetention()
	if keep {
		return nil, 0, 0, nil
	}
	cutoff := now.UnixMilli() - int64(longest)*dayMillis
	rows, err := s.db.QueryContext(ctx, `SELECT month FROM month_keys ORDER BY month`)
	if err != nil {
		return nil, 0, 0, err
	}
	var expired []int
	for rows.Next() {
		var m int
		if err := rows.Scan(&m); err != nil {
			rows.Close()
			return nil, 0, 0, err
		}
		if monthEnd(m)-1 <= cutoff {
			expired = append(expired, m)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, 0, err
	}
	for _, m := range expired {
		ids, nf, err := s.deleteMonth(ctx, m)
		if err != nil {
			return months, msgs, files, err
		}
		for _, id := range ids {
			s.unindexMessage(id)
		}
		s.forgetMonthKey(m)
		months = append(months, m)
		msgs += len(ids)
		files += nf
	}
	if len(months) > 0 {
		if err := s.checkpoint(ctx); err != nil {
			s.log.Warn("WAL checkpoint after month key deletion failed; it runs again after the next deletion or restart", "err", err)
		}
	}
	return months, msgs, files, nil
}

// deleteMonth deletes a month's remaining messages, files and reports and
// its key in one transaction.
func (s *Server) deleteMonth(ctx context.Context, m int) (msgIDs []int64, files int, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM messages WHERE month = ?`, m)
	if err != nil {
		return nil, 0, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, 0, err
		}
		msgIDs = append(msgIDs, id)
	}
	rows.Close()
	if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE month = ?`, m); err != nil {
		return nil, 0, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM files WHERE month = ?`, m)
	if err != nil {
		return nil, 0, err
	}
	n, _ := res.RowsAffected()
	// Reports of that month go with its messages (their reasons are sealed
	// under the same key).
	if _, err := tx.ExecContext(ctx, `DELETE FROM reports WHERE month = ?`, m); err != nil {
		return nil, 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM month_keys WHERE month = ?`, m); err != nil {
		return nil, 0, err
	}
	return msgIDs, int(n), tx.Commit()
}

func (s *Server) purgeLoop(ctx context.Context) {
	t := time.NewTicker(s.cfg.PurgeInterval.Duration)
	defer t.Stop()
	for {
		r, err := s.purge(ctx, s.now())
		s.counters.purgeRuns.Add(1)
		if err != nil {
			s.counters.purgeFailures.Add(1)
			s.log.Error("purge failed", "err", err)
		} else if r.msgs+r.files+len(r.months) > 0 {
			s.log.Info("retention purge", "messages", r.msgs, "files", r.files, "month_keys_deleted", r.months)
		}
		if len(r.months) > 0 {
			// Reports of the deleted months went with them.
			s.pushModQueue()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) gcLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cutoff := s.now().Add(-2 * maxActionSkew).UnixMilli()
		s.bestEffort(ctx, `DELETE FROM action_nonces WHERE at < ?`, cutoff)
		s.recent.prune(s.now().Add(-recentWindow).UnixMilli())
		s.mu.Lock()
		now := s.now()
		s.expireUploads(now)
		s.mu.Unlock()
		s.expireStatusKeys(ctx, now)
		if n, err := s.expireStaged(ctx, now); err != nil {
			s.log.Error("expiring staged uploads failed", "err", err)
		} else if n > 0 {
			s.log.Info("expired staged uploads", "files", n)
		}
	}
}

// ---------------------------------------------------------------- backups

// Backups are age v1 files (age-encryption.org/v1, the published format;
// filippo.io/age) holding a VACUUM INTO snapshot of the database. The file
// key is wrapped with age's scrypt recipient under a passphrase that is the
// storage key in hex, and the payload is age's STREAM (ChaCha20-Poly1305 in
// 64 KiB chunks), so a backup is written and read in bounded memory however
// large the database is, and the stock age tool can open one given the
// storage key. The passphrase is a random 256-bit key, so scrypt's work
// factor adds nothing; it is set low (backupWorkFactor) to keep backups
// fast. docs/operations.md "Backups"; crypto inventory #7.
const (
	backupWorkFactor = 10
	// maxBackupWorkFactor bounds the scrypt work a backup file can ask a
	// restore for.
	maxBackupWorkFactor = 18
	// Pre-release backup formats, recognised only to refuse them clearly.
	backupMagicV1 = "FRAGBK1\x00"
	backupMagicV2 = "FRAGBK2\x00"
)

func (s *Server) backupLoop(ctx context.Context) {
	if s.cfg.BackupInterval.Duration == 0 {
		return
	}
	t := time.NewTicker(s.cfg.BackupInterval.Duration)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		path, err := s.Backup(ctx)
		if err != nil {
			s.log.Error("scheduled backup failed", "err", err)
		} else {
			s.log.Info("backup written", "path", path)
		}
	}
}

// Backup writes an encrypted snapshot now and returns its path. It doesn't
// hold the server's lock: VACUUM INTO reads one consistent state of the
// database while the server keeps serving (WAL), and the encryption
// streams from the snapshot file. Backups run one at a time.
func (s *Server) Backup(ctx context.Context) (string, error) {
	s.bmu.Lock()
	defer s.bmu.Unlock()
	path, err := s.writeBackup(ctx)
	s.countBackup(err)
	return path, err
}

func (s *Server) writeBackup(ctx context.Context) (string, error) {
	if err := os.MkdirAll(s.cfg.BackupDir, 0o700); err != nil {
		return "", err
	}
	stamp := s.now().UTC().Format("20060102-150405.000")
	tmp := filepath.Join(s.cfg.BackupDir, ".snapshot-"+stamp+".db")
	defer os.Remove(tmp)
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, tmp); err != nil {
		return "", err
	}
	if s.backupHook != nil {
		s.backupHook()
	}
	snap, err := os.Open(tmp)
	if err != nil {
		return "", err
	}
	defer snap.Close()
	out := filepath.Join(s.cfg.BackupDir, "community-"+strings.ReplaceAll(stamp, ".", "")+".fbk")
	part := out + ".part"
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	defer os.Remove(part)
	err = encryptBackup(s.storageKey, f, snap)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(part, out)
	}
	if err != nil {
		return "", err
	}
	if d, err := os.Open(s.cfg.BackupDir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	s.pruneBackups()
	return out, nil
}

func (s *Server) pruneBackups() {
	matches, _ := filepath.Glob(filepath.Join(s.cfg.BackupDir, "community-*.fbk"))
	sort.Strings(matches)
	for len(matches) > s.cfg.BackupKeep {
		os.Remove(matches[0])
		matches = matches[1:]
	}
}

func backupPassphrase(key []byte) string { return hex.EncodeToString(key) }

// encryptBackup streams src into dst as an age file for the storage key.
func encryptBackup(key []byte, dst io.Writer, src io.Reader) error {
	r, err := age.NewScryptRecipient(backupPassphrase(key))
	if err != nil {
		return err
	}
	r.SetWorkFactor(backupWorkFactor)
	w, err := age.Encrypt(dst, r)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, src); err != nil {
		return err
	}
	return w.Close()
}

// OpenBackup returns a reader of a backup's plaintext. The plaintext is
// authenticated chunk by chunk; a damaged or truncated backup makes a read
// fail (at the latest at the end), so a caller must read to io.EOF without
// error before trusting what it read.
func OpenBackup(key []byte, src io.Reader) (io.Reader, error) {
	br := bufio.NewReader(src)
	if head, _ := br.Peek(len(backupMagicV1)); string(head) == backupMagicV1 || string(head) == backupMagicV2 {
		return nil, errors.New("this backup was made by a pre-release version and can't be restored")
	}
	id, err := age.NewScryptIdentity(backupPassphrase(key))
	if err != nil {
		return nil, err
	}
	id.SetMaxWorkFactor(maxBackupWorkFactor)
	r, err := age.Decrypt(br, id)
	var noMatch *age.NoIdentityMatchError
	switch {
	case errors.As(err, &noMatch) || errors.Is(err, age.ErrIncorrectIdentity):
		return nil, errors.New("backup can't be decrypted with this storage key")
	case err != nil:
		return nil, errors.New("not a community backup (or it is damaged)")
	}
	return r, nil
}

// ---------------------------------------------------------------- maintainer nodes

type httpClient struct{ c *http.Client }

// newHTTPClient announces this build's protocol versions on every request
// (docs/protocol.md §7).
func newHTTPClient() httpClient {
	return httpClient{c: &http.Client{Timeout: 10 * time.Second, Transport: version.Transport{}}}
}

// statusError is a maintainer node's refusal (a non-200 status).
type statusError struct {
	url  string
	code int
	msg  string
}

func (e *statusError) Error() string { return fmt.Sprintf("%s: %d %s", e.url, e.code, e.msg) }

func (h httpClient) post(ctx context.Context, url string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	resp, err := h.c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := version.CheckResponse(resp); err != nil {
		return fmt.Errorf("%s: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &statusError{url: url, code: resp.StatusCode, msg: strings.TrimSpace(string(msg))}
	}
	return nil
}

func (h httpClient) get(ctx context.Context, url string, out proto.Message) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := h.c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := version.CheckResponse(resp); err != nil {
		return fmt.Errorf("%s: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	return proto.Unmarshal(b, out)
}

// noDirectoryFor is how long a maintainer whose directory is off isn't
// sent listings.
const noDirectoryFor = time.Hour

func (s *Server) directoryOff(node string) bool {
	s.noDirMu.Lock()
	defer s.noDirMu.Unlock()
	at, ok := s.noDirectory[node]
	return ok && s.now().Sub(at) < noDirectoryFor
}

func (s *Server) noteNoDirectory(node string) {
	s.noDirMu.Lock()
	defer s.noDirMu.Unlock()
	if _, known := s.noDirectory[node]; !known {
		s.log.Info("maintainer node lists no communities (its directory is off); the listing isn't sent there", "maintainer", node)
	}
	s.noDirectory[node] = s.now()
}

// maintainerURLs are the nodes to publish to and read from: each
// configured maintainer cluster's members (from its verified member list,
// refreshed every 6 h), then the plain URLs (docs/protocol.md §7.5).
func (s *Server) maintainerURLs(ctx context.Context) []string {
	var op []byte
	if s.cfg.MaintainerOperatorKey != "" {
		op, _ = identity.Decode(s.cfg.MaintainerOperatorKey)
	}
	return s.clusters.Expand(ctx, s.cfg.Maintainers, op)
}

func (s *Server) fetchRelays(ctx context.Context) ([]*wire.RelayInfo, error) {
	seen := map[string]*wire.RelayInfo{}
	var lastErr error
	for _, m := range s.maintainerURLs(ctx) {
		var list wire.RelayList
		if err := s.http.get(ctx, strings.TrimRight(m, "/")+"/v1/relays", &list); err != nil {
			lastErr = err
			continue
		}
		for _, r := range list.Relays {
			if u, err := relayurl.Canonical(r.Url); err == nil {
				r.Url = u
				seen[u] = r
			}
		}
	}
	if len(seen) == 0 {
		return nil, lastErr
	}
	out := make([]*wire.RelayInfo, 0, len(seen))
	for _, r := range seen {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Url < out[j].Url })
	return out, nil
}

func (s *Server) requestPublish() {
	select {
	case s.publishCh <- struct{}{}:
	default:
	}
}

func (s *Server) publishLoop(ctx context.Context) {
	t := time.NewTicker(s.cfg.PublishInterval.Duration)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.publishCh:
			// Coalesce bursts of changes.
			select {
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
		}
		if err := s.PublishNow(ctx); err != nil {
			s.log.Warn("publish to maintainers failed", "err", err)
		}
	}
}

// PublishNow sends the signed route (always) and listing (public communities,
// or a withdrawal) to every maintainer node.
func (s *Server) PublishNow(ctx context.Context) error {
	if len(s.cfg.Maintainers) == 0 {
		return nil
	}
	s.mu.RLock()
	claimed := s.ownerKey != nil
	key, cert := s.key, s.cert
	settings := cloneSettings(s.settings)
	memberRange := s.memberRange()
	owner := s.ownerKey
	s.mu.RUnlock()
	if !claimed || cert == nil {
		return nil
	}
	if _, err := identity.VerifyServerCert(cert, key.Public, owner); err != nil {
		// A restored server waits for the owner to adopt it (move.go).
		return nil
	}
	relays := s.AttachedRelays()
	if len(relays) == 0 {
		return nil
	}
	now := s.now().UnixMilli()
	route, _ := proto.Marshal(&wire.Route{RouteId: s.routeID, ServerKey: key.Public, Relays: relays, Seq: uint64(now), PublishedAt: now,
		ReregisteredAt: s.reregisteredAt(ctx)})
	signedRoute, _ := proto.Marshal(&wire.SignedRoute{Route: route, Signature: key.Sign(identity.CtxRoute, route), Cert: cert})

	listed, _ := s.getMeta(ctx, "listed")
	public := settings.Visibility == wire.Visibility_VISIBILITY_PUBLIC
	var signedListing []byte
	var icon []byte
	if public || len(listed) > 0 {
		l := &wire.Listing{
			Community: owner, ServerKey: key.Public, RouteId: s.routeID, Seq: uint64(now), PublishedAt: now,
			Withdrawn: !public,
		}
		if public {
			l.Name, l.Description, l.Tags, l.Language = settings.Name, settings.Description, settings.Tags, settings.Language
			l.MemberRange, l.JoinPolicy, l.Relays = memberRange, settings.JoinPolicy, relays
			l.PublicJoinKey = s.publicJoinKey(ctx)
			if len(settings.Icon) > 0 {
				// The icon travels next to the signed bytes, bound by its
				// hash, so directory snapshots can leave it out (docs/protocol.md §7.2).
				sum := sha256.Sum256(settings.Icon)
				l.IconSha256, icon = sum[:], settings.Icon
			}
		}
		lb, _ := proto.Marshal(l)
		signedListing, _ = proto.Marshal(&wire.SignedListing{Listing: lb, Signature: key.Sign(identity.CtxListing, lb), Cert: cert, Icon: icon})
	}
	var errs []error
	ok := false
	for _, m := range s.maintainerURLs(ctx) {
		base := strings.TrimRight(m, "/")
		if err := s.http.post(ctx, base+"/v1/routes", signedRoute); err != nil {
			if se := (*statusError)(nil); errors.As(err, &se) && se.code == http.StatusGone {
				s.noteReaped(ctx, m)
			}
			errs = append(errs, err)
			continue
		}
		if signedListing != nil && !s.directoryOff(m) {
			if err := s.http.post(ctx, base+"/v1/listings", signedListing); err != nil {
				var se *statusError
				if !errors.As(err, &se) || se.code != http.StatusNotImplemented {
					errs = append(errs, err)
					continue
				}
				// 501: this node lists no communities (docs/protocol.md §7.2):
				// not an error; members join through invites.
				s.noteNoDirectory(m)
			}
		}
		ok = true
	}
	if ok && signedListing != nil {
		if public {
			if err := s.setMeta(ctx, s.db, "listed", []byte{1}); err != nil {
				s.log.Error("record listing state", "err", err)
			}
		} else {
			s.bestEffort(ctx, `DELETE FROM meta WHERE k = 'listed'`)
		}
	}
	return errors.Join(errs...)
}
