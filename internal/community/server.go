// Package community is the community server: member roll, roles, join keys,
// channels, messages, files, retention and search (docs/architecture.md).
// It attaches outward to relays, accepts Noise sessions from members over relay streams,
// and publishes encrypted, signed channel events to relay topics.
package community

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flynn/noise"
	"golang.org/x/crypto/chacha20poly1305"
	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/meta/clusterset"
	"github.com/hyperkubeorg/openarcanum/internal/relayclient"
	"github.com/hyperkubeorg/openarcanum/internal/session"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Server is a running community server.
type Server struct {
	// insights: today's activity counts (insights.go).
	insights insightsState
	cfg      Config
	// host runs this community with others (host.go); nil for a server
	// opened on its own (tests, tools).
	host *Host
	log  *slog.Logger
	db   *sql.DB
	http httpClient
	// clusters caches the member lists of the maintainer clusters the
	// config names (maintainerURLs).
	clusters *clusterset.Cache
	// noDirectory: maintainer nodes whose public directory is off (they
	// answered a listing with 501), and when; listings skip them for an
	// hour. Guarded by noDirMu.
	noDirMu     sync.Mutex
	noDirectory map[string]time.Time

	storageKey []byte
	lookupKey  []byte

	// bmu runs backups one at a time (jobs.go); backupHook, when set
	// (tests), runs after a backup's snapshot is taken.
	bmu        sync.Mutex
	backupHook func()
	// lastManualBackup is when an owner last asked for a backup
	// (ServerBackup); guarded by mu.
	lastManualBackup time.Time

	// kmu guards the month-key cache (atrest.go).
	kmu       sync.Mutex
	monthKeys map[int][]byte

	// mu guards the model and keys below. Mutations hold it for writing.
	mu          sync.RWMutex
	key         *identity.Key
	static      noise.DHKey
	pendingKey  *identity.Key
	ownerKey    []byte
	ownerMember []byte
	owners      *identity.OwnerSet
	cert        *wire.SignedServerCert
	claimHash   []byte
	moveHash    []byte
	routeID     []byte
	settings    *wire.CommunitySettings
	roles       map[int64]*roleRec
	everyoneID  int64
	channels    map[int64]*channelRec
	categories  map[int64]*categoryRec
	// Slowmode: when each member last posted in each channel (channels.go).
	lastPost   map[slowKey]int64
	members    map[string]*memberRec
	uploads    map[int64]*upload
	nextUpload int64
	quota      uploadQuota
	// Pinned message ids (pins table), for message views.
	pinned map[int64]bool

	smu      sync.Mutex
	sessions map[*sess]struct{}
	// The online state last recorded per member (presenceChanged), and the
	// changes the next presence batch carries (flushPresence).
	presence        map[string]bool
	presencePending map[string]bool
	// Members whose status entry changed since the last batch, and the
	// Invisible flags of members with sessions (status.go).
	statusPending map[string]bool
	invisible     map[string]bool
	// Statuses: devices, ciphertexts and rate limits (status.go).
	status statusState

	rmu      sync.RWMutex
	relays   map[string]*relayclient.Client
	attached map[string]context.CancelFunc
	// Whether each relay said it is dedicated to this server when the
	// server last attached to it (netblock.go).
	dedicated map[string]bool

	// amu guards the AutoMod rule snapshot, which posts read without s.mu.
	amu     sync.RWMutex
	automod *autoModSet
	// emoji is the custom emoji list (guarded by s.mu); reports limits
	// how often each member reports.
	emoji   map[int64]*emojiRec
	reports rateLimiter
	// alertLimit limits AutoMod alert reports per message author.
	alertLimit rateLimiter
	// openReports is the open report count, or -1 until it is counted
	// again (reports.go openReportCount).
	openReports atomic.Int64
	// profileLimit limits profile changes per member (each one rebuilds
	// every session's state).
	profileLimit rateLimiter
	// cardFetchLimit and cardPairLimit limit contact-card fetches that hand
	// out one-time key packages, per member and per (member, card).
	cardFetchLimit, cardPairLimit rateLimiter
	// recent is who posted what in the last 7 days (ban history deletion).
	recent recentAuthors

	search   *searchIndex
	voice    *voiceState
	files    *byteBucket
	previews *previewFetcher

	// Metrics (metrics.go): event counts, and the backup outcome counter
	// once RegisterMetrics has run.
	counters opCounters

	publishCh chan struct{}
	relayKick chan struct{}
	pubq      chan pubItem
	now       func() time.Time

	// When each channel last had an event published (in memory; a
	// channel with none since startedAt counts from startedAt), for
	// rotateForNewMember. Guarded by mu.
	lastPublish map[int64]time.Time
	lpmu        sync.Mutex // lastPublish alone: typing publishes under the read lock
	startedAt   time.Time

	// reapMu serialises recording that a maintainer reaped the community
	// (status.go).
	reapMu sync.Mutex
	// relayFailed is when each relay last failed to attach or dropped the
	// connection (cleared on attaching), so automatic mode moves to other
	// relays at once instead of waiting for the registry to drop it.
	// Guarded by rmu.
	relayFailed map[string]time.Time
	// relaysWanted is how many relays the last reconcile chose (automatic
	// mode: relay_count, or fewer when the registry has fewer). Guarded by
	// rmu.
	relaysWanted int
}

// Open loads a data directory created by Init.
func Open(ctx context.Context, cfg Config, log *slog.Logger) (*Server, error) {
	cfg.Resolve()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	s := &Server{
		cfg:         cfg,
		log:         log,
		http:        newHTTPClient(),
		clusters:    clusterset.NewCache(&http.Client{Timeout: 15 * time.Second}, 6*time.Hour),
		noDirectory: map[string]time.Time{},
		sessions:    map[*sess]struct{}{},
		relays:      map[string]*relayclient.Client{},
		attached:    map[string]context.CancelFunc{},
		dedicated:   map[string]bool{},
		uploads:     map[int64]*upload{},

		relayFailed: map[string]time.Time{},
		lastPost:    map[slowKey]int64{},
		publishCh:   make(chan struct{}, 1),
		relayKick:   make(chan struct{}, 1),
		pubq:        make(chan pubItem, 4096),
		now:         time.Now,

		lastPublish: map[int64]time.Time{},
	}
	s.startedAt = s.now()
	s.openReports.Store(-1)
	var err error
	if s.storageKey, err = cfg.readStorageKey(); err != nil {
		return nil, fmt.Errorf("storage key: %w", err)
	}
	seed, err := readHexFile(filepath.Join(cfg.DataDir, "server.key"), 32)
	if err != nil {
		return nil, fmt.Errorf("server key: %w", err)
	}
	if s.key, err = identity.FromSeed(seed); err != nil {
		return nil, err
	}
	priv, err := readHexFile(filepath.Join(cfg.DataDir, "noise.key"), 32)
	if err != nil {
		return nil, fmt.Errorf("noise key: %w", err)
	}
	if s.static, err = session.StaticFromPrivate(priv); err != nil {
		return nil, err
	}
	if err := s.openDB(ctx); err != nil {
		return nil, err
	}
	if err := s.loadModel(ctx); err != nil {
		s.db.Close()
		return nil, err
	}
	if err := s.loadPins(ctx); err != nil {
		s.db.Close()
		return nil, err
	}
	if err := s.loadStatuses(ctx); err != nil {
		s.db.Close()
		return nil, err
	}
	s.previews = newPreviewFetcher(cfg)
	if err := s.loadAutoMod(ctx); err != nil {
		s.db.Close()
		return nil, err
	}
	s.scheduleTimeoutEnds()
	if err := s.loadRecentAuthors(ctx); err != nil {
		s.db.Close()
		return nil, err
	}
	s.voice = newVoiceState()
	s.files = newByteBucket(s.settings.FileBandwidthLimit)
	if err := s.rebuildSearch(ctx); err != nil {
		s.db.Close()
		return nil, err
	}
	if err := s.recommitTopics(ctx); err != nil {
		s.db.Close()
		return nil, err
	}
	if err := s.loadInsights(ctx); err != nil {
		s.db.Close()
		return nil, fmt.Errorf("insights: %w", err)
	}
	if s.ownerKey != nil {
		s.removeClaimFile()
	}
	if err := s.writePublicKeyFile(s.key.Public); err != nil {
		s.db.Close()
		return nil, fmt.Errorf("server_public_key_file: %w", err)
	}
	return s, nil
}

// Close releases the database. Call after Run returns. It first copies the
// WAL into the database file and truncates it, so pages freed since the last
// checkpoint (a deleted month key, purged rows) don't linger in the WAL; a
// checkpoint that fails is logged, and the next start's open retries it.
func (s *Server) Close() error {
	// A backup in progress finishes first (it reads the database).
	s.bmu.Lock()
	defer s.bmu.Unlock()
	if s.previews != nil {
		s.previews.wait()
	}
	if s.search != nil {
		s.search.close()
	}
	if err := s.checkpoint(context.Background()); err != nil && s.log != nil {
		s.log.Warn("WAL checkpoint at shutdown failed", "err", err)
	}
	return s.db.Close()
}

// ServerKey returns the current server public key.
func (s *Server) ServerKey() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]byte(nil), s.key.Public...)
}

// SetClock replaces the server's clock (tests: retention and expiry boundaries).
func (s *Server) SetClock(now func() time.Time) {
	s.mu.Lock()
	s.now = now
	s.mu.Unlock()
}

// RouteID returns the opaque route id.
func (s *Server) RouteID() []byte { return s.routeID }

// Claimed reports whether an owner has claimed this server.
func (s *Server) Claimed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ownerKey != nil
}

// Run attaches to relays and serves until ctx ends.
func (s *Server) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	jobs := []func(context.Context){s.relayManager, s.publisher, s.purgeLoop, s.backupLoop, s.publishLoop, s.gcLoop, s.voiceRecheckLoop, s.presenceLoop, s.heartbeatLoop}
	for _, job := range jobs {
		wg.Add(1)
		go func(j func(context.Context)) {
			defer wg.Done()
			j(ctx)
		}(job)
	}
	<-ctx.Done()
	// Tell the maintainers first, so members see the community offline now
	// rather than after the maintainers' offline window.
	stopCtx, cancelStop := context.WithTimeout(context.Background(), 3*time.Second)
	if err := s.SendHeartbeat(stopCtx, true); err != nil {
		s.log.Debug("stopping heartbeat failed", "err", err)
	}
	cancelStop()
	s.rmu.Lock()
	for _, cancel := range s.attached {
		cancel()
	}
	for _, c := range s.relays {
		c.Close()
	}
	s.rmu.Unlock()
	s.smu.Lock()
	for ss := range s.sessions {
		ss.close("server shutting down")
	}
	s.smu.Unlock()
	wg.Wait()
	return nil
}

// ---------------------------------------------------------------- relays

// desiredRelays combines the community's relay setting with the node config.
func (s *Server) desiredRelays(ctx context.Context) []string {
	s.mu.RLock()
	set := s.settings
	s.mu.RUnlock()
	if set.RelayMode == wire.RelayMode_RELAY_MODE_PINNED && len(set.PinnedRelays) > 0 {
		return append([]string(nil), set.PinnedRelays...)
	}
	if s.cfg.RelayMode == "pinned" {
		return append([]string(nil), s.cfg.Relays...)
	}
	relays, err := s.fetchRelays(ctx)
	if err != nil || len(relays) == 0 {
		// Keep what we have rather than dropping off the network.
		s.rmu.RLock()
		defer s.rmu.RUnlock()
		var keep []string
		for u := range s.attached {
			keep = append(keep, u)
		}
		if len(keep) == 0 {
			keep = append(keep, s.cfg.Relays...)
		}
		return keep
	}
	// Automatic failover: prefer relays we're connected to, then healthy
	// ones (not failed in the last relayFailCooldown), the configured ones
	// first, then the least loaded. A relay that just failed is picked only
	// when there aren't enough others.
	s.rmu.RLock()
	now := s.now()
	healthy := func(u string) bool {
		at, failed := s.relayFailed[u]
		return !failed || now.Sub(at) > relayFailCooldown
	}
	configured := map[string]bool{}
	for _, u := range s.cfg.Relays {
		configured[u] = true
	}
	rank := func(r *wire.RelayInfo) int {
		_, connected := s.relays[r.Url]
		switch {
		case connected:
			return 0
		case !healthy(r.Url):
			return 3
		case configured[r.Url]:
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(relays, func(i, j int) bool {
		if ri, rj := rank(relays[i]), rank(relays[j]); ri != rj {
			return ri < rj
		}
		li := float64(relays[i].Connections) / float64(max(relays[i].Capacity, 1))
		lj := float64(relays[j].Connections) / float64(max(relays[j].Capacity, 1))
		return li < lj
	})
	s.rmu.RUnlock()
	var out []string
	for _, r := range relays {
		if len(out) == s.cfg.RelayCount {
			break
		}
		out = append(out, r.Url)
	}
	return out
}

// relayFailCooldown is how long a relay that failed stays behind healthy
// ones when automatic mode picks relays.
const relayFailCooldown = 2 * time.Minute

// relayFailedNow notes that url failed (to attach, or the connection
// dropped). In automatic mode the relay manager then reconsiders at once, so
// the server moves to another healthy relay and publishes its new route;
// pinned and dedicated relays are kept and retried, never replaced.
func (s *Server) relayFailedNow(url string) {
	s.rmu.Lock()
	s.relayFailed[url] = s.now()
	s.rmu.Unlock()
	if _, pinned := s.relayWanted(); !pinned {
		s.kickRelayManager()
	}
}

func (s *Server) relayManager(ctx context.Context) {
	reconcile := func() {
		want := s.desiredRelays(ctx)
		wantSet := map[string]bool{}
		for _, u := range want {
			wantSet[u] = true
		}
		s.rmu.Lock()
		s.relaysWanted = len(want)
		for u, cancel := range s.attached {
			if !wantSet[u] {
				cancel()
				delete(s.attached, u)
			}
		}
		for _, u := range want {
			if _, ok := s.attached[u]; !ok {
				actx, cancel := context.WithCancel(ctx)
				s.attached[u] = cancel
				go s.attachLoop(actx, u)
			}
		}
		s.rmu.Unlock()
	}
	reconcile()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			reconcile()
		case <-s.relaysChanged():
			reconcile()
		}
	}
}

func (s *Server) relaysChanged() <-chan struct{} { return s.relayKick }

func (s *Server) kickRelayManager() {
	select {
	case s.relayKick <- struct{}{}:
	default:
	}
}

func (s *Server) attachLoop(ctx context.Context, url string) {
	backoff := 500 * time.Millisecond
	for ctx.Err() == nil {
		c, err := relayclient.Dial(ctx, url)
		if err == nil {
			s.mu.RLock()
			key := s.key
			s.mu.RUnlock()
			err = c.RegisterEndpoint(ctx, key)
			if err == nil {
				err = s.authorizeFanout(ctx, c, key)
			}
			if err != nil {
				c.Close()
			}
		}
		if err != nil {
			s.log.Warn("relay attach failed", "relay", url, "err", err)
			s.relayFailedNow(url)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff + time.Duration(rand.Int64N(int64(backoff)/2+1))):
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = 500 * time.Millisecond
		s.rmu.Lock()
		s.relays[url] = c
		delete(s.relayFailed, url)
		s.rmu.Unlock()
		s.mu.RLock()
		key := s.key.Public
		s.mu.RUnlock()
		s.noteRelay(url, c, key)
		s.log.Info("attached to relay", "relay", url, "dedicated", s.isDedicated(c))
		if s.isDedicated(c) {
			// Blocks lifted while this relay was out of reach.
			go s.liftNetBlocks(ctx, nil, url)
		}
		s.requestPublish()
		go func() {
			<-ctx.Done()
			c.Close()
		}()
		go s.renewFanout(ctx, c)
		for {
			st, err := c.Accept(ctx)
			if err != nil {
				break
			}
			go s.serveStream(ctx, st)
		}
		s.rmu.Lock()
		if s.relays[url] == c {
			delete(s.relays, url)
		}
		s.rmu.Unlock()
		s.log.Info("detached from relay", "relay", url, "err", c.Err())
		s.requestPublish()
		s.voiceRelayLost(url)
		if ctx.Err() == nil {
			s.relayFailedNow(url)
		}
	}
}

// AttachedRelays lists relays the server is currently connected to.
func (s *Server) AttachedRelays() []string {
	s.rmu.RLock()
	defer s.rmu.RUnlock()
	out := make([]string, 0, len(s.relays))
	for u := range s.relays {
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}

func (s *Server) relayClients() []*relayclient.Client {
	s.rmu.RLock()
	defer s.rmu.RUnlock()
	out := make([]*relayclient.Client, 0, len(s.relays))
	for _, c := range s.relays {
		out = append(out, c)
	}
	return out
}

// reRegister points every relay at a new server key (after key rotation).
func (s *Server) reRegister(ctx context.Context, key *identity.Key) {
	if err := s.writePublicKeyFile(key.Public); err != nil {
		s.log.Error("writing server_public_key_file after key rotation failed", "err", err)
	}
	for _, c := range s.relayClients() {
		if err := c.RegisterEndpoint(ctx, key); err != nil {
			s.log.Warn("re-register after key rotation failed", "relay", c.URL, "err", err)
		}
		if err := s.authorizeFanout(ctx, c, key); err != nil {
			s.log.Warn("fan-out token after key rotation failed", "relay", c.URL, "err", err)
		}
	}
}

// ---------------------------------------------------------------- sessions

type sess struct {
	srv *Server
	s   *session.Session
	// The relay stream the session runs on (network blocks name it).
	stream  *relayclient.Stream
	member  []byte
	device  []byte
	name    string
	started time.Time

	mu     sync.Mutex
	status wire.MemberStatus

	out    chan []byte
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
	reason string
	// Last typing event per channel (messaging.go typing), under mu.
	typing map[int64]time.Time
}

func (ss *sess) getStatus() wire.MemberStatus {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.status
}

func (ss *sess) setStatus(st wire.MemberStatus) {
	ss.mu.Lock()
	ss.status = st
	ss.mu.Unlock()
	if st == wire.MemberStatus_MEMBER_STATUS_ACTIVE && ss.s != nil {
		ss.s.Admit()
	}
}

func (ss *sess) send(m *wire.ServerMessage) {
	b, err := proto.Marshal(m)
	if err != nil {
		return
	}
	select {
	case ss.out <- b:
	case <-ss.ctx.Done():
	default:
		ss.srv.log.Warn("session send queue full; closing", "member", identity.Fingerprint(ss.member))
		ss.close("slow consumer")
	}
}

// close ends the session. The writer flushes anything queued (a final status
// or StateChanged) and then closes the transport.
func (ss *sess) close(reason string) {
	ss.once.Do(func() {
		ss.mu.Lock()
		ss.reason = reason
		ss.mu.Unlock()
		ss.cancel()
	})
}

func (ss *sess) writer() {
	defer func() {
		ss.mu.Lock()
		reason := ss.reason
		ss.mu.Unlock()
		ss.s.Close(reason)
	}()
	for {
		select {
		case b := <-ss.out:
			if err := ss.s.Send(b); err != nil {
				ss.close("send failed")
				return
			}
		case <-ss.ctx.Done():
			for {
				select {
				case b := <-ss.out:
					if ss.s.Send(b) != nil {
						return
					}
				default:
					// Give the peer a moment to read the last frames.
					time.Sleep(100 * time.Millisecond)
					return
				}
			}
		}
	}
}

// Request size bounds. The Noise session reassembles a message of up to
// session.MaxMessage (32 MiB) before the server sees it, so the stream is
// metered here, fragment by fragment, and closed as soon as one message
// passes the bound for the caller: maxPreAdmitRequest before they are an
// admitted member (a claim with its icon is the largest such request) and
// maxMemberRequest after (a contact card with every device's key
// packages is the largest).
const (
	maxPreAdmitRequest = 512 << 10
	maxMemberRequest   = 8 << 20
)

// recvGuard counts the bytes of the request being reassembled; the
// session loop resets it after each whole request.
type recvGuard struct {
	session.Transport
	n  int64
	ss *sess // nil until the handshake is done
}

func (g *recvGuard) Recv(ctx context.Context) ([]byte, error) {
	b, err := g.Transport.Recv(ctx)
	if err != nil {
		return nil, err
	}
	limit := int64(maxPreAdmitRequest)
	if g.ss != nil && g.ss.getStatus() == wire.MemberStatus_MEMBER_STATUS_ACTIVE {
		limit = maxMemberRequest
	}
	if g.n += int64(len(b)); g.n > limit {
		g.Close("request too large")
		return nil, errors.New("request too large")
	}
	return b, nil
}

func (s *Server) serveStream(ctx context.Context, st *relayclient.Stream) {
	hctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	guard := &recvGuard{Transport: st}
	sn, err := session.Accept(hctx, guard, session.ServerConfig{
		Static:   s.static,
		Identity: s.currentKey(),
		Cert:     s.currentCert,
		// Until the peer is an active member (setStatus), it can't make
		// the server reassemble more than session.PreAdmissionMaxMessage.
		PreAdmission: true,
	})
	cancel()
	if err != nil {
		s.log.Debug("handshake failed", "err", err)
		st.Close("handshake failed")
		return
	}
	sctx, scancel := context.WithCancel(ctx)
	ss := &sess{
		srv:     s,
		s:       sn,
		stream:  st,
		member:  sn.Client.IdentityKey,
		device:  sn.PeerStatic,
		name:    truncate(sn.Client.DeviceName, 64),
		started: s.now(),
		out:     make(chan []byte, 1024),
		ctx:     sctx,
		cancel:  scancel,
	}
	guard.n, guard.ss = 0, ss
	go ss.writer()
	status := s.admitSession(ctx, ss)
	if status == wire.MemberStatus_MEMBER_STATUS_ACTIVE {
		s.insightSeen(ss.member)
	}
	ss.setStatus(status)
	ss.send(&wire.ServerMessage{Body: &wire.ServerMessage_Status{Status: &wire.SessionStatus{Status: status, Claimed: s.Claimed()}}})
	if status == wire.MemberStatus_MEMBER_STATUS_BANNED {
		s.applyPendingNetBlock(ctx, ss.member, st)
	}
	switch status {
	case wire.MemberStatus_MEMBER_STATUS_BANNED, wire.MemberStatus_MEMBER_STATUS_REMOVED,
		wire.MemberStatus_MEMBER_STATUS_DEVICE_REVOKED, wire.MemberStatus_MEMBER_STATUS_DEVICE_LIMIT:
		// Tell the client, then hang up.
		ss.close("not admitted")
		return
	}
	s.noteInvisible(ss.member)
	for _, old := range s.capSessions(ss) {
		old.close("replaced by a newer session")
	}
	s.presenceChanged(ss.member)
	defer func() {
		s.smu.Lock()
		delete(s.sessions, ss)
		s.smu.Unlock()
		s.presenceChanged(ss.member)
		s.voiceDisconnect(ss)
		ss.close("session ended")
	}()

	limiter := newByteBucket(0)
	limiter.rate, limiter.burst, limiter.tokens = 100, 200, 200
	// Before admission a session gets a tenth of that, and GetInfo, whose
	// reply carries the community icon (up to 256 KiB) and is open to
	// anyone, one a second (the join preview asks once).
	pre := newByteBucket(0)
	pre.rate, pre.burst, pre.tokens = 10, 20, 20
	info := newByteBucket(0)
	info.rate, info.burst, info.tokens = 1, 3, 3
	for {
		b, err := sn.Recv(sctx)
		if err != nil {
			return
		}
		guard.n = 0
		if limiter.wait(sctx, 1) != nil {
			return
		}
		if ss.getStatus() != wire.MemberStatus_MEMBER_STATUS_ACTIVE && pre.wait(sctx, 1) != nil {
			return
		}
		var m wire.ClientMessage
		if err := proto.Unmarshal(b, &m); err != nil || m.Id == 0 {
			ss.send(&wire.ServerMessage{Body: errBody(errInvalid("malformed request"))})
			continue
		}
		if _, ok := m.Body.(*wire.ClientMessage_GetInfo); ok && !info.take(1) {
			ss.send(&wire.ServerMessage{ReplyTo: m.Id, Body: errBody(errRateLimited("too many requests for the community's info; try again in a moment"))})
			continue
		}
		reply := s.dispatch(sctx, ss, &m)
		reply.ReplyTo = m.Id
		ss.send(reply)
	}
}

func (s *Server) currentKey() *identity.Key {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.key
}

func (s *Server) currentCert() *wire.SignedServerCert {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cert
}

// admitSession checks the member roll for a new session and records new
// device keys (docs/protocol.md §4.2, §10).
func (s *Server) admitSession(ctx context.Context, ss *sess) wire.MemberStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	banned, err := s.banned(ctx, ss.member)
	if err != nil || banned {
		// Fail closed: a ban list we can't read admits nobody.
		if err != nil {
			s.log.Error("reading the ban list failed", "err", err)
		}
		return wire.MemberStatus_MEMBER_STATUS_BANNED
	}
	dev, err := s.loadDevice(ctx, ss.device)
	if err != nil {
		s.log.Error("reading a device record failed", "err", err)
		return wire.MemberStatus_MEMBER_STATUS_DEVICE_REVOKED
	}
	if dev != nil && (dev.Revoked || string(dev.Member) != string(ss.member)) {
		return wire.MemberStatus_MEMBER_STATUS_DEVICE_REVOKED
	}
	m := s.members[string(ss.member)]
	if m == nil {
		return wire.MemberStatus_MEMBER_STATUS_UNKNOWN
	}
	switch m.Status {
	case wire.MemberStatus_MEMBER_STATUS_ACTIVE, wire.MemberStatus_MEMBER_STATUS_PENDING:
	default:
		return m.Status
	}
	if dev != nil {
		dev.LastSeen, dev.Name = s.now().UnixMilli(), ss.name
		if err := s.saveDevice(ctx, s.db, dev); err != nil {
			s.log.Error("database write failed", "err", err)
		}
		return m.Status
	}
	if err := s.recordDevice(ctx, ss.member, ss.device, ss.name); err != nil {
		if errors.Is(err, errDeviceLimit) {
			return wire.MemberStatus_MEMBER_STATUS_DEVICE_LIMIT
		}
		s.log.Error("record device", "err", err)
	}
	return m.Status
}

var errDeviceLimit = errors.New("device limit reached")

// recordDevice adds a device key for member; callers hold s.mu.
func (s *Server) recordDevice(ctx context.Context, member, device []byte, name string) error {
	if d, err := s.loadDevice(ctx, device); err != nil || d != nil {
		return err
	}
	devs, err := s.devicesOf(ctx, member)
	if err != nil {
		return err
	}
	active := 0
	for _, d := range devs {
		if !d.Revoked {
			active++
		}
	}
	if uint32(active) >= s.settings.MaxDevicesPerMember {
		return errDeviceLimit
	}
	now := s.now().UnixMilli()
	return s.saveDevice(ctx, s.db, &deviceRec{DeviceKey: device, Member: member, Name: name, FirstSeen: now, LastSeen: now})
}

// capSessions registers ss and returns the sessions it displaces: the
// oldest live session of the same device key once that key has
// MaxSessionsPerDevice, and the oldest of the member once they have
// MaxSessionsPerMember. Each session costs a goroutine, a send queue and
// a state build per broadcast; a device that reconnects while its old
// sessions linger replaces them rather than being refused.
func (s *Server) capSessions(ss *sess) []*sess {
	s.smu.Lock()
	defer s.smu.Unlock()
	s.sessions[ss] = struct{}{}
	var out []*sess
	gone := map[*sess]bool{}
	for _, limit := range []struct {
		same func(*sess) bool
		max  int
	}{
		{func(o *sess) bool { return string(o.device) == string(ss.device) }, s.cfg.MaxSessionsPerDevice},
		{func(o *sess) bool { return string(o.member) == string(ss.member) }, s.cfg.MaxSessionsPerMember},
	} {
		n := 0
		var oldest *sess
		for o := range s.sessions {
			if o == ss || gone[o] || o.ctx.Err() != nil || !limit.same(o) {
				continue
			}
			n++
			if oldest == nil || o.started.Before(oldest.started) {
				oldest = o
			}
		}
		if n >= limit.max && oldest != nil {
			gone[oldest] = true
			out = append(out, oldest)
		}
	}
	return out
}

func (s *Server) sessionsFor(member []byte) []*sess {
	s.smu.Lock()
	defer s.smu.Unlock()
	var out []*sess
	for ss := range s.sessions {
		if string(ss.member) == string(member) {
			out = append(out, ss)
		}
	}
	return out
}

func (s *Server) allSessions() []*sess {
	s.smu.Lock()
	defer s.smu.Unlock()
	out := make([]*sess, 0, len(s.sessions))
	for ss := range s.sessions {
		out = append(out, ss)
	}
	return out
}

// broadcastState pushes each active session its own view of the community.
func (s *Server) broadcastState() {
	for _, ss := range s.allSessions() {
		if ss.getStatus() != wire.MemberStatus_MEMBER_STATUS_ACTIVE {
			continue
		}
		st := s.buildState(ss.member)
		if st == nil {
			continue
		}
		ss.send(&wire.ServerMessage{Body: &wire.ServerMessage_StateChanged{StateChanged: &wire.StateChanged{State: st}}})
	}
}

// ---------------------------------------------------------------- channel fan-out

// publishEvent encrypts ev with the channel key, signs it with the server key,
// and queues it for publication once to the channel topic on every attached
// relay. Callers hold s.mu; the network writes happen on the publisher
// goroutine, in order.
func (s *Server) publishEvent(ch *channelRec, ev *wire.ChannelEvent) {
	pt, err := proto.Marshal(ev)
	if err != nil {
		return
	}
	aead, err := chacha20poly1305.New(ch.Key)
	if err != nil {
		return
	}
	nonce := randomBytes(aead.NonceSize())
	env, _ := proto.Marshal(&wire.ChannelEnvelope{
		ChannelId:  ch.ID,
		KeyEpoch:   ch.Epoch,
		Nonce:      nonce,
		Ciphertext: aead.Seal(nil, nonce, pt, ChannelAAD(ch.ID, ch.Epoch)),
	})
	signed, _ := proto.Marshal(&wire.SignedEnvelope{Envelope: env, Signature: s.key.Sign(identity.CtxChannelEnvelope, env)})
	if s.lastPublish != nil {
		s.lpmu.Lock()
		s.lastPublish[ch.ID] = s.now()
		s.lpmu.Unlock()
	}
	select {
	case s.pubq <- pubItem{topic: ch.Topic, data: signed}:
	default:
		// Members catch up from history; dropping beats blocking the server.
		s.log.Warn("publish queue full; dropping a channel event", "channel", ch.ID)
	}
}

type pubItem struct {
	topic, data []byte
}

func (s *Server) publisher(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case it := <-s.pubq:
			for _, c := range s.relayClients() {
				// Channel envelopes are end-to-end encrypted: relays with a
				// chunk cache may keep them for members that reconnect.
				if err := c.PublishCacheable(it.topic, it.data); err != nil {
					s.log.Debug("publish failed", "relay", c.URL, "err", err)
				}
			}
		}
	}
}

// ChannelAAD is the associated data for channel event encryption.
func ChannelAAD(channelID int64, epoch uint32) []byte {
	aad := []byte("openarcanum/channel-event/v1")
	aad = append(aad, be64(channelID)...)
	return append(aad, be32(epoch)...)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// ---------------------------------------------------------------- init

// removeClaimFile deletes the claim code's file (ClaimFile) once the server
// is claimed: the code no longer works, and nobody should copy it around.
func (s *Server) removeClaimFile() {
	if s.cfg.ClaimFile == "" {
		return
	}
	if err := os.Remove(s.cfg.ClaimFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.log.Warn("removing the claim file", "err", err)
	}
}

// InitResult is what `community init` prints.
type InitResult struct {
	ServerKey []byte
	RouteID   []byte
	ClaimCode string
}

// Init creates a new data directory: server key, Noise key, storage key, an
// empty database and a one-time claim secret.
func Init(ctx context.Context, cfg Config) (*InitResult, error) {
	cfg.Resolve()
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "server.key")); err == nil {
		return nil, fmt.Errorf("%s is already initialized", cfg.DataDir)
	}
	key, err := identity.Generate()
	if err != nil {
		return nil, err
	}
	static, err := session.GenerateStatic()
	if err != nil {
		return nil, err
	}
	if err := identity.WriteSeed(filepath.Join(cfg.DataDir, "server.key"), key); err != nil {
		return nil, err
	}
	if err := writeHexFile(filepath.Join(cfg.DataDir, "noise.key"), static.Private); err != nil {
		return nil, err
	}
	if _, err := os.Stat(cfg.StorageKeyFile); cfg.StorageKey == nil && errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(cfg.StorageKeyFile), 0o700); err != nil {
			return nil, err
		}
		if err := writeHexFile(cfg.StorageKeyFile, randomBytes(32)); err != nil {
			return nil, err
		}
	}
	storageKey, err := cfg.readStorageKey()
	if err != nil {
		return nil, fmt.Errorf("storage key: %w", err)
	}
	s := &Server{cfg: cfg, storageKey: storageKey}
	if err := s.openDB(ctx); err != nil {
		return nil, err
	}
	defer s.db.Close()
	secret := randomBytes(32)
	routeID := randomBytes(16)
	if err := s.setMeta(ctx, s.db, "claim_hash", hashSecret(string(secret))); err != nil {
		return nil, err
	}
	if err := s.setMeta(ctx, s.db, "route_id", routeID); err != nil {
		return nil, err
	}
	relays := cfg.Relays
	if len(relays) == 0 && cfg.RelayMode == "automatic" {
		// The app reaches an unclaimed server through the relays its claim
		// code names: in automatic mode, the ones the maintainers list now
		// (the server attaches to some of them when it starts).
		if relays, err = registryRelays(ctx, cfg.Maintainers); err != nil {
			return nil, fmt.Errorf("no relays for the claim code: %w", err)
		}
	}
	code, err := EncodeCode(ClaimPrefix, &wire.ClaimCode{ServerKey: key.Public, RouteId: routeID, Relays: relays, Secret: secret})
	if err != nil {
		return nil, err
	}
	return &InitResult{ServerKey: key.Public, RouteID: routeID, ClaimCode: code}, nil
}

// Code prefixes for strings people paste (docs/protocol.md §5).
const (
	InvitePrefix = "openarcanum-invite:"
	ClaimPrefix  = "openarcanum-claim:"
	MovePrefix   = "openarcanum-move:"
)

// EncodeCode renders prefix + base64url(proto).
func EncodeCode(prefix string, m proto.Message) (string, error) {
	b, err := proto.Marshal(m)
	if err != nil {
		return "", err
	}
	return prefix + identity.Encode(b), nil
}

func checkSecret(hash []byte, secret []byte) bool {
	return len(hash) > 0 && subtle.ConstantTimeCompare(hash, hashSecret(string(secret))) == 1
}
