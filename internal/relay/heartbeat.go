package relay

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/meta/clusterset"
	"github.com/hyperkubeorg/openarcanum/internal/relayurl"
	"github.com/hyperkubeorg/openarcanum/internal/version"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Heartbeater sends signed heartbeats to the relay registry on maintainer
// nodes (docs/protocol.md §7.3). The registry key identifies the relay in the
// registry; it is the relay's identity key, which it also proves in every
// Hello (docs/protocol.md §2 "Relay identity").
type Heartbeater struct {
	cfg  Config
	srv  *Server
	log  *slog.Logger
	key  *identity.Key
	http *http.Client
	// clusters expands registry_urls entries naming a maintainer cluster.
	clusters *clusterset.Cache
	// url is the announced URL (public_url, or the wss:// default with
	// TLS on) in canonical form (internal/relayurl), as the registry stores
	// and compares it.
	url string
}

// NewHeartbeater signs with the relay's key (registry_key_file, loaded by
// New; without a key file, a fresh key for each run).
func NewHeartbeater(cfg Config, srv *Server, log *slog.Logger) (*Heartbeater, error) {
	if err := srv.Err(); err != nil {
		return nil, err
	}
	key := srv.Key()
	if log == nil {
		log = slog.Default()
	}
	url, err := relayurl.Canonical(cfg.AnnouncedURL())
	if err != nil {
		return nil, fmt.Errorf("public_url: %w", err)
	}
	hc := &http.Client{Timeout: 10 * time.Second}
	if srv != nil {
		srv.SetRegistryKey(key.Public) // proof of control of url (registrykey.go)
	}
	return &Heartbeater{cfg: cfg, srv: srv, log: log, key: key, http: hc, clusters: clusterset.NewCache(hc, 6*time.Hour), url: url}, nil
}

// Run sends a heartbeat now and then every HeartbeatInterval until ctx ends.
func (h *Heartbeater) Run(ctx context.Context) {
	t := time.NewTicker(h.cfg.HeartbeatInterval.Duration)
	defer t.Stop()
	for {
		h.beat(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (h *Heartbeater) beat(ctx context.Context) {
	body, err := proto.Marshal(&wire.RelayHeartbeat{
		Url:         h.url,
		Region:      h.cfg.Region,
		Capacity:    uint32(h.cfg.MaxConnections),
		Connections: uint32(h.srv.Connections()),
		RelayKey:    h.key.Public,
		At:          time.Now().UnixMilli(),
	})
	if err != nil {
		return
	}
	signed, _ := proto.Marshal(&wire.SignedHeartbeat{Heartbeat: body, Signature: h.key.Sign(identity.CtxRelayHeartbeat, body)})
	var op []byte
	if h.cfg.MaintainerOperatorKey != "" {
		op, _ = identity.Decode(h.cfg.MaintainerOperatorKey)
	}
	for _, base := range h.clusters.Expand(ctx, h.cfg.RegistryURLs, op) {
		if err := h.post(ctx, strings.TrimRight(base, "/")+"/v1/relays/heartbeat", signed); err != nil {
			h.log.Warn("relay heartbeat failed", "registry", base, "err", err)
		}
	}
}

func (h *Heartbeater) post(ctx context.Context, url string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	version.SetRequest(req)
	resp, err := h.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if err := version.CheckResponse(resp); err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
