package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/config"
	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/relay"
)

type validator interface{ Validate() error }

// node is one node type's config: a fresh default and its env prefix.
type node struct {
	prefix string
	fresh  func() any
}

var nodes = []node{
	{"OPENARCANUM_RELAY_", func() any { c := relay.DefaultConfig(); return &c }},
	{"OPENARCANUM_META_", func() any { c := meta.DefaultConfig(); return &c }},
	{"OPENARCANUM_COMMUNITY_", func() any { c := community.DefaultConfig("data"); return &c }},
}

// envNames lists the env override names a config struct declares.
func envNames(cfg any) []string {
	var out []string
	t := reflect.TypeOf(cfg).Elem()
	for i := 0; i < t.NumField(); i++ {
		if name := t.Field(i).Tag.Get("env"); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// FuzzLoad loads arbitrary config files and one arbitrary environment
// override into each node type's config, then validates the result, the way
// cmd/relay, cmd/meta and cmd/community start. Nothing may panic, and a
// config file that loads must survive Save and Load unchanged.
func FuzzLoad(f *testing.F) {
	f.Add(uint8(0), []byte(`{"listen":":7000","max_chunk":4096,"ping_interval":"5s"}`), uint8(0), "")
	f.Add(uint8(1), []byte(`{"peers":["http://a"],"mailbox_ttl":"1h"}`), uint8(2), "a, b,,c")
	f.Add(uint8(2), []byte(`{"relays":["ws://r/ws"]}`), uint8(3), "true")
	f.Add(uint8(0), []byte(`{"lisen":":1"}`), uint8(4), "9")
	f.Add(uint8(0), []byte(`{"max_chunk":-1}`), uint8(5), "1e400")
	f.Add(uint8(1), []byte(`{"relay_ttl":5}`), uint8(6), "-5m")
	f.Add(uint8(2), []byte(`[`), uint8(1), "\xff")
	f.Add(uint8(0), []byte(``), uint8(0), "")
	f.Fuzz(func(t *testing.T, which uint8, file []byte, field uint8, value string) {
		n := nodes[int(which)%len(nodes)]
		path := filepath.Join(t.TempDir(), "c.json")
		if err := os.WriteFile(path, file, 0o600); err != nil {
			t.Fatal(err)
		}
		names := envNames(n.fresh())
		if value != "" {
			t.Setenv(n.prefix+names[int(field)%len(names)], strings.ReplaceAll(value, "\x00", ""))
		}
		cfg := n.fresh()
		if err := config.Load(path, n.prefix, cfg); err != nil {
			return
		}
		_ = cfg.(validator).Validate()

		if value != "" {
			return // env strings may hold bytes JSON can't carry
		}
		out := filepath.Join(t.TempDir(), "saved.json")
		if err := config.Save(out, cfg); err != nil {
			t.Fatalf("save: %v", err)
		}
		again := n.fresh()
		if err := config.Load(out, n.prefix, again); err != nil {
			t.Fatalf("reload of a saved config failed: %v", err)
		}
		if !reflect.DeepEqual(cfg, again) {
			t.Fatalf("config changed across save and load:\n%+v\n%+v", cfg, again)
		}
	})
}
