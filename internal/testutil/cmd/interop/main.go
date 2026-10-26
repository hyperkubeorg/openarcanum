// Command interop is a test helper for Go<->Python interop tests
// (clients/desktop/tests/test_interop.py). It is not shipped.
//
//	interop serve -relay URL [-tamper]  print {"server_key":...}, then echo sessions
//	                                    (-tamper: send a corrupted static-key proof)
//	interop dial -relay URL -server K  handshake, exchange messages, print result
//	interop sign -seed HEX -ctx C -msg HEX   print an Ed25519 signature (hex)
//	interop owner-cert                 build an owner chain (founder adds an
//	                                    owner, who removes the founder and
//	                                    certifies a server key); print it (hex)
//	interop verify-cert -cert HEX -server HEX -community HEX
//	                                    verify a SignedServerCert and print the
//	                                    owner set it proves
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/relayclient"
	"github.com/hyperkubeorg/openarcanum/internal/session"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

func out(v any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: interop serve|dial|sign ...")
		os.Exit(2)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	relayURL := fs.String("relay", "", "relay ws url")
	server := fs.String("server", "", "server key (base64url)")
	tamper := fs.Bool("tamper", false, "corrupt our identity signature")
	seed := fs.String("seed", "", "hex seed")
	ctxName := fs.String("ctx", "", "signature context")
	msg := fs.String("msg", "", "hex message")
	certHex := fs.String("cert", "", "hex SignedServerCert")
	community := fs.String("community", "", "hex community id")
	fs.Parse(os.Args[2:])

	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(*relayURL, *tamper)
	case "dial":
		err = dial(*relayURL, *server, *tamper)
	case "sign":
		err = sign(*seed, *ctxName, *msg)
	case "owner-cert":
		err = ownerCert()
	case "verify-cert":
		err = verifyCert(*certHex, *server, *community)
	default:
		err = errors.New("unknown mode")
	}
	if err != nil {
		out(map[string]string{"error": err.Error()})
		os.Exit(1)
	}
}

func sign(seedHex, ctx, msgHex string) error {
	seed, err := hex.DecodeString(seedHex)
	if err != nil {
		return err
	}
	k, err := identity.FromSeed(seed)
	if err != nil {
		return err
	}
	m, err := hex.DecodeString(msgHex)
	if err != nil {
		return err
	}
	out(map[string]string{"public": hex.EncodeToString(k.Public), "signature": hex.EncodeToString(k.Sign(ctx, m))})
	return nil
}

func ownerCert() error {
	founder, _ := identity.Generate()
	second, _ := identity.Generate()
	member, _ := identity.Generate()
	server, _ := identity.Generate()
	set := identity.FounderSet(founder.Public)
	add, err := identity.RequestOwnerKey(second, member, founder.Public)
	if err != nil {
		return err
	}
	var chain []*wire.SignedOwnerChange
	for _, step := range []func() (*wire.SignedOwnerChange, error){
		func() (*wire.SignedOwnerChange, error) { return identity.SignOwnerChange(founder, set, add, nil) },
		func() (*wire.SignedOwnerChange, error) {
			return identity.SignOwnerChange(second, set, nil, founder.Public)
		},
	} {
		sc, err := step()
		if err != nil {
			return err
		}
		if _, err := set.Apply(sc); err != nil {
			return err
		}
		chain = append(chain, sc)
	}
	cert, err := identity.IssueServerCert(second, server.Public, 3, set, chain)
	if err != nil {
		return err
	}
	b, _ := proto.Marshal(cert)
	out(map[string]string{
		"cert": hex.EncodeToString(b), "server": hex.EncodeToString(server.Public),
		"community": hex.EncodeToString(founder.Public), "owner": hex.EncodeToString(second.Public),
		"founder_seed": hex.EncodeToString(founder.Seed()),
	})
	return nil
}

func verifyCert(certHex, serverHex, communityHex string) error {
	raw, err := hex.DecodeString(certHex)
	if err != nil {
		return err
	}
	server, _ := hex.DecodeString(serverHex)
	community, _ := hex.DecodeString(communityHex)
	var sc wire.SignedServerCert
	if err := proto.Unmarshal(raw, &sc); err != nil {
		return err
	}
	v, err := identity.VerifyCert(&sc, server, community)
	if err != nil {
		return err
	}
	owners := make([]string, 0, len(v.Owners.Owners))
	for _, o := range v.Owners.Owners {
		owners = append(owners, hex.EncodeToString(o))
	}
	out(map[string]any{"owners": owners, "seq": v.Owners.Seq, "head": hex.EncodeToString(v.Owners.Head()), "signer": hex.EncodeToString(v.Cert.OwnerKey)})
	return nil
}

func serve(url string, tamper bool) error {
	ctx := context.Background()
	c, err := relayclient.Dial(ctx, url)
	if err != nil {
		return err
	}
	id, _ := identity.Generate()
	static, _ := session.GenerateStatic()
	if err := c.RegisterEndpoint(ctx, id); err != nil {
		return err
	}
	out(map[string]string{"server_key": identity.Encode(id.Public)})
	cfg := session.ServerConfig{Static: static, Identity: id}
	if tamper {
		cfg.StaticSig = session.SignStatic(id, static.Public)
		cfg.StaticSig[0] ^= 0x80
	}
	for {
		st, err := c.Accept(ctx)
		if err != nil {
			return err
		}
		go func() {
			hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			s, err := session.Accept(hctx, st, cfg)
			if err != nil {
				kind := "handshake"
				if errors.Is(err, session.ErrIdentity) {
					kind = "identity"
				}
				out(map[string]string{"event": "rejected", "reason": kind})
				st.Close("handshake failed")
				return
			}
			out(map[string]string{
				"event":    "accepted",
				"identity": identity.Encode(s.Client.IdentityKey),
				"device":   identity.Encode(s.PeerStatic),
				"name":     s.Client.DeviceName,
			})
			for {
				m, err := s.Recv(context.Background())
				if err != nil {
					return
				}
				if err := s.Send(append([]byte("echo:"), m...)); err != nil {
					return
				}
			}
		}()
	}
}

func dial(url, serverKey string, tamper bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	want, err := identity.Decode(serverKey)
	if err != nil {
		return err
	}
	c, err := relayclient.Dial(ctx, url)
	if err != nil {
		return err
	}
	defer c.Close()
	st, err := c.OpenStream(ctx, want)
	if err != nil {
		return err
	}
	id, _ := identity.Generate()
	static, _ := session.GenerateStatic()
	sig := session.SignStatic(id, static.Public)
	if tamper {
		sig[0] ^= 0x80
	}
	s, err := session.Dial(ctx, st, session.ClientConfig{
		Static: static, IdentityKey: id.Public, StaticSig: sig, DeviceName: "go-device",
		VerifyServer: func(si *wire.ServerIdentity) error {
			if string(si.ServerKey) != string(want) {
				return errors.New("server key mismatch")
			}
			return nil
		},
	})
	if err != nil {
		return err
	}
	big := make([]byte, 200_000)
	rand.Read(big)
	for _, m := range [][]byte{[]byte("ping"), big} {
		if err := s.Send(m); err != nil {
			return err
		}
		got, err := s.Recv(ctx)
		if err != nil {
			return err
		}
		if string(got) != "echo:"+string(m) {
			return errors.New("echo mismatch")
		}
	}
	out(map[string]string{"ok": "true", "identity": identity.Encode(id.Public)})
	return nil
}
