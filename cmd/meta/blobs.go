package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/meta"
)

const blobsUsage = `usage:
  meta blobs stats -data DIR           list stored blobs and store usage
  meta blobs delete -data DIR -id HEX  delete one blob by its id
  meta blobs purge-expired -data DIR   delete expired blobs and abandoned uploads now`

// blobsCommand manages a maintainer node's DM blob store through its data
// directory. It is safe to run while the node is running.
func blobsCommand(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(blobsUsage)
	}
	sub := args[0]
	fs := flag.NewFlagSet("meta blobs "+sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dataDir := fs.String("data", "", "the node's data_dir")
	id := fs.String("id", "", "blob id (hex SHA-256), for delete")
	if err := fs.Parse(args[1:]); err != nil {
		return fmt.Errorf("%w\n%s", err, blobsUsage)
	}
	if *dataDir == "" || fs.NArg() > 0 {
		return errors.New(blobsUsage)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	switch sub {
	case "stats", "delete", "purge-expired":
	default:
		return fmt.Errorf("unknown command %q\n%s", sub, blobsUsage)
	}
	adm, err := meta.OpenBlobAdmin(ctx, *dataDir, log)
	if err != nil {
		return err
	}
	defer func() { _ = adm.Close() }()
	var lines []string
	say := func(format string, a ...any) { lines = append(lines, fmt.Sprintf(format, a...)) }
	switch sub {
	case "stats":
		st, err := adm.Stats(ctx)
		if err != nil {
			return err
		}
		say("stored: %d blobs, %s\n", st.Stored, mib(st.StoredBytes))
		say("uploading: %d blobs, %s reserved\n", st.Uploading, mib(st.UploadingBytes))
		say("total: %s\n", mib(st.StoredBytes+st.UploadingBytes))
		for _, b := range st.Blobs {
			state := "stored"
			if !b.Complete {
				state = "uploading"
			}
			say("%s %10d bytes %-9s %3d tokens  expires %s\n", b.ID, b.Size, state, b.Tokens, b.ExpiresAt.Format(time.RFC3339))
		}
	case "delete":
		if *id == "" {
			return errors.New("delete needs -id HEX\n" + blobsUsage)
		}
		ok, err := adm.Delete(ctx, *id)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no blob %s", *id)
		}
		say("deleted %s\n", *id)
	case "purge-expired":
		n, err := adm.PurgeExpired(ctx)
		if err != nil {
			return err
		}
		say("deleted %d blobs\n", n)
	}
	_, err = io.WriteString(out, strings.Join(lines, ""))
	return err
}

func mib(n uint64) string { return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20)) }
