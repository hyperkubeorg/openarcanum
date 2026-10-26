package site

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Backup files are site-backup-<UTC time>.db: a consistent copy of the
// database (VACUUM INTO), safe to take while the site runs. Reports and
// second factors in it stay sealed; restoring needs the storage key too,
// which the backup doesn't contain (keep a copy of it apart from the
// backups).
const (
	BackupPrefix = "site-backup-"
	BackupSuffix = ".db"
)

// Backup writes a backup into outDir (made 0700) and keeps the newest keep
// backups there (0 keeps all). It returns the new file's path.
func (s *Store) Backup(ctx context.Context, outDir string, keep int) (string, error) {
	if err := privateDir(outDir); err != nil {
		return "", err
	}
	name := BackupPrefix + time.Now().UTC().Format("20060102T150405.000Z") + BackupSuffix
	tmp := filepath.Join(outDir, ".tmp-"+name)
	_ = os.Remove(tmp)
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, tmp); err != nil {
		return "", fmt.Errorf("database snapshot: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return "", err
	}
	out := filepath.Join(outDir, name)
	if err := os.Rename(tmp, out); err != nil {
		return "", err
	}
	if keep > 0 {
		entries, err := os.ReadDir(outDir)
		if err != nil {
			return out, err
		}
		var old []string
		for _, e := range entries {
			if n := e.Name(); strings.HasPrefix(n, BackupPrefix) && strings.HasSuffix(n, BackupSuffix) {
				old = append(old, n)
			}
		}
		sort.Strings(old)
		for len(old) > keep {
			_ = os.Remove(filepath.Join(outDir, old[0]))
			old = old[1:]
		}
	}
	return out, nil
}
