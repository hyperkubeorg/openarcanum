package community

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// FileChunkSize is the plaintext size of one stored file chunk.
const FileChunkSize = 1 << 20

type upload struct {
	id        int64
	member    []byte
	channelID int64
	name      string
	mime      string
	text      string
	size      uint64
	staged    bool
	buf       bytes.Buffer
	started   time.Time
	// last is when the upload began or last received a chunk.
	last time.Time
}

// Uploads are held in memory until they finish, so what may be in flight
// is bounded: per member (MaxUploadBytesPerMember, announced sizes of their
// unfinished uploads) and on the server (MaxUploadBytesInFlight), and an
// unfinished upload is dropped after UploadIdleTimeout without a chunk or
// UploadMaxAge in all. Variables for tests.
var (
	MaxUploadBytesPerMember uint64 = 2 * MaxFileSizeLimit
	MaxUploadBytesInFlight  uint64 = 512 << 20
	UploadIdleTimeout              = 3 * time.Minute
	UploadMaxAge                   = 15 * time.Minute
)

// expireUploads drops unfinished uploads past UploadIdleTimeout or
// UploadMaxAge (and the room they held). Callers hold s.mu.
func (s *Server) expireUploads(now time.Time) {
	for id, u := range s.uploads {
		if now.Sub(u.last) > UploadIdleTimeout || now.Sub(u.started) > UploadMaxAge {
			delete(s.uploads, id)
		}
	}
}

func (s *Server) uploadBegin(ctx context.Context, ss *sess, b *wire.UploadBegin) (*wire.ServerMessage, error) {
	name := strings.TrimSpace(b.Name)
	if name == "" || utf8.RuneCountInString(name) > 200 || strings.ContainsAny(name, "/\\\x00") {
		return nil, errInvalid("file names are 1 to 200 characters without slashes")
	}
	if b.Text != "" {
		if _, err := validText(b.Text); err != nil {
			return nil, err
		}
	}
	s.mu.RLock()
	newUntil := s.newMemberUntil(s.members[string(ss.member)])
	s.mu.RUnlock()
	if newUntil != 0 {
		return nil, errDenied(fmt.Sprintf("New members can attach files from %s.", fmtUTC(newUntil)))
	}
	if err := s.preGate(ss, b.ChannelId, PermViewChannel|PermSendMessages|PermAttachFiles); err != nil {
		return nil, err
	}
	hits := s.autoModCheck(b.Text)
	s.mu.Lock()
	var after []func()
	defer s.unlockThen(&after)
	me := s.members[string(ss.member)]
	if err := s.participationError(me); err != nil {
		return nil, err
	}
	c, err := s.readableChannel(ss, b.ChannelId, PermViewChannel|PermSendMessages|PermAttachFiles)
	if err != nil {
		return nil, err
	}
	if err := s.messagesAllowed(c); err != nil {
		return nil, err
	}
	if err := s.allowed(FeatureAttachments, c, nil, 0); err != nil {
		return nil, err
	}
	if err := s.attachmentKindAllowed(b.Mime, nil); err != nil {
		return nil, err
	}
	// A staged upload posts nothing yet; the Post that attaches it is
	// subject to slowmode.
	if !b.Staged {
		if err := s.checkSlowmode(c, s.members[string(ss.member)]); err != nil {
			return nil, err
		}
	}
	if b.Size == 0 {
		return nil, errInvalid("file is empty")
	}
	if b.Size > s.settings.MaxFileSize {
		return nil, errTooLarge(fmt.Sprintf("files may be at most %s in this community", humanBytes(s.settings.MaxFileSize)))
	}
	s.expireUploads(s.now())
	n := 0
	var mine, all uint64
	for _, u := range s.uploads {
		all += u.size
		if string(u.member) == string(ss.member) {
			n++
			mine += u.size
		}
	}
	if n >= 4 || mine+b.Size > MaxUploadBytesPerMember {
		return nil, errRateLimited("too many uploads in progress")
	}
	if all+b.Size > MaxUploadBytesInFlight {
		return nil, errRateLimited("the server is busy with other uploads; try again in a minute")
	}
	if b.Staged {
		if err := s.checkStagedLimits(ctx, ss.member, c.ID); err != nil {
			return nil, err
		}
	}
	if err := s.checkUploadQuota(ctx, ss.member, b.Size); err != nil {
		return nil, err
	}
	// The caption is checked now, so a blocked one fails before the upload;
	// AutoMod last, so it acts only on an upload every other check allows.
	if _, blockedAfter, err := s.gateText(ctx, me, c, b.Text, hits); err != nil {
		after = blockedAfter
		return nil, err
	}
	s.chargeUpload(ss.member, b.Size)
	s.nextUpload++
	u := &upload{id: s.nextUpload, member: ss.member, channelID: c.ID, name: name, mime: truncate(b.Mime, 100), text: b.Text, size: b.Size, staged: b.Staged, started: s.now(), last: s.now()}
	s.uploads[u.id] = u
	return &wire.ServerMessage{Body: &wire.ServerMessage_UploadStarted{UploadStarted: &wire.UploadStarted{UploadId: u.id, ChunkSize: FileChunkSize}}}, nil
}

// UploadQuotaWindow is the rolling window of the per-member upload quota
// (Config.UploadQuotaPerMember).
const UploadQuotaWindow = 24 * time.Hour

type uploadUse struct {
	at   time.Time
	size uint64
}

// uploadQuota holds the state behind the file quotas; s.mu guards it.
type uploadQuota struct {
	// used lists each member's accepted uploads within UploadQuotaWindow.
	// It is kept in memory only; a restart forgets it.
	used map[string][]uploadUse
	// stored is the ciphertext bytes in file_chunks when known is set.
	// storeFile adds to it. Deletes (retention, purges, message and
	// channel deletes) only lower the real total, so stored may run high
	// between recounts: the store is recounted when it is unknown, after a
	// purge, and before any upload is refused for lack of room.
	stored int64
	known  bool
}

// storedSize is the ciphertext size of a file of size plaintext bytes.
func storedSize(size uint64) int64 {
	chunks := (size + FileChunkSize - 1) / FileChunkSize
	return int64(size + chunks*chacha20poly1305.Overhead)
}

// checkUploadQuota refuses an upload of size bytes that would take the
// member past UploadQuotaPerMember in the last UploadQuotaWindow, or the
// file store (with the uploads in progress) past FileStoreMax. Callers
// hold s.mu for writing.
func (s *Server) checkUploadQuota(ctx context.Context, member []byte, size uint64) error {
	cut := s.now().Add(-UploadQuotaWindow)
	uses := s.quota.used[string(member)]
	for len(uses) > 0 && !uses[0].at.After(cut) {
		uses = uses[1:]
	}
	if len(uses) == 0 {
		delete(s.quota.used, string(member))
	} else {
		s.quota.used[string(member)] = uses
	}
	var used uint64
	for _, u := range uses {
		used += u.size
	}
	if limit := uint64(s.cfg.UploadQuotaPerMember); used+size > limit {
		return errRateLimited(fmt.Sprintf("you've uploaded %s in the last 24 hours; this community allows %s per member per day", humanBytes(used), humanBytes(limit)))
	}
	pending := storedSize(size)
	for _, u := range s.uploads {
		pending += storedSize(u.size)
	}
	ok, err := s.fileStoreHasRoom(ctx, pending)
	if err != nil {
		return err
	}
	if !ok {
		return errTooLarge("this community's file storage is full; try again after older files expire")
	}
	return nil
}

// chargeUpload records an accepted upload against the member's quota.
// Callers hold s.mu for writing.
func (s *Server) chargeUpload(member []byte, size uint64) {
	if s.quota.used == nil {
		s.quota.used = map[string][]uploadUse{}
	}
	s.quota.used[string(member)] = append(s.quota.used[string(member)], uploadUse{at: s.now(), size: size})
}

// fileStoreHasRoom reports whether n more ciphertext bytes fit under
// FileStoreMax, recounting the store when the cached total is unknown or
// says no. Callers hold s.mu for writing.
func (s *Server) fileStoreHasRoom(ctx context.Context, n int64) (bool, error) {
	if s.quota.known && s.quota.stored+n <= s.cfg.FileStoreMax {
		return true, nil
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(data)), 0) FROM file_chunks`).Scan(&total); err != nil {
		return false, err
	}
	s.quota.stored, s.quota.known = total, true
	return total+n <= s.cfg.FileStoreMax, nil
}

// Unposted (staged) uploads expire after StagedFileTTL. Until then each
// member may hold MaxStagedPerMember of them in a channel, so one member
// can't use up a channel's room; MaxStagedPerChannel bounds the channel.
const (
	MaxStagedPerMember  = 4 * MaxAttachments
	MaxStagedPerChannel = 10 * MaxStagedPerMember
)

// checkStagedLimits refuses a new staged upload over the member's or the
// channel's limit. The uploader is sealed in each file's metadata, so the
// member's count reads the channel's staged files (at most
// MaxStagedPerChannel). Callers hold s.mu.
func (s *Server) checkStagedLimits(ctx context.Context, member []byte, channelID int64) error {
	mine := 0
	for _, u := range s.uploads {
		if u.staged && u.channelID == channelID && string(u.member) == string(member) {
			mine++
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM files WHERE message_id = 0 AND channel_id = ?`, channelID)
	if err != nil {
		return err
	}
	var ids []int64
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
	if len(ids) >= MaxStagedPerChannel {
		return errRateLimited("too many unsent attachments in this channel; send or wait a while")
	}
	for _, id := range ids {
		_, meta, err := s.loadFileMeta(ctx, id)
		if err != nil {
			continue // incomplete, or expired since
		}
		if string(meta.Uploader) == string(member) {
			mine++
		}
	}
	if mine >= MaxStagedPerMember {
		return errRateLimited(fmt.Sprintf("you have %d unsent attachments in this channel; send them or wait a while", mine))
	}
	return nil
}

func (s *Server) uploadChunk(ctx context.Context, ss *sess, c *wire.UploadChunk) (*wire.ServerMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.uploads[c.UploadId]
	if u == nil || string(u.member) != string(ss.member) {
		return nil, errNotFound("no such upload")
	}
	if c.Offset != uint64(u.buf.Len()) {
		return nil, errInvalid(fmt.Sprintf("expected offset %d", u.buf.Len()))
	}
	if uint64(u.buf.Len())+uint64(len(c.Data)) > u.size || len(c.Data) > FileChunkSize {
		delete(s.uploads, u.id)
		return nil, errTooLarge("upload is larger than announced")
	}
	u.buf.Write(c.Data)
	u.last = s.now()
	return okBody(), nil
}

func (s *Server) uploadFinish(ctx context.Context, ss *sess, f *wire.UploadFinish) (*wire.ServerMessage, error) {
	s.mu.Lock()
	var after []func()
	defer s.unlockThen(&after)
	u := s.uploads[f.UploadId]
	if u == nil || string(u.member) != string(ss.member) {
		return nil, errNotFound("no such upload")
	}
	delete(s.uploads, u.id)
	// A timeout can start while the file is uploading.
	me := s.members[string(ss.member)]
	if err := s.participationError(me); err != nil {
		return nil, err
	}
	hits := s.autoModCheck(u.text)
	data := u.buf.Bytes()
	if uint64(len(data)) != u.size {
		return nil, errInvalid("upload is incomplete")
	}
	if sum := sha256.Sum256(data); len(f.Sha256) > 0 && !bytes.Equal(sum[:], f.Sha256) {
		return nil, errInvalid("upload checksum mismatch")
	}
	c, err := s.readableChannel(ss, u.channelID, PermViewChannel|PermSendMessages|PermAttachFiles)
	if err != nil {
		return nil, err
	}
	if err := s.allowed(FeatureAttachments, c, nil, 0); err != nil {
		return nil, err
	}
	if err := s.attachmentKindAllowed(u.mime, data); err != nil {
		return nil, err
	}
	o, blockedAfter, err := s.gateText(ctx, me, c, u.text, hits)
	if err != nil {
		after = blockedAfter
		return nil, err
	}
	fileID, err := s.storeFile(ctx, c.ID, 0, u.name, u.mime, ss.member, data)
	if err != nil {
		return nil, err
	}
	if u.staged {
		return &wire.ServerMessage{Body: &wire.ServerMessage_StagedFile{StagedFile: &wire.StagedFile{File: s.fileRef(ctx, fileID)}}}, nil
	}
	body := &wire.Message{Author: ss.member, Text: u.text}
	s.parseMentions(me, c, u.text).apply(body)
	m, err := s.insertMessage(ctx, c, body, []int64{fileID})
	if err != nil {
		return nil, err
	}
	after = s.autoModAct(ctx, o, me, c.ID, m.Id)
	return &wire.ServerMessage{Body: &wire.ServerMessage_Message{Message: m}}, nil
}

// storeFile encrypts and stores a file for channelID (messageID 0 = not yet
// posted) and returns its id; callers hold s.mu.
func (s *Server) storeFile(ctx context.Context, channelID, messageID int64, name, mime string, uploader, data []byte) (int64, error) {
	fileKey := randomBytes(chacha20poly1305.KeySize)
	aead, _ := chacha20poly1305.New(fileKey)
	now := s.now().UnixMilli()
	month := monthOf(now)
	// The month key is created outside the transaction (atrest.go monthKey).
	monthKey, err := s.monthKey(ctx, month, true)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO files (channel_id, message_id, created_at, month, meta) VALUES (?, ?, ?, ?, x'')`, channelID, messageID, now, month)
	if err != nil {
		return 0, err
	}
	fileID, _ := res.LastInsertId()
	chunks := 0
	var stored int64
	for off := 0; off < len(data); off += FileChunkSize {
		end := min(off+FileChunkSize, len(data))
		ct := aead.Seal(nil, chunkNonce(uint32(chunks)), data[off:end], fileChunkAAD(fileID, uint32(chunks)))
		if _, err := tx.ExecContext(ctx, `INSERT INTO file_chunks (file_id, idx, data) VALUES (?, ?, ?)`, fileID, chunks, ct); err != nil {
			return 0, err
		}
		stored += int64(len(ct))
		chunks++
	}
	meta, _ := json.Marshal(&fileMeta{Name: name, Mime: mime, Size: uint64(len(data)), Key: fileKey, Chunks: uint32(chunks), Uploader: uploader,
		ChannelID: channelID, MessageID: messageID})
	if _, err := tx.ExecContext(ctx, `UPDATE files SET meta = ?, complete = 1 WHERE id = ?`,
		sealWith(monthKey, "files", "meta", rowID(fileID), meta), fileID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	s.quota.stored += stored
	return fileID, nil
}

// chunkNonce and fileChunkAAD define stored-chunk encryption; clients mirror
// them to decrypt (docs/protocol.md §6.3).
func chunkNonce(idx uint32) []byte {
	n := make([]byte, chacha20poly1305.NonceSize)
	copy(n[8:], be32(idx))
	return n
}

func fileChunkAAD(fileID int64, idx uint32) []byte {
	aad := []byte("openarcanum/file-chunk/v1")
	aad = append(aad, be64(fileID)...)
	return append(aad, be32(idx)...)
}

// loadFileMeta reads and decrypts a complete file's metadata. It returns
// errNotFound for a missing, incomplete or retention-expired file; a row
// that fails authentication is a TamperError.
func (s *Server) loadFileMeta(ctx context.Context, fileID int64) (channelID int64, meta *fileMeta, err error) {
	var month int
	var messageID int64
	var enc []byte
	err = s.db.QueryRowContext(ctx, `SELECT channel_id, message_id, month, meta FROM files WHERE id = ? AND complete = 1`, fileID).Scan(&channelID, &messageID, &month, &enc)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, errNotFound("that file has expired or was deleted")
	}
	if err != nil {
		return 0, nil, err
	}
	pt, err := s.openMonth(ctx, month, "files", "meta", rowID(fileID), enc)
	if errors.Is(err, errMonthKeyDeleted) {
		return 0, nil, errNotFound("that file has expired or was deleted")
	}
	if err != nil {
		return 0, nil, err
	}
	meta = &fileMeta{}
	if err := json.Unmarshal(pt, meta); err != nil {
		return 0, nil, &TamperError{"files", "meta", rowID(fileID)}
	}
	if meta.ChannelID != channelID {
		return 0, nil, &TamperError{"files", "channel_id", rowID(fileID)}
	}
	if meta.MessageID != messageID {
		return 0, nil, &TamperError{"files", "message_id", rowID(fileID)}
	}
	return channelID, meta, nil
}

// attachFile links a staged file to the message that posts it: it sets
// files.message_id and reseals the file's metadata with the message id, so
// the plaintext column stays bound (loadFileMeta checks it). Fails if the
// file was already posted or has expired. Runs in the message's
// transaction.
func (s *Server) attachFile(ctx context.Context, tx *sql.Tx, fileID, messageID int64) error {
	var month int
	var cur int64
	var enc []byte
	err := tx.QueryRowContext(ctx, `SELECT message_id, month, meta FROM files WHERE id = ? AND complete = 1`, fileID).Scan(&cur, &month, &enc)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && cur != 0) {
		return errConflict("an attachment was already posted or has expired")
	}
	if err != nil {
		return err
	}
	k, err := s.monthKey(ctx, month, false)
	if errors.Is(err, errMonthKeyDeleted) {
		return errConflict("an attachment was already posted or has expired")
	}
	if err != nil {
		return err
	}
	pt, err := openWith(k, "files", "meta", rowID(fileID), enc)
	if err != nil {
		return err
	}
	var meta fileMeta
	if err := json.Unmarshal(pt, &meta); err != nil || meta.MessageID != 0 {
		return &TamperError{"files", "message_id", rowID(fileID)}
	}
	meta.MessageID = messageID
	b, _ := json.Marshal(&meta)
	_, err = tx.ExecContext(ctx, `UPDATE files SET message_id = ?, meta = ? WHERE id = ?`, messageID, sealWith(k, "files", "meta", rowID(fileID), b), fileID)
	return err
}

// fileRef builds the reference (with key) carried in messages; callers hold s.mu.
func (s *Server) fileRef(ctx context.Context, fileID int64) *wire.FileRef {
	_, meta, err := s.loadFileMeta(ctx, fileID)
	var re *rpcError
	switch {
	case errors.As(err, &re):
		return &wire.FileRef{FileId: fileID, Name: "(expired file)"}
	case err != nil:
		s.log.Error("file metadata unreadable", "file", fileID, "err", err)
		return &wire.FileRef{FileId: fileID, Name: "(unreadable file)"}
	}
	return &wire.FileRef{FileId: fileID, Name: meta.Name, Size: meta.Size, Mime: meta.Mime, Key: meta.Key, ChunkSize: FileChunkSize}
}

func (s *Server) fileGet(ctx context.Context, ss *sess, g *wire.FileGet) (*wire.ServerMessage, error) {
	s.mu.RLock()
	channelID, meta, err := s.loadFileMeta(ctx, g.FileId)
	if err != nil {
		s.mu.RUnlock()
		return nil, err
	}
	chunks := meta.Chunks
	if _, err := s.readableChannel(ss, channelID, PermViewChannel); err != nil {
		s.mu.RUnlock()
		return nil, err
	}
	if g.Chunk >= chunks {
		s.mu.RUnlock()
		return nil, errInvalid("no such chunk")
	}
	var data []byte
	err = s.db.QueryRowContext(ctx, `SELECT data FROM file_chunks WHERE file_id = ? AND idx = ?`, g.FileId, g.Chunk).Scan(&data)
	s.mu.RUnlock()
	if err != nil {
		return nil, errNotFound("chunk missing")
	}
	// Per-community bandwidth limit for downloads (docs/settings.md).
	if err := s.files.wait(ctx, len(data)); err != nil {
		return nil, err
	}
	return &wire.ServerMessage{Body: &wire.ServerMessage_FileChunk{FileChunk: &wire.FileChunk{FileId: g.FileId, Chunk: g.Chunk, Chunks: chunks, Data: data}}}, nil
}

func humanBytes(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

// byteBucket is a shared token bucket; rate 0 means unlimited.
type byteBucket struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func newByteBucket(rate uint64) *byteBucket {
	b := &byteBucket{last: time.Now()}
	b.setRate(rate)
	return b
}

func (b *byteBucket) setRate(rate uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rate = float64(rate)
	b.burst = max(float64(rate), FileChunkSize)
	b.tokens = b.burst
}

func (b *byteBucket) wait(ctx context.Context, n int) error {
	b.mu.Lock()
	if b.rate == 0 {
		b.mu.Unlock()
		return nil
	}
	now := time.Now()
	b.tokens = min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
	b.last = now
	b.tokens -= float64(n)
	var d time.Duration
	if b.tokens < 0 {
		d = time.Duration(-b.tokens / b.rate * float64(time.Second))
	}
	b.mu.Unlock()
	if d == 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
