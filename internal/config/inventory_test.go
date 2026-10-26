package config_test

// The settings inventory (docs/settings.md "Node settings") and the operator
// guide (docs/operations.md) list every node config key, and only keys that
// exist: a key added, renamed or removed in a node's Config without the docs
// fails here. The client's half (docs/settings.md "User settings" against
// settings.py) is clients/desktop/tests/test_settings.py::test_settings_doc_matches_settings.

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/ops"
	"github.com/hyperkubeorg/openarcanum/internal/relay"
	"github.com/hyperkubeorg/openarcanum/internal/site"
)

// configKeys returns the JSON keys of a node config struct (embedded structs
// flattened, as encoding/json and config.Load do) with their env tags.
func configKeys(t reflect.Type) map[string]string {
	out := map[string]string{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous && f.Type.Kind() == reflect.Struct && f.Tag.Get("json") == "" {
			for k, v := range configKeys(f.Type) {
				out[k] = v
			}
			continue
		}
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "-" || !f.IsExported() {
			continue
		}
		out[name] = f.Tag.Get("env")
	}
	return out
}

var keyToken = regexp.MustCompile("`([a-z][a-z0-9_]*)`")

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSettingsInventoryMatchesNodeConfigs(t *testing.T) {
	opsKeys := configKeys(reflect.TypeOf(ops.Settings{}))
	nodes := map[string]map[string]string{
		"Relay":            configKeys(reflect.TypeOf(relay.Config{})),
		"Community server": configKeys(reflect.TypeOf(community.Config{})),
		"Maintainer node":  configKeys(reflect.TypeOf(meta.Config{})),
		"Website":          configKeys(reflect.TypeOf(site.Config{})),
		"All nodes":        opsKeys,
	}
	for node, keys := range nodes {
		for k, env := range keys {
			if env == "" {
				t.Errorf("%s: config key %q has no environment override (env tag)", node, k)
			}
		}
	}

	doc := readRepoFile(t, "docs/settings.md")
	start := strings.Index(doc, "\n## Node settings\n")
	if start < 0 {
		t.Fatal(`docs/settings.md has no "Node settings" section`)
	}
	sec := doc[start+1:]
	if end := strings.Index(sec, "\n## "); end >= 0 {
		sec = sec[:end]
	}

	listed := map[string]map[string]bool{}
	rows := 0
	for _, line := range strings.Split(sec, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) < 4 || strings.HasPrefix(line, "|---") || strings.TrimSpace(cells[1]) == "Node" {
			continue
		}
		rows++
		node := strings.TrimSpace(cells[1])
		keys, ok := nodes[node]
		if !ok {
			t.Errorf("docs/settings.md: unknown node %q", node)
			continue
		}
		if listed[node] == nil {
			listed[node] = map[string]bool{}
		}
		for _, m := range keyToken.FindAllStringSubmatch(line, -1) {
			k := m[1]
			if _, ok := keys[k]; !ok {
				t.Errorf("docs/settings.md lists %q under %s, but that config has no such key", k, node)
			}
			listed[node][k] = true
		}
	}
	if rows == 0 {
		t.Fatal(`docs/settings.md "Node settings" has no table rows`)
	}

	ops := readRepoFile(t, "docs/operations.md")
	for node, keys := range nodes {
		var missing, undocumented []string
		for k := range keys {
			if _, shared := opsKeys[k]; shared && node != "All nodes" {
				continue // listed once, under "All nodes"
			}
			if !listed[node][k] {
				missing = append(missing, k)
			}
			if !strings.Contains(ops, "`"+k+"`") {
				undocumented = append(undocumented, k)
			}
		}
		sort.Strings(missing)
		sort.Strings(undocumented)
		if len(missing) > 0 {
			t.Errorf("%s config keys missing from docs/settings.md: %v", node, missing)
		}
		if len(undocumented) > 0 {
			t.Errorf("%s config keys missing from docs/operations.md: %v", node, undocumented)
		}
	}
}

// The settings rule's "rejects bad input" for keys whose own package tests
// don't already cover it: each bad value alone makes Validate fail, naming
// the key.
func TestNodeConfigsRejectBadValues(t *testing.T) {
	relayBad := func(f func(c *relay.Config)) func() error {
		return func() error { c := relay.DefaultConfig(); f(&c); return c.Validate() }
	}
	metaBad := func(f func(c *meta.Config)) func() error {
		return func() error { c := meta.DefaultConfig(); c.DataDir = "x"; f(&c); return c.Validate() }
	}
	communityBad := func(f func(c *community.Config)) func() error {
		return func() error {
			c := community.DefaultConfig("x")
			c.Relays = []string{"ws://127.0.0.1:7000/ws"}
			f(&c)
			return c.Validate()
		}
	}
	checks := []struct {
		key string
		err func() error
	}{
		{"write_timeout", relayBad(func(c *relay.Config) { c.WriteTimeout.Duration = 0 })},
		{"media_listen_ip", relayBad(func(c *relay.Config) { c.MediaListenIP = "not-an-ip" })},
		{"media_udp_port", relayBad(func(c *relay.Config) { c.MediaUDPPort = 70000 })},
		{"max_calls", relayBad(func(c *relay.Config) { c.MaxCalls = 0 })},
		{"max_call_peers", relayBad(func(c *relay.Config) { c.MaxCallPeers = 0 })},
		{"max_call_peers_per_ip", relayBad(func(c *relay.Config) { c.MaxCallPeersPerIP = 0 })},
		{"call_peer_bitrate", relayBad(func(c *relay.Config) { c.CallPeerBitrate = 1000 })},
		{"call_servers", relayBad(func(c *relay.Config) { c.CallServers = []string{"not-hex"} })},
		{"host_bytes_per_stream", relayBad(func(c *relay.Config) { c.HostBytesPerStream = -1 })},
		{"host_bytes_per_second", relayBad(func(c *relay.Config) { c.HostBytesPerSecond = -1 })},
		{"host_byte_burst", relayBad(func(c *relay.Config) { c.HostByteBurst = -1 })},
		{"dedicated_server_key", relayBad(func(c *relay.Config) { c.DedicatedServerKey, c.BlockFile = "not-hex", "/b" })},
		{"dedicated_server_key_file", relayBad(func(c *relay.Config) {
			c.DedicatedServerKey, c.DedicatedServerKeyFile, c.BlockFile = strings.Repeat("ab", 32), "/k", "/b"
		})},
		{"block_file", relayBad(func(c *relay.Config) { c.DedicatedServerKey = strings.Repeat("ab", 32) })},
		{"max_blocked_networks", relayBad(func(c *relay.Config) { c.MaxBlockedNetworks = 0 })},
		{"max_connections_per_ip", relayBad(func(c *relay.Config) { c.MaxConnectionsPerIP = 0 })},
		{"max_subscriptions_per_ip", relayBad(func(c *relay.Config) { c.MaxSubscriptionsPerIP = 0 })},
		{"max_subscriptions_total", relayBad(func(c *relay.Config) { c.MaxSubscriptionsTotal = -1 })},
		{"pow_ip_connections", relayBad(func(c *relay.Config) { c.PowIPConnections = 0 })},
		{"replication_interval", metaBad(func(c *meta.Config) { c.ReplicationInterval.Duration = 0 })},
		{"backup_keep", communityBad(func(c *community.Config) { c.BackupKeep = 0 })},
		{"purge_interval", communityBad(func(c *community.Config) { c.PurgeInterval.Duration = 0 })},
		{"preview_max_bytes", communityBad(func(c *community.Config) { c.PreviewMaxBytes = 10 })},
		{"preview_image_max_bytes", communityBad(func(c *community.Config) { c.PreviewImageMaxBytes = -1 })},
	}
	if err := relayBad(func(*relay.Config) {})(); err != nil {
		t.Fatalf("relay defaults: %v", err)
	}
	if err := metaBad(func(*meta.Config) {})(); err != nil {
		t.Fatalf("meta defaults: %v", err)
	}
	if err := communityBad(func(*community.Config) {})(); err != nil {
		t.Fatalf("community defaults: %v", err)
	}
	for _, c := range checks {
		if err := c.err(); err == nil || !strings.Contains(err.Error(), c.key) {
			t.Errorf("bad %s: Validate returned %v", c.key, err)
		}
	}
}
