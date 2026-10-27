package meta

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Contact cards for permanent contact codes (docs/protocol.md §9.2): the
// owner keeps its current SignedContactCard here under an address only it
// can write to, and whoever was given the code reads it to start a direct
// message. The node stores the card as it is: readers check it against the
// identity in the code.

const contactCardSchema = `
CREATE TABLE contact_cards (
	address BLOB PRIMARY KEY,
	card BLOB NOT NULL,
	updated_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL
);
CREATE INDEX contact_cards_expires ON contact_cards (expires_at);
`

const (
	// MaxContactCard is a card's size limit, as in community servers.
	MaxContactCard = 64 << 10
	// contactCardTTL: a card not replaced for this long is deleted. The
	// owner's client replaces it at least every few days (its fallback key).
	contactCardTTL = 60 * 24 * time.Hour
	// maxContactCards bounds the table (addresses are free to pick).
	maxContactCards = 200_000
)

// ContactCardAddress is where a card is kept: SHA-256 of the owner's token.
func ContactCardAddress(token []byte) []byte {
	h := sha256.New()
	h.Write([]byte("openarcanum/contact-card/v1\x00"))
	h.Write(token)
	return h.Sum(nil)
}

func (s *Server) contactCardPut(r *http.Request, body []byte) (proto.Message, error) {
	var p wire.ContactCardPut
	if err := proto.Unmarshal(body, &p); err != nil || len(p.Token) != 32 || len(p.Card) == 0 {
		return nil, bad("malformed contact card")
	}
	if len(p.Card) > MaxContactCard {
		return nil, &httpError{http.StatusRequestEntityTooLarge, "contact card too large"}
	}
	addr := ContactCardAddress(p.Token)
	now := s.now()
	ctx := r.Context()
	s.mu.Lock()
	defer s.mu.Unlock()
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM contact_cards WHERE address = ?`, addr).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM contact_cards`).Scan(&n); err != nil {
			return nil, err
		}
		if n >= maxContactCards {
			s.log.Warn("contact cards full", "count", n)
			return nil, &httpError{http.StatusInsufficientStorage, "this node keeps no more contact cards; try another maintainer"}
		}
	} else if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO contact_cards (address, card, updated_at, expires_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (address) DO UPDATE SET card = excluded.card, updated_at = excluded.updated_at, expires_at = excluded.expires_at`,
		addr, p.Card, now.UnixMilli(), now.Add(contactCardTTL).UnixMilli())
	return nil, err
}

func (s *Server) contactCardDelete(r *http.Request, body []byte) (proto.Message, error) {
	var d wire.ContactCardDelete
	if err := proto.Unmarshal(body, &d); err != nil || len(d.Token) != 32 {
		return nil, bad("malformed delete")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(r.Context(), `DELETE FROM contact_cards WHERE address = ?`, ContactCardAddress(d.Token))
	return nil, err
}

func (s *Server) contactCardGet(w http.ResponseWriter, r *http.Request) {
	addr, err := base64.RawURLEncoding.Strict().DecodeString(r.PathValue("address"))
	if err != nil || len(addr) != sha256.Size {
		http.Error(w, "bad address", http.StatusBadRequest)
		return
	}
	out := &wire.ContactCardFetched{}
	err = s.db.QueryRowContext(r.Context(), `SELECT card, updated_at FROM contact_cards WHERE address = ? AND expires_at > ?`,
		addr, s.now().UnixMilli()).Scan(&out.Card, &out.UpdatedAt)
	if err != nil {
		http.Error(w, "no contact card here", http.StatusNotFound)
		return
	}
	writeProto(w, out)
}
