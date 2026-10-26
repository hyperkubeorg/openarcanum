package session

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/flynn/noise"
)

// The Noise community test vectors, shared with the Python client's
// tests/test_noise_vectors.py. Sources: that directory's README.md.
var vectorDir = filepath.Join("..", "..", "clients", "desktop", "tests", "vectors", "noise")

var vectorFiles = []string{"cacophony.txt", "snow.txt"}

// What flynn/noise can express: Curve25519, the fundamental patterns and at
// most one PSK modifier.
var goSupported = regexp.MustCompile(
	`^Noise_(N|K|X|NN|KN|NK|KK|NX|KX|XN|IN|XK|IK|XX|IX)(?:psk(\d))?_25519_(ChaChaPoly|AESGCM)_(SHA256|SHA512|BLAKE2s|BLAKE2b)$`)

var patterns = map[string]noise.HandshakePattern{
	"N": noise.HandshakeN, "K": noise.HandshakeK, "X": noise.HandshakeX,
	"NN": noise.HandshakeNN, "KN": noise.HandshakeKN, "NK": noise.HandshakeNK,
	"KK": noise.HandshakeKK, "NX": noise.HandshakeNX, "KX": noise.HandshakeKX,
	"XN": noise.HandshakeXN, "IN": noise.HandshakeIN, "XK": noise.HandshakeXK,
	"IK": noise.HandshakeIK, "XX": noise.HandshakeXX, "IX": noise.HandshakeIX,
}

var ciphers = map[string]noise.CipherFunc{"ChaChaPoly": noise.CipherChaChaPoly, "AESGCM": noise.CipherAESGCM}

var hashes = map[string]noise.HashFunc{
	"SHA256": noise.HashSHA256, "SHA512": noise.HashSHA512,
	"BLAKE2s": noise.HashBLAKE2s, "BLAKE2b": noise.HashBLAKE2b,
}

type hexBytes []byte

func (h *hexBytes) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	d, err := hex.DecodeString(s)
	*h = d
	return err
}

type vector struct {
	Name             string     `json:"protocol_name"`
	InitPrologue     hexBytes   `json:"init_prologue"`
	InitPSKs         []hexBytes `json:"init_psks"`
	InitStatic       hexBytes   `json:"init_static"`
	InitEphemeral    hexBytes   `json:"init_ephemeral"`
	InitRemoteStatic hexBytes   `json:"init_remote_static"`
	RespPrologue     hexBytes   `json:"resp_prologue"`
	RespPSKs         []hexBytes `json:"resp_psks"`
	RespStatic       hexBytes   `json:"resp_static"`
	RespEphemeral    hexBytes   `json:"resp_ephemeral"`
	RespRemoteStatic hexBytes   `json:"resp_remote_static"`
	HandshakeHash    hexBytes   `json:"handshake_hash"`
	Messages         []struct {
		Payload    hexBytes `json:"payload"`
		Ciphertext hexBytes `json:"ciphertext"`
	} `json:"messages"`
}

func readSums(t *testing.T) map[string]string {
	f, err := os.Open(filepath.Join(vectorDir, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sums := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 {
			sums[fields[1]] = fields[0]
		}
	}
	return sums
}

func loadVectors(t *testing.T, name string, sums map[string]string) []vector {
	raw, err := os.ReadFile(filepath.Join(vectorDir, name))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != sums[name] {
		t.Fatalf("%s: sha256 %s, SHA256SUMS says %s", name, got, sums[name])
	}
	var file struct {
		Vectors []vector `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	return file.Vectors
}

func dhKey(priv []byte) noise.DHKey {
	if len(priv) == 0 {
		return noise.DHKey{}
	}
	k, err := StaticFromPrivate(priv)
	if err != nil {
		panic(err)
	}
	return k
}

func vectorConfig(v *vector, m []string, initiator bool) noise.Config {
	cs := noise.NewCipherSuite(noise.DH25519, ciphers[m[3]], hashes[m[4]])
	c := noise.Config{CipherSuite: cs, Pattern: patterns[m[1]], Initiator: initiator}
	prologue, psks, static, eph, remote := v.RespPrologue, v.RespPSKs, v.RespStatic, v.RespEphemeral, v.RespRemoteStatic
	if initiator {
		prologue, psks, static, eph, remote = v.InitPrologue, v.InitPSKs, v.InitStatic, v.InitEphemeral, v.InitRemoteStatic
	}
	c.Prologue = prologue
	c.StaticKeypair = dhKey(static)
	c.PeerStatic = remote
	// flynn/noise draws the ephemeral private key from Random.
	c.Random = bytes.NewReader(eph)
	if m[2] != "" {
		c.PresharedKeyPlacement, _ = strconv.Atoi(m[2])
		c.PresharedKey = psks[0]
	}
	return c
}

func runVector(t *testing.T, v *vector, m []string) {
	hsI, err := noise.NewHandshakeState(vectorConfig(v, m, true))
	if err != nil {
		t.Fatal(err)
	}
	hsR, err := noise.NewHandshakeState(vectorConfig(v, m, false))
	if err != nil {
		t.Fatal(err)
	}
	oneWay := len(patterns[m[1]].Messages) == 1
	var iSend, iRecv, rSend, rRecv *noise.CipherState // i→r is cs1, r→i is cs2
	done := false
	for i, msg := range v.Messages {
		fromInit := oneWay || i%2 == 0
		var ct, pt []byte
		if !done {
			w, r := hsI, hsR
			if !fromInit {
				w, r = hsR, hsI
			}
			var cs1, cs2 *noise.CipherState
			ct, cs1, cs2, err = w.WriteMessage(nil, msg.Payload)
			if err != nil {
				t.Fatalf("message %d: write: %v", i, err)
			}
			var rc1, rc2 *noise.CipherState
			pt, rc1, rc2, err = r.ReadMessage(nil, ct)
			if err != nil {
				t.Fatalf("message %d: read: %v", i, err)
			}
			if (cs1 == nil) != (rc1 == nil) {
				t.Fatalf("message %d: handshake finished on one side only", i)
			}
			if cs1 != nil {
				done = true
				if fromInit {
					iSend, rSend, rRecv, iRecv = cs1, cs2, rc1, rc2
				} else {
					rRecv, iRecv, iSend, rSend = cs1, cs2, rc1, rc2
				}
			}
		} else {
			enc, dec := iSend, rRecv
			if !fromInit {
				enc, dec = rSend, iRecv
			}
			ct, err = enc.Encrypt(nil, nil, msg.Payload)
			if err != nil {
				t.Fatalf("message %d: encrypt: %v", i, err)
			}
			pt, err = dec.Decrypt(nil, nil, ct)
			if err != nil {
				t.Fatalf("message %d: decrypt: %v", i, err)
			}
		}
		if !bytes.Equal(ct, msg.Ciphertext) {
			t.Fatalf("message %d: ciphertext\n got %x\nwant %x", i, ct, msg.Ciphertext)
		}
		if !bytes.Equal(pt, msg.Payload) {
			t.Fatalf("message %d: payload mismatch", i)
		}
	}
	if !done {
		t.Fatal("handshake never finished")
	}
	if len(v.HandshakeHash) > 0 {
		if !bytes.Equal(hsI.ChannelBinding(), v.HandshakeHash) || !bytes.Equal(hsR.ChannelBinding(), v.HandshakeHash) {
			t.Fatal("handshake hash mismatch")
		}
	}
}

// TestNoiseVectors runs every cacophony and snow vector flynn/noise can
// express, including Noise_XX_25519_ChaChaPoly_SHA256 from both files.
func TestNoiseVectors(t *testing.T) {
	sums := readSums(t)
	if len(sums) != len(vectorFiles) {
		t.Fatalf("SHA256SUMS lists %d files, want %d", len(sums), len(vectorFiles))
	}
	total, ours := 0, 0
	for _, file := range vectorFiles {
		vs := loadVectors(t, file, sums)
		for i := range vs {
			v := &vs[i]
			m := goSupported.FindStringSubmatch(v.Name)
			if m == nil {
				continue
			}
			if len(v.InitPSKs) > 1 || len(v.RespPSKs) > 1 {
				continue // multi-PSK (snow): flynn/noise takes one PSK
			}
			total++
			if v.Name == "Noise_XX_"+string(suite.Name()) {
				ours++
			}
			t.Run(file+"/"+strconv.Itoa(i)+"/"+v.Name, func(t *testing.T) { runVector(t, v, m) })
		}
	}
	t.Logf("ran %d vectors (%d for %s)", total, ours, suite.Name())
	if ours != 2 {
		t.Fatalf("ran %d vectors for OpenArcanum's suite, want 2 (one per file)", ours)
	}
	if total != 408 { // 288 cacophony (25519 only) + 120 snow (no multi-PSK)
		t.Fatalf("ran %d vectors, want 408", total)
	}
}
