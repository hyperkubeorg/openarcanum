// Package meta is the maintainer node: the community directory with search,
// route lookup for private communities, the relay registry, DM mailboxes,
// the DM file blob store (blobs.go) and device-link nameplates
// (docs/protocol.md §7, §10.1 and §11).
//
// It holds no community content. Listings and routes are signed by community
// server keys (with owner certs), so a maintainer can accept or drop them but
// never alter them; clients verify signatures themselves.
package meta

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/ops"
	"github.com/hyperkubeorg/openarcanum/internal/relayurl"
	"github.com/hyperkubeorg/openarcanum/internal/store"
	"github.com/hyperkubeorg/openarcanum/internal/version"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

var migrations = append(sqlSteps(`
CREATE TABLE listings (
	id INTEGER PRIMARY KEY,
	community BLOB NOT NULL UNIQUE,
	signed BLOB NOT NULL,
	seq INTEGER NOT NULL,
	withdrawn INTEGER NOT NULL,
	rev INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE VIRTUAL TABLE listings_fts USING fts5(name, description, tags, tokenize = 'unicode61 remove_diacritics 2');
CREATE TABLE listing_tags (listing_id INTEGER NOT NULL, tag TEXT NOT NULL, PRIMARY KEY (listing_id, tag));
CREATE TABLE routes (
	route_id BLOB PRIMARY KEY,
	owner BLOB NOT NULL,
	signed BLOB NOT NULL,
	seq INTEGER NOT NULL,
	rev INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE TABLE relays (
	url TEXT PRIMARY KEY,
	relay_key BLOB NOT NULL,
	signed BLOB NOT NULL,
	at INTEGER NOT NULL,
	region TEXT NOT NULL,
	capacity INTEGER NOT NULL,
	connections INTEGER NOT NULL,
	rev INTEGER NOT NULL
);
CREATE TABLE mailbox (
	id INTEGER PRIMARY KEY,
	inbox BLOB NOT NULL,
	blob BLOB NOT NULL,
	size INTEGER NOT NULL,
	received_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL
);
CREATE INDEX mailbox_inbox ON mailbox (inbox, id);
CREATE INDEX mailbox_expiry ON mailbox (expires_at);
CREATE TABLE nameplates (
	number INTEGER PRIMARY KEY,
	topic BLOB NOT NULL,
	relay TEXT NOT NULL,
	expires_at INTEGER NOT NULL,
	claimed INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE counters (k TEXT PRIMARY KEY, v INTEGER NOT NULL);
`, blobSchema, blobSchemaUploads, mailboxIDsNeverReused, searchIndexes, delistedSchema,
), append([]store.Migration{{Go: canonicalRelayURLs}}, append(sqlSteps(communityStatusSchema, peerSchema, maintainerSetSchema), store.Migration{Go: mailboxGlobalIDs}, store.Migration{SQL: blobReplSchema}, store.Migration{SQL: decisionsSchema},
	store.Migration{SQL: decisionsSignedSchema})...)...)

func sqlSteps(sql ...string) []store.Migration {
	out := make([]store.Migration, len(sql))
	for i, q := range sql {
		out[i] = store.Migration{SQL: q}
	}
	return out
}

// Indexes for search's tag filter and for browsing the newest listings,
// which otherwise scan and sort every listing (docs/load-tests.md).
const searchIndexes = `
CREATE INDEX listing_tags_tag ON listing_tags (tag, listing_id);
CREATE INDEX listings_recent ON listings (withdrawn, updated_at);
`

// Mailbox ids are a reader's position (MailboxFetch.after_id): a deleted
// item's id must never come back, or a reader would skip the new item.
// Without AUTOINCREMENT, SQLite reuses the highest id once it's deleted.
const mailboxIDsNeverReused = `
CREATE TABLE mailbox_new (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	inbox BLOB NOT NULL,
	blob BLOB NOT NULL,
	size INTEGER NOT NULL,
	received_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL
);
INSERT INTO mailbox_new (id, inbox, blob, size, received_at, expires_at)
	SELECT id, inbox, blob, size, received_at, expires_at FROM mailbox;
DROP TABLE mailbox;
ALTER TABLE mailbox_new RENAME TO mailbox;
CREATE INDEX mailbox_inbox ON mailbox (inbox, id);
CREATE INDEX mailbox_expiry ON mailbox (expires_at);
`

// maxBody bounds any request body (a mailbox drop of mailbox_max_item is
// the largest).
const maxBody = 27 << 20

// Size bounds of what the node stores and passes on, as re-encoded with
// only the fields it knows (unknown fields are dropped). Each is far above
// what an honest server or relay sends: a listing is at most its 256 KiB
// icon, an owner chain of MaxOwnerChanges changes and a few KiB of text; a
// route the chain and 16 relay URLs. Without bounds one post could store
// megabytes that every search, snapshot and replication batch repeats.
const (
	MaxListingBytes   = 640 << 10
	MaxRouteBytes     = 256 << 10
	MaxHeartbeatBytes = 2 << 10
	// MaxReplicationBatch bounds the entries of one replication batch
	// (GET /v1/replicate) in bytes, besides the 1000 entry bound; a batch
	// holds at least one entry.
	MaxReplicationBatch = 4 << 20
	// smallBody bounds requests that carry a few fields.
	smallBody            = 4 << 10
	mailboxDeleteMaxBody = 64 << 10
)

// absentTombstonesPerHour bounds the tombstones one network may leave per
// hour for mailbox items this node doesn't hold (deletes of items held here
// are not limited).
const absentTombstonesPerHour = 500

// maxRelayCapacity bounds the capacity a relay heartbeat may claim.
const maxRelayCapacity = 1_000_000

// claimFailuresPerMinute bounds failed link code claims for the whole
// node: the per-network limit alone lets a sweep from many networks guess
// at live codes at scale. Claims that succeed don't count, so linking is
// unaffected until a sweep is under way.
const claimFailuresPerMinute = 200

// Bounds on listing strings (besides name and description).
const (
	maxTagLen    = 64
	maxShortText = 64
	maxJoinKey   = 512
)

// unmarshalKnown parses b keeping only the fields this build knows, so
// what is stored and passed on is re-encoded from those alone.
func unmarshalKnown(b []byte, m proto.Message) error {
	return proto.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(b, m)
}

// encodeKnown re-encodes a record parsed with unmarshalKnown and checks it
// against its bound.
func encodeKnown(m proto.Message, limit int, what string) ([]byte, error) {
	b, err := proto.Marshal(m)
	if err != nil {
		return nil, bad("malformed " + what)
	}
	if len(b) > limit {
		return nil, &httpError{http.StatusRequestEntityTooLarge, what + " is too large"}
	}
	return b, nil
}

// Server is a maintainer node.
type Server struct {
	cfg  Config
	log  *slog.Logger
	db   *sql.DB
	http *http.Client
	now  func() time.Time

	// mu serializes writes so revisions are assigned in order.
	mu sync.Mutex

	delisted map[string]bool
	// delistedRelays holds the hosts of the config's delisted_relays.
	delistedRelays map[string]bool
	// relayProofs proves relay URLs serve their heartbeats' keys
	// (relayproof.go); relayProofHook replaces it in tests.
	relayProofs    *relayProofs
	relayProofHook func(ctx context.Context, url string, key []byte) error

	// Per-client limits (limits.go): by client address (per network,
	// capped per IPv6 /48), by community or by inbox.
	writes  *netRateLimit
	reads   *netRateLimit
	listing *rateLimit
	drops   *rateLimit
	plates  *netRateLimit
	claims  *netRateLimit
	// claimFailures bounds failed link code claims node-wide
	// (nameplateClaim).
	claimFailures *rateLimit
	// absentTombstones bounds tombstones for mail not held here, per
	// network (mailboxDelete).
	absentTombstones *netRateLimit

	blobs       *blobStore
	blobCreates *rateLimit
	blobQuota   *keyed[[]quotaUse]
	// mailboxBudget is mailbox_bytes_per_ip, per network.
	mailboxBudget *netByteBudget
	// blobBudget is peer_blob_bytes_per_hour, for copying blob content
	// from cluster peers in the background.
	blobBudget *byteBudget
	// mailboxBytes is what all mailboxes hold (mailbox_store_max); only
	// this process changes mailbox rows. Guarded by mu.
	mailboxBytes uint64
	// mailboxEpoch changes on every restore (MailboxItems.epoch).
	mailboxEpoch uint64
	// replEpoch changes on every restore too (ReplicationBatch.epoch).
	replEpoch uint64
	// peerEpoch guards the private peer log (PeerBatch.epoch).
	peerEpoch uint64

	// id is the node's key pair for peer sessions; peers its sessions with
	// the trusted nodes of its cluster (peer.go).
	id    *nodeIdentity
	peers *peerState
	// operator is cluster_operator_key (nil: not in a maintainer cluster).
	operator []byte
	// refusedVec counts replicated entries not stored (set by RegisterMetrics).
	refusedVec atomic.Pointer[ops.CounterVec]

	// stats caches table counts for the metrics endpoint (metrics.go).
	stats storeStats
	// Protocol versions this node speaks on its API (docs/protocol.md §7).
	versions []uint32
}

// Open opens (creating) the node's database.
func Open(ctx context.Context, cfg Config, log *slog.Logger) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	db, err := store.Open(cfg.DataDir + "/meta.db")
	if err != nil {
		return nil, err
	}
	if _, err := store.Apply(ctx, db, migrations); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s/meta.db: %w", cfg.DataDir, err)
	}
	s := &Server{
		cfg: cfg, log: log, db: db, http: &http.Client{Timeout: 15 * time.Second}, now: time.Now,
		delisted: map[string]bool{}, delistedRelays: map[string]bool{},
		relayProofs:      newRelayProofs(cfg.AllowPrivateRelays),
		writes:           newNetRateLimit(cfg.WritesPerMinute, time.Minute),
		reads:            newNetRateLimit(cfg.ReadsPerMinute, time.Minute),
		listing:          newRateLimit(cfg.ListingsPerHour, time.Hour),
		drops:            newRateLimit(cfg.MailboxDropsPerMin, time.Minute),
		plates:           newNetRateLimit(cfg.NameplatesPerMinute, time.Minute),
		claims:           newNetRateLimit(cfg.NameplatesPerMinute, time.Minute),
		claimFailures:    newRateLimit(claimFailuresPerMinute, time.Minute),
		absentTombstones: newNetRateLimit(absentTombstonesPerHour, time.Hour),
		blobCreates:      newRateLimit(cfg.BlobUploadsPerMinute, time.Minute),
		blobQuota:        newKeyed[[]quotaUse](BlobQuotaWindow, maxLimitKeys),
		mailboxBudget:    newNetByteBudget(cfg.MailboxBytesPerIP, MailboxBudgetWindow),
		blobBudget:       newByteBudget(cfg.PeerBlobBytesPerHour, time.Hour),
		versions:         version.Supported(),
	}
	s.http.Transport = version.Transport{}
	s.peers = newPeerState()
	if cfg.NodeKeyDir != "" {
		s.id, err = loadKeysFrom(cfg.NodeKeyDir)
	} else {
		s.id, err = loadNodeIdentity(cfg.DataDir)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	s.blobs, err = newBlobStore(db, cfg.DataDir, func() time.Time { return s.now() }, log)
	if err == nil {
		s.blobs.onStored = s.logBlobStored
		err = s.blobs.recover(ctx)
	}
	if err == nil {
		err = s.loadMailboxState(ctx)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	for _, d := range cfg.Delisted {
		b, err := identity.Decode(d)
		if err != nil || !identity.ValidPublic(b) {
			db.Close()
			return nil, fmt.Errorf("delisted entry %q is not a community id", d)
		}
		s.delisted[string(b)] = true
	}
	for _, r := range cfg.DelistedRelays {
		u, err := relayurl.Canonical(r)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("delisted_relays entry %q: %w", r, err)
		}
		s.delistedRelays[relayurl.Host(u)] = true
	}
	if cfg.ClusterOperatorKey != "" {
		s.operator, _ = identity.Decode(cfg.ClusterOperatorKey)
	}
	if err := s.publishSetFile(ctx); err != nil {
		db.Close()
		return nil, err
	}
	// A directory switched off holds no listings.
	if !s.directoryOn(ctx) {
		if err := deleteListings(ctx, s.db); err != nil {
			db.Close()
			return nil, err
		}
	}
	// Apply the delisting list to what's already stored.
	for k := range s.delisted {
		s.removeListing(ctx, []byte(k))
	}
	return s, nil
}

// SetVersions replaces the protocol versions the API speaks (tests prove a
// node one version ahead still serves current clients).
func (s *Server) SetVersions(vs []uint32) { s.versions = vs }

// SetClock replaces the clock (tests).
func (s *Server) SetClock(now func() time.Time) { s.now = now }

// Close ends the peer sessions and releases the database.
func (s *Server) Close() error {
	s.peers.close()
	return s.db.Close()
}

// Handler returns the HTTP API.
func (s *Server) Handler() http.Handler {
	return s.cors(s.versioned(s.routes()))
}

// versioned checks the protocol version (with the versions current at each
// request, which SetVersions can change) before mux serves.
func (s *Server) versioned(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		version.Middleware(s.versions, mux).ServeHTTP(w, r)
	})
}

// routes is the API's request router, without the version check.
func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Live(r.Context()); err != nil {
			http.Error(w, "unhealthy: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("POST /v1/listings", s.writeLimit(MaxListingBytes, s.postListing))
	mux.HandleFunc("GET /v1/listings/{id}", s.readLimit(s.getListing))
	mux.HandleFunc("GET /v1/search", s.readLimit(s.search))
	mux.HandleFunc("GET /v1/directory/snapshot", s.readLimit(s.snapshot))
	mux.HandleFunc("POST /v1/routes", s.writeLimit(MaxRouteBytes, s.postRoute))
	mux.HandleFunc("GET /v1/routes/{id}", s.readLimit(s.getRoute))
	mux.HandleFunc("POST /v1/relays/heartbeat", s.writeLimit(MaxHeartbeatBytes, s.postHeartbeat))
	mux.HandleFunc("GET /v1/relays", s.readLimit(s.getRelays))
	mux.HandleFunc("POST /v1/communities/heartbeat", s.writeLimit(MaxHeartbeatBytes, s.postCommunityHeartbeat))
	mux.HandleFunc("GET /v1/communities/{id}/status", s.readLimit(s.getCommunityStatus))
	mux.HandleFunc("POST /v1/mailbox/drop", s.writeLimit(int(min(s.cfg.MailboxMaxItem, maxBody))+smallBody, s.mailboxDrop))
	mux.HandleFunc("POST /v1/mailbox/fetch", s.writeLimit(smallBody, s.mailboxFetch))
	mux.HandleFunc("POST /v1/mailbox/delete", s.writeLimit(mailboxDeleteMaxBody, s.mailboxDelete))
	mux.HandleFunc("POST /v1/blobs/create", s.writeLimit(blobCreateMaxBody, s.blobCreate))
	mux.HandleFunc("POST /v1/blobs/chunk", s.writeLimit(blobChunkMaxBody, s.blobChunk))
	mux.HandleFunc("POST /v1/blobs/fetch", s.writeLimit(blobSmallMaxBody, s.blobFetch))
	mux.HandleFunc("POST /v1/blobs/ack", s.writeLimit(blobSmallMaxBody, s.blobAck))
	mux.HandleFunc("POST /v1/nameplates", s.writeLimit(smallBody, s.nameplateCreate))
	mux.HandleFunc("POST /v1/nameplates/claim", s.writeLimit(smallBody, s.nameplateClaim))
	mux.HandleFunc("GET /v1/replicate", s.readLimit(s.replicate))
	mux.HandleFunc("GET /v1/node", s.readLimit(s.getNode))
	mux.HandleFunc("GET /v1/maintainers", s.readLimit(s.getMaintainers))
	mux.HandleFunc("GET /v1/peer", s.peerAccept)
	return mux
}

// Run does background work (expiry, replication) until ctx ends.
func (s *Server) Run(ctx context.Context) {
	expire := time.NewTicker(s.cfg.expireEvery())
	defer expire.Stop()
	repl := time.NewTicker(s.cfg.ReplicationInterval.Duration)
	defer repl.Stop()
	go s.runPeers(ctx)
	s.ReplicateOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-expire.C:
			s.Expire(ctx)
		case <-repl.C:
			s.ReplicateOnce(ctx)
		}
	}
}

// ---------------------------------------------------------------- plumbing

type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func bad(msg string) error       { return &httpError{http.StatusBadRequest, msg} }
func forbidden(msg string) error { return &httpError{http.StatusForbidden, msg} }
func conflict(msg string) error  { return &httpError{http.StatusConflict, msg} }
func gone(msg string) error      { return &httpError{http.StatusGone, msg} }
func notFound(msg string) error  { return &httpError{http.StatusNotFound, msg} }
func limited(msg string) error   { return &httpError{http.StatusTooManyRequests, msg} }

// clientKey is the per-network key of the client that sent r.
func clientKey(r *http.Request) string { return ops.NetworkKey(ops.RemoteIP(r)) }

type writeHandler func(r *http.Request, body []byte) (proto.Message, error)

// writeLimit wraps a POST handler: per-IP rate limit, body read (at most
// maxBody bytes), error mapping.
func (s *Server) writeLimit(maxBody int, h writeHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.writes.allow(ops.RemoteIP(r), s.now()) {
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, int64(maxBody)+1))
		if err != nil || len(body) > maxBody {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		out, err := h(r, body)
		if err != nil {
			var he *httpError
			if errors.As(err, &he) {
				http.Error(w, he.msg, he.status)
				return
			}
			s.log.Error("request failed", "route", r.Pattern, "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeProto(w, out)
	}
}

// readLimit wraps a GET handler: per-IP rate limit (reads_per_minute).
// Only /healthz and /v1/peer (its own caps, peer.go) are outside it.
func (s *Server) readLimit(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.reads.allow(ops.RemoteIP(r), s.now()) {
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		h(w, r)
	}
}

func writeProto(w http.ResponseWriter, m proto.Message) {
	if m == nil {
		m = &wire.Ok{}
	}
	b, err := proto.Marshal(m)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	_, _ = w.Write(b)
}

func (s *Server) nextRev(ctx context.Context, tx *sql.Tx) (int64, error) {
	var v int64
	err := tx.QueryRowContext(ctx, `INSERT INTO counters (k, v) VALUES ('rev', 1) ON CONFLICT (k) DO UPDATE SET v = v + 1 RETURNING v`).Scan(&v)
	return v, err
}

// ---------------------------------------------------------------- listings

// verifyListing checks a signed listing end to end: an owner's cert for the
// server key (through the community's owner chain), and the server key's
// signature over the listing bytes.
func verifyListing(sl *wire.SignedListing) (*wire.Listing, *identity.OwnerSet, error) {
	var l wire.Listing
	if err := proto.Unmarshal(sl.Listing, &l); err != nil {
		return nil, nil, bad("malformed listing")
	}
	if !identity.ValidPublic(l.Community) || !identity.ValidPublic(l.ServerKey) {
		return nil, nil, bad("listing keys are malformed")
	}
	v, err := identity.VerifyCert(sl.Cert, l.ServerKey, l.Community)
	if err != nil {
		return nil, nil, forbidden("listing's server key isn't certified by an owner of the community")
	}
	if err := identity.Verify(l.ServerKey, identity.CtxListing, sl.Listing, sl.Signature); err != nil {
		return nil, nil, forbidden("listing signature is invalid")
	}
	if !l.Withdrawn && l.Name == "" {
		return nil, nil, bad("listing fields are out of range")
	}
	if len(l.Name) > 400 || len(l.Description) > 4000 || len(l.Tags) > 10 || len(sl.Icon) > MaxIconBytes ||
		len(l.Language) > maxShortText || len(l.MemberRange) > maxShortText || len(l.PublicJoinKey) > maxJoinKey ||
		len(l.RouteId) > 32 || len(l.IconSha256) > sha256.Size || !shortStrings(l.Tags, maxTagLen, 10) ||
		!shortStrings(l.Relays, relayurl.MaxLen, 16) {
		return nil, nil, bad("listing fields are out of range")
	}
	if err := checkIcon(&l, sl.Icon); err != nil {
		return nil, nil, err
	}
	return &l, v.Owners, nil
}

// shortStrings reports whether ss has at most n strings of at most max
// bytes each.
func shortStrings(ss []string, max, n int) bool {
	if len(ss) > n {
		return false
	}
	for _, s := range ss {
		if len(s) > max {
			return false
		}
	}
	return true
}

// MaxIconBytes bounds a listing's icon (community settings allow 256 KiB).
const MaxIconBytes = 256 << 10

// checkIcon binds the unsigned icon next to a listing to the hash the
// listing signs: both empty, or the icon hashes to icon_sha256.
func checkIcon(l *wire.Listing, icon []byte) error {
	if len(icon) == 0 && len(l.IconSha256) == 0 {
		return nil
	}
	sum := sha256.Sum256(icon)
	if len(icon) == 0 || !bytes.Equal(sum[:], l.IconSha256) {
		return forbidden("listing icon doesn't match its signed hash")
	}
	return nil
}

// storedOwners is the owner set of a stored listing or route's cert (nil if
// it can't be read, which lets a valid update replace it).
func storedOwners(cert *wire.SignedServerCert, community []byte) *identity.OwnerSet {
	if cert == nil {
		return nil
	}
	set, err := identity.VerifyOwnerChain(community, cert.OwnerChain)
	if err != nil {
		return nil
	}
	return set
}

// MaxSeqAhead bounds how far a listing's or route's seq may be ahead of this
// node's clock. Servers use the publish time in unix milliseconds as seq, so
// an honest seq is never far ahead; without a bound one post with a seq near
// the uint64 maximum would freeze the entry for everyone under that owner
// set.
const MaxSeqAhead = 24 * time.Hour

// newer decides whether an incoming listing or route (owner set owners, seq)
// may replace the stored one. Entries are ordered by owner set first: one
// certified under a later set beats any under an earlier set whatever the
// seqs, so a removed owner's high seq posted where the removal wasn't seen
// yet is replaced as soon as the current set's entry arrives. Within one
// set the seq must increase. An entry under a set that doesn't extend the
// stored one is refused.
func (s *Server) newer(owners, stored *identity.OwnerSet, seq, storedSeq uint64, what string) error {
	if seq > uint64(s.now().Add(MaxSeqAhead).UnixMilli()) {
		return bad("the " + what + "'s sequence number is too far ahead of this node's clock")
	}
	if !owners.Extends(stored) {
		return forbidden("the " + what + "'s owner set doesn't follow the stored one")
	}
	if (stored == nil || owners.Seq == stored.Seq) && seq <= storedSeq {
		return conflict("a newer " + what + " is already stored")
	}
	return nil
}

func (s *Server) postListing(r *http.Request, body []byte) (proto.Message, error) {
	if !s.directoryOn(r.Context()) {
		return nil, errDirectoryOff
	}
	var sl wire.SignedListing
	if err := unmarshalKnown(body, &sl); err != nil {
		return nil, bad("malformed listing")
	}
	l, owners, err := verifyListing(&sl)
	if err != nil {
		return nil, err
	}
	raw, err := encodeKnown(&sl, MaxListingBytes, "listing")
	if err != nil {
		return nil, err
	}
	if !s.listing.allow(string(l.Community), s.now()) {
		return nil, limited("too many listing updates for this community")
	}
	return nil, s.storeListing(r.Context(), l, owners, raw)
}

func (s *Server) storeListing(ctx context.Context, l *wire.Listing, owners *identity.OwnerSet, raw []byte) error {
	if err := s.stale(l.PublishedAt, "listing"); err != nil {
		return err
	}
	if !s.directoryOn(ctx) {
		return errDirectoryOff // also listings arriving by replication
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Checked inside the transaction, so a delisting by `meta directory
	// delist` (another process) can't interleave with this write.
	if gone, err := s.communityDelisted(ctx, tx, l.Community); err != nil {
		return err
	} else if gone {
		return forbidden("this community has been delisted by this maintainer")
	}
	// A reaped community lists again only after its route re-registered it.
	if dead, err := tombstoned(ctx, tx, `community`, l.Community); err != nil {
		return err
	} else if dead {
		return s.terminatedMsg()
	}
	var id int64
	var seq uint64
	var stored []byte
	err = tx.QueryRowContext(ctx, `SELECT id, seq, signed FROM listings WHERE community = ?`, l.Community).Scan(&id, &seq, &stored)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if err := s.newer(owners, nil, l.Seq, 0, "listing"); err != nil {
			return err
		}
		if l.Withdrawn {
			return nil
		}
	case err != nil:
		return err
	default:
		var prev wire.SignedListing
		var prevOwners *identity.OwnerSet
		if proto.Unmarshal(stored, &prev) == nil {
			prevOwners = storedOwners(prev.Cert, l.Community)
		}
		if err := s.newer(owners, prevOwners, l.Seq, seq, "listing"); err != nil {
			return err
		}
	}
	rev, err := s.nextRev(ctx, tx)
	if err != nil {
		return err
	}
	now := s.now().UnixMilli()
	if id == 0 {
		res, err := tx.ExecContext(ctx, `INSERT INTO listings (community, signed, seq, withdrawn, rev, updated_at, published_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			l.Community, raw, l.Seq, boolInt(l.Withdrawn), rev, now, min(l.PublishedAt, now))
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
	} else if _, err := tx.ExecContext(ctx, `UPDATE listings SET signed = ?, seq = ?, withdrawn = ?, rev = ?, updated_at = ?, published_at = ? WHERE id = ?`,
		raw, l.Seq, boolInt(l.Withdrawn), rev, now, min(l.PublishedAt, now), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM listings_fts WHERE rowid = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM listing_tags WHERE listing_id = ?`, id); err != nil {
		return err
	}
	if !l.Withdrawn {
		if _, err := tx.ExecContext(ctx, `INSERT INTO listings_fts (rowid, name, description, tags) VALUES (?, ?, ?, ?)`,
			id, l.Name, l.Description, strings.Join(l.Tags, " ")); err != nil {
			return err
		}
		for _, t := range l.Tags {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO listing_tags (listing_id, tag) VALUES (?, ?)`, id, strings.ToLower(t)); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Server) removeListing(ctx context.Context, community []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var id int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM listings WHERE community = ?`, community).Scan(&id); err != nil {
		return
	}
	for _, q := range []string{`DELETE FROM listings_fts WHERE rowid = ?`, `DELETE FROM listing_tags WHERE listing_id = ?`, `DELETE FROM listings WHERE id = ?`} {
		if _, err := s.db.ExecContext(ctx, q, id); err != nil {
			s.log.Error("delist", "err", err)
		}
	}
}

func (s *Server) getListing(w http.ResponseWriter, r *http.Request) {
	if s.refuseDirectory(w, r) {
		return
	}
	id, err := identity.Decode(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	var raw []byte
	var withdrawn int
	if err := s.db.QueryRowContext(r.Context(), `SELECT signed, withdrawn FROM listings WHERE community = ?`, id).Scan(&raw, &withdrawn); err != nil || withdrawn != 0 {
		http.Error(w, "not listed", http.StatusNotFound)
		return
	}
	writeStored(w, raw, &wire.SignedListing{})
}

// writeStored serves a stored record re-encoded with known fields only
// (rows stored before records were re-encoded may carry unknown ones).
func writeStored(w http.ResponseWriter, raw []byte, m proto.Message) {
	if unmarshalKnown(raw, m) != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeProto(w, m)
}

// ftsQuery turns user input into quoted prefix terms (no FTS syntax).
func ftsQuery(q string) string {
	var terms []string
	for _, word := range strings.FieldsFunc(q, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		if len(terms) == 8 {
			break
		}
		terms = append(terms, `"`+word+`"*`)
	}
	return strings.Join(terms, " ")
}

// search answers directory queries. By policy, queries are never logged.
// Search by tag, and browsing (no query), newest first; both use the
// searchIndexes indexes (TestSearchUsesIndexes).
const (
	searchTagSQL = `SELECT l.signed FROM listings l JOIN listing_tags t ON t.listing_id = l.id
WHERE t.tag = ? AND l.withdrawn = 0 ORDER BY l.updated_at DESC LIMIT ?`
	searchRecentSQL = `SELECT signed FROM listings WHERE withdrawn = 0 ORDER BY updated_at DESC LIMIT ?`
)

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	if s.refuseDirectory(w, r) {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	tag := strings.ToLower(strings.TrimSpace(q.Get("tag")))
	match := ftsQuery(q.Get("q"))
	var rows *sql.Rows
	var err error
	ctx := r.Context()
	switch {
	case match != "" && tag != "":
		rows, err = s.db.QueryContext(ctx, `SELECT l.signed FROM listings_fts f JOIN listings l ON l.id = f.rowid
JOIN listing_tags t ON t.listing_id = l.id WHERE listings_fts MATCH ? AND t.tag = ? AND l.withdrawn = 0 ORDER BY rank LIMIT ?`, match, tag, limit)
	case match != "":
		rows, err = s.db.QueryContext(ctx, `SELECT l.signed FROM listings_fts f JOIN listings l ON l.id = f.rowid
WHERE listings_fts MATCH ? AND l.withdrawn = 0 ORDER BY rank LIMIT ?`, match, limit)
	case tag != "":
		rows, err = s.db.QueryContext(ctx, searchTagSQL, tag, limit)
	default:
		rows, err = s.db.QueryContext(ctx, searchRecentSQL, limit)
	}
	if err != nil {
		http.Error(w, "bad query", http.StatusBadRequest)
		return
	}
	defer rows.Close()
	out := &wire.SearchResponse{}
	for rows.Next() {
		var raw []byte
		if rows.Scan(&raw) != nil {
			continue
		}
		var sl wire.SignedListing
		if unmarshalKnown(raw, &sl) == nil {
			out.Listings = append(out.Listings, &sl)
		}
	}
	out.Statuses = s.listingStatuses(ctx, out.Listings)
	writeProto(w, out)
}

// ---------------------------------------------------------------- routes

func verifyRoute(sr *wire.SignedRoute) (*wire.Route, *identity.VerifiedCert, error) {
	var rt wire.Route
	if err := proto.Unmarshal(sr.Route, &rt); err != nil {
		return nil, nil, bad("malformed route")
	}
	if len(rt.RouteId) < 16 || len(rt.RouteId) > 32 || len(rt.Relays) == 0 || !shortStrings(rt.Relays, relayurl.MaxLen, 16) {
		return nil, nil, bad("route fields are out of range")
	}
	cert, err := identity.VerifyCert(sr.Cert, rt.ServerKey, nil)
	if err != nil {
		return nil, nil, forbidden("route's server key isn't certified by an owner")
	}
	if err := identity.Verify(rt.ServerKey, identity.CtxRoute, sr.Route, sr.Signature); err != nil {
		return nil, nil, forbidden("route signature is invalid")
	}
	return &rt, cert, nil
}

func (s *Server) postRoute(r *http.Request, body []byte) (proto.Message, error) {
	var sr wire.SignedRoute
	if err := unmarshalKnown(body, &sr); err != nil {
		return nil, bad("malformed route")
	}
	rt, cert, err := verifyRoute(&sr)
	if err != nil {
		return nil, err
	}
	raw, err := encodeKnown(&sr, MaxRouteBytes, "route")
	if err != nil {
		return nil, err
	}
	return nil, s.storeRoute(r.Context(), rt, cert, raw)
}

// storeRoute keeps the first community that registered a route id (the
// `owner` column holds its community id): later updates (a moved community,
// a rotated key) must be certified by an owner of the same community, under
// an owner set that follows the stored one.
func (s *Server) storeRoute(ctx context.Context, rt *wire.Route, cert *identity.VerifiedCert, raw []byte) error {
	community := cert.Owners.Community
	if err := s.stale(rt.PublishedAt, "route"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if gone, err := s.communityDelisted(ctx, tx, community); err != nil {
		return err
	} else if gone {
		return forbidden("this community has been delisted by this maintainer")
	}
	if err := s.routeTombstone(ctx, tx, rt, community); err != nil {
		return err
	}
	var owner, stored []byte
	var seq uint64
	err = tx.QueryRowContext(ctx, `SELECT owner, seq, signed FROM routes WHERE route_id = ?`, rt.RouteId).Scan(&owner, &seq, &stored)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if err := s.newer(cert.Owners, nil, rt.Seq, 0, "route"); err != nil {
			return err
		}
	case err != nil:
		return err
	case !bytes.Equal(owner, community):
		return forbidden("this route belongs to another community")
	default:
		var prev wire.SignedRoute
		var prevOwners *identity.OwnerSet
		if proto.Unmarshal(stored, &prev) == nil {
			prevOwners = storedOwners(prev.Cert, community)
		}
		if err := s.newer(cert.Owners, prevOwners, rt.Seq, seq, "route"); err != nil {
			return err
		}
	}
	rev, err := s.nextRev(ctx, tx)
	if err != nil {
		return err
	}
	now := s.now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `INSERT INTO routes (route_id, owner, signed, seq, rev, updated_at, published_at) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (route_id) DO UPDATE SET signed = excluded.signed, seq = excluded.seq, rev = excluded.rev, updated_at = excluded.updated_at,
	published_at = excluded.published_at`,
		rt.RouteId, community, raw, rt.Seq, rev, now, min(rt.PublishedAt, now)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) getRoute(w http.ResponseWriter, r *http.Request) {
	id, err := identity.Decode(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	var raw []byte
	if err := s.db.QueryRowContext(r.Context(), `SELECT signed FROM routes WHERE route_id = ?`, id).Scan(&raw); err != nil {
		http.Error(w, "unknown route", http.StatusNotFound)
		return
	}
	writeStored(w, raw, &wire.SignedRoute{})
}

// ---------------------------------------------------------------- relay registry

// verifyHeartbeat checks a relay's signed heartbeat and returns it with
// its URL in canonical form (internal/relayurl), which is what the registry
// stores, compares and serves.
func verifyHeartbeat(sh *wire.SignedHeartbeat) (*wire.RelayHeartbeat, string, error) {
	var hb wire.RelayHeartbeat
	if err := proto.Unmarshal(sh.Heartbeat, &hb); err != nil {
		return nil, "", bad("malformed heartbeat")
	}
	if err := identity.Verify(hb.RelayKey, identity.CtxRelayHeartbeat, sh.Heartbeat, sh.Signature); err != nil {
		return nil, "", forbidden("heartbeat signature is invalid")
	}
	if len(hb.Region) > maxShortText {
		return nil, "", bad("heartbeat region is too long")
	}
	url, err := relayurl.Canonical(hb.Url)
	if err != nil {
		return nil, "", bad(err.Error())
	}
	return &hb, url, nil
}

func (s *Server) postHeartbeat(r *http.Request, body []byte) (proto.Message, error) {
	var sh wire.SignedHeartbeat
	if err := unmarshalKnown(body, &sh); err != nil {
		return nil, bad("malformed heartbeat")
	}
	hb, url, err := verifyHeartbeat(&sh)
	if err != nil {
		return nil, err
	}
	raw, err := encodeKnown(&sh, MaxHeartbeatBytes, "heartbeat")
	if err != nil {
		return nil, err
	}
	return nil, s.storeHeartbeat(r.Context(), hb, url, raw, true, ops.RemoteIP(r))
}

// storeHeartbeat stores a verified relay heartbeat once its URL is proven
// to serve its key (relayproof.go); wait false (replication) never waits
// for that check. from is the client address ("" by replication).
func (s *Server) storeHeartbeat(ctx context.Context, hb *wire.RelayHeartbeat, url string, raw []byte, wait bool, from string) error {
	now := s.now()
	if d := now.Sub(time.UnixMilli(hb.At)); d > s.cfg.RelayTTL.Duration || d < -time.Minute {
		return bad("heartbeat is stale")
	}
	if gone, err := s.relayRemoved(ctx, s.db, url); err != nil {
		return err
	} else if gone {
		return forbidden("this relay has been removed from the registry")
	}
	if err := s.proveRelay(ctx, url, hb.RelayKey, wait, from); err != nil {
		return err
	}
	// Capacity and connections are the relay's own claims; bounded so a
	// relay can't claim to be arbitrarily large or empty.
	capacity := min(hb.Capacity, maxRelayCapacity)
	connections := min(hb.Connections, capacity)
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var key []byte
	var at int64
	if gone, err := s.relayRemoved(ctx, tx, url); err != nil {
		return err
	} else if gone {
		return forbidden("this relay has been removed from the registry")
	}
	err = tx.QueryRowContext(ctx, `SELECT relay_key, at FROM relays WHERE url = ?`, url).Scan(&key, &at)
	if err == nil {
		fresh := now.Sub(time.UnixMilli(at)) < s.cfg.RelayTTL.Duration
		if !bytes.Equal(key, hb.RelayKey) && fresh {
			return conflict("another relay key holds this URL")
		}
		if hb.At <= at {
			return nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	rev, err := s.nextRev(ctx, tx)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO relays (url, relay_key, signed, at, region, capacity, connections, rev) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (url) DO UPDATE SET relay_key = excluded.relay_key, signed = excluded.signed, at = excluded.at, region = excluded.region,
	capacity = excluded.capacity, connections = excluded.connections, rev = excluded.rev`,
		url, hb.RelayKey, raw, hb.At, truncate(hb.Region, 32), capacity, connections, rev); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) getRelays(w http.ResponseWriter, r *http.Request) {
	cutoff := s.now().Add(-s.cfg.RelayTTL.Duration).UnixMilli()
	removed, err := removedRelayHosts(r.Context(), s.db)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT url, region, capacity, connections, at FROM relays WHERE at >= ? ORDER BY url`, cutoff)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := &wire.RelayList{}
	region := r.URL.Query().Get("region")
	for rows.Next() {
		ri := &wire.RelayInfo{}
		if rows.Scan(&ri.Url, &ri.Region, &ri.Capacity, &ri.Connections, &ri.LastSeen) != nil {
			continue
		}
		if host := relayurl.Host(ri.Url); removed[host] || s.delistedRelays[host] {
			continue
		}
		if region != "" && ri.Region != region {
			continue
		}
		out.Relays = append(out.Relays, ri)
	}
	writeProto(w, out)
}

// ---------------------------------------------------------------- mailboxes

// InboxAddress is where mail for an inbox token is stored. Only the token
// holder can fetch or delete (docs/protocol.md §7).
func InboxAddress(token []byte) []byte {
	h := sha256.New()
	h.Write([]byte("openarcanum/inbox/v1\x00"))
	h.Write(token)
	return h.Sum(nil)
}

func (s *Server) mailboxDrop(r *http.Request, body []byte) (proto.Message, error) {
	var d wire.MailboxDrop
	if err := proto.Unmarshal(body, &d); err != nil || len(d.Inbox) != 32 {
		return nil, bad("malformed drop")
	}
	if len(d.Blob) == 0 || uint64(len(d.Blob)) > s.cfg.MailboxMaxItem {
		return nil, &httpError{http.StatusRequestEntityTooLarge, "message too large for a mailbox"}
	}
	// First-contact inboxes are the spam surface: limit drops per inbox.
	if !s.drops.allow(string(d.Inbox), s.now()) {
		return nil, limited("this inbox is receiving too much mail; try later")
	}
	gid := MailboxGID(d.Inbox, d.Blob)
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := r.Context()
	// The same message dropped again (another copy from its sender, or
	// one a peer already brought here), or one already deleted: done.
	if has, err := mailboxHas(ctx, s.db, gid, d.Inbox); err != nil || has {
		return nil, err
	}
	var used sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT sum(size) FROM mailbox WHERE inbox = ?`, d.Inbox).Scan(&used); err != nil {
		return nil, err
	}
	if uint64(used.Int64)+uint64(len(d.Blob)) > s.cfg.MailboxQuota {
		return nil, &httpError{http.StatusInsufficientStorage, "inbox is full"}
	}
	// Inbox addresses are free to pick, so the per-inbox quota alone
	// doesn't bound the store: all inboxes together and each network get
	// a byte budget too.
	if s.mailboxBytes+uint64(len(d.Blob)) > s.cfg.MailboxStoreMax {
		s.log.Warn("mailbox store full", "used", s.mailboxBytes, "max", s.cfg.MailboxStoreMax)
		return nil, &httpError{http.StatusInsufficientStorage, "this node's mailboxes are full; try another maintainer or later"}
	}
	now := s.now()
	if !s.mailboxBudget.charge(ops.RemoteIP(r), uint64(len(d.Blob)), now) {
		return nil, limited("too much mail from your network today; try again later")
	}
	expires := now.Add(s.cfg.MailboxTTL.Duration).UnixMilli()
	tx, err := beginWrite(ctx, s.db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO mailbox (inbox, blob, size, received_at, expires_at, gid, origin_received_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		d.Inbox, d.Blob, len(d.Blob), now.UnixMilli(), expires, gid, now.UnixMilli()); err != nil {
		return nil, err
	}
	if err := logPeer(ctx, tx, logItem, gid, expires); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.mailboxBytes += uint64(len(d.Blob))
	s.notifyPeers()
	return nil, nil
}

// deleteMail runs a DELETE on mailbox rows that returns their sizes, and
// takes them off mailboxBytes. Callers hold s.mu.
func (s *Server) deleteMail(ctx context.Context, query string, args ...any) error {
	rows, err := s.db.QueryContext(ctx, query+` RETURNING size`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var n uint64
		if err := rows.Scan(&n); err != nil {
			return err
		}
		s.mailboxBytes -= min(n, s.mailboxBytes)
	}
	return rows.Err()
}

// loadMailboxState reads what all mailboxes hold and the mailbox epoch,
// creating the epoch on a new node.
func (s *Server) loadMailboxState(ctx context.Context) error {
	var used int64
	if err := s.db.QueryRowContext(ctx, `SELECT coalesce(sum(size), 0) FROM mailbox`).Scan(&used); err != nil {
		return err
	}
	s.mailboxBytes = uint64(used)
	epoch, err := readMailboxEpoch(ctx, s.db)
	if err != nil {
		return err
	}
	if epoch == 0 {
		if epoch, err = newMailboxEpoch(ctx, s.db); err != nil {
			return err
		}
	}
	s.mailboxEpoch = epoch
	if s.replEpoch, err = loadEpoch(ctx, s.db, replEpochKey); err != nil {
		return err
	}
	s.peerEpoch, err = loadEpoch(ctx, s.db, peerEpochKey)
	return err
}

func readMailboxEpoch(ctx context.Context, q querier) (uint64, error) {
	var v int64
	err := q.QueryRowContext(ctx, `SELECT v FROM counters WHERE k = 'mailbox_epoch'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return uint64(v), err
}

// newMailboxEpoch stores a fresh random mailbox epoch (never 0).
func newMailboxEpoch(ctx context.Context, x execer) (uint64, error) {
	return newEpoch(ctx, x, "mailbox_epoch")
}

// Epoch counters: each changes when the node is restored from a backup
// (prepareRestored), so readers of the sequence it guards start over.
const (
	// replEpochKey guards the public replication log (rev, GET /v1/replicate).
	replEpochKey = "repl_epoch"
)

// newEpoch stores a fresh random epoch (never 0) under counter k.
func newEpoch(ctx context.Context, x execer, k string) (uint64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	v := int64(binary.BigEndian.Uint64(b[:])>>1) | 1
	_, err := x.ExecContext(ctx, `INSERT INTO counters (k, v) VALUES (?, ?) ON CONFLICT (k) DO UPDATE SET v = excluded.v`, k, v)
	return uint64(v), err
}

// loadEpoch reads epoch counter k, creating it when missing.
func loadEpoch(ctx context.Context, x execer, k string) (uint64, error) {
	var v int64
	err := x.QueryRowContext(ctx, `SELECT v FROM counters WHERE k = ?`, k).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && v == 0) {
		return newEpoch(ctx, x, k)
	}
	return uint64(v), err
}

func (s *Server) mailboxFetch(r *http.Request, body []byte) (proto.Message, error) {
	var f wire.MailboxFetch
	if err := proto.Unmarshal(body, &f); err != nil || len(f.Token) != 32 {
		return nil, bad("malformed fetch")
	}
	inbox := InboxAddress(f.Token)
	ctx := r.Context()
	rows, err := s.db.QueryContext(ctx, `SELECT id, blob, received_at, gid FROM mailbox WHERE inbox = ? AND id > ? AND expires_at > ? ORDER BY id LIMIT 200`,
		inbox, f.AfterId, s.now().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &wire.MailboxItems{QuotaBytes: s.cfg.MailboxQuota, Epoch: s.mailboxEpoch}
	var total int
	for rows.Next() {
		it := &wire.MailboxItem{}
		if err := rows.Scan(&it.Id, &it.Blob, &it.ReceivedAt, &it.Gid); err != nil {
			return nil, err
		}
		out.Items = append(out.Items, it)
		total += len(it.Blob)
		if total > 24<<20 {
			break
		}
	}
	var used sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT sum(size) FROM mailbox WHERE inbox = ?`, inbox).Scan(&used); err == nil {
		out.UsedBytes = uint64(used.Int64)
	}
	return out, nil
}

func (s *Server) mailboxDelete(r *http.Request, body []byte) (proto.Message, error) {
	var d wire.MailboxDelete
	if err := proto.Unmarshal(body, &d); err != nil || len(d.Token) != 32 || len(d.Ids)+len(d.Gids) > 1000 {
		return nil, bad("malformed delete")
	}
	for _, g := range d.Gids {
		if len(g) != sha256.Size {
			return nil, bad("malformed delete")
		}
	}
	inbox := InboxAddress(d.Token)
	ctx := r.Context()
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := beginWrite(ctx, s.db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Each delete leaves a tombstone, replicated to the cluster, that
	// keeps a copy elsewhere from coming back: until the item's own
	// expiry when it is here, else as long as any copy could live.
	gids := d.Gids
	expiry := map[string]int64{}
	for _, id := range d.Ids {
		var gid []byte
		var exp int64
		err := tx.QueryRowContext(ctx, `SELECT gid, expires_at FROM mailbox WHERE id = ? AND inbox = ?`, id, inbox).Scan(&gid, &exp)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		gids = append(gids, gid)
		expiry[string(gid)] = exp
	}
	var freed uint64
	changed := false
	for _, gid := range gids {
		exp, ok := expiry[string(gid)]
		if !ok {
			err := tx.QueryRowContext(ctx, `SELECT expires_at FROM mailbox WHERE gid = ? AND inbox = ?`, gid, inbox).Scan(&exp)
			if errors.Is(err, sql.ErrNoRows) {
				// Not held here (a copy on another cluster member, or any
				// 32 bytes): its tombstone lasts weeks and goes to every
				// member, so these are bounded per network.
				if !s.absentTombstones.allow(ops.RemoteIP(r), s.now()) {
					continue
				}
				exp = s.now().Add(s.cfg.MailboxTTL.Duration).UnixMilli()
			} else if err != nil {
				return nil, err
			}
		}
		added, n, err := s.tombstoneMail(ctx, tx, gid, inbox, exp+TombstoneSlack.Milliseconds())
		if err != nil {
			return nil, err
		}
		freed += n
		changed = changed || added
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.mailboxBytes -= min(freed, s.mailboxBytes)
	if changed {
		s.notifyPeers()
	}
	return nil, nil
}

// ---------------------------------------------------------------- nameplates

func (s *Server) nameplateCreate(r *http.Request, body []byte) (proto.Message, error) {
	var req wire.NameplateRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		return nil, bad("malformed request")
	}
	relay, err := relayurl.Canonical(req.Relay)
	if err != nil {
		return nil, bad(err.Error())
	}
	req.Relay = relay
	if !s.plates.allow(ops.RemoteIP(r), s.now()) {
		return nil, limited("too many link codes; wait a minute")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := r.Context()
	now := s.now()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM nameplates WHERE expires_at <= ?`, now.UnixMilli()); err != nil {
		return nil, err
	}
	// A random six-digit number: the next free one would let anyone claim
	// a live code before the device it was made for (a claim works once).
	// Nameplates aren't replicated (a code is claimed once, on one node),
	// so in a maintainer cluster each member hands out only numbers whose
	// last digit is its position in the set: two members never give out
	// the same code, and the new device knows which member to ask first.
	n, err := s.freeNameplate(ctx, s.setIndex(ctx))
	if err != nil {
		return nil, err
	}
	topic := make([]byte, 32)
	if _, err := rand.Read(topic); err != nil {
		return nil, err
	}
	exp := now.Add(s.cfg.NameplateTTL.Duration).UnixMilli()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO nameplates (number, topic, relay, expires_at) VALUES (?, ?, ?, ?)`, n, topic, req.Relay, exp); err != nil {
		return nil, err
	}
	return &wire.Nameplate{Number: n, Topic: topic, Relay: req.Relay, ExpiresAt: exp}, nil
}

// freeNameplate picks a random unused nameplate number of six digits
// (callers hold s.mu); in a cluster (index >= 0) its last digit is index.
func (s *Server) freeNameplate(ctx context.Context, index int) (uint32, error) {
	for range 32 {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		r := binary.BigEndian.Uint32(b[:])
		var n uint32
		if index >= 0 {
			n = (10_000+r%90_000)*10 + uint32(index%10)
		} else {
			n = 100_000 + r%900_000
		}
		var used int
		err := s.db.QueryRowContext(ctx, `SELECT 1 FROM nameplates WHERE number = ?`, n).Scan(&used)
		if errors.Is(err, sql.ErrNoRows) {
			return n, nil
		}
		if err != nil {
			return 0, err
		}
	}
	return 0, limited("no free nameplates")
}

// nameplateClaim hands the topic to the one device that claims the number.
// A nameplate works once: the second claim is refused, so a code gets one
// guess (docs/protocol.md §10.1). Claims are rate
// limited per network (nameplates_per_minute) and failed ones node-wide
// (claimFailuresPerMinute), so numbers can't be swept.
func (s *Server) nameplateClaim(r *http.Request, body []byte) (proto.Message, error) {
	var c wire.NameplateClaim
	if err := proto.Unmarshal(body, &c); err != nil {
		return nil, bad("malformed claim")
	}
	now := s.now()
	if !s.claims.allow(ops.RemoteIP(r), now) || s.claimFailures.full("", now) {
		return nil, limited("too many link code attempts; wait a minute")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := r.Context()
	var np wire.Nameplate
	var claimed int
	err := s.db.QueryRowContext(ctx, `SELECT number, topic, relay, expires_at, claimed FROM nameplates WHERE number = ?`, c.Number).
		Scan(&np.Number, &np.Topic, &np.Relay, &np.ExpiresAt, &claimed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if errors.Is(err, sql.ErrNoRows) || now.UnixMilli() >= np.ExpiresAt || claimed != 0 {
		s.claimFailures.allow("", now)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("no such link code (it may have expired)")
	}
	if now.UnixMilli() >= np.ExpiresAt {
		return nil, gone("this link code has expired")
	}
	if claimed != 0 {
		return nil, gone("this link code was already used")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE nameplates SET claimed = 1 WHERE number = ?`, c.Number); err != nil {
		return nil, err
	}
	return &np, nil
}

// ---------------------------------------------------------------- expiry & replication

// Expire deletes expired mail, nameplates and blobs, abandoned blob
// uploads and stray blob files, and reaps communities whose servers stopped
// reporting (status.go). Run calls it every minute.
func (s *Server) Expire(ctx context.Context) {
	now := s.now().UnixMilli()
	s.mu.Lock()
	if err := s.deleteMail(ctx, `DELETE FROM mailbox WHERE expires_at <= ?`, now); err != nil {
		s.log.Error("expiry", "err", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM nameplates WHERE expires_at <= ?`, now); err != nil {
		s.log.Error("expiry", "err", err)
	}
	// Expired items need no tombstone (every copy expires by itself), and
	// a log entry is pruned once what it names has expired everywhere.
	for _, q := range []string{`DELETE FROM mailbox_tombstones WHERE expires_at <= ?`, `DELETE FROM peer_log WHERE at <= ?`,
		`DELETE FROM blob_releases WHERE expires_at <= ?`} {
		if _, err := s.db.ExecContext(ctx, q, now); err != nil {
			s.log.Error("expiry", "err", err)
		}
	}
	s.mu.Unlock()
	if err := s.pruneDecisions(ctx); err != nil {
		s.log.Error("expiry", "err", err)
	}
	if _, err := s.reap(ctx); err != nil {
		s.log.Error("community reaping", "err", err)
	}
	if n, err := s.blobs.collect(ctx); err != nil {
		s.log.Error("blob expiry", "err", err)
	} else if n > 0 {
		s.log.Info("blob expiry", "deleted", n)
	}
	if err := s.blobs.sweepFiles(ctx); err != nil {
		s.log.Error("blob file sweep", "err", err)
	}
}

// replicate serves everything changed after a cursor to peer maintainers, in
// revision order, so the returned cursor never skips anything. This is the
// public replication any peer may pull; mailboxes and other private data
// replicate only between trusted members of a maintainer cluster, over
// /v1/peer (peer.go).
func (s *Server) replicate(w http.ResponseWriter, r *http.Request) {
	cursor, _ := strconv.ParseInt(r.URL.Query().Get("cursor"), 10, 64)
	// A node whose directory is off passes no listings on.
	rows, err := s.db.QueryContext(r.Context(), `
SELECT 'l', signed, rev FROM listings WHERE rev > ?1 AND ?2
UNION ALL SELECT 'r', signed, rev FROM routes WHERE rev > ?1
UNION ALL SELECT 'h', signed, rev FROM relays WHERE rev > ?1
UNION ALL SELECT 'c', heartbeat, rev FROM community_status WHERE rev > ?1
UNION ALL SELECT 'm', signed, rev FROM maintainer_sets WHERE rev > ?1
ORDER BY rev LIMIT 1000`, cursor, s.directoryOn(r.Context()))
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := &wire.ReplicationBatch{Cursor: uint64(cursor), Epoch: s.replEpoch}
	// Entries go out re-encoded with known fields only; one over its bound
	// (stored before the bounds) is skipped, and the batch ends before
	// MaxReplicationBatch bytes, so a peer can always take it and the
	// cursor always moves.
	size := 0
	for rows.Next() {
		var kind string
		var raw []byte
		var rev int64
		if err := rows.Scan(&kind, &raw, &rev); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		var m proto.Message
		limit := MaxHeartbeatBytes
		switch kind {
		case "l":
			m, limit = &wire.SignedListing{}, MaxListingBytes
		case "r":
			m, limit = &wire.SignedRoute{}, MaxRouteBytes
		case "h":
			m = &wire.SignedHeartbeat{}
		case "c":
			m = &wire.SignedCommunityHeartbeat{}
		case "m":
			m, limit = &wire.SignedMaintainerSet{}, MaxRouteBytes
		default:
			continue
		}
		if unmarshalKnown(raw, m) != nil {
			out.Cursor = uint64(rev)
			continue
		}
		n := proto.Size(m)
		if n > limit {
			out.Cursor = uint64(rev)
			continue
		}
		if size > 0 && size+n > MaxReplicationBatch {
			break
		}
		size += n
		out.Cursor = uint64(rev)
		switch m := m.(type) {
		case *wire.SignedListing:
			out.Listings = append(out.Listings, m)
		case *wire.SignedRoute:
			out.Routes = append(out.Routes, m)
		case *wire.SignedHeartbeat:
			out.Heartbeats = append(out.Heartbeats, m)
		case *wire.SignedCommunityHeartbeat:
			out.CommunityHeartbeats = append(out.CommunityHeartbeats, m)
		case *wire.SignedMaintainerSet:
			out.MaintainerSets = append(out.MaintainerSets, m)
		}
	}
	writeProto(w, out)
}

// ReplicateOnce pulls from every peer and applies what verifies.
func (s *Server) ReplicateOnce(ctx context.Context) {
	for _, peer := range s.cfg.Peers {
		s.replicateFrom(ctx, peer)
	}
}

// replicateFrom pulls one batch from peer. The cursor is a position in the
// peer's revision sequence, which goes back when the peer is restored from
// a backup; the batch's epoch says so, and the cursor starts over from 0
// (entries pulled again are refused as routine duplicates).
func (s *Server) replicateFrom(ctx context.Context, peer string) {
	key := "cursor:" + peer
	epochKey := "cursor_epoch:" + peer
	var cursor, epoch int64
	if err := s.db.QueryRowContext(ctx, `SELECT v FROM counters WHERE k = ?`, key).Scan(&cursor); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err := s.db.QueryRowContext(ctx, `SELECT v FROM counters WHERE k = ?`, epochKey).Scan(&epoch); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return
	}
	for attempt := 0; attempt < 2; attempt++ {
		batch := s.pullBatch(ctx, peer, cursor)
		if batch == nil {
			return
		}
		if batch.Epoch != 0 && epoch != 0 && int64(batch.Epoch) != epoch && cursor != 0 {
			s.log.Info("replication: peer was restored from a backup; pulling everything again", "peer", peer)
			cursor, epoch = 0, int64(batch.Epoch)
			continue
		}
		if !s.applyBatch(ctx, peer, batch) {
			return
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO counters (k, v) VALUES (?, ?), (?, ?) ON CONFLICT (k) DO UPDATE SET v = excluded.v`,
			key, int64(batch.Cursor), epochKey, int64(batch.Epoch)); err != nil {
			s.log.Error("replication cursor", "peer", peer, "err", err)
		}
		return
	}
}

// pullBatch fetches one replication batch from peer (nil on failure, logged).
func (s *Server) pullBatch(ctx context.Context, peer string, cursor int64) *wire.ReplicationBatch {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(peer, "/")+"/v1/replicate?cursor="+strconv.FormatInt(cursor, 10), nil)
	if err != nil {
		return nil
	}
	resp, err := s.http.Do(req)
	if err != nil {
		s.log.Warn("replication pull failed", "peer", peer, "err", err)
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	resp.Body.Close()
	if err != nil {
		s.log.Warn("replication pull: reading the reply failed", "peer", peer, "err", err)
		return nil
	}
	if err := version.CheckResponse(resp); err != nil {
		s.log.Warn("replication pull: no protocol version in common", "peer", peer, "err", err)
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		s.log.Warn("replication pull refused", "peer", peer, "status", resp.StatusCode)
		return nil
	}
	var batch wire.ReplicationBatch
	if err := unmarshalKnown(b, &batch); err != nil {
		s.log.Warn("replication pull: unparseable batch", "peer", peer, "bytes", len(b), "err", err)
		return nil
	}
	return &batch
}

// applyBatch stores what verifies in a batch and reports whether the
// cursor may move past it.
func (s *Server) applyBatch(ctx context.Context, peer string, batch *wire.ReplicationBatch) bool {
	// An entry that failed for a reason other than a routine refusal
	// (a database error) must be pulled again, so the cursor stays put
	// and the whole batch is retried next round; entries already stored
	// are then refused as routine duplicates.
	// The batch was parsed with known fields only (pullBatch); entries over
	// their bound are refused like any other that doesn't verify.
	failed := 0
	for _, sl := range batch.Listings {
		l, owners, err := verifyListing(sl)
		var raw []byte
		if err == nil {
			raw, err = encodeKnown(sl, MaxListingBytes, "listing")
		}
		if err == nil {
			failed += s.replicationErr(peer, "listing", s.storeListing(ctx, l, owners, raw))
		} else {
			s.replicationErr(peer, "listing", err)
		}
	}
	for _, sr := range batch.Routes {
		rt, cert, err := verifyRoute(sr)
		var raw []byte
		if err == nil {
			raw, err = encodeKnown(sr, MaxRouteBytes, "route")
		}
		if err == nil {
			failed += s.replicationErr(peer, "route", s.storeRoute(ctx, rt, cert, raw))
		} else {
			s.replicationErr(peer, "route", err)
		}
	}
	for _, sh := range batch.Heartbeats {
		hb, url, err := verifyHeartbeat(sh)
		var raw []byte
		if err == nil {
			raw, err = encodeKnown(sh, MaxHeartbeatBytes, "heartbeat")
		}
		if err == nil {
			failed += s.replicationErr(peer, "relay heartbeat", s.storeHeartbeat(ctx, hb, url, raw, false, ""))
		} else {
			s.replicationErr(peer, "relay heartbeat", err)
		}
	}
	// After the routes of the same batch, which they're checked against.
	for _, sh := range batch.CommunityHeartbeats {
		hb, err := verifyCommunityHeartbeat(sh)
		var raw []byte
		if err == nil {
			raw, err = encodeKnown(sh, MaxHeartbeatBytes, "heartbeat")
		}
		if err == nil {
			failed += s.replicationErr(peer, "community heartbeat", s.storeCommunityHeartbeat(ctx, hb, raw))
		} else {
			s.replicationErr(peer, "community heartbeat", err)
		}
	}
	// Only this node's own cluster's sets are kept.
	for _, sm := range batch.MaintainerSets {
		if s.operator != nil {
			failed += s.replicationErr(peer, "maintainer set", s.storeSet(ctx, sm))
		}
	}
	if failed > 0 {
		s.log.Warn("replication: cursor not advanced; the batch is retried next round", "peer", peer, "failed", failed)
		return false
	}
	return true
}

// replicationErr logs why a replicated entry wasn't stored (never its
// content): routine refusals (a newer copy is already here, delisted, a
// signature that doesn't verify) quietly, anything else loudly. It returns 1
// for a failure that isn't routine, so the caller keeps the cursor.
func (s *Server) replicationErr(peer, kind string, err error) int {
	if err == nil {
		return 0
	}
	var he *httpError
	if errors.As(err, &he) {
		s.log.Debug("replication: entry refused", "peer", peer, "kind", kind, "reason", he.msg)
		return 0
	}
	s.log.Warn("replication: entry not stored", "peer", peer, "kind", kind, "err", err)
	return 1
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
