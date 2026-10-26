package meta

import (
	"context"
	"errors"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/meta/clusterset"
)

// Cluster administration through the data directory (`meta trust`,
// `meta peers`), safe while the node runs: the node reads the trusted_peers
// table every round and on every peer request.

// TrustAdd trusts the node with key at url (base URL, http or https): the
// node exchanges private data with it from its next round. The other node
// must trust this one too.
func (a *Admin) TrustAdd(ctx context.Context, url string, key []byte) error {
	url, err := clusterset.CleanURL(url)
	if err != nil {
		return err
	}
	if !identity.ValidPublic(key) {
		return errors.New("a node key is 32 bytes (base64url, as `meta id` prints it)")
	}
	_, err = a.db.ExecContext(ctx, `INSERT INTO trusted_peers (key, url, added_at) VALUES (?, ?, ?)
ON CONFLICT (key) DO UPDATE SET url = excluded.url`, key, url, a.now().UnixMilli())
	return err
}

// TrustRemove stops trusting a node added with TrustAdd; its sessions end
// at once. It reports whether it was trusted that way.
func (a *Admin) TrustRemove(ctx context.Context, key []byte) (bool, error) {
	res, err := a.db.ExecContext(ctx, `DELETE FROM trusted_peers WHERE key = ?`, key)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// TrustList lists the nodes trusted with TrustAdd and in the config file.
func (a *Admin) TrustList(ctx context.Context) ([]TrustedPeer, error) {
	var out []TrustedPeer
	for _, e := range a.cfg.TrustedPeers {
		if url, key, err := ParseTrustedPeer(e); err == nil {
			out = append(out, TrustedPeer{Key: key, URL: url, Source: "config"})
		}
	}
	rows, err := a.db.QueryContext(ctx, `SELECT key, url FROM trusted_peers ORDER BY added_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		p := TrustedPeer{Source: "trust"}
		if err := rows.Scan(&p.Key, &p.URL); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PeerStatus is what the running node last recorded about each peer.
func (a *Admin) PeerStatus(ctx context.Context) ([]PeerStatus, error) {
	return readPeerStatus(ctx, a.db)
}
