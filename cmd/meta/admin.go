package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/config"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/meta"
)

const directoryUsage = `usage:
  meta directory list -data DIR [-q TEXT] [-limit N]        listings stored on this node
  meta directory delist -data DIR -id COMMUNITY [-reason R] remove a community and refuse it from now on
  meta directory relist -data DIR -id COMMUNITY             lift a delisting
  meta directory decisions -data DIR [-config FILE]         every delisting and relay removal
  meta directory disable -data DIR                          switch the public directory off (deletes every listing)
  meta directory enable -data DIR [-config FILE]            switch it on again
  meta directory status -data DIR [-config FILE]            whether it is on
COMMUNITY is the community id (base64url, as shown by list). -config names
the node's config file, so entries it lists are shown and protected.`

const relaysUsage = `usage:
  meta relays list -data DIR                       the relay registry as stored
  meta relays remove -data DIR -url URL [-reason R] drop a relay and refuse its heartbeats
  meta relays restore -data DIR -url URL           lift a removal`

// adminFlags parses the flags shared by `meta directory` and `meta relays`.
type adminFlags struct {
	data, cfgPath, id, url, reason, query string
	limit                                 int
}

func parseAdmin(name string, args []string, usage string) (string, *adminFlags, error) {
	if len(args) == 0 {
		return "", nil, errors.New(usage)
	}
	f := &adminFlags{}
	fs := flag.NewFlagSet(name+" "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&f.data, "data", "", "the node's data_dir")
	fs.StringVar(&f.cfgPath, "config", "", "the node's config file")
	fs.StringVar(&f.id, "id", "", "community id")
	fs.StringVar(&f.url, "url", "", "relay URL")
	fs.StringVar(&f.reason, "reason", "", "a note kept with the decision")
	fs.StringVar(&f.query, "q", "", "name filter")
	fs.IntVar(&f.limit, "limit", 100, "most entries to list")
	if err := fs.Parse(args[1:]); err != nil {
		return "", nil, fmt.Errorf("%w\n%s", err, usage)
	}
	if f.data == "" || fs.NArg() > 0 || f.limit < 1 {
		return "", nil, errors.New(usage)
	}
	return args[0], f, nil
}

// openAdmin opens the node's database with its settings: the config file,
// if given, and the OPENARCANUM_META_* environment (a command run inside the
// node's container sees the node's own, such as node_key_dir).
func openAdmin(ctx context.Context, f *adminFlags) (*meta.Admin, error) {
	cfg := meta.DefaultConfig()
	if err := config.Load(f.cfgPath, "OPENARCANUM_META_", &cfg); err != nil {
		return nil, err
	}
	return meta.OpenAdmin(ctx, f.data, cfg)
}

// directoryCommand moderates the directory on a node's database. It is safe
// to run while the node is running.
func directoryCommand(ctx context.Context, args []string, out io.Writer) error {
	sub, f, err := parseAdmin("meta directory", args, directoryUsage)
	if err != nil {
		return err
	}
	switch sub {
	case "list", "delist", "relist", "decisions", "disable", "enable", "status":
	default:
		return fmt.Errorf("unknown command %q\n%s", sub, directoryUsage)
	}
	var community []byte
	if sub == "delist" || sub == "relist" {
		community, err = identity.Decode(f.id)
		if err != nil || !identity.ValidPublic(community) {
			return fmt.Errorf("%s needs -id COMMUNITY (a base64url community id)\n%s", sub, directoryUsage)
		}
	}
	adm, err := openAdmin(ctx, f)
	if err != nil {
		return err
	}
	defer func() { _ = adm.Close() }()
	var b strings.Builder
	switch sub {
	case "list":
		ls, err := adm.Listings(ctx, f.query, f.limit)
		if err != nil {
			return err
		}
		for _, l := range ls {
			state := "listed"
			if l.Withdrawn {
				state = "withdrawn"
			}
			fmt.Fprintf(&b, "%s  %-9s  %s  %q\n", l.Community, state, l.UpdatedAt.UTC().Format(time.RFC3339), l.Name)
		}
		fmt.Fprintf(&b, "%d listings\n", len(ls))
	case "delist":
		n, err := adm.Delist(ctx, community, f.reason)
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "delisted %s (removed %d stored entries)\n", f.id, n)
	case "relist":
		ok, err := adm.Relist(ctx, community)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%s isn't delisted", f.id)
		}
		fmt.Fprintf(&b, "relisted %s; its next publication lists it again\n", f.id)
	case "disable":
		n, err := adm.DisableDirectory(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "public directory off: %d listings deleted; listing posts, search and snapshots now answer \"directory disabled\" (routes and invites keep working)\n", n)
	case "enable":
		if err := adm.EnableDirectory(ctx); err != nil {
			if errors.Is(err, meta.ErrInConfig) {
				return errors.New("directory_enabled is false in the node's config file; change it there and restart the node")
			}
			return err
		}
		fmt.Fprintf(&b, "public directory on; communities list again with their next publication\n")
	case "status":
		st, err := adm.Directory(ctx)
		if err != nil {
			return err
		}
		switch {
		case st.On:
			b.WriteString("public directory: on\n")
		case st.OffInConfig:
			b.WriteString("public directory: off (directory_enabled is off in the config: the default; set it to true to list communities)\n")
		default:
			b.WriteString("public directory: off (meta directory disable)\n")
		}
	case "decisions":
		ds, err := adm.Decisions(ctx)
		if err != nil {
			return err
		}
		for _, d := range ds {
			src := d.At.UTC().Format(time.RFC3339)
			if d.FromConfig {
				src = "config file"
			} else if d.Source != "" && d.Source != "local" {
				src += "  followed from " + d.Source
			}
			fmt.Fprintf(&b, "%-9s  %s  %s  %s\n", d.Kind, d.Key, src, d.Reason)
		}
		fmt.Fprintf(&b, "%d decisions\n", len(ds))
	}
	_, err = io.WriteString(out, b.String())
	return err
}

// relaysCommand manages the relay registry on a node's database. It is safe
// to run while the node is running.
func relaysCommand(ctx context.Context, args []string, out io.Writer) error {
	sub, f, err := parseAdmin("meta relays", args, relaysUsage)
	if err != nil {
		return err
	}
	switch sub {
	case "list":
	case "remove", "restore":
		if f.url == "" {
			return fmt.Errorf("%s needs -url URL\n%s", sub, relaysUsage)
		}
	default:
		return fmt.Errorf("unknown command %q\n%s", sub, relaysUsage)
	}
	adm, err := openAdmin(ctx, f)
	if err != nil {
		return err
	}
	defer func() { _ = adm.Close() }()
	var b strings.Builder
	switch sub {
	case "list":
		rs, err := adm.Relays(ctx)
		if err != nil {
			return err
		}
		for _, r := range rs {
			fmt.Fprintf(&b, "%s  region=%q  %d/%d connections  last seen %s\n", r.URL, r.Region, r.Connections, r.Capacity, r.LastSeen.UTC().Format(time.RFC3339))
		}
		fmt.Fprintf(&b, "%d relays\n", len(rs))
	case "remove":
		had, err := adm.RemoveRelay(ctx, f.url, f.reason)
		if err != nil {
			return err
		}
		note := "it wasn't registered; its heartbeats are refused from now on"
		if had {
			note = "removed from the registry; its heartbeats are refused from now on"
		}
		fmt.Fprintf(&b, "%s: %s\n", f.url, note)
	case "restore":
		ok, err := adm.RestoreRelay(ctx, f.url)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%s wasn't removed", f.url)
		}
		fmt.Fprintf(&b, "%s restored; its next heartbeat registers it\n", f.url)
	}
	_, err = io.WriteString(out, b.String())
	return err
}
