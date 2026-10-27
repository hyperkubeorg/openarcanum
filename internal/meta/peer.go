package meta

// Maintainer clusters (docs/protocol.md §7.5): a small set of nodes run by
// parties who trust each other replicate private data (DM mailboxes and
// their deletes, blob metadata, moderation decisions they follow,
// community tombstones) to each other, so any member can stand in for any
// other.
//
// Each node has an Ed25519 node key (DATA/node.key) and an X25519 Noise
// static key (DATA/node.noise). Nodes talk over GET /v1/peer: a WebSocket
// carrying the Noise XX session of internal/session with its own prologue
// ("openarcanum/meta-peer/v1"), each side proving its node key by signing its
// Noise static key. A node accepts a session only from a node key it
// trusts, and dials only node keys it trusts, so trust has to go both ways
// before anything private moves. The trusted keys are the members of the
// newest valid maintainer set for the node's cluster_operator_key
// (cluster.go), the config's trusted_peers and the `meta trust` table.
//
// Every node keeps a private log of changes (peer_log; its position mrev
// is separate from the public rev) and pulls every trusted peer's log from
// a cursor kept per peer key. The log includes what a node received from
// its peers, so a cluster that isn't fully connected still converges.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/flynn/noise"
	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/meta/clusterset"
	"github.com/hyperkubeorg/openarcanum/internal/ops"
	"github.com/hyperkubeorg/openarcanum/internal/session"
	"github.com/hyperkubeorg/openarcanum/internal/version"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// PeerPrologue is the Noise prologue of node-to-node sessions, so a peer
// session can never be taken for a community session.
const PeerPrologue = "openarcanum/meta-peer/v1"

const (
	// peerEpochKey guards the peer log (mrev, PeerBatch.epoch).
	peerEpochKey = "peer_epoch"
	// peerBatchMax and peerBatchBytes bound one PeerBatch.
	peerBatchMax   = 1000
	peerBatchBytes = 8 << 20
	// peerPullsPerRound bounds back-to-back pulls from one peer per round.
	peerPullsPerRound = 64
	// peerRequestTimeout bounds one request on a peer session.
	peerRequestTimeout = 30 * time.Second
	// peerHandshakeTimeout bounds an inbound handshake, which runs before
	// the caller's node key is known; peerHandshakesPerNet and
	// peerHandshakesAtOnce bound how many run at once per client network
	// and in all (trusted peers are few; anyone can start one).
	peerHandshakeTimeout = 10 * time.Second
	peerHandshakesPerNet = 4
	peerHandshakesAtOnce = 64
	// peerBackoffMax caps the wait after repeated failures.
	peerBackoffMax = 5 * time.Minute
)

// peerSchema adds the node-to-node tables: operator-added trusted peers,
// the private replication log, and each peer's cursor and status.
const peerSchema = `
CREATE TABLE trusted_peers (
	key BLOB PRIMARY KEY,
	url TEXT NOT NULL,
	added_at INTEGER NOT NULL
);
CREATE TABLE peer_log (
	mrev INTEGER PRIMARY KEY AUTOINCREMENT,
	kind TEXT NOT NULL,
	key BLOB NOT NULL,
	at INTEGER NOT NULL
);
CREATE INDEX peer_log_at ON peer_log (at);
CREATE TABLE peer_cursors (
	peer_key BLOB PRIMARY KEY,
	url TEXT NOT NULL DEFAULT '',
	epoch INTEGER NOT NULL DEFAULT 0,
	cursor INTEGER NOT NULL DEFAULT 0,
	head INTEGER NOT NULL DEFAULT 0,
	last_ok INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '',
	throttled_until INTEGER NOT NULL DEFAULT 0
);
`

// ---------------------------------------------------------------- node identity

// nodeIdentity is the node's key pair for peer sessions.
type nodeIdentity struct {
	key   *identity.Key
	noise noise.DHKey
	sig   []byte // SignStatic(key, noise.Public)
}

// loadNodeIdentity reads DATA/node.key and DATA/node.noise, creating them
// (mode 0600) on a new node. Backups never include them, so a node restored
// or seeded from another node's backup gets keys of its own.
func loadNodeIdentity(dataDir string) (*nodeIdentity, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	return loadKeys(dataDir)
}

// loadKeysFrom reads node.key and node.noise from keyDir (node_key_dir:
// keys made beforehand, such as a Kubernetes Secret), never creating them.
func loadKeysFrom(keyDir string) (*nodeIdentity, error) {
	for _, f := range []string{"node.key", "node.noise"} {
		if _, err := os.Stat(filepath.Join(keyDir, f)); err != nil {
			return nil, fmt.Errorf("node_key_dir: %w (make them with `meta id -data DIR` and copy both files)", err)
		}
	}
	return loadKeys(keyDir)
}

func loadKeys(dir string) (*nodeIdentity, error) {
	k, err := identity.LoadOrCreate(filepath.Join(dir, "node.key"))
	if err != nil {
		return nil, err
	}
	st, err := loadOrCreateNoise(filepath.Join(dir, "node.noise"))
	if err != nil {
		return nil, err
	}
	return &nodeIdentity{key: k, noise: st, sig: session.SignStatic(k, st.Public)}, nil
}

func loadOrCreateNoise(path string) (noise.DHKey, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		priv, derr := hex.DecodeString(strings.TrimSpace(string(b)))
		if derr != nil || len(priv) != 32 {
			return noise.DHKey{}, fmt.Errorf("%s: not a Noise static key", path)
		}
		return session.StaticFromPrivate(priv)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return noise.DHKey{}, err
	}
	st, err := session.GenerateStatic()
	if err != nil {
		return noise.DHKey{}, err
	}
	if err := identity.WriteFileAtomic(path, []byte(hex.EncodeToString(st.Private)+"\n"), 0o600); err != nil {
		return noise.DHKey{}, err
	}
	return st, nil
}

// NodeKeys returns the public node key and Noise static key of the node in
// dataDir, creating them on a new node (`meta id`).
func NodeKeys(dataDir string) (nodeKey, noiseKey []byte, err error) {
	id, err := loadNodeIdentity(dataDir)
	if err != nil {
		return nil, nil, err
	}
	return id.key.Public, id.noise.Public, nil
}

// NodeKey is this node's public node key.
func (s *Server) NodeKey() []byte { return s.id.key.Public }

func (s *Server) getNode(w http.ResponseWriter, r *http.Request) {
	writeProto(w, s.nodeInfo(r.Context()))
}

// ---------------------------------------------------------------- trust

// TrustedPeer is a node this node exchanges private data with.
type TrustedPeer struct {
	Key []byte
	URL string
	// Source: "config" (trusted_peers), "trust" (`meta trust add`) or "set"
	// (the cluster's maintainer set).
	Source string
}

// ParseTrustedPeer reads "URL#KEY" (KEY: the node key in base64url).
func ParseTrustedPeer(s string) (string, []byte, error) {
	u, k, ok := strings.Cut(strings.TrimSpace(s), "#")
	if !ok {
		return "", nil, fmt.Errorf("trusted peer %q must be URL#NODEKEY", s)
	}
	url, err := clusterset.CleanURL(u)
	if err != nil {
		return "", nil, err
	}
	key, err := identity.Decode(k)
	if err != nil || !identity.ValidPublic(key) {
		return "", nil, fmt.Errorf("trusted peer %q: the node key isn't a base64url Ed25519 key", s)
	}
	return url, key, nil
}

// trustedPeers is every node key this node trusts now (never its own),
// with the URL to reach it. A key named twice keeps its first URL: the
// config's trusted_peers first, then `meta trust`, then the maintainer set,
// so an operator can reach a member at an internal address while clients
// use the one in the set.
func (s *Server) trustedPeers(ctx context.Context) map[string]TrustedPeer {
	out := map[string]TrustedPeer{}
	add := func(p TrustedPeer) {
		if bytes.Equal(p.Key, s.id.key.Public) {
			return
		}
		if _, ok := out[string(p.Key)]; !ok {
			out[string(p.Key)] = p
		}
	}
	for _, e := range s.cfg.TrustedPeers {
		if url, key, err := ParseTrustedPeer(e); err == nil {
			add(TrustedPeer{Key: key, URL: url, Source: "config"})
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT key, url FROM trusted_peers ORDER BY added_at`)
	if err != nil {
		s.log.Error("trusted peers", "err", err)
	} else {
		for rows.Next() {
			var p TrustedPeer
			if rows.Scan(&p.Key, &p.URL) == nil {
				p.Source = "trust"
				add(p)
			}
		}
		rows.Close()
	}
	for _, p := range s.setPeers(ctx) {
		add(p)
	}
	return out
}

func (s *Server) isTrusted(ctx context.Context, key []byte) bool {
	_, ok := s.trustedPeers(ctx)[string(key)]
	return ok
}

// errUntrusted refuses a peer session from a key this node doesn't trust.
var errUntrusted = errors.New("this node doesn't trust your node key")

// ---------------------------------------------------------------- transport

// wsTransport carries a session's messages as binary WebSocket messages.
type wsTransport struct {
	ws  *websocket.Conn
	ctx context.Context
}

func (t *wsTransport) Send(b []byte) error {
	ctx, cancel := context.WithTimeout(t.ctx, peerRequestTimeout)
	defer cancel()
	return t.ws.Write(ctx, websocket.MessageBinary, b)
}

func (t *wsTransport) Recv(ctx context.Context) ([]byte, error) {
	typ, b, err := t.ws.Read(ctx)
	if err != nil {
		return nil, err
	}
	if typ != websocket.MessageBinary {
		return nil, errors.New("peer sent a text message")
	}
	return b, nil
}

func (t *wsTransport) Close(reason string) {
	_ = t.ws.Close(websocket.StatusNormalClosure, truncate(reason, 100))
}

// wsReadLimit fits one Noise transport message.
const wsReadLimit = 1 << 17

// ---------------------------------------------------------------- the responder side

// peerAccept serves GET /v1/peer: the WebSocket upgrade, the handshake
// (refused unless the initiator's node key is trusted), then requests
// until the session ends. The session runs on after the handler returns,
// until either side closes it or the server does.
func (s *Server) peerAccept(w http.ResponseWriter, r *http.Request) {
	from := ops.NetworkKey(ops.RemoteIP(r))
	if !s.peers.beginHandshake(from) {
		http.Error(w, "too many peer handshakes under way", http.StatusTooManyRequests)
		return
	}
	defer s.peers.endHandshake(from)
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	ws.SetReadLimit(wsReadLimit)
	ctx := s.peers.ctx
	t := &wsTransport{ws: ws, ctx: ctx}
	hctx, cancel := context.WithTimeout(ctx, peerHandshakeTimeout)
	defer cancel()
	var peerKey []byte
	sess, err := session.Accept(hctx, t, session.ServerConfig{
		Static: s.id.noise, Identity: s.id.key, StaticSig: s.id.sig, Prologue: PeerPrologue,
		VerifyClient: func(ci *wire.ClientIdentity) error {
			if !s.isTrusted(hctx, ci.IdentityKey) {
				return errUntrusted
			}
			peerKey = ci.IdentityKey
			return nil
		},
	})
	if err != nil {
		if errors.Is(err, errUntrusted) {
			s.log.Warn("peer session refused: node key not trusted")
			_ = ws.Close(websocket.StatusPolicyViolation, errUntrusted.Error())
		} else {
			_ = ws.Close(websocket.StatusProtocolError, "handshake failed")
		}
		return
	}
	go s.servePeer(ctx, sess, peerKey)
}

// inbound is an accepted session: pokes for it are coalesced into one
// pending notice.
type inbound struct {
	key  []byte
	sess *session.Session
	poke chan struct{}
}

func (s *Server) servePeer(ctx context.Context, sess *session.Session, key []byte) {
	in := &inbound{key: key, sess: sess, poke: make(chan struct{}, 1)}
	s.peers.mu.Lock()
	s.peers.inbound[in] = true
	s.peers.mu.Unlock()
	sctx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		s.peers.mu.Lock()
		delete(s.peers.inbound, in)
		s.peers.mu.Unlock()
		sess.Close("session ended")
	}()
	go func() {
		for {
			select {
			case <-sctx.Done():
				return
			case <-in.poke:
				b, _ := proto.Marshal(&wire.PeerMessage{Body: &wire.PeerMessage_Poke{Poke: &wire.PeerPoke{}}})
				if sess.Send(b) != nil {
					cancel()
					return
				}
			}
		}
	}()
	for {
		b, err := sess.Recv(sctx)
		if err != nil {
			return
		}
		// Trust is checked again on every request: a peer removed from
		// the set or `meta trust` loses its session at once.
		if !s.isTrusted(sctx, key) {
			s.log.Warn("peer session closed: node key no longer trusted", "peer", identity.Fingerprint(key))
			sess.Close(errUntrusted.Error())
			return
		}
		var m wire.PeerMessage
		if err := proto.Unmarshal(b, &m); err != nil {
			return
		}
		reply := s.handlePeer(sctx, key, &m)
		if reply == nil || m.Id == 0 {
			continue
		}
		reply.Id = m.Id
		out, err := proto.Marshal(reply)
		if err != nil || sess.Send(out) != nil {
			return
		}
	}
}

func peerErr(msg string) *wire.PeerMessage {
	return &wire.PeerMessage{Body: &wire.PeerMessage_Error{Error: &wire.PeerError{Message: msg}}}
}

// handlePeer answers one request from a trusted peer.
func (s *Server) handlePeer(ctx context.Context, peer []byte, m *wire.PeerMessage) *wire.PeerMessage {
	switch b := m.Body.(type) {
	case *wire.PeerMessage_Pull:
		batch, err := s.peerBatch(ctx, b.Pull)
		if err != nil {
			s.log.Error("peer log read failed", "err", err)
			return peerErr("internal error")
		}
		return &wire.PeerMessage{Body: &wire.PeerMessage_Batch{Batch: batch}}
	case *wire.PeerMessage_Poke:
		s.peers.wakeUp()
		return nil
	}
	if r := s.handlePeerMore(ctx, peer, m); r != nil {
		return r
	}
	return peerErr("unknown request")
}

// peerBatch is the peer log after a cursor (from 0 if the cursor comes
// from another epoch: this node was restored since).
func (s *Server) peerBatch(ctx context.Context, pull *wire.PeerPull) (*wire.PeerBatch, error) {
	cursor := int64(pull.Cursor)
	if pull.Epoch != s.peerEpoch {
		cursor = 0
	}
	out := &wire.PeerBatch{Epoch: s.peerEpoch, Cursor: uint64(cursor)}
	if err := s.db.QueryRowContext(ctx, `SELECT coalesce(max(mrev), 0) FROM peer_log`).Scan(&out.Head); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT mrev, kind, key FROM peer_log WHERE mrev > ? ORDER BY mrev LIMIT ?`, cursor, peerBatchMax+1)
	if err != nil {
		return nil, err
	}
	type logRow struct {
		mrev int64
		kind string
		key  []byte
	}
	var log []logRow
	for rows.Next() {
		var r logRow
		if err := rows.Scan(&r.mrev, &r.kind, &r.key); err != nil {
			rows.Close()
			return nil, err
		}
		log = append(log, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	size := 0
	for i, r := range log {
		if i == peerBatchMax || size > peerBatchBytes {
			out.More = true
			break
		}
		e, err := s.peerEntry(ctx, r.kind, r.key)
		if err != nil {
			return nil, err
		}
		out.Cursor = uint64(r.mrev)
		if e == nil {
			continue // since deleted or expired; a later entry says so
		}
		e.Mrev = uint64(r.mrev)
		size += proto.Size(e)
		out.Entries = append(out.Entries, e)
	}
	return out, nil
}

// beginWrite starts a transaction that takes the database's write lock at
// once, so what it reads can't be overtaken by another connection's commit
// before it writes (which SQLite refuses with SQLITE_BUSY_SNAPSHOT instead
// of waiting): the empty DELETE makes it a write transaction from the
// start, waiting for the lock within the busy timeout.
func beginWrite(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM counters WHERE 0`); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

// logPeer appends to the peer log (callers hold s.mu or run in tx) and
// returns nothing to the caller but an error; at is when the entry may be
// pruned (unix ms).
func logPeer(ctx context.Context, x execer, kind string, key []byte, at int64) error {
	_, err := x.ExecContext(ctx, `INSERT INTO peer_log (kind, key, at) VALUES (?, ?, ?)`, kind, key, at)
	return err
}

// ---------------------------------------------------------------- the initiator side

// peerLink is this node's outbound session to one trusted peer, with its
// health.
type peerLink struct {
	key []byte
	url string

	mu       sync.Mutex
	sess     *session.Session
	pending  map[uint64]chan *wire.PeerMessage
	nextID   uint64
	failures int
	nextTry  time.Time
	lastErr  string
}

// peerState is the node's peer sessions.
type peerState struct {
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	links   map[string]*peerLink
	inbound map[*inbound]bool
	wake    chan struct{}
	limits  map[string]*peerLimits
	// handshakes counts inbound handshakes under way per client network.
	handshakes map[string]int
}

func newPeerState() *peerState {
	ctx, cancel := context.WithCancel(context.Background())
	return &peerState{ctx: ctx, cancel: cancel, links: map[string]*peerLink{}, inbound: map[*inbound]bool{},
		wake: make(chan struct{}, 1), limits: map[string]*peerLimits{}, handshakes: map[string]int{}}
}

// beginHandshake takes a handshake slot for client network from, if one is
// free for it (peerHandshakesPerNet, peerHandshakesAtOnce).
func (p *peerState) beginHandshake(from string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	all := 0
	for _, n := range p.handshakes {
		all += n
	}
	if all >= peerHandshakesAtOnce || p.handshakes[from] >= peerHandshakesPerNet {
		return false
	}
	p.handshakes[from]++
	return true
}

func (p *peerState) endHandshake(from string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.handshakes[from]--; p.handshakes[from] <= 0 {
		delete(p.handshakes, from)
	}
}

func (p *peerState) wakeUp() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// close ends every peer session.
func (p *peerState) close() {
	p.cancel()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, l := range p.links {
		l.drop("node stopping")
	}
	for in := range p.inbound {
		in.sess.Close("node stopping")
	}
}

// notifyPeers tells every peer pulling from this node that its log grew.
func (s *Server) notifyPeers() {
	s.peers.mu.Lock()
	defer s.peers.mu.Unlock()
	for in := range s.peers.inbound {
		select {
		case in.poke <- struct{}{}:
		default:
		}
	}
}

// DropPeerSessions closes every peer session in both directions (tests: a
// network partition; the next round dials again).
func (s *Server) DropPeerSessions() {
	s.peers.mu.Lock()
	defer s.peers.mu.Unlock()
	for _, l := range s.peers.links {
		l.drop("dropped")
	}
	for in := range s.peers.inbound {
		in.sess.Close("dropped")
	}
}

func (l *peerLink) drop(reason string) {
	l.mu.Lock()
	sess := l.sess
	l.sess = nil
	for id, ch := range l.pending {
		close(ch)
		delete(l.pending, id)
	}
	l.mu.Unlock()
	if sess != nil {
		sess.Close(reason)
	}
}

// link returns the link for a trusted peer, replacing one whose URL changed.
func (p *peerState) link(tp TrustedPeer) *peerLink {
	p.mu.Lock()
	defer p.mu.Unlock()
	l := p.links[string(tp.Key)]
	if l != nil && l.url != tp.URL {
		go l.drop("peer moved")
		l = nil
	}
	if l == nil {
		l = &peerLink{key: tp.Key, url: tp.URL, pending: map[uint64]chan *wire.PeerMessage{}}
		p.links[string(tp.Key)] = l
	}
	return l
}

// prune drops the links and sessions of peers no longer trusted.
func (p *peerState) prune(trusted map[string]TrustedPeer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, l := range p.links {
		if _, ok := trusted[k]; !ok {
			go l.drop("no longer trusted")
			delete(p.links, k)
		}
	}
	for in := range p.inbound {
		if _, ok := trusted[string(in.key)]; !ok {
			in.sess.Close("no longer trusted")
		}
	}
}

// session returns the link's session, dialing the peer if there is none.
func (s *Server) linkSession(ctx context.Context, l *peerLink) (*session.Session, error) {
	l.mu.Lock()
	sess := l.sess
	l.mu.Unlock()
	if sess != nil {
		return sess, nil
	}
	dctx, cancel := context.WithTimeout(ctx, peerRequestTimeout)
	defer cancel()
	h := http.Header{}
	h.Set(version.Header, version.Format(version.Supported()))
	ws, _, err := websocket.Dial(dctx, l.url+"/v1/peer", &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		return nil, fmt.Errorf("connecting: %w", err)
	}
	ws.SetReadLimit(wsReadLimit)
	t := &wsTransport{ws: ws, ctx: s.peers.ctx}
	sess, err = session.Dial(dctx, t, session.ClientConfig{
		Static: s.id.noise, IdentityKey: s.id.key.Public, StaticSig: s.id.sig, Prologue: PeerPrologue,
		VerifyServer: func(si *wire.ServerIdentity) error {
			if !bytes.Equal(si.ServerKey, l.key) {
				return errors.New("the node at this URL has another node key than the one trusted")
			}
			return nil
		},
	})
	if err != nil {
		_ = ws.Close(websocket.StatusProtocolError, "handshake failed")
		return nil, fmt.Errorf("handshake: %w", err)
	}
	l.mu.Lock()
	l.sess = sess
	l.mu.Unlock()
	go s.readLink(l, sess)
	return sess, nil
}

// readLink delivers replies to their requests and turns pokes into a pull,
// until the session ends.
func (s *Server) readLink(l *peerLink, sess *session.Session) {
	for {
		b, err := sess.Recv(s.peers.ctx)
		if err != nil {
			l.mu.Lock()
			if l.sess == sess {
				l.sess = nil
				l.lastErr = closeReason(err)
				for id, ch := range l.pending {
					close(ch)
					delete(l.pending, id)
				}
			}
			l.mu.Unlock()
			sess.Close("read failed")
			return
		}
		var m wire.PeerMessage
		if proto.Unmarshal(b, &m) != nil {
			continue
		}
		if m.Id == 0 {
			if m.GetPoke() != nil {
				s.peers.wakeUp()
			}
			continue
		}
		l.mu.Lock()
		ch := l.pending[m.Id]
		delete(l.pending, m.Id)
		l.mu.Unlock()
		if ch != nil {
			ch <- &m
		}
	}
}

// closeReason is the peer's reason for closing, if it gave one.
func closeReason(err error) string {
	var ce websocket.CloseError
	if errors.As(err, &ce) && ce.Reason != "" {
		return "the peer closed the session: " + ce.Reason
	}
	return err.Error()
}

// request sends m on the link's session and waits for the reply.
func (s *Server) request(ctx context.Context, l *peerLink, m *wire.PeerMessage) (*wire.PeerMessage, error) {
	sess, err := s.linkSession(ctx, l)
	if err != nil {
		return nil, err
	}
	ch := make(chan *wire.PeerMessage, 1)
	l.mu.Lock()
	l.nextID++
	m.Id = l.nextID
	l.pending[m.Id] = ch
	l.mu.Unlock()
	b, err := proto.Marshal(m)
	if err != nil {
		l.drop("send failed")
		return nil, err
	}
	if err := sess.Send(b); err != nil {
		// A peer that refuses the session closes it right after the
		// handshake, and the send can lose that race to the close frame:
		// wait for readLink to close ch and report the peer's reason.
		why := ""
		select {
		case _, ok := <-ch:
			if !ok {
				l.mu.Lock()
				why = l.lastErr
				l.mu.Unlock()
			}
		case <-time.After(time.Second):
		}
		l.drop("send failed")
		if why != "" {
			return nil, errors.New(why)
		}
		return nil, err
	}
	t := time.NewTimer(peerRequestTimeout)
	defer t.Stop()
	select {
	case r, ok := <-ch:
		if !ok {
			l.mu.Lock()
			why := l.lastErr
			l.mu.Unlock()
			if why == "" {
				why = "session closed"
			}
			return nil, errors.New(why)
		}
		if e := r.GetError(); e != nil {
			return nil, fmt.Errorf("the peer refused: %s", e.Message)
		}
		return r, nil
	case <-t.C:
		l.drop("request timed out")
		return nil, errors.New("the peer didn't answer in time")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ---------------------------------------------------------------- pulling

// PeerSyncOnce pulls every trusted peer's log now, ignoring backoff
// (tests and `meta peers`; Run pulls on its own).
func (s *Server) PeerSyncOnce(ctx context.Context) { s.peerRound(ctx, false) }

func (s *Server) peerRound(ctx context.Context, backoff bool) {
	trusted := s.trustedPeers(ctx)
	s.peers.prune(trusted)
	now := s.now()
	var wg sync.WaitGroup
	for _, tp := range trusted {
		if tp.URL == "" {
			continue
		}
		l := s.peers.link(tp)
		l.mu.Lock()
		due := !backoff || !now.Before(l.nextTry)
		l.mu.Unlock()
		if !due || s.throttled(tp.Key, now) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.pullPeer(ctx, l)
			s.linkResult(ctx, l, err)
		}()
	}
	wg.Wait()
}

// linkResult records how a pull went: the backoff and the status `meta
// peers status` shows.
func (s *Server) linkResult(ctx context.Context, l *peerLink, err error) {
	now := s.now()
	l.mu.Lock()
	msg := ""
	if err != nil {
		l.failures++
		wait := min(s.cfg.TrustedInterval.Duration<<min(l.failures, 10), peerBackoffMax)
		l.nextTry = now.Add(wait)
		msg = err.Error()
		if l.lastErr != msg {
			s.log.Warn("peer pull failed", "peer", l.url, "err", msg, "retry_in", wait.String())
		}
		l.lastErr = msg
	} else {
		if l.failures > 0 {
			s.log.Info("peer reachable again", "peer", l.url)
		}
		l.failures, l.nextTry, l.lastErr = 0, time.Time{}, ""
	}
	l.mu.Unlock()
	q := `INSERT INTO peer_cursors (peer_key, url, last_error) VALUES (?, ?, ?)
ON CONFLICT (peer_key) DO UPDATE SET url = excluded.url, last_error = excluded.last_error`
	args := []any{l.key, l.url, msg}
	if err == nil {
		q = `INSERT INTO peer_cursors (peer_key, url, last_error, last_ok) VALUES (?, ?, '', ?)
ON CONFLICT (peer_key) DO UPDATE SET url = excluded.url, last_error = '', last_ok = excluded.last_ok`
		args = []any{l.key, l.url, now.UnixMilli()}
	}
	if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
		s.log.Error("peer status", "err", err)
	}
}

// pullPeer pulls a peer's log until it has nothing more (or the round's
// bound), applying each batch and saving the cursor after it.
func (s *Server) pullPeer(ctx context.Context, l *peerLink) error {
	for range peerPullsPerRound {
		var cursor, epoch int64
		err := s.db.QueryRowContext(ctx, `SELECT cursor, epoch FROM peer_cursors WHERE peer_key = ?`, l.key).Scan(&cursor, &epoch)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		r, err := s.request(ctx, l, &wire.PeerMessage{Body: &wire.PeerMessage_Pull{Pull: &wire.PeerPull{Cursor: uint64(cursor), Epoch: uint64(epoch)}}})
		if err != nil {
			return err
		}
		b := r.GetBatch()
		if b == nil {
			return errors.New("the peer answered a pull with something else")
		}
		if b.Epoch != uint64(epoch) && epoch != 0 {
			s.log.Info("peer was restored from a backup; pulling its log from the start", "peer", l.url)
		}
		next, stop, err := s.applyPeerBatch(ctx, l, b)
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO peer_cursors (peer_key, url, epoch, cursor, head) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (peer_key) DO UPDATE SET url = excluded.url, epoch = excluded.epoch, cursor = excluded.cursor, head = excluded.head`,
			l.key, l.url, int64(b.Epoch), int64(next), int64(b.Head)); err != nil {
			return err
		}
		if stop || !b.More {
			return nil
		}
	}
	return nil
}

// runPeers pulls every trusted peer each trusted_interval, and at once
// when a peer says its log grew, until ctx ends. Blob content is copied
// on its own clock (runBlobCopies): a member slow to serve chunks holds
// up copies, never the pulls.
func (s *Server) runPeers(ctx context.Context) {
	go s.runBlobCopies(ctx)
	t := time.NewTicker(s.cfg.TrustedInterval.Duration)
	defer t.Stop()
	for {
		s.peerRound(ctx, true)
		select {
		case <-ctx.Done():
			return
		case <-s.peers.ctx.Done():
			return
		case <-t.C:
		case <-s.peers.wake:
		}
	}
}

// ---------------------------------------------------------------- status

// PeerStatus is one peer as `meta peers status` shows it.
type PeerStatus struct {
	Key            string
	URL            string
	Cursor, Head   uint64
	Epoch          uint64
	LastOK         time.Time
	LastError      string
	ThrottledUntil time.Time
}

// Lag is how many log entries this node is behind the peer.
func (p PeerStatus) Lag() uint64 {
	if p.Head > p.Cursor {
		return p.Head - p.Cursor
	}
	return 0
}

func readPeerStatus(ctx context.Context, q queryer) ([]PeerStatus, error) {
	rows, err := q.QueryContext(ctx, `SELECT peer_key, url, cursor, head, epoch, last_ok, last_error, throttled_until FROM peer_cursors`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PeerStatus
	for rows.Next() {
		var p PeerStatus
		var key []byte
		var ok, thr int64
		if err := rows.Scan(&key, &p.URL, &p.Cursor, &p.Head, &p.Epoch, &ok, &p.LastError, &thr); err != nil {
			return nil, err
		}
		p.Key = identity.Encode(key)
		if ok > 0 {
			p.LastOK = time.UnixMilli(ok)
		}
		if thr > 0 {
			p.ThrottledUntil = time.UnixMilli(thr)
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	return out, rows.Err()
}

// PeerStatuses is what this node knows about each peer it pulled from.
func (s *Server) PeerStatuses(ctx context.Context) ([]PeerStatus, error) {
	return readPeerStatus(ctx, s.db)
}
