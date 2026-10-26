package meta

// DM blob store (docs/protocol.md §11).
//
// A blob is one client-encrypted file. Its id is the SHA-256 of the
// ciphertext, checked by the node once the last chunk arrives. The node never
// learns who the recipients are: the sender registers one fetch-token hash
// per recipient device, a device downloads with its token and then releases
// it, and the blob is deleted once every token is released or blob_ttl has
// passed since the upload started, whichever comes first.
//
// Each upload is stored under its upload id, SHA-256 over the blob id, the
// size and the sorted token hashes (BlobUploadID). Only the sender knows all
// the token hashes, so only the sender can resume an upload or send it
// chunks; anyone else who names the same blob id starts a separate upload of
// their own. That keeps the answers to create and chunk the same whether or
// not a blob with that id exists, so they can't be used to probe for it (a
// sender learning that every recipient has fetched would be a read receipt).
//
// Ciphertext lives in files under DATA/blobs (one file per upload, written in
// place while uploading as "<upload id>.part" and renamed to "<upload id>"
// once verified); meta.db holds only the ids, size, chunk bitmap, times and
// token hashes. Files rather than SQLite rows because blobs are up to
// blob_max_size (26 MiB) each and short-lived: chunks are written in place
// without rewriting a large row, nothing large passes through the WAL, and
// deleting a file returns its space immediately instead of leaving free
// pages that need a VACUUM.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/store"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

const (
	// BlobChunkSize is the upload and download unit, small enough for any
	// reverse proxy's default request limit.
	BlobChunkSize = 1 << 20
	// MaxBlobTokens is the most fetch tokens one blob can carry: a group DM
	// of 10 members with up to 20 devices each.
	MaxBlobTokens = 200
	// BlobStaleUpload is how long an unfinished upload may go without a new
	// chunk (one it didn't have yet) before it is discarded.
	BlobStaleUpload = time.Hour
	// BlobMaxUploadTime is how long an upload may stay unfinished at all,
	// however often chunks arrive, so its reserved size can't be held
	// indefinitely.
	BlobMaxUploadTime = 6 * time.Hour
	// BlobQuotaWindow is the rolling window for the per-network blob quotas
	// (blob_quota_per_ip, blob_uploads_per_ip, blob_tokens_per_ip).
	BlobQuotaWindow = 24 * time.Hour

	blobCreateMaxBody = 64 << 10
	blobChunkMaxBody  = BlobChunkSize + 1<<10
	blobSmallMaxBody  = 1 << 10
)

const blobSchema = `
CREATE TABLE blobs (
	hash BLOB PRIMARY KEY,
	size INTEGER NOT NULL,
	received BLOB NOT NULL,
	complete INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	touched_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL
);
CREATE INDEX blobs_expiry ON blobs (expires_at);
CREATE TABLE blob_tokens (
	hash BLOB NOT NULL,
	token_hash BLOB NOT NULL,
	PRIMARY KEY (hash, token_hash)
) WITHOUT ROWID;
`

// blobSchemaUploads keys blobs by upload id (schema version 3). Existing rows
// keep their blob id as their upload id, so their files keep their names; a
// resumed schema 2 upload starts over under its new id and the
// old one is collected as abandoned.
const blobSchemaUploads = `
CREATE TABLE blobs_v3 (
	id BLOB PRIMARY KEY,
	hash BLOB NOT NULL,
	size INTEGER NOT NULL,
	received BLOB NOT NULL,
	complete INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	touched_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL
);
INSERT INTO blobs_v3 (id, hash, size, received, complete, created_at, touched_at, expires_at)
	SELECT hash, hash, size, received, complete, created_at, touched_at, expires_at FROM blobs;
CREATE TABLE blob_tokens_v3 (
	upload BLOB NOT NULL,
	token_hash BLOB NOT NULL,
	PRIMARY KEY (upload, token_hash)
) WITHOUT ROWID;
INSERT INTO blob_tokens_v3 (upload, token_hash) SELECT hash, token_hash FROM blob_tokens;
DROP TABLE blob_tokens;
DROP TABLE blobs;
ALTER TABLE blobs_v3 RENAME TO blobs;
ALTER TABLE blob_tokens_v3 RENAME TO blob_tokens;
CREATE INDEX blobs_expiry ON blobs (expires_at);
CREATE INDEX blobs_hash ON blobs (hash);
CREATE INDEX blob_tokens_token ON blob_tokens (token_hash);
`

// BlobTokenHash is what a sender registers for a fetch token, so the node
// can check a token without storing it.
func BlobTokenHash(token []byte) []byte {
	h := sha256.New()
	h.Write([]byte("openarcanum/blob-token/v1\x00"))
	h.Write(token)
	return h.Sum(nil)
}

// BlobUploadID names one upload: SHA-256("openarcanum/blob-upload/v1" || 0x00
// || hash || size (8 bytes, big-endian) || the token hashes in byte order).
func BlobUploadID(hash []byte, size uint64, tokenHashes [][]byte) []byte {
	sorted := slices.Clone(tokenHashes)
	slices.SortFunc(sorted, bytes.Compare)
	h := sha256.New()
	h.Write([]byte("openarcanum/blob-upload/v1\x00"))
	h.Write(hash)
	h.Write(binary.BigEndian.AppendUint64(nil, size))
	for _, th := range sorted {
		h.Write(th)
	}
	return h.Sum(nil)
}

// blobStore is the on-disk store, shared by the HTTP handlers and the
// `meta blobs` admin commands.
type blobStore struct {
	db  *sql.DB
	dir string
	now func() time.Time
	log *slog.Logger
	// mu serializes changes to blob rows and files within this process.
	mu sync.Mutex
	// onStored, if set, learns of every upload that completes (the node
	// logs it for its cluster peers).
	onStored func(context.Context, *blobRow)
}

func newBlobStore(db *sql.DB, dataDir string, now func() time.Time, log *slog.Logger) (*blobStore, error) {
	dir := filepath.Join(dataDir, "blobs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &blobStore{db: db, dir: dir, now: now, log: log}, nil
}

type blobRow struct {
	id        []byte // upload id
	hash      []byte // blob id
	size      int64
	received  []byte
	complete  bool
	createdAt int64
	touchedAt int64
	expiresAt int64
}

func chunkCount(size int64) int { return int((size + BlobChunkSize - 1) / BlobChunkSize) }

// chunkPast reports whether chunk index is beyond a blob of size bytes.
// Compared as int64: a large index converted to int is negative on a
// 32-bit build and would pass a plain comparison.
func chunkPast(size int64, index uint32) bool { return int64(index) >= int64(chunkCount(size)) }

func chunkLen(size int64, index uint32) int64 {
	if rest := size - int64(index)*BlobChunkSize; rest < BlobChunkSize {
		return rest
	}
	return BlobChunkSize
}

func (r *blobRow) has(i uint32) bool { return r.received[i/8]&(1<<(i%8)) != 0 }

func (r *blobRow) missing() []uint32 {
	var out []uint32
	for i := 0; i < chunkCount(r.size); i++ {
		if !r.has(uint32(i)) {
			out = append(out, uint32(i))
		}
	}
	return out
}

func (r *blobRow) status() *wire.BlobUploadStatus {
	st := &wire.BlobUploadStatus{Hash: r.hash, Size: uint64(r.size), ChunkSize: BlobChunkSize, Complete: r.complete, ExpiresAt: r.expiresAt, Upload: r.id}
	if !r.complete {
		st.Missing = r.missing()
	}
	return st
}

// open reports whether an upload can still take chunks at now.
func (r *blobRow) open(now time.Time) bool {
	return r.expiresAt > now.UnixMilli() && (r.complete || r.createdAt > now.Add(-BlobMaxUploadTime).UnixMilli())
}

func (b *blobStore) path(id []byte) string { return filepath.Join(b.dir, hex.EncodeToString(id)) }
func (b *blobStore) partPath(id []byte) string {
	return b.path(id) + ".part"
}

func (b *blobStore) get(ctx context.Context, id []byte) (*blobRow, error) {
	r := &blobRow{id: id}
	var complete int
	err := b.db.QueryRowContext(ctx, `SELECT hash, size, received, complete, created_at, touched_at, expires_at FROM blobs WHERE id = ?`, id).
		Scan(&r.hash, &r.size, &r.received, &complete, &r.createdAt, &r.touchedAt, &r.expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	r.complete = complete != 0
	return r, err
}

// used is what the store holds or has reserved: uploads in progress and
// complete blobs, not records of blobs whose content is on cluster peers
// (blobs_repl.go).
func (b *blobStore) used(ctx context.Context) (uint64, error) {
	var n int64
	err := b.db.QueryRowContext(ctx, `SELECT coalesce(sum(size), 0) FROM blobs WHERE remote = 0`).Scan(&n)
	return uint64(n), err
}

// remove deletes an upload's row, tokens and files. Callers hold mu.
func (b *blobStore) remove(ctx context.Context, id []byte, reason string) error {
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM blob_tokens WHERE upload = ?`, id); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM blobs WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, p := range []string{b.path(id), b.partPath(id)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			b.log.Error("blob file removal failed", "upload", hex.EncodeToString(id), "err", err)
		}
	}
	if n, _ := res.RowsAffected(); n > 0 {
		b.log.Info("blob deleted", "upload", hex.EncodeToString(id), "reason", reason)
	}
	return nil
}

// collect deletes expired blobs and stale or overdue unfinished uploads and
// returns how many it deleted.
func (b *blobStore) collect(ctx context.Context) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	rows, err := b.db.QueryContext(ctx, `SELECT id, expires_at <= ? FROM blobs WHERE expires_at <= ? OR (complete = 0 AND (touched_at <= ? OR created_at <= ?))`,
		now.UnixMilli(), now.UnixMilli(), now.Add(-BlobStaleUpload).UnixMilli(), now.Add(-BlobMaxUploadTime).UnixMilli())
	if err != nil {
		return 0, err
	}
	type victim struct {
		id      []byte
		expired bool
	}
	var victims []victim
	for rows.Next() {
		var v victim
		if err := rows.Scan(&v.id, &v.expired); err != nil {
			rows.Close()
			return 0, err
		}
		victims = append(victims, v)
	}
	rows.Close()
	for _, v := range victims {
		reason := "unfinished upload abandoned"
		if v.expired {
			reason = "lifetime ended"
		}
		if err := b.remove(ctx, v.id, reason); err != nil {
			return 0, err
		}
	}
	return len(victims), nil
}

// recover makes files and rows agree after a crash: files without a row are
// removed, rows without their file are dropped, and an upload that was
// renamed into place but not yet marked complete is verified and finished.
// Only the server runs it (at startup, before serving).
func (b *blobStore) recover(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.sweepFilesLocked(ctx); err != nil {
		return err
	}
	// Records of blobs whose content is on cluster peers have no file, or
	// a partial .part file.
	rows, err := b.db.QueryContext(ctx, `SELECT id, complete FROM blobs WHERE remote = 0`)
	if err != nil {
		return err
	}
	var lost [][]byte
	for rows.Next() {
		var id []byte
		var complete bool
		if err := rows.Scan(&id, &complete); err != nil {
			rows.Close()
			return err
		}
		_, partErr := os.Stat(b.partPath(id))
		_, fullErr := os.Stat(b.path(id))
		if (complete && errors.Is(fullErr, os.ErrNotExist)) || (!complete && errors.Is(partErr, os.ErrNotExist) && errors.Is(fullErr, os.ErrNotExist)) {
			lost = append(lost, id)
		}
	}
	rows.Close()
	for _, id := range lost {
		if err := b.remove(ctx, id, "file missing at startup"); err != nil {
			return err
		}
	}
	return nil
}

// sweepFiles removes blob files that have no row, and unfinished files of
// finished uploads. The node runs it with every expiry pass: a `meta blobs`
// command in another process deletes rows and files without the node's
// lock, so a node finishing an upload at that moment can leave a file
// behind that the store's size (a sum over rows) doesn't count. Only the
// node may run it: under the node's lock, no upload is between creating its
// file and inserting its row.
func (b *blobStore) sweepFiles(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sweepFilesLocked(ctx)
}

func (b *blobStore) sweepFilesLocked(ctx context.Context) error {
	entries, err := os.ReadDir(b.dir)
	if err != nil {
		return err
	}
	// One query for every row's state, not one per file.
	complete := map[string]bool{}
	remote := map[string]bool{}
	rows, err := b.db.QueryContext(ctx, `SELECT id, complete, remote FROM blobs`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id []byte
		var c, r bool
		if err := rows.Scan(&id, &c, &r); err != nil {
			rows.Close()
			return err
		}
		complete[string(id)] = c
		remote[string(id)] = r
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		base, part := strings.CutSuffix(name, ".part")
		id, err := hex.DecodeString(base)
		c, known := complete[string(id)]
		switch {
		case err != nil || len(id) != sha256.Size || !known:
			b.removeFile(name, "no record")
		case remote[string(id)]:
			if !part {
				b.removeFile(name, "content of a blob still being copied")
			}
		case part && c:
			b.removeFile(name, "already finished")
		case !part && !c:
			// Crashed between the rename and the database update.
			r, err := b.get(ctx, id)
			if err != nil {
				return err
			}
			if r == nil {
				continue
			}
			if err := b.finish(ctx, r, b.path(id)); err != nil && !errors.Is(err, errHashMismatch) {
				return err
			}
		}
	}
	return nil
}

func (b *blobStore) removeFile(name, why string) {
	if err := os.Remove(filepath.Join(b.dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		b.log.Error("blob file removal failed", "err", err)
		return
	}
	b.log.Info("stray blob file removed", "reason", why)
}

// errHashMismatch means the uploaded bytes don't hash to the blob id.
var errHashMismatch = bad("the uploaded data doesn't match its hash; upload it again")

// finish verifies a fully received blob at path, moves it into place and
// marks it complete. On a hash mismatch the upload is discarded. Callers hold
// mu.
func (b *blobStore) finish(ctx context.Context, r *blobRow, path string) error {
	ok, err := b.verify(r, path)
	if err != nil {
		return err
	}
	if !ok {
		if err := b.remove(ctx, r.id, "hash mismatch"); err != nil {
			return err
		}
		return errHashMismatch
	}
	return b.place(ctx, r, path)
}

// verify reports whether the file at path has the blob's size and hash.
func (b *blobStore) verify(r *blobRow, path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	_ = f.Close()
	if err != nil {
		return false, err
	}
	return n == r.size && bytes.Equal(h.Sum(nil), r.hash), nil
}

// place moves a verified blob at path into place and marks it complete.
// Callers hold mu.
func (b *blobStore) place(ctx context.Context, r *blobRow, path string) error {
	if path != b.path(r.id) {
		if err := os.Rename(path, b.path(r.id)); err != nil {
			return err
		}
		if err := syncDir(b.dir); err != nil {
			return err
		}
	}
	for i := range r.received {
		r.received[i] = 0xff
	}
	if _, err := b.db.ExecContext(ctx, `UPDATE blobs SET complete = 1, received = ? WHERE id = ?`, r.received, r.id); err != nil {
		return err
	}
	r.complete = true
	b.log.Info("blob stored", "upload", hex.EncodeToString(r.id), "size", r.size)
	if b.onStored != nil {
		b.onStored(ctx, r)
	}
	return nil
}

// completeIfReady finishes an upload that has every chunk but isn't marked
// complete: the request that delivered the last chunk failed or was cut off
// after recording it, possibly after the file was renamed into place. It
// makes completion idempotent, so a resend of any chunk or a repeated create
// finishes the upload. Callers hold mu.
func (b *blobStore) completeIfReady(ctx context.Context, r *blobRow) error {
	if r.complete || len(r.missing()) > 0 {
		return nil
	}
	src := b.partPath(r.id)
	if _, err := os.Stat(b.path(r.id)); err == nil {
		src = b.path(r.id)
	}
	return b.finish(ctx, r, src)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

// ---------------------------------------------------------------- HTTP

type quotaUse struct {
	at     time.Time
	size   uint64
	tokens uint64
}

// chargeQuota records an upload of size bytes with tokens fetch tokens
// against key if it fits every per-network limit in the rolling window:
// bytes (blob_quota_per_ip), uploads (blob_uploads_per_ip) and token rows
// (blob_tokens_per_ip). The last two bound how many database rows one
// network can create with tiny blobs. Usage is kept in memory only, so client
// addresses never reach the disk; a restart forgets it.
func (s *Server) chargeQuota(key string, size, tokens uint64) error {
	now := s.now()
	cut := now.Add(-BlobQuotaWindow)
	var err error
	if !s.blobQuota.with(key, now, func(uses *[]quotaUse) {
		for len(*uses) > 0 && !(*uses)[0].at.After(cut) {
			*uses = (*uses)[1:]
		}
		var bytesUsed, tokensUsed uint64
		for _, u := range *uses {
			bytesUsed += u.size
			tokensUsed += u.tokens
		}
		switch {
		case bytesUsed+size > s.cfg.BlobQuotaPerIP:
			err = limited("upload quota for your network is used up; try again later")
		case len(*uses)+1 > s.cfg.BlobUploadsPerIP:
			err = limited("too many uploads from your network today; try again later")
		case tokensUsed+tokens > s.cfg.BlobTokensPerIP:
			err = limited("too many recipients for uploads from your network today; try again later")
		default:
			*uses = append(*uses, quotaUse{at: now, size: size, tokens: tokens})
		}
	}) {
		return errBusy
	}
	return err
}

// errBusy refuses a request from a new network while a limit table is full
// of networks still within their window (limits.go).
var errBusy = limited("this node is busy; try again in a minute")

func validHash(h []byte) bool { return len(h) == sha256.Size }

// DecodeBlobCreate parses and checks a BlobCreate body (size limits against
// the node's config are applied by the handler).
func DecodeBlobCreate(body []byte) (*wire.BlobCreate, error) {
	var c wire.BlobCreate
	if err := proto.Unmarshal(body, &c); err != nil || !validHash(c.Hash) || c.Size == 0 {
		return nil, bad("malformed upload request")
	}
	if len(c.TokenHashes) == 0 || len(c.TokenHashes) > MaxBlobTokens {
		return nil, bad(fmt.Sprintf("an upload needs 1 to %d fetch tokens", MaxBlobTokens))
	}
	seen := make(map[string]bool, len(c.TokenHashes))
	for _, th := range c.TokenHashes {
		if !validHash(th) || seen[string(th)] {
			return nil, bad("fetch token hashes must be distinct 32-byte values")
		}
		seen[string(th)] = true
	}
	return &c, nil
}

// DecodeBlobChunk parses and checks a BlobChunk body.
func DecodeBlobChunk(body []byte) (*wire.BlobChunk, error) {
	var c wire.BlobChunk
	if err := proto.Unmarshal(body, &c); err != nil || !validHash(c.Hash) || !validHash(c.Upload) || len(c.Data) == 0 || len(c.Data) > BlobChunkSize {
		return nil, bad("malformed chunk")
	}
	return &c, nil
}

// DecodeBlobFetch parses and checks a BlobFetch body.
func DecodeBlobFetch(body []byte) (*wire.BlobFetch, error) {
	var f wire.BlobFetch
	if err := proto.Unmarshal(body, &f); err != nil || !validHash(f.Hash) || len(f.Token) != 32 {
		return nil, bad("malformed fetch")
	}
	return &f, nil
}

// DecodeBlobAck parses and checks a BlobAck body.
func DecodeBlobAck(body []byte) (*wire.BlobAck, error) {
	var a wire.BlobAck
	if err := proto.Unmarshal(body, &a); err != nil || !validHash(a.Hash) || len(a.Token) != 32 {
		return nil, bad("malformed ack")
	}
	return &a, nil
}

var (
	errNoBlob   = notFound("file not found: it may have expired or been fetched by every device")
	errNoUpload = notFound("no such upload: it may have expired; start again")
)

func (s *Server) blobCreate(r *http.Request, body []byte) (proto.Message, error) {
	c, err := DecodeBlobCreate(body)
	if err != nil {
		return nil, err
	}
	if c.Size > s.cfg.BlobMaxSize {
		return nil, &httpError{http.StatusRequestEntityTooLarge, fmt.Sprintf("file is larger than this node's %d-byte limit", s.cfg.BlobMaxSize)}
	}
	key := clientKey(r)
	if !s.blobCreates.allow(key, s.now()) {
		return nil, limited("too many uploads; wait a minute")
	}
	b := s.blobs
	// A client that hangs up mid-request must not leave a half-done step.
	ctx := context.WithoutCancel(r.Context())
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	id := BlobUploadID(c.Hash, c.Size, c.TokenHashes)
	existing, err := b.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing != nil && !existing.open(now) {
		if err := b.remove(ctx, id, "lifetime ended"); err != nil {
			return nil, err
		}
		existing = nil
	}
	if existing != nil {
		// Resuming (only the sender can name this upload): report what's
		// missing, finishing it first if every chunk is already here.
		if err := b.completeIfReady(ctx, existing); err != nil {
			return nil, err
		}
		return existing.status(), nil
	}
	used, err := b.used(ctx)
	if err != nil {
		return nil, err
	}
	if used+c.Size > s.cfg.BlobStoreMax {
		s.log.Warn("blob store full", "used", used, "max", s.cfg.BlobStoreMax)
		return nil, &httpError{http.StatusInsufficientStorage, "this node's file store is full; try another maintainer or later"}
	}
	if err := s.chargeQuota(key, c.Size, uint64(len(c.TokenHashes))); err != nil {
		return nil, err
	}
	row := &blobRow{id: id, hash: c.Hash, size: int64(c.Size), received: make([]byte, (chunkCount(int64(c.Size))+7)/8),
		createdAt: now.UnixMilli(), touchedAt: now.UnixMilli(), expiresAt: now.Add(s.cfg.BlobTTL.Duration).UnixMilli()}
	f, err := os.OpenFile(b.partPath(id), os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	err = f.Truncate(row.size)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = b.insert(ctx, row, c.TokenHashes)
	}
	if err != nil {
		_ = os.Remove(b.partPath(id))
		return nil, err
	}
	return row.status(), nil
}

func (b *blobStore) insert(ctx context.Context, row *blobRow, tokens [][]byte) error {
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO blobs (id, hash, size, received, complete, created_at, touched_at, expires_at) VALUES (?, ?, ?, ?, 0, ?, ?, ?)`,
		row.id, row.hash, row.size, row.received, row.createdAt, row.touchedAt, row.expiresAt); err != nil {
		return err
	}
	for _, th := range tokens {
		if _, err := tx.ExecContext(ctx, `INSERT INTO blob_tokens (upload, token_hash) VALUES (?, ?)`, row.id, th); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Server) blobChunk(r *http.Request, body []byte) (proto.Message, error) {
	c, err := DecodeBlobChunk(body)
	if err != nil {
		return nil, err
	}
	b := s.blobs
	ctx := context.WithoutCancel(r.Context())
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	row, err := b.get(ctx, c.Upload)
	if err != nil {
		return nil, err
	}
	if row == nil || !bytes.Equal(row.hash, c.Hash) || !row.open(now) {
		return nil, errNoUpload
	}
	if row.complete {
		return row.status(), nil
	}
	if chunkPast(row.size, c.Index) {
		return nil, bad("chunk index is past the end of the file")
	}
	if int64(len(c.Data)) != chunkLen(row.size, c.Index) {
		return nil, bad("chunk has the wrong length")
	}
	// A chunk the node already has is not written again and doesn't count
	// as activity, so resending one can't keep an abandoned upload (and the
	// size it reserves) alive.
	if !row.has(c.Index) {
		f, err := os.OpenFile(b.partPath(row.id), os.O_WRONLY, 0)
		if errors.Is(err, os.ErrNotExist) {
			return nil, errNoUpload
		}
		if err != nil {
			return nil, err
		}
		_, err = f.WriteAt(c.Data, int64(c.Index)*BlobChunkSize)
		if err == nil {
			err = f.Sync()
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, err
		}
		row.received[c.Index/8] |= 1 << (c.Index % 8)
		row.touchedAt = now.UnixMilli()
		if _, err := b.db.ExecContext(ctx, `UPDATE blobs SET received = ?, touched_at = ? WHERE id = ?`, row.received, row.touchedAt, row.id); err != nil {
			return nil, err
		}
	}
	if err := b.completeIfReady(ctx, row); err != nil {
		return nil, err
	}
	return row.status(), nil
}

func (s *Server) blobFetch(r *http.Request, body []byte) (proto.Message, error) {
	f, err := DecodeBlobFetch(body)
	if err != nil {
		return nil, err
	}
	b := s.blobs
	ctx := r.Context()
	var id []byte
	var size, expires int64
	var complete bool
	err = b.db.QueryRowContext(ctx, `SELECT b.id, b.size, b.complete, b.expires_at FROM blob_tokens t JOIN blobs b ON b.id = t.upload
WHERE t.token_hash = ? AND b.hash = ? ORDER BY b.complete DESC, b.created_at LIMIT 1`, BlobTokenHash(f.Token), f.Hash).Scan(&id, &size, &complete, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNoBlob
	}
	if err != nil {
		return nil, err
	}
	if expires <= b.now().UnixMilli() {
		return nil, errNoBlob
	}
	if !complete {
		return nil, conflict("the upload isn't finished yet")
	}
	if chunkPast(size, f.Index) {
		return nil, bad("chunk index is past the end of the file")
	}
	if remote, err := s.isRemote(ctx, id); err != nil {
		return nil, err
	} else if remote {
		// The record came from a cluster peer; the content comes from
		// there too, as clients ask for it.
		data, err := s.remoteChunk(ctx, id, f.Index)
		if err != nil {
			return nil, err
		}
		return &wire.BlobChunkData{Size: uint64(size), ChunkSize: BlobChunkSize, Index: f.Index, Data: data, ExpiresAt: expires}, nil
	}
	file, err := os.Open(b.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, errNoBlob
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data := make([]byte, chunkLen(size, f.Index))
	if _, err := file.ReadAt(data, int64(f.Index)*BlobChunkSize); err != nil {
		return nil, err
	}
	return &wire.BlobChunkData{Size: uint64(size), ChunkSize: BlobChunkSize, Index: f.Index, Data: data, ExpiresAt: expires}, nil
}

// blobAck releases a fetch token. It answers the same whether or not the
// token was registered, so it can't be used to probe for blobs.
func (s *Server) blobAck(r *http.Request, body []byte) (proto.Message, error) {
	a, err := DecodeBlobAck(body)
	if err != nil {
		return nil, err
	}
	b := s.blobs
	ctx := context.WithoutCancel(r.Context())
	b.mu.Lock()
	defer b.mu.Unlock()
	th := BlobTokenHash(a.Token)
	rows, err := b.db.QueryContext(ctx, `SELECT t.upload, b.expires_at FROM blob_tokens t JOIN blobs b ON b.id = t.upload WHERE t.token_hash = ? AND b.hash = ?`, th, a.Hash)
	if err != nil {
		return nil, err
	}
	type upload struct {
		id      []byte
		expires int64
	}
	var uploads []upload
	for rows.Next() {
		var u upload
		if err := rows.Scan(&u.id, &u.expires); err != nil {
			rows.Close()
			return nil, err
		}
		uploads = append(uploads, u)
	}
	rows.Close()
	changed := false
	for _, u := range uploads {
		// The release is remembered and replicated, so the token is
		// released on every cluster member (blobs_repl.go).
		added, err := s.release(ctx, u.id, th, u.expires)
		if err != nil {
			return nil, err
		}
		changed = changed || added
	}
	if changed {
		s.notifyPeers()
	}
	return nil, nil
}

// ---------------------------------------------------------------- admin

// BlobAdmin works on a node's blob store directly through its data
// directory (`meta blobs`). It is safe to use while the node runs: SQLite
// serializes the database, and a blob deleted under a running node just
// answers "not found".
type BlobAdmin struct {
	b *blobStore
}

// OpenBlobAdmin opens the blob store in dataDir. The database must already
// exist (a node has run there).
func OpenBlobAdmin(ctx context.Context, dataDir string, log *slog.Logger) (*BlobAdmin, error) {
	if log == nil {
		log = slog.Default()
	}
	dbPath := filepath.Join(dataDir, "meta.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("no maintainer database in %s: %w", dataDir, err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	// Never migrate: the node may be running an older release on this
	// database right now.
	if err := store.Check(ctx, db, len(migrations)); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", dbPath, err)
	}
	b, err := newBlobStore(db, dataDir, time.Now, log)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &BlobAdmin{b: b}, nil
}

// SetClock replaces the clock (tests).
func (a *BlobAdmin) SetClock(now func() time.Time) { a.b.now = now }

// Close releases the database.
func (a *BlobAdmin) Close() error { return a.b.db.Close() }

// BlobInfo describes one stored blob (one upload of it).
type BlobInfo struct {
	ID        string // hex SHA-256 of the ciphertext (the blob id)
	Size      uint64
	Complete  bool
	Tokens    int // fetch tokens not yet released
	CreatedAt time.Time
	ExpiresAt time.Time
}

// BlobStats summarizes the store.
type BlobStats struct {
	Stored, Uploading           int
	StoredBytes, UploadingBytes uint64
	Blobs                       []BlobInfo // oldest first
}

// Stats lists every blob.
func (a *BlobAdmin) Stats(ctx context.Context) (*BlobStats, error) {
	rows, err := a.b.db.QueryContext(ctx, `SELECT b.hash, b.size, b.complete, b.created_at, b.expires_at,
	(SELECT count(*) FROM blob_tokens t WHERE t.upload = b.id) FROM blobs b ORDER BY b.created_at, b.hash`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	st := &BlobStats{}
	for rows.Next() {
		var hash []byte
		var created, expires int64
		var bi BlobInfo
		if err := rows.Scan(&hash, &bi.Size, &bi.Complete, &created, &expires, &bi.Tokens); err != nil {
			return nil, err
		}
		bi.ID = hex.EncodeToString(hash)
		bi.CreatedAt, bi.ExpiresAt = time.UnixMilli(created).UTC(), time.UnixMilli(expires).UTC()
		if bi.Complete {
			st.Stored++
			st.StoredBytes += bi.Size
		} else {
			st.Uploading++
			st.UploadingBytes += bi.Size
		}
		st.Blobs = append(st.Blobs, bi)
	}
	return st, rows.Err()
}

// Delete removes every upload of one blob by its hex id and reports whether
// any existed.
func (a *BlobAdmin) Delete(ctx context.Context, id string) (bool, error) {
	hash, err := hex.DecodeString(id)
	if err != nil || !validHash(hash) {
		return false, errors.New("blob id must be 64 hex characters")
	}
	a.b.mu.Lock()
	defer a.b.mu.Unlock()
	rows, err := a.b.db.QueryContext(ctx, `SELECT id FROM blobs WHERE hash = ?`, hash)
	if err != nil {
		return false, err
	}
	var uploads [][]byte
	for rows.Next() {
		var u []byte
		if err := rows.Scan(&u); err != nil {
			rows.Close()
			return false, err
		}
		uploads = append(uploads, u)
	}
	rows.Close()
	for _, u := range uploads {
		if err := a.b.remove(ctx, u, "deleted by operator"); err != nil {
			return false, err
		}
	}
	return len(uploads) > 0, nil
}

// PurgeExpired deletes expired blobs and stale unfinished uploads now,
// returning how many were deleted.
func (a *BlobAdmin) PurgeExpired(ctx context.Context) (int, error) { return a.b.collect(ctx) }
