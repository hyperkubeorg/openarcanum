package meta_test

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/meta"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// FuzzBlobDecoders feeds arbitrary bodies to every blob request decoder.
// None may panic, and anything accepted must meet the documented shape.
func FuzzBlobDecoders(f *testing.F) {
	h := bytes.Repeat([]byte{1}, 32)
	for _, m := range []proto.Message{
		&wire.BlobCreate{Hash: h, Size: 10, TokenHashes: [][]byte{h}},
		&wire.BlobCreate{Hash: h, Size: 10, TokenHashes: [][]byte{h, h}},
		&wire.BlobChunk{Hash: h, Upload: h, Index: 3, Data: []byte("abc")},
		&wire.BlobFetch{Hash: h, Token: h, Index: 1},
		&wire.BlobAck{Hash: h, Token: h},
	} {
		b, _ := proto.Marshal(m)
		f.Add(b)
	}
	f.Add([]byte{})
	f.Add([]byte{0x0a, 0xff, 0xff, 0xff, 0xff, 0x0f})
	f.Fuzz(func(t *testing.T, body []byte) {
		if c, err := meta.DecodeBlobCreate(body); err == nil {
			if len(c.Hash) != 32 || c.Size == 0 || len(c.TokenHashes) == 0 || len(c.TokenHashes) > meta.MaxBlobTokens {
				t.Fatalf("accepted bad create: %v", c)
			}
			seen := map[string]bool{}
			for _, th := range c.TokenHashes {
				if len(th) != 32 || seen[string(th)] {
					t.Fatalf("accepted bad token hashes: %v", c)
				}
				seen[string(th)] = true
			}
		}
		if c, err := meta.DecodeBlobChunk(body); err == nil && (len(c.Hash) != 32 || len(c.Upload) != 32 || len(c.Data) == 0 || len(c.Data) > meta.BlobChunkSize) {
			t.Fatalf("accepted bad chunk: %v", c)
		}
		if fe, err := meta.DecodeBlobFetch(body); err == nil && (len(fe.Hash) != 32 || len(fe.Token) != 32) {
			t.Fatalf("accepted bad fetch: %v", fe)
		}
		if a, err := meta.DecodeBlobAck(body); err == nil && (len(a.Hash) != 32 || len(a.Token) != 32) {
			t.Fatalf("accepted bad ack: %v", a)
		}
	})
}
