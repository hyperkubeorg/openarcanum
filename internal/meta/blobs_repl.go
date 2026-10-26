package meta

// DM blobs inside a maintainer cluster (docs/protocol.md §7.5): a completed
// upload's record (BlobMeta) and every released fetch token (BlobRelease)
// replicate at once through the peer log, so any member can serve a blob
// and an ack on any member ends it everywhere. Content moves on demand: a
// member that has the record but not the content fetches the chunks a
// client asks for from a member that has them (PeerBlobChunkRequest),
// keeps them, and copies the rest in the background within
// peer_blob_bytes_per_hour, so the blob survives the loss of the node it
// was uploaded to. A record whose content isn't complete here is "remote":
// its chunks collect in the .part file (the received bitmap says which),
// and it counts towards blob_store_max only once complete.

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Peer log kinds for blobs.
const (
	logBlobMeta    = "b"
	logBlobRelease = "r"
)

// peerChunkTimeout bounds fetching one chunk from a member.
const peerChunkTimeout = 10 * time.Second

// blobReplSchema (schema version 12) marks records whose content isn't
// (fully) here and keeps released tokens, so a late record can't bring a
// released token back.
const blobReplSchema = `
ALTER TABLE blobs ADD COLUMN remote INTEGER NOT NULL DEFAULT 0;
CREATE TABLE blob_releases (
	upload BLOB NOT NULL,
	token_hash BLOB NOT NULL,
	expires_at INTEGER NOT NULL,
	PRIMARY KEY (upload, token_hash)
) WITHOUT ROWID;
CREATE INDEX blob_releases_expiry ON blob_releases (expires_at);
`

// logBlobStored puts a completed upload into the peer log (blobStore.onStored).
func (s *Server) logBlobStored(ctx context.Context, r *blobRow) {
	if err := logPeer(ctx, s.db, logBlobMeta, r.id, r.expiresAt); err != nil {
		s.log.Error("peer log", "err", err)
		return
	}
	s.notifyPeers()
}

// blobMetaEntry is the record of a complete upload still held.
func (s *Server) blobMetaEntry(ctx context.Context, id []byte) (*wire.PeerEntry, error) {
	m := &wire.PeerBlobMeta{UploadId: id}
	err := s.db.QueryRowContext(ctx, `SELECT hash, size, expires_at FROM blobs WHERE id = ? AND complete = 1`, id).Scan(&m.Hash, &m.Size, &m.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT token_hash FROM blob_tokens WHERE upload = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var th []byte
		if err := rows.Scan(&th); err != nil {
			return nil, err
		}
		m.TokenHashes = append(m.TokenHashes, th)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(m.TokenHashes) == 0 {
		return nil, nil
	}
	return &wire.PeerEntry{Entry: &wire.PeerEntry_BlobMeta{BlobMeta: m}}, nil
}

// blobReleaseEntry is a release still remembered (key: upload id || token hash).
func (s *Server) blobReleaseEntry(ctx context.Context, key []byte) (*wire.PeerEntry, error) {
	if len(key) != 64 {
		return nil, nil
	}
	r := &wire.PeerBlobRelease{UploadId: key[:32], TokenHash: key[32:]}
	err := s.db.QueryRowContext(ctx, `SELECT expires_at FROM blob_releases WHERE upload = ? AND token_hash = ?`, r.UploadId, r.TokenHash).Scan(&r.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &wire.PeerEntry{Entry: &wire.PeerEntry_BlobRelease{BlobRelease: r}}, nil
}

// releaseToken records a released token (inside tx) and logs it for the
// peers; it reports whether the release is new.
func releaseToken(ctx context.Context, tx *sql.Tx, upload, th []byte, expires int64) (bool, error) {
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO blob_releases (upload, token_hash, expires_at) VALUES (?, ?, ?)`, upload, th, expires)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	return true, logPeer(ctx, tx, logBlobRelease, append(bytes.Clone(upload), th...), expires)
}

// applyBlobMeta stores a peer's upload record as a remote blob, without
// the tokens released already.
func (s *Server) applyBlobMeta(ctx context.Context, m *wire.PeerBlobMeta) (bool, error) {
	now := s.now()
	expires := min(m.ExpiresAt, now.Add(s.cfg.BlobTTL.Duration).UnixMilli())
	switch {
	case !validHash(m.Hash) || m.Size == 0 || m.Size > s.cfg.BlobMaxSize || len(m.TokenHashes) == 0 || len(m.TokenHashes) > MaxBlobTokens:
		s.refused(refusedInvalid)
		return false, nil
	case !bytes.Equal(m.UploadId, BlobUploadID(m.Hash, m.Size, m.TokenHashes)):
		s.refused(refusedInvalid)
		return false, nil
	case expires <= now.UnixMilli():
		s.refused(refusedExpired)
		return false, nil
	}
	seen := map[string]bool{}
	for _, th := range m.TokenHashes {
		if !validHash(th) || seen[string(th)] {
			s.refused(refusedInvalid)
			return false, nil
		}
		seen[string(th)] = true
	}
	b := s.blobs
	b.mu.Lock()
	defer b.mu.Unlock()
	tx, err := beginWrite(ctx, s.db)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM blobs WHERE id = ?`, m.UploadId).Scan(&n); err != nil || n > 0 {
		return false, err // held already (or this node's own upload of it)
	}
	var live [][]byte
	for _, th := range m.TokenHashes {
		var r int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM blob_releases WHERE upload = ? AND token_hash = ?`, m.UploadId, th).Scan(&r)
		if errors.Is(err, sql.ErrNoRows) {
			live = append(live, th)
		} else if err != nil {
			return false, err
		}
	}
	if len(live) == 0 {
		return false, nil
	}
	received := make([]byte, (chunkCount(int64(m.Size))+7)/8)
	if _, err := tx.ExecContext(ctx, `INSERT INTO blobs (id, hash, size, received, complete, created_at, touched_at, expires_at, remote) VALUES (?, ?, ?, ?, 1, ?, ?, ?, 1)`,
		m.UploadId, m.Hash, m.Size, received, now.UnixMilli(), now.UnixMilli(), expires); err != nil {
		return false, err
	}
	for _, th := range live {
		if _, err := tx.ExecContext(ctx, `INSERT INTO blob_tokens (upload, token_hash) VALUES (?, ?)`, m.UploadId, th); err != nil {
			return false, err
		}
	}
	if err := logPeer(ctx, tx, logBlobMeta, m.UploadId, expires); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// applyBlobRelease releases a token a peer saw released; the blob goes once
// none is left.
func (s *Server) applyBlobRelease(ctx context.Context, r *wire.PeerBlobRelease) (bool, error) {
	if !validHash(r.UploadId) || !validHash(r.TokenHash) {
		s.refused(refusedInvalid)
		return false, nil
	}
	now := s.now()
	expires := min(r.ExpiresAt, now.Add(s.cfg.BlobTTL.Duration).UnixMilli())
	if expires <= now.UnixMilli() {
		return false, nil
	}
	b := s.blobs
	b.mu.Lock()
	defer b.mu.Unlock()
	return s.release(ctx, r.UploadId, r.TokenHash, expires)
}

// release drops one token of an upload, remembers the release and logs
// it; the upload is removed once no token is left. Callers hold b.mu.
func (s *Server) release(ctx context.Context, upload, th []byte, expires int64) (bool, error) {
	tx, err := beginWrite(ctx, s.db)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	added, err := releaseToken(ctx, tx, upload, th, expires)
	if err != nil || !added {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM blob_tokens WHERE upload = ? AND token_hash = ?`, upload, th); err != nil {
		return false, err
	}
	var left, rows int
	if err := tx.QueryRowContext(ctx, `SELECT count(*), (SELECT count(*) FROM blobs WHERE id = ?1) FROM blob_tokens WHERE upload = ?1`, upload).Scan(&left, &rows); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	if left == 0 && rows > 0 {
		if err := s.blobs.remove(ctx, upload, "fetched by every device"); err != nil {
			return true, err
		}
	}
	return true, nil
}

// ---------------------------------------------------------------- content

// chunkFromPeers asks the trusted members, one after another, for a chunk.
func (s *Server) chunkFromPeers(ctx context.Context, upload []byte, index uint32) []byte {
	s.peers.mu.Lock()
	links := make([]*peerLink, 0, len(s.peers.links))
	for _, l := range s.peers.links {
		links = append(links, l)
	}
	s.peers.mu.Unlock()
	if len(links) == 0 {
		// No session yet (a node that just started): dial the trusted peers.
		for _, tp := range s.trustedPeers(ctx) {
			if tp.URL != "" {
				links = append(links, s.peers.link(tp))
			}
		}
	}
	for _, l := range links {
		cctx, cancel := context.WithTimeout(ctx, peerChunkTimeout)
		r, err := s.request(cctx, l, &wire.PeerMessage{Body: &wire.PeerMessage_BlobChunkRequest{BlobChunkRequest: &wire.PeerBlobChunkRequest{UploadId: upload, Index: index}}})
		cancel()
		if err != nil {
			continue
		}
		if c := r.GetBlobChunk(); c != nil && bytes.Equal(c.UploadId, upload) && c.Index == index {
			return c.Data
		}
	}
	return nil
}

// localChunk reads a chunk this node holds (nil: not here).
func (s *Server) localChunk(ctx context.Context, upload []byte, index uint32) ([]byte, error) {
	r, err := s.blobs.get(ctx, upload)
	if err != nil || r == nil || !r.complete || chunkPast(r.size, index) {
		return nil, err
	}
	remote, err := s.isRemote(ctx, upload)
	if err != nil {
		return nil, err
	}
	path := s.blobs.path(upload)
	if remote {
		if !r.has(index) {
			return nil, nil
		}
		path = s.blobs.partPath(upload)
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data := make([]byte, chunkLen(r.size, index))
	if _, err := f.ReadAt(data, int64(index)*BlobChunkSize); err != nil {
		return nil, err
	}
	return data, nil
}

func (s *Server) isRemote(ctx context.Context, upload []byte) (bool, error) {
	var remote bool
	err := s.db.QueryRowContext(ctx, `SELECT remote FROM blobs WHERE id = ?`, upload).Scan(&remote)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return remote, err
}

// remoteChunk serves a chunk of a remote blob: from the .part file if it
// arrived already, else from a member (kept here when the store has room).
func (s *Server) remoteChunk(ctx context.Context, upload []byte, index uint32) ([]byte, error) {
	if data, err := s.localChunk(ctx, upload, index); err != nil || data != nil {
		return data, err
	}
	data := s.chunkFromPeers(ctx, upload, index)
	if data == nil {
		return nil, errNoBlob
	}
	if err := s.keepChunk(ctx, upload, index, data); err != nil {
		s.log.Warn("blob chunk from a peer not kept", "upload", shortID(upload), "err", err)
	}
	return data, nil
}

// keepChunk writes a chunk fetched from a member into a remote blob's
// .part file, and finishes the blob (hash checked) once every chunk is
// here. A store without room keeps only the record.
func (s *Server) keepChunk(ctx context.Context, upload []byte, index uint32, data []byte) error {
	b := s.blobs
	b.mu.Lock()
	defer b.mu.Unlock()
	r, err := b.get(ctx, upload)
	if err != nil || r == nil {
		return err
	}
	remote, err := s.isRemote(ctx, upload)
	if err != nil || !remote || chunkPast(r.size, index) || r.has(index) || int64(len(data)) != chunkLen(r.size, index) {
		return err
	}
	used, err := b.used(ctx)
	if err != nil {
		return err
	}
	if used+uint64(r.size) > s.cfg.BlobStoreMax {
		return errors.New("blob store full; serving from peers only")
	}
	f, err := os.OpenFile(b.partPath(upload), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if st, err := f.Stat(); err == nil && st.Size() != r.size {
		err = f.Truncate(r.size)
		if err != nil {
			f.Close()
			return err
		}
	}
	_, err = f.WriteAt(data, int64(index)*BlobChunkSize)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	r.received[index/8] |= 1 << (index % 8)
	if _, err := s.db.ExecContext(ctx, `UPDATE blobs SET received = ? WHERE id = ?`, r.received, upload); err != nil {
		return err
	}
	if len(r.missing()) > 0 {
		return nil
	}
	// Every chunk is here: check the hash and make it a local blob.
	if err := b.finishRemote(ctx, r); err != nil {
		return err
	}
	return nil
}

// finishRemote verifies a remote blob whose chunks have all arrived and
// moves it into place as a local blob. On a hash mismatch (a member served
// a wrong chunk) the chunks are discarded but the record is kept, so the
// content is fetched again, from any member that has it. Callers hold b.mu.
func (b *blobStore) finishRemote(ctx context.Context, r *blobRow) error {
	ok, err := b.verify(r, b.partPath(r.id))
	if err != nil {
		return err
	}
	if !ok {
		if err := os.Remove(b.partPath(r.id)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		clear(r.received)
		if _, err := b.db.ExecContext(ctx, `UPDATE blobs SET received = ? WHERE id = ?`, r.received, r.id); err != nil {
			return err
		}
		b.log.Warn("replicated blob content doesn't match its hash; discarded, to be fetched again", "upload", shortID(r.id))
		return errHashMismatch
	}
	if err := b.place(ctx, r, b.partPath(r.id)); err != nil {
		return err
	}
	_, err = b.db.ExecContext(ctx, `UPDATE blobs SET remote = 0 WHERE id = ?`, r.id)
	return err
}

// peerBlobChunk answers a member's request for a chunk this node holds.
func (s *Server) peerBlobChunk(ctx context.Context, req *wire.PeerBlobChunkRequest) *wire.PeerMessage {
	if !validHash(req.UploadId) {
		return peerErr("malformed request")
	}
	data, err := s.localChunk(ctx, req.UploadId, req.Index)
	if err != nil {
		s.log.Error("peer blob chunk", "err", err)
		return peerErr("internal error")
	}
	if data == nil {
		return peerErr("not here")
	}
	return &wire.PeerMessage{Body: &wire.PeerMessage_BlobChunk{BlobChunk: &wire.PeerBlobChunk{UploadId: req.UploadId, Index: req.Index, Data: data}}}
}

// copyRemoteBlobs fills in the content of remote blobs from the members,
// oldest first, within peer_blob_bytes_per_hour (the run loop calls it
// after each round), so a blob outlives the node it was uploaded to.
func (s *Server) copyRemoteBlobs(ctx context.Context) {
	now := s.now()
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM blobs WHERE remote = 1 AND expires_at > ? ORDER BY created_at LIMIT 100`, now.UnixMilli())
	if err != nil {
		return
	}
	var ids [][]byte
	for rows.Next() {
		var id []byte
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		r, err := s.blobs.get(ctx, id)
		if err != nil || r == nil {
			continue
		}
		for _, i := range r.missing() {
			n := uint64(chunkLen(r.size, i))
			if !s.blobBudget.charge("copy", n, s.now()) {
				return
			}
			data := s.chunkFromPeers(ctx, id, i)
			if data == nil {
				break // no member has it now; try again next round
			}
			if err := s.keepChunk(ctx, id, i, data); errors.Is(err, errHashMismatch) {
				break // starts over next round
			} else if err != nil {
				return // store full: keep records only
			}
		}
	}
}
