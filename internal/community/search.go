package community

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hyperkubeorg/openarcanum/internal/store"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// searchIndex is an in-memory FTS5 index over decrypted message text. Keeping
// it in memory means no plaintext index ever reaches the disk; it's rebuilt
// from the encrypted messages at startup (docs/protocol.md §6.4).
//
// Index updates never wait for a search. Posting, editing and deleting run
// under the server's model lock, so they only queue the change (qmu is held
// for an append) and whoever holds mu applies the queue: the poster itself
// when the index is free, otherwise the search holding it, before its query
// and again as it lets go. A search therefore sees every change made before
// it started, and a slow search can't stall the server.
type searchIndex struct {
	mu sync.Mutex
	db *sql.DB

	qmu     sync.Mutex
	pending []indexOp
	// logErr reports a failed index change (never its text).
	logErr func(error)
}

// indexOp is one queued index change: put (id, channel, text), drop one
// message (del with id), or drop a whole channel (del with id 0).
type indexOp struct {
	id, channel int64
	text        string
	del         bool
}

func (x *searchIndex) close() {
	x.lock()
	defer x.mu.Unlock()
	if x.db != nil {
		x.db.Close()
		x.db = nil
	}
}

// lock takes mu and applies what is queued.
func (x *searchIndex) lock() {
	x.mu.Lock()
	x.drainLocked()
}

// unlock applies what was queued meanwhile and releases mu. A change queued
// between the last drain and the release is applied here too when mu is
// free again, or else by whoever holds it then.
func (x *searchIndex) unlock() {
	for {
		x.drainLocked()
		x.mu.Unlock()
		x.qmu.Lock()
		more := len(x.pending) > 0
		x.qmu.Unlock()
		if !more || !x.mu.TryLock() {
			return
		}
	}
}

// enqueue queues a change and applies it at once unless a search (or a
// rebuild's swap) holds the index.
func (x *searchIndex) enqueue(op indexOp) {
	x.qmu.Lock()
	x.pending = append(x.pending, op)
	x.qmu.Unlock()
	if x.mu.TryLock() {
		x.unlock()
	}
}

func (x *searchIndex) drainLocked() {
	x.qmu.Lock()
	ops := x.pending
	x.pending = nil
	x.qmu.Unlock()
	if x.db == nil {
		return
	}
	for _, op := range ops {
		var err error
		switch {
		case op.del && op.id == 0:
			_, err = x.db.Exec(`DELETE FROM idx WHERE channel_id = ?`, op.channel)
		case op.del:
			_, err = x.db.Exec(`DELETE FROM idx WHERE rowid = ?`, op.id)
		default:
			if _, err = x.db.Exec(`DELETE FROM idx WHERE rowid = ?`, op.id); err == nil {
				_, err = x.db.Exec(`INSERT INTO idx (rowid, body, channel_id, msg_id) VALUES (?, ?, ?, ?)`, op.id, op.text, op.channel, op.id)
			}
		}
		if err != nil && x.logErr != nil {
			x.logErr(err)
		}
	}
}

func (s *Server) rebuildSearch(ctx context.Context) error {
	if s.search == nil {
		s.search = &searchIndex{logErr: func(err error) { s.log.Error("search index update", "err", err) }}
	}
	s.search.close()
	s.mu.RLock()
	enabled := s.settings.SearchEnabled
	s.mu.RUnlock()
	if !enabled {
		return nil
	}
	// temp_store(2): FTS5's sorts and merges stay in memory too, never in
	// temporary files.
	db, err := store.Open(":memory:", "temp_store(2)")
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `CREATE VIRTUAL TABLE idx USING fts5(body, channel_id UNINDEXED, msg_id UNINDEXED, tokenize = 'unicode61 remove_diacritics 2')`); err != nil {
		db.Close()
		return err
	}
	if err := s.fillSearch(ctx, db); err != nil {
		db.Close()
		return err
	}
	s.search.mu.Lock()
	s.search.db = db
	s.search.unlock()
	return nil
}

// searchBatch is how many rows each index transaction takes during a rebuild.
const searchBatch = 2000

// fillSearch streams every undeleted message into the empty index db: one
// stored row is decrypted and inserted at a time, committed every
// searchBatch rows, so the rebuild's own working memory stays bounded however
// long the history is (only the index itself grows with it;
// docs/operations.md "Search index").
func (s *Server) fillSearch(ctx context.Context, db *sql.DB) error {
	rows, err := s.db.QueryContext(ctx, `SELECT `+msgCols+` FROM messages WHERE deleted = 0`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var tx *sql.Tx
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()
	n := 0
	for rows.Next() {
		r, err := scanStored(rows)
		if err != nil {
			return err
		}
		m, err := s.openBody(ctx, r)
		if err != nil {
			// Reported, not silently dropped: the message stays out of the
			// index, and history requests that reach it fail with the same error.
			s.log.Error("stored message unreadable; left out of the search index", "message", r.id, "err", err)
			continue
		}
		text := m.Text
		if len(m.Files) > 0 {
			s.mu.RLock()
			for _, f := range m.Files {
				text += " " + s.fileRef(ctx, f.FileId).Name
			}
			s.mu.RUnlock()
		}
		if tx == nil {
			if tx, err = db.BeginTx(ctx, nil); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO idx (rowid, body, channel_id, msg_id) VALUES (?, ?, ?, ?)`, r.id, text, r.channelID, r.id); err != nil {
			return err
		}
		if n++; n%searchBatch == 0 {
			err := tx.Commit()
			tx = nil
			if err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if tx != nil {
		err := tx.Commit()
		tx = nil
		return err
	}
	return nil
}

// indexMessage queues a message's (new) text for the index. Callers may
// hold s.mu: it never waits for a search.
func (s *Server) indexMessage(id, channelID int64, text string, files []*wire.FileRef) {
	if s.search == nil {
		return
	}
	for _, f := range files {
		text += " " + f.Name
	}
	s.search.enqueue(indexOp{id: id, channel: channelID, text: text})
}

func (s *Server) unindexMessage(id int64) {
	if s.search == nil {
		return
	}
	s.search.enqueue(indexOp{id: id, del: true})
}

func (s *Server) unindexChannel(channelID int64) {
	if s.search == nil {
		return
	}
	s.search.enqueue(indexOp{channel: channelID, del: true})
}

// Search limits. A query is at most maxSearchQuery bytes and maxSearchTerms
// words of at most maxSearchTerm bytes. A word becomes a prefix match only
// from minPrefixRunes characters: a one- or two-letter prefix expands to a
// large part of the vocabulary, and a handful of `a*` terms could take tens
// of seconds over a long history. Each search runs for at most
// searchTimeout.
const (
	maxSearchQuery = 256
	maxSearchTerms = 5
	maxSearchTerm  = 64
	minPrefixRunes = 3
	searchRowLimit = 1000
)

// searchTimeout bounds one search (a variable for tests).
var searchTimeout = 2 * time.Second

// ftsQuery turns user input into a safe FTS5 query: each word becomes a
// quoted term (a prefix term from minPrefixRunes characters), so operators
// and quotes in the input can't break the syntax.
func ftsQuery(q string) string {
	var terms []string
	for _, w := range strings.FieldsFunc(q, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		if len(terms) == maxSearchTerms {
			break
		}
		if len(w) > maxSearchTerm {
			w = w[:maxSearchTerm]
			for !utf8.ValidString(w) {
				w = w[:len(w)-1]
			}
		}
		if utf8.RuneCountInString(w) >= minPrefixRunes {
			terms = append(terms, `"`+w+`"*`)
		} else {
			terms = append(terms, `"`+w+`"`)
		}
	}
	return strings.Join(terms, " ")
}

// searchMessages searches readable channels only (results never
// include a channel the searcher can't read).
func (s *Server) searchMessages(ctx context.Context, ss *sess, q *wire.SearchQuery) (*wire.ServerMessage, error) {
	s.mu.RLock()
	if err := s.allowed(FeatureSearch, nil, nil, 0); err != nil {
		s.mu.RUnlock()
		return nil, err
	}
	m := s.members[string(ss.member)]
	readable := map[int64]bool{}
	const need = PermViewChannel | PermReadMessageHistory
	for id, c := range s.channels {
		if s.channelPerms(m, c)&need == need && (q.ChannelId == 0 || q.ChannelId == id) {
			readable[id] = true
		}
	}
	s.mu.RUnlock()
	if len(q.Query) > maxSearchQuery {
		return nil, errInvalid("searches are at most 256 characters")
	}
	match := ftsQuery(q.Query)
	if match == "" {
		return nil, errInvalid("enter something to search for")
	}
	limit := int(q.Limit)
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	ids, err := s.search.query(ctx, match, readable, limit)
	if err != nil {
		return nil, err
	}

	out := &wire.SearchResults{}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, id := range ids {
		r, err := scanStored(s.db.QueryRowContext(ctx, `SELECT `+msgCols+` FROM messages WHERE id = ?`, id))
		if err != nil || r.deleted || !readable[r.channelID] {
			continue
		}
		// Re-check against the current model: permissions may have changed
		// since the index lookup.
		m, c := s.members[string(ss.member)], s.channels[r.channelID]
		if s.channelPerms(m, c)&need != need || !s.historyVisible(m, c, r.created) {
			continue
		}
		msg, err := s.scanMessage(ctx, r)
		if err != nil {
			return nil, err
		}
		out.Messages = append(out.Messages, msg)
	}
	return &wire.ServerMessage{Body: &wire.ServerMessage_SearchResults{SearchResults: out}}, nil
}

// errSearchTooSlow is the reply to a search cut off by searchTimeout.
func errSearchTooSlow() error {
	return errRateLimited("that search took too long; try longer or more specific words")
}

// query runs one FTS match, bounded by searchTimeout (the sqlite driver
// interrupts the statement when its context ends), and returns up to limit
// message ids in readable channels.
func (x *searchIndex) query(ctx context.Context, match string, readable map[int64]bool, limit int) ([]int64, error) {
	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	x.lock()
	defer x.unlock()
	if x.db == nil {
		return nil, nil
	}
	rows, err := x.db.QueryContext(ctx, `SELECT msg_id, channel_id FROM idx WHERE idx MATCH ? ORDER BY rank LIMIT ?`, match, searchRowLimit)
	if err != nil {
		if ctx.Err() != nil {
			return nil, errSearchTooSlow()
		}
		return nil, errInvalid("couldn't understand that search")
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id, cid int64
		if rows.Scan(&id, &cid) == nil && readable[cid] {
			ids = append(ids, id)
			if len(ids) == limit {
				break
			}
		}
	}
	if rows.Err() != nil && ctx.Err() != nil {
		return nil, errSearchTooSlow()
	}
	return ids, nil
}
