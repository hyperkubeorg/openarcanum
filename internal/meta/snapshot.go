package meta

import (
	"net/http"
	"strconv"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Directory snapshot for local search (docs/protocol.md §7.2). A client downloads every listed community's signed listing, page
// by page, verifies each one like a search result and searches on the
// device, so no maintainer sees its queries. Icons are left out (each
// listing signs its icon's hash; a client fetches the full listing when it
// opens one).
const (
	// SnapshotPageListings and SnapshotPageBytes bound one page.
	SnapshotPageListings = 500
	SnapshotPageBytes    = 4 << 20
)

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	if s.refuseDirectory(w, r) {
		return
	}
	after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if err != nil && r.URL.Query().Get("after") != "" {
		http.Error(w, "bad cursor", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	out := &wire.DirectorySnapshot{GeneratedAt: s.now().UnixMilli(), Next: uint64(max(after, 0))}
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM listings WHERE withdrawn = 0`).Scan(&total); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out.Total = uint64(total)
	// One more row than a page tells whether another page follows.
	rows, err := s.db.QueryContext(ctx, `SELECT id, signed FROM listings WHERE withdrawn = 0 AND id > ? ORDER BY id LIMIT ?`, max(after, 0), SnapshotPageListings+1)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	size := 0
	for rows.Next() {
		var id int64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		var sl wire.SignedListing
		if unmarshalKnown(raw, &sl) != nil {
			continue
		}
		sl.Icon = nil
		n := proto.Size(&sl)
		if len(out.Listings) == SnapshotPageListings || (len(out.Listings) > 0 && size+n > SnapshotPageBytes) {
			out.More = true
			break
		}
		out.Listings = append(out.Listings, &sl)
		out.Next = uint64(id)
		size += n
	}
	writeProto(w, out)
}
