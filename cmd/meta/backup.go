package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/hyperkubeorg/openarcanum/internal/meta"
)

const backupUsage = `usage:
  meta backup -data DIR [-out DIR] [-keep N]   write a backup (default -out DIR/backups); keep the newest N (default 7)
  meta restore -data NEWDIR -backup FILE       restore into a new, empty data directory`

// backupCommand writes a consistent backup of a node's data directory. It
// is safe to run while the node runs (systemd: openarcanum-meta-backup.timer).
func backupCommand(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("meta backup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dataDir := fs.String("data", "", "the node's data_dir")
	outDir := fs.String("out", "", "where backups go (default DATA/backups)")
	keep := fs.Int("keep", 7, "backups to keep in -out (older ones are deleted)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w\n%s", err, backupUsage)
	}
	if *dataDir == "" || fs.NArg() > 0 {
		return errors.New(backupUsage)
	}
	if *keep < 1 {
		return errors.New("-keep must be at least 1")
	}
	if *outDir == "" {
		*outDir = filepath.Join(*dataDir, "backups")
	}
	res, err := meta.Backup(ctx, *dataDir, *outDir, nil)
	if err != nil {
		return err
	}
	deleted, err := meta.PruneBackups(*outDir, *keep)
	if err != nil {
		return fmt.Errorf("backup written to %s, but pruning old backups failed: %w", res.Path, err)
	}
	_, _ = fmt.Fprintln(out, res.Path)
	_, _ = fmt.Fprintf(out, "blobs: %d (%s)", res.Blobs, mib(uint64(res.BlobBytes)))
	if res.Skipped > 0 {
		_, _ = fmt.Fprintf(out, ", %d left out (deleted while the backup ran)", res.Skipped)
	}
	_, _ = fmt.Fprintf(out, "; schema %d; %d old backup(s) deleted\n", res.Schema, len(deleted))
	return nil
}

// restoreCommand restores a backup into a new data directory.
func restoreCommand(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("meta restore", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dataDir := fs.String("data", "", "the new data_dir (must not hold a database)")
	backup := fs.String("backup", "", "backup file (meta-*.tar)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w\n%s", err, backupUsage)
	}
	if *dataDir == "" || *backup == "" || fs.NArg() > 0 {
		return errors.New(backupUsage)
	}
	res, err := meta.Restore(ctx, *backup, *dataDir)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "restored the backup of %s into %s: %d blobs (%s), schema %d\n",
		res.CreatedAt.Format("2006-01-02 15:04:05 UTC"), *dataDir, res.Blobs, mib(uint64(res.BlobBytes)), res.Schema)
	_, _ = fmt.Fprintln(out, "Start the node with data_dir set to that directory. Mailbox items and blobs past their lifetime are deleted on its first expiry pass.")
	return nil
}
