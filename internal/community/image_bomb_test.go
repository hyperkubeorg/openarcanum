package community_test

import (
	"encoding/binary"
	"hash/crc32"
	"testing"

	"github.com/hyperkubeorg/openarcanum/internal/community"
	"github.com/hyperkubeorg/openarcanum/internal/testutil"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// pngHeader is a PNG signature and IHDR chunk declaring a w × h RGBA
// canvas: a few dozen bytes a client would try to decode into w*h*4.
func pngHeader(w, h uint32) []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 6 // 8-bit RGBA
	chunk := append([]byte("IHDR"), ihdr...)
	b := []byte("\x89PNG\r\n\x1a\n")
	b = binary.BigEndian.AppendUint32(b, 13)
	b = append(b, chunk...)
	b = binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(chunk))
	return append(b, make([]byte, 64)...)
}

// Avatars and the community icon, which every member's client decodes,
// are limited in pixels from their header (decompression bombs), not only
// sniffed by their first bytes.
func TestImagePixelLimits(t *testing.T) {
	if community.IsImage(pngHeader(50000, 50000)) || community.IsImage(pngHeader(8193, 10)) {
		t.Fatal("an oversized canvas passed")
	}
	if !community.IsImage(pngHeader(4096, 4096)) {
		t.Fatal("a 4096 × 4096 canvas refused")
	}
	e := setup(t, nil)
	ctx := testutil.Ctx(t)
	alice := e.member("Alice", e.invite.Secret)
	_, err := alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_SetProfile{SetProfile: &wire.SetProfile{Avatar: pngHeader(30000, 30000)}}})
	wantErr(t, err, "megapixels")
	if _, err := alice.Call(ctx, &wire.ClientMessage{Body: &wire.ClientMessage_SetProfile{SetProfile: &wire.SetProfile{Avatar: pngOf(t, 64, 64, "")}}}); err != nil {
		t.Fatal(err)
	}
	s := cloneSettings(fresh(t, e.owner).Settings)
	s.Icon = pngHeader(30000, 30000)
	_, err = e.adminAs(e.owner, &wire.AdminAction{Op: &wire.AdminAction_UpdateSettings{UpdateSettings: s}})
	wantErr(t, err, "megapixels")
}
