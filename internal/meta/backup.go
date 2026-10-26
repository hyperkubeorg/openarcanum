package meta

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/store"
)

// Maintainer node backups (docs/operations.md "Backups"): one tar file
// holding a consistent snapshot of meta.db and the DM blob files it
// refers to, plus a manifest with every file's size and SHA-256.
//
//	blobs/<upload id hex>   each complete blob, as stored
//	meta.db                 VACUUM INTO snapshot, trimmed to the blobs above
//	manifest.json           format, schema version, file list with hashes
//
// It is taken online: VACUUM INTO reads one consistent database state while
// the node keeps serving. Blob files are then read one by one; a blob the
// node deleted in between (expired, released) is dropped from the snapshot,
// and uploads still in progress are left out (their senders upload again
// after a restore). Mailbox items, listings, routes, the relay registry,
// nameplates and counters are rows in meta.db and come along whole.
//
// The backup holds what the data directory holds: ciphertext, signed public
// records and metadata (inbox addresses, sizes, times). It is not
// encrypted again; keep it with the same care as the data directory.

const (
	backupFormat  = "openarcanum-meta-backup"
	backupVersion = 1
	// BackupPrefix and BackupSuffix name backup files: meta-<UTC time>.tar.
	BackupPrefix = "meta-"
	BackupSuffix = ".tar"
)

// BackupManifest is manifest.json.
type BackupManifest struct {
	Format    string       `json:"format"`
	Version   int          `json:"version"`
	CreatedAt time.Time    `json:"created_at"`
	Schema    int          `json:"schema"`
	Database  BackupFile   `json:"database"`
	Blobs     []BackupFile `json:"blobs"`
}

// BackupFile is one file in a backup.
type BackupFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// BackupResult describes a backup written or restored.
type BackupResult struct {
	Path string
	// Blobs and BlobBytes count the blobs in the backup; Skipped counts
	// blobs left out because the node deleted them while the backup ran
	// or their file didn't match its record.
	Blobs     int
	BlobBytes int64
	Skipped   int
	Schema    int
	CreatedAt time.Time
}

var blobEntry = regexp.MustCompile(`^blobs/[0-9a-f]{64}$`)

// Backup writes a backup of the node in dataDir to outDir and returns its
// path. It is safe while the node runs. The node's database must be at
// this build's schema version (run the backup with the node's own binary).
func Backup(ctx context.Context, dataDir, outDir string, log *slog.Logger) (*BackupResult, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	dbPath := filepath.Join(dataDir, "meta.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("no maintainer database in %s: %w", dataDir, err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := store.Check(ctx, db, len(migrations)); err != nil {
		return nil, err
	}
	schema := len(migrations)
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return nil, err
	}
	created := time.Now().UTC()
	stamp := created.Format("20060102T150405.000Z")
	work, err := os.MkdirTemp(outDir, ".meta-backup-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	snap := filepath.Join(work, "meta.db")
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, snap); err != nil {
		return nil, fmt.Errorf("database snapshot: %w", err)
	}
	db.Close()

	out := filepath.Join(outDir, BackupPrefix+stamp+BackupSuffix)
	tmp := filepath.Join(work, "backup.tar")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	res := &BackupResult{Path: out, Schema: schema, CreatedAt: created}
	man := BackupManifest{Format: backupFormat, Version: backupVersion, CreatedAt: created, Schema: schema}

	sdb, err := sql.Open("sqlite", "file:"+snap)
	if err != nil {
		return nil, err
	}
	defer sdb.Close()
	if err := trimSnapshot(ctx, sdb); err != nil {
		return nil, err
	}
	blobs, err := snapshotBlobs(ctx, sdb)
	if err != nil {
		return nil, err
	}
	var dropped [][]byte
	for _, b := range blobs {
		bf, err := addBlob(tw, filepath.Join(dataDir, "blobs"), b, created)
		if err != nil {
			return nil, err
		}
		if bf == nil {
			dropped = append(dropped, b.id)
			continue
		}
		man.Blobs = append(man.Blobs, *bf)
		res.Blobs++
		res.BlobBytes += bf.Size
	}
	res.Skipped = len(dropped)
	for _, id := range dropped {
		if _, err := sdb.ExecContext(ctx, `DELETE FROM blob_tokens WHERE upload = ?`, id); err != nil {
			return nil, err
		}
		if _, err := sdb.ExecContext(ctx, `DELETE FROM blobs WHERE id = ?`, id); err != nil {
			return nil, err
		}
	}
	if err := sdb.Close(); err != nil {
		return nil, err
	}
	dbFile, err := addFile(tw, "meta.db", snap, created)
	if err != nil {
		return nil, err
	}
	man.Database = *dbFile
	mj, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o600, Size: int64(len(mj)), ModTime: created, Typeflag: tar.TypeReg}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(mj); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := f.Sync(); err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, out); err != nil {
		return nil, err
	}
	if res.Skipped > 0 {
		log.Info("backup: blobs left out (deleted while the backup ran, or damaged)", "count", res.Skipped)
	}
	return res, nil
}

// trimSnapshot leaves out uploads in progress (their files are still being
// written, and their senders start over against the restored node) and
// records of blobs whose content is on cluster peers (the restored node's
// peers send the records again: its peer log epoch is new).
func trimSnapshot(ctx context.Context, db *sql.DB) error {
	for _, q := range []string{
		`DELETE FROM blob_tokens WHERE upload IN (SELECT id FROM blobs WHERE complete = 0 OR remote = 1)`,
		`DELETE FROM blobs WHERE complete = 0 OR remote = 1`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

type snapBlob struct {
	id, hash []byte
	size     int64
}

func snapshotBlobs(ctx context.Context, db *sql.DB) ([]snapBlob, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, hash, size FROM blobs ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []snapBlob
	for rows.Next() {
		var b snapBlob
		if err := rows.Scan(&b.id, &b.hash, &b.size); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// addBlob streams one blob file into the archive, checking its size and
// hash against the snapshot's row. It returns nil (and writes nothing) for
// a blob whose file is gone or has the wrong size; a file whose content
// doesn't hash to its id is written but left out of the manifest, so a
// restore ignores it.
func addBlob(tw *tar.Writer, dir string, b snapBlob, mod time.Time) (*BackupFile, error) {
	name := hex.EncodeToString(b.id)
	src, err := os.Open(filepath.Join(dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer src.Close()
	st, err := src.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() != b.size {
		return nil, nil
	}
	if err := tw.WriteHeader(&tar.Header{Name: "blobs/" + name, Mode: 0o600, Size: b.size, ModTime: mod, Typeflag: tar.TypeReg}); err != nil {
		return nil, err
	}
	h := sha256.New()
	if _, err := io.CopyN(io.MultiWriter(tw, h), src, b.size); err != nil {
		return nil, err
	}
	sum := h.Sum(nil)
	if !bytes.Equal(sum, b.hash) {
		return nil, nil
	}
	return &BackupFile{Path: "blobs/" + name, Size: b.size, SHA256: hex.EncodeToString(sum)}, nil
}

func addFile(tw *tar.Writer, name, path string, mod time.Time) (*BackupFile, error) {
	src, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	st, err := src.Stat()
	if err != nil {
		return nil, err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: st.Size(), ModTime: mod, Typeflag: tar.TypeReg}); err != nil {
		return nil, err
	}
	h := sha256.New()
	if _, err := io.CopyN(io.MultiWriter(tw, h), src, st.Size()); err != nil {
		return nil, err
	}
	return &BackupFile{Path: name, Size: st.Size(), SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

// PruneBackups deletes all but the newest keep backups in dir and returns
// what it deleted.
func PruneBackups(dir string, keep int) ([]string, error) {
	if keep < 1 {
		return nil, errors.New("keep must be at least 1")
	}
	matches, err := filepath.Glob(filepath.Join(dir, BackupPrefix+"*"+BackupSuffix))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	var deleted []string
	for len(matches) > keep {
		if err := os.Remove(matches[0]); err != nil {
			return deleted, err
		}
		deleted = append(deleted, matches[0])
		matches = matches[1:]
	}
	return deleted, nil
}

// Restore unpacks a backup into dataDir, which must not hold a database
// or blob files yet. Everything happens in a staging directory inside
// dataDir first: every file is checked against the manifest (and every
// blob against its id), the database is opened once there (which migrates
// a backup from an older release) and prepared for running clients (see
// prepareRestored); only then are the blobs and, last, meta.db moved into
// place. A restore that fails leaves dataDir as it was, so it can simply be
// run again.
func Restore(ctx context.Context, backupPath, dataDir string) (*BackupResult, error) {
	if _, err := os.Stat(filepath.Join(dataDir, "meta.db")); err == nil {
		return nil, fmt.Errorf("%s already has a database; restore into a new, empty data directory", dataDir)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	if entries, err := os.ReadDir(filepath.Join(dataDir, "blobs")); err == nil && len(entries) > 0 {
		return nil, fmt.Errorf("%s already has blob files; restore into a new, empty data directory", dataDir)
	}
	stage, err := os.MkdirTemp(dataDir, ".restore-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	unpacked := filepath.Join(stage, "unpacked")
	man, err := unpack(backupPath, unpacked)
	if err != nil {
		return nil, err
	}
	res := &BackupResult{Path: backupPath, Schema: man.Schema, CreatedAt: man.CreatedAt}
	if err := checkFile(unpacked, man.Database); err != nil {
		return nil, err
	}
	listed := map[string]BackupFile{}
	for _, b := range man.Blobs {
		if !blobEntry.MatchString(b.Path) {
			return nil, fmt.Errorf("manifest lists %q, which is not a blob path", b.Path)
		}
		if _, dup := listed[b.Path]; dup {
			return nil, fmt.Errorf("manifest lists %s twice", b.Path)
		}
		if err := checkFile(unpacked, b); err != nil {
			return nil, err
		}
		listed[b.Path] = b
		res.Blobs++
		res.BlobBytes += b.Size
	}
	if err := checkSnapshot(ctx, filepath.Join(unpacked, "meta.db"), listed); err != nil {
		return nil, err
	}
	// The data directory as it will be, built in the stage. Unlisted blob
	// entries (damaged when the backup was made) stay behind.
	built := filepath.Join(stage, "data")
	if err := os.MkdirAll(filepath.Join(built, "blobs"), 0o700); err != nil {
		return nil, err
	}
	for p := range listed {
		if err := os.Rename(filepath.Join(unpacked, p), filepath.Join(built, p)); err != nil {
			return nil, err
		}
	}
	if err := os.Rename(filepath.Join(unpacked, "meta.db"), filepath.Join(built, "meta.db")); err != nil {
		return nil, err
	}
	cfg := DefaultConfig()
	cfg.DataDir = built
	// Only checking that it opens: the restored node applies its own
	// directory setting when it starts (a default-off open here would
	// delete the backup's listings).
	cfg.DirectoryEnabled = true
	srv, err := Open(ctx, cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		return nil, fmt.Errorf("the restored database doesn't open: %w", err)
	}
	if err := srv.Close(); err != nil {
		return nil, err
	}
	if err := prepareRestored(ctx, filepath.Join(built, "meta.db")); err != nil {
		return nil, err
	}
	if err := moveIntoPlace(built, dataDir); err != nil {
		return nil, err
	}
	return res, nil
}

// MailboxIDGap is how far a restore moves the mailbox id sequence past
// the backup's highest id.
//
// A mailbox id is a reader's position (MailboxFetch.after_id). The backup
// holds the id sequence as it was when the backup was taken; ids the node
// issued after that, before it was lost, would be issued again, and a
// client still running with a position among them would skip the new
// items. Moving the sequence on by 2^32 puts every id issued after the
// restore past any id the lost node could have issued (it would have had to
// accept four billion drops after the backup), and the mailbox epoch
// changes, so clients also start their positions over.
const MailboxIDGap = 1 << 32

// restoreEpochs are the epochs a restore changes: the mailbox epoch
// (MailboxItems.epoch) and the public replication log's
// (ReplicationBatch.epoch), whose revision counter goes back to the backup's,
// and the peer log's (PeerBatch.epoch), likewise.
var restoreEpochs = []string{"mailbox_epoch", replEpochKey, peerEpochKey}

// prepareRestored moves the mailbox id sequence past ids the lost node may
// have issued after the backup, and changes the restored node's epochs.
func prepareRestored(ctx context.Context, path string) error {
	db, err := store.Open(path)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var seq, maxID int64
	if err := tx.QueryRowContext(ctx, `SELECT coalesce((SELECT seq FROM sqlite_sequence WHERE name = 'mailbox'), 0)`).Scan(&seq); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(id), 0) FROM mailbox`).Scan(&maxID); err != nil {
		return err
	}
	next := max(seq, maxID) + MailboxIDGap
	res, err := tx.ExecContext(ctx, `UPDATE sqlite_sequence SET seq = ? WHERE name = 'mailbox'`, next)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sqlite_sequence (name, seq) VALUES ('mailbox', ?)`, next); err != nil {
			return err
		}
	}
	for _, k := range restoreEpochs {
		if _, err := newEpoch(ctx, tx, k); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return err
	}
	return db.Close()
}

// moveIntoPlace moves a restored data directory's blobs and database from
// built into dataDir, the database last (its presence is what makes dataDir
// a node's data directory). On failure what was moved goes back.
func moveIntoPlace(built, dataDir string) error {
	blobs := filepath.Join(dataDir, "blobs")
	// An empty blobs directory (checked by Restore) is replaced.
	if err := os.Remove(blobs); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s: %w", blobs, err)
	}
	if err := os.Rename(filepath.Join(built, "blobs"), blobs); err != nil {
		return err
	}
	var moved []string
	undo := func() {
		for _, name := range moved {
			_ = os.Rename(filepath.Join(dataDir, name), filepath.Join(built, name))
		}
		_ = os.Rename(blobs, filepath.Join(built, "blobs"))
	}
	for _, name := range []string{"meta.db-wal", "meta.db-shm", "meta.db"} {
		err := os.Rename(filepath.Join(built, name), filepath.Join(dataDir, name))
		if errors.Is(err, os.ErrNotExist) && name != "meta.db" {
			continue
		}
		if err != nil {
			undo()
			return err
		}
		moved = append(moved, name)
	}
	return syncDir(dataDir)
}

// maxManifest bounds manifest.json (a manifest line per blob).
const maxManifest = 64 << 20

// unpack extracts a backup into dir, accepting only the names a backup
// has (no paths outside dir, no links), and returns its manifest.
func unpack(path, dir string) (*BackupManifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	if err := os.MkdirAll(filepath.Join(dir, "blobs"), 0o700); err != nil {
		return nil, err
	}
	var man *BackupManifest
	seen := map[string]bool{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("not a maintainer backup (or damaged): %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("backup entry %q is not a regular file", h.Name)
		}
		if seen[h.Name] {
			return nil, fmt.Errorf("backup has %q twice", h.Name)
		}
		seen[h.Name] = true
		switch {
		case h.Name == "manifest.json":
			if h.Size > maxManifest {
				return nil, errors.New("manifest.json is too large")
			}
			b, err := io.ReadAll(io.LimitReader(tr, maxManifest))
			if err != nil {
				return nil, err
			}
			man = &BackupManifest{}
			dec := json.NewDecoder(bytes.NewReader(b))
			dec.DisallowUnknownFields()
			if err := dec.Decode(man); err != nil {
				return nil, fmt.Errorf("manifest.json: %w", err)
			}
		case h.Name == "meta.db" || blobEntry.MatchString(h.Name):
			if err := extract(tr, filepath.Join(dir, filepath.FromSlash(h.Name)), h.Size); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("backup entry %q is not part of a maintainer backup", h.Name)
		}
	}
	if man == nil {
		return nil, errors.New("not a maintainer backup: manifest.json is missing")
	}
	if man.Format != backupFormat {
		return nil, fmt.Errorf("not a maintainer backup (format %q)", man.Format)
	}
	if man.Version != backupVersion {
		return nil, fmt.Errorf("backup format version %d is not supported by this build (%d)", man.Version, backupVersion)
	}
	if man.Schema > len(migrations) {
		return nil, fmt.Errorf("the backup's database is at schema version %d, newer than this build supports (%d): restore it with that release or a newer one", man.Schema, len(migrations))
	}
	if man.Database.Path != "meta.db" {
		return nil, errors.New("manifest doesn't name meta.db")
	}
	return man, nil
}

func extract(r io.Reader, path string, size int64) error {
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.CopyN(out, r, size); err != nil {
		out.Close()
		return fmt.Errorf("backup is truncated: %w", err)
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// checkFile compares an extracted file with its manifest entry; for a
// blob, the manifest hash is also its id's content hash (checked against
// the database in checkSnapshot).
func checkFile(dir string, bf BackupFile) error {
	f, err := os.Open(filepath.Join(dir, filepath.FromSlash(bf.Path)))
	if err != nil {
		return fmt.Errorf("backup is missing %s", bf.Path)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	if n != bf.Size || hex.EncodeToString(h.Sum(nil)) != strings.ToLower(bf.SHA256) {
		return fmt.Errorf("%s in the backup is damaged (size or SHA-256 differs from the manifest)", bf.Path)
	}
	return nil
}

// checkSnapshot checks that the database names exactly the listed blobs,
// each with the hash and size of its file.
func checkSnapshot(ctx context.Context, path string, listed map[string]BackupFile) error {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	var ok string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&ok); err != nil || ok != "ok" {
		return fmt.Errorf("the backup's database is damaged: %v %s", err, ok)
	}
	rows, err := db.QueryContext(ctx, `SELECT id, hash, size, complete FROM blobs`)
	if err != nil {
		return err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id, hash []byte
		var size int64
		var complete bool
		if err := rows.Scan(&id, &hash, &size, &complete); err != nil {
			return err
		}
		p := "blobs/" + hex.EncodeToString(id)
		bf, ok := listed[p]
		if !complete || !ok {
			return fmt.Errorf("the backup's database names a blob (%s) that the backup doesn't hold", p)
		}
		// checkFile verified the file against bf; the record must agree.
		if bf.Size != size || !strings.EqualFold(bf.SHA256, hex.EncodeToString(hash)) {
			return fmt.Errorf("blob %s doesn't match its record", p)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if n != len(listed) {
		return errors.New("the backup holds blobs its database doesn't name")
	}
	return nil
}
