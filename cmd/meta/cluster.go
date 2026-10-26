package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/config"
	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/meta/clusterset"
	"github.com/hyperkubeorg/openarcanum/internal/version"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Maintainer cluster commands (docs/operations.md "Maintainer clusters").

const idUsage = `usage:
  meta id -data DIR       this node's node key and Noise key (created on a new node)`

const trustUsage = `usage:
  meta trust add -data DIR URL NODEKEY   trust the node at URL with private data (both sides must)
  meta trust remove -data DIR NODEKEY    stop trusting it
  meta trust list -data DIR [-config FILE]
NODEKEY is the other node's key as its ` + "`meta id`" + ` prints it`

const operatorUsage = `usage:
  meta operator keygen -out op.key
      a new operator key for a maintainer cluster (keep it offline; prints its public key)
  meta operator sign-set -key op.key -seq N -node URL=NODEKEY[:root][:nodirectory] ... [-valid 4320h] -out set.bin
      sign the cluster's member list, in preference order (at most 8 nodes);
      each new set needs a higher -seq`

const setUsage = `usage:
  meta set publish -data DIR [-config FILE] set.bin
      store a signed maintainer set on this node; it serves it at
      /v1/maintainers and replicates it to the other members`

// nodeFlags collects repeated -node values.
type nodeFlags []string

func (n *nodeFlags) String() string     { return strings.Join(*n, ",") }
func (n *nodeFlags) Set(v string) error { *n = append(*n, v); return nil }

// parseSetNode reads URL=NODEKEY[:root][:nodirectory].
func parseSetNode(v string) (*wire.MaintainerNode, error) {
	i := strings.LastIndex(v, "=")
	if i < 0 {
		return nil, fmt.Errorf("-node %q: want URL=NODEKEY[:root][:nodirectory]", v)
	}
	url, err := clusterset.CleanURL(v[:i])
	if err != nil {
		return nil, fmt.Errorf("-node %q: %w", v, err)
	}
	parts := strings.Split(v[i+1:], ":")
	key, err := identity.Decode(parts[0])
	if err != nil || !identity.ValidPublic(key) {
		return nil, fmt.Errorf("-node %q: the node key isn't a base64url Ed25519 key (`meta id` prints it)", v)
	}
	n := &wire.MaintainerNode{Url: url, NodeKey: key, Role: clusterset.RoleMember}
	for _, opt := range parts[1:] {
		switch opt {
		case "root":
			n.Role = clusterset.RoleRoot
		case "nodirectory":
			n.NoDirectory = true
		default:
			return nil, fmt.Errorf("-node %q: unknown option %q (root, nodirectory)", v, opt)
		}
	}
	return n, nil
}

func operatorCommand(_ context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(operatorUsage)
	}
	fs := flag.NewFlagSet("meta operator "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	outPath := fs.String("out", "", "file to write")
	keyPath := fs.String("key", "", "the operator key file")
	seq := fs.Uint64("seq", 0, "the set's sequence number")
	valid := fs.Duration("valid", clusterset.DefaultLifetime, "how long the set stays valid")
	var nodes nodeFlags
	fs.Var(&nodes, "node", "URL=NODEKEY[:root][:nodirectory]")
	if err := fs.Parse(args[1:]); err != nil {
		return fmt.Errorf("%w\n%s", err, operatorUsage)
	}
	if *outPath == "" || fs.NArg() > 0 {
		return errors.New(operatorUsage)
	}
	switch args[0] {
	case "keygen":
		if _, err := os.Stat(*outPath); err == nil {
			return fmt.Errorf("%s already exists; an operator key is never overwritten", *outPath)
		}
		k, err := identity.Generate()
		if err != nil {
			return err
		}
		if err := identity.WriteSeed(*outPath, k); err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "operator key written to %s (keep it offline)\npublic key: %s\n", *outPath, identity.Encode(k.Public))
		return err
	case "sign-set":
		if *keyPath == "" || *seq == 0 || len(nodes) == 0 {
			return fmt.Errorf("sign-set needs -key, -seq (at least 1) and one -node per member\n%s", operatorUsage)
		}
		b, err := os.ReadFile(*keyPath)
		if err != nil {
			return err
		}
		seed, err := hex.DecodeString(strings.TrimSpace(string(b)))
		if err != nil {
			return fmt.Errorf("%s isn't an operator key file", *keyPath)
		}
		op, err := identity.FromSeed(seed)
		if err != nil {
			return err
		}
		now := time.Now()
		set := &wire.MaintainerSet{Seq: *seq, IssuedAt: now.UnixMilli(), ExpiresAt: now.Add(*valid).UnixMilli()}
		for _, v := range nodes {
			n, err := parseSetNode(v)
			if err != nil {
				return err
			}
			set.Nodes = append(set.Nodes, n)
		}
		signed, err := clusterset.Sign(op, set)
		if err != nil {
			return err
		}
		if _, err := clusterset.Verify(signed, op.Public, now); err != nil {
			return err
		}
		raw, err := proto.Marshal(signed)
		if err != nil {
			return err
		}
		if err := identity.WriteFileAtomic(*outPath, raw, 0o644); err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "maintainer set #%d with %d nodes, valid until %s, written to %s\noperator key: %s\n",
			*seq, len(set.Nodes), time.UnixMilli(set.ExpiresAt).UTC().Format(time.RFC3339), *outPath, identity.Encode(op.Public))
		return err
	}
	return fmt.Errorf("unknown command %q\n%s", args[0], operatorUsage)
}

func setCommand(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "publish" {
		return errors.New(setUsage)
	}
	data, cfgPath, rest, err := dataFlags("meta set publish", args[1:], setUsage)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New(setUsage)
	}
	raw, err := os.ReadFile(rest[0])
	if err != nil {
		return err
	}
	var operator []byte
	cfg := meta.DefaultConfig()
	if cfgPath != "" {
		if err := config.Load(cfgPath, "OPENARCANUM_META_", &cfg); err != nil {
			return err
		}
		if cfg.ClusterOperatorKey != "" {
			operator, _ = identity.Decode(cfg.ClusterOperatorKey)
		}
	}
	adm, err := openAdmin(ctx, &adminFlags{data: data, cfgPath: cfgPath})
	if err != nil {
		return err
	}
	defer func() { _ = adm.Close() }()
	set, err := adm.PublishSet(ctx, raw, operator)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "published maintainer set #%d (%d nodes, operator %s); nodes configured with this cluster_operator_key trust its members\n",
		set.Seq, len(set.Nodes), identity.Fingerprint(set.OperatorKey))
	return err
}

const initUsage = `usage:
  meta init -replica-of ROOT_URL -data DIR [-url THIS_URL] [-config FILE] [-operator KEY] [-from-backup FILE]
      set up this node as a replica of the root node of a maintainer cluster:
      writes a config that trusts the root and follows its moderation
      decisions, creates this node's keys and prints what to run on the root.
      -from-backup seeds the data directory from a backup of the root first`

const peersUsage = `usage:
  meta peers status -data DIR      each trusted node: lag, last success, errors, throttling`

// fetchNode reads GET /v1/node from a maintainer node.
func fetchNode(ctx context.Context, url string) (*wire.NodeInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/v1/node", nil)
	if err != nil {
		return nil, err
	}
	version.SetRequest(req)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	if err := version.CheckResponse(resp); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s/v1/node: %s (is it a maintainer node of this release or newer?)", url, resp.Status)
	}
	var info wire.NodeInfo
	if err := proto.Unmarshal(b, &info); err != nil || !identity.ValidPublic(info.NodeKey) {
		return nil, fmt.Errorf("%s/v1/node: not a node identity", url)
	}
	return &info, nil
}

func initCommand(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("meta init", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root := fs.String("replica-of", "", "the root node's URL")
	data := fs.String("data", "", "this node's data directory")
	self := fs.String("url", "", "this node's own URL, as the root will reach it")
	cfgPath := fs.String("config", "", "config file to write (default DIR/meta.json)")
	opFlag := fs.String("operator", "", "the cluster's operator key, if you know it (else taken from the root and shown)")
	backup := fs.String("from-backup", "", "a backup of the root to seed this node with")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w\n%s", err, initUsage)
	}
	if *root == "" || *data == "" || fs.NArg() > 0 {
		return errors.New(initUsage)
	}
	rootURL, err := clusterset.CleanURL(*root)
	if err != nil {
		return err
	}
	selfURL := "https://THIS-NODE"
	if *self != "" {
		if selfURL, err = clusterset.CleanURL(*self); err != nil {
			return err
		}
	}
	if *cfgPath == "" {
		*cfgPath = filepath.Join(*data, "meta.json")
	}
	if _, err := os.Stat(*cfgPath); err == nil {
		return fmt.Errorf("%s already exists; meta init never overwrites a config", *cfgPath)
	}
	info, err := fetchNode(ctx, rootURL)
	if err != nil {
		return fmt.Errorf("the root: %w", err)
	}
	var operator []byte
	if *opFlag != "" {
		if operator, err = identity.Decode(*opFlag); err != nil || !identity.ValidPublic(operator) {
			return errors.New("-operator isn't a base64url Ed25519 key")
		}
	}
	// The root's maintainer set, if it is in a cluster.
	var set *wire.MaintainerSet
	if s, _, err := fetchSet(ctx, rootURL, operator); err == nil {
		set = s
		operator = s.OperatorKey
	} else if operator != nil {
		return fmt.Errorf("the root has no valid maintainer set for -operator: %w", err)
	}
	if *backup != "" {
		res, err := meta.Restore(ctx, *backup, *data)
		if err != nil {
			return fmt.Errorf("seeding from the backup: %w", err)
		}
		if _, err := fmt.Fprintf(out, "seeded from %s (%d blobs); this node gets keys and epochs of its own\n", *backup, res.Blobs); err != nil {
			return err
		}
	}
	key, _, err := meta.NodeKeys(*data)
	if err != nil {
		return err
	}
	if bytes.Equal(key, info.NodeKey) {
		return errors.New("this data directory has the root's node key; use a new data directory")
	}
	cfg := map[string]any{
		"data_dir":         *data,
		"peers":            []string{rootURL},
		"trusted_peers":    []string{rootURL + "#" + identity.Encode(info.NodeKey)},
		"follow_decisions": []string{identity.Encode(info.NodeKey)},
	}
	if operator != nil {
		cfg["cluster_operator_key"] = identity.Encode(operator)
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.MkdirAll(filepath.Dir(*cfgPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(*cfgPath, append(b, '\n'), 0o600); err != nil {
		return err
	}
	selfKey := identity.Encode(key)
	var w strings.Builder
	fmt.Fprintf(&w, "wrote %s: trusts the root %s (%s) and follows its moderation decisions\n", *cfgPath, rootURL, identity.Fingerprint(info.NodeKey))
	fmt.Fprintf(&w, "this node's key: %s\n\n", selfKey)
	if set != nil {
		fmt.Fprintf(&w, "The root is in maintainer set #%d (operator %s). Check that fingerprint with the operator, then\n", set.Seq, identity.Fingerprint(operator))
		fmt.Fprintf(&w, "sign a new set with this node added and publish it on any member:\n  meta operator sign-set -key op.key -seq %d", set.Seq+1)
		for _, n := range set.Nodes {
			opt := ""
			if n.Role == clusterset.RoleRoot {
				opt = ":root"
			}
			fmt.Fprintf(&w, " \\\n    -node %s=%s%s", n.Url, identity.Encode(n.NodeKey), opt)
		}
		fmt.Fprintf(&w, " \\\n    -node %s=%s -out set.bin\n  meta set publish -data ROOT_DATA set.bin\n", selfURL, selfKey)
		fmt.Fprintf(&w, "Or, until then, on the root:\n")
	} else {
		fmt.Fprintf(&w, "On the root, run:\n")
	}
	fmt.Fprintf(&w, "  meta trust add -data ROOT_DATA %s %s\n\n", selfURL, selfKey)
	fmt.Fprintf(&w, "Then start this node: meta -config %s\nIts first start pulls everything from the root.\n", *cfgPath)
	_, err = io.WriteString(out, w.String())
	return err
}

// fetchSet reads the root's maintainer set: for operator if given, else
// whatever cluster the root is in (shown to the operator to check).
func fetchSet(ctx context.Context, url string, operator []byte) (*wire.MaintainerSet, *wire.SignedMaintainerSet, error) {
	if operator != nil {
		return clusterset.Fetch(ctx, &http.Client{Timeout: 15 * time.Second}, url, operator, time.Now())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/v1/maintainers", nil)
	if err != nil {
		return nil, nil, err
	}
	version.SetRequest(req)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("no maintainer set (%s)", resp.Status)
	}
	var signed wire.SignedMaintainerSet
	if err := proto.Unmarshal(b, &signed); err != nil {
		return nil, nil, err
	}
	set, err := clusterset.Verify(&signed, nil, time.Now())
	return set, &signed, err
}

func peersCommand(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "status" {
		return errors.New(peersUsage)
	}
	data, cfgPath, rest, err := dataFlags("meta peers status", args[1:], peersUsage)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return errors.New(peersUsage)
	}
	adm, err := openAdmin(ctx, &adminFlags{data: data, cfgPath: cfgPath})
	if err != nil {
		return err
	}
	defer func() { _ = adm.Close() }()
	ps, err := adm.PeerStatus(ctx)
	if err != nil {
		return err
	}
	var b strings.Builder
	now := time.Now()
	for _, p := range ps {
		k, _ := identity.Decode(p.Key)
		last := "never"
		if !p.LastOK.IsZero() {
			last = now.Sub(p.LastOK).Truncate(time.Second).String() + " ago"
		}
		fmt.Fprintf(&b, "%s  %s  lag %d  last success %s  epoch %x", p.URL, identity.Fingerprint(k), p.Lag(), last, p.Epoch)
		if p.ThrottledUntil.After(now) {
			fmt.Fprintf(&b, "  THROTTLED until %s", p.ThrottledUntil.UTC().Format(time.RFC3339))
		}
		if p.LastError != "" {
			fmt.Fprintf(&b, "\n    last error: %s", p.LastError)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "%d peers\n", len(ps))
	_, err = io.WriteString(out, b.String())
	return err
}

// dataFlags parses -data (and -config) before positional arguments.
func dataFlags(name string, args []string, usage string) (data, cfgPath string, rest []string, err error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&data, "data", "", "the node's data_dir")
	fs.StringVar(&cfgPath, "config", "", "the node's config file")
	if err := fs.Parse(args); err != nil {
		return "", "", nil, fmt.Errorf("%w\n%s", err, usage)
	}
	if data == "" {
		return "", "", nil, errors.New(usage)
	}
	return data, cfgPath, fs.Args(), nil
}

func idCommand(_ context.Context, args []string, out io.Writer) error {
	data, _, rest, err := dataFlags("meta id", args, idUsage)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return errors.New(idUsage)
	}
	key, noiseKey, err := meta.NodeKeys(data)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "node key:     %s\nnoise key:    %s\nfingerprint:  %s\n",
		identity.Encode(key), identity.Encode(noiseKey), identity.Fingerprint(key))
	return err
}

func trustCommand(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(trustUsage)
	}
	sub := args[0]
	data, cfgPath, rest, err := dataFlags("meta trust "+sub, args[1:], trustUsage)
	if err != nil {
		return err
	}
	want := map[string]int{"add": 2, "remove": 1, "list": 0}
	n, ok := want[sub]
	if !ok {
		return fmt.Errorf("unknown command %q\n%s", sub, trustUsage)
	}
	if len(rest) != n {
		return errors.New(trustUsage)
	}
	adm, err := openAdmin(ctx, &adminFlags{data: data, cfgPath: cfgPath})
	if err != nil {
		return err
	}
	defer func() { _ = adm.Close() }()
	var b strings.Builder
	switch sub {
	case "add":
		key, err := identity.Decode(rest[1])
		if err != nil || !identity.ValidPublic(key) {
			return fmt.Errorf("%q isn't a node key\n%s", rest[1], trustUsage)
		}
		if err := adm.TrustAdd(ctx, rest[0], key); err != nil {
			return err
		}
		fmt.Fprintf(&b, "trusting %s (%s); the other node must trust this one too\n", rest[0], identity.Fingerprint(key))
	case "remove":
		key, err := identity.Decode(rest[0])
		if err != nil || !identity.ValidPublic(key) {
			return fmt.Errorf("%q isn't a node key\n%s", rest[0], trustUsage)
		}
		had, err := adm.TrustRemove(ctx, key)
		if err != nil {
			return err
		}
		if !had {
			return fmt.Errorf("%s isn't trusted with `meta trust add` (a node in the config file or the maintainer set is removed there)", rest[0])
		}
		fmt.Fprintf(&b, "no longer trusting %s; its sessions end now\n", identity.Fingerprint(key))
	case "list":
		ps, err := adm.TrustList(ctx)
		if err != nil {
			return err
		}
		for _, p := range ps {
			fmt.Fprintf(&b, "%s  %s  %s\n", identity.Encode(p.Key), p.URL, p.Source)
		}
		fmt.Fprintf(&b, "%d trusted nodes (members of the maintainer set are trusted too)\n", len(ps))
	}
	_, err = io.WriteString(out, b.String())
	return err
}
