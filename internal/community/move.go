package community

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/identity"
	"github.com/hyperkubeorg/openarcanum/internal/session"
	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Moving a community to new hardware (docs/protocol.md §8):
//
//  1. `community restore` on the new machine decrypts a backup with the
//     storage key, generates a fresh server key and prints a move code.
//  2. An owner pastes the move code in the client, which signs a cert for the
//     new server key with their owner key and sends AdoptServer. Any key in
//     the current owner set can do this. The client attaches the owner chain
//     it knows, so a backup taken before an owner change still adopts a cert
//     from an owner added later.
//  3. The new server publishes its route; clients look up the route, check the
//     cert against the community id they pinned (through the owner chain), and
//     reconnect.

// MoveResult is what `community restore` prints.
type MoveResult struct {
	ServerKey []byte
	MoveCode  string
}

// Restore creates a data directory from a backup on new hardware.
//
// Everything is built in a staging directory inside the data directory
// first: the backup is decrypted there as it streams in (bounded memory),
// the new server keys are generated there, and the database is opened
// (migrated) and checked there. Only then do the keys and, last, the
// database move into place, so a restore that fails leaves the data
// directory as it was and can simply be run again. An existing storage key
// file is never overwritten: if cfg's storage_key_file already holds a
// different key, the restore is refused.
func Restore(ctx context.Context, cfg Config, backupPath, storageKeyPath string) (*MoveResult, error) {
	key, err := readHexFile(storageKeyPath, 32)
	if err != nil {
		return nil, fmt.Errorf("storage key: %w", err)
	}
	return restore(ctx, cfg, backupPath, storageKeyPath, key)
}

// RestoreHosted restores the backup of community id from a host whose
// storage key is in hostKeyPath: that community's key is derived from it
// (host.go), and kept in cfg's storage_key_file.
func RestoreHosted(ctx context.Context, cfg Config, backupPath, hostKeyPath, id string) (*MoveResult, error) {
	hostKey, err := readHexFile(hostKeyPath, 32)
	if err != nil {
		return nil, fmt.Errorf("storage key: %w", err)
	}
	return restore(ctx, cfg, backupPath, "", deriveCommunityKey(hostKey, id))
}

func restore(ctx context.Context, cfg Config, backupPath, storageKeyPath string, key []byte) (*MoveResult, error) {
	cfg.Resolve()
	dbPath := filepath.Join(cfg.DataDir, "community.db")
	if _, err := os.Stat(dbPath); err == nil {
		return nil, fmt.Errorf("%s already has a community database", cfg.DataDir)
	}
	writeKey, err := storageKeyTarget(cfg.StorageKeyFile, storageKeyPath, key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(cfg.DataDir, ".restore-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	if err := decryptBackupFile(key, backupPath, filepath.Join(stage, "community.db")); err != nil {
		return nil, err
	}
	serverKey, err := identity.Generate()
	if err != nil {
		return nil, err
	}
	static, err := session.GenerateStatic()
	if err != nil {
		return nil, err
	}
	if err := identity.WriteSeed(filepath.Join(stage, "server.key"), serverKey); err != nil {
		return nil, err
	}
	if err := writeHexFile(filepath.Join(stage, "noise.key"), static.Private); err != nil {
		return nil, err
	}
	staged := cfg
	staged.DataDir = stage
	code, err := prepareRestored(ctx, staged, key, serverKey)
	if err != nil {
		return nil, err
	}
	if writeKey {
		if err := writeHexFile(cfg.StorageKeyFile, key); err != nil {
			return nil, err
		}
	}
	for _, name := range []string{"server.key", "noise.key", "community.db-wal", "community.db-shm", "community.db"} {
		err := os.Rename(filepath.Join(stage, name), filepath.Join(cfg.DataDir, name))
		if errors.Is(err, os.ErrNotExist) && name != "community.db" && name != "server.key" && name != "noise.key" {
			continue
		}
		if err != nil {
			return nil, err
		}
	}
	return &MoveResult{ServerKey: serverKey.Public, MoveCode: code}, nil
}

// storageKeyTarget decides whether a restore writes the storage key to
// target: not when the key was read from target itself or target already
// holds the same key; an error when target holds a different key (a
// restore never replaces a storage key, whose loss would make that
// server's data and backups unreadable).
func storageKeyTarget(target, source string, key []byte) (bool, error) {
	if target == source {
		return false, nil
	}
	existing, err := readHexFile(target, 32)
	switch {
	case err == nil && bytes.Equal(existing, key):
		return false, nil
	case err == nil:
		return false, fmt.Errorf("%s already holds a different storage key; restore with that key, or point storage_key_file elsewhere", target)
	case errors.Is(err, os.ErrNotExist):
		return true, nil
	default:
		return false, fmt.Errorf("%s exists but isn't a storage key; move it away first: %w", target, err)
	}
}

// decryptBackupFile streams a backup's plaintext into a new file at out.
func decryptBackupFile(key []byte, backupPath, out string) error {
	in, err := os.Open(backupPath)
	if err != nil {
		return err
	}
	defer in.Close()
	plain, err := OpenBackup(key, in)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, plain); err != nil {
		f.Close()
		return fmt.Errorf("backup is damaged or truncated: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// prepareRestored opens the restored database in cfg.DataDir (the stage),
// checks it is a claimed server's, arms the move code for serverKey and
// returns that code.
func prepareRestored(ctx context.Context, cfg Config, key []byte, serverKey *identity.Key) (string, error) {
	s := &Server{cfg: cfg, storageKey: key}
	if err := s.openDB(ctx); err != nil {
		return "", err
	}
	defer s.db.Close()
	owner, err := s.getMeta(ctx, "owner_key")
	if err != nil || owner == nil {
		return "", errors.New("the backup is of an unclaimed server")
	}
	routeID, _ := s.getMeta(ctx, "route_id")
	secret := randomBytes(32)
	if err := s.setMeta(ctx, s.db, "move_hash", hashSecret(string(secret))); err != nil {
		return "", err
	}
	code, err := EncodeCode(MovePrefix, &wire.MoveCode{
		ServerKey: serverKey.Public, RouteId: routeID, Relays: cfg.Relays, Secret: secret, OwnerKey: owner,
	})
	if err != nil {
		return "", err
	}
	if err := s.checkpoint(ctx); err != nil {
		return "", err
	}
	return code, s.db.Close()
}

// adoptServer accepts the owner's cert for this (restored) server's key.
func (s *Server) adoptServer(ctx context.Context, ss *sess, a *wire.AdoptServer) (*wire.ServerMessage, error) {
	s.mu.Lock()
	if s.moveHash == nil {
		s.mu.Unlock()
		return nil, errConflict("this server isn't waiting to be adopted")
	}
	if !checkSecret(s.moveHash, a.Secret) {
		s.mu.Unlock()
		return nil, errDenied("wrong move code")
	}
	v, err := identity.VerifyCert(a.Cert, s.key.Public, s.ownerKey)
	if err != nil {
		s.mu.Unlock()
		return nil, errInvalid("the owner's certificate for this server is invalid")
	}
	if !v.Owners.Extends(s.owners) {
		s.mu.Unlock()
		return nil, errInvalid("the certificate's owner set doesn't follow this backup's")
	}
	if v.Cert.Seq <= s.certSeq() {
		s.mu.Unlock()
		return nil, errInvalid("the new certificate must have a higher sequence number")
	}
	b, _ := proto.Marshal(a.Cert)
	if err := s.setMeta(ctx, s.db, "cert", b); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.bestEffort(ctx, `DELETE FROM meta WHERE k = 'move_hash'`)
	s.cert = a.Cert
	s.owners = v.Owners
	s.moveHash = nil
	s.mu.Unlock()
	s.log.Info("community adopted on new hardware", "server", identity.Fingerprint(s.ServerKey()))
	// The adopting session's status may now be known (it's the owner member).
	ss.setStatus(s.admitSession(ctx, ss))
	s.smu.Lock()
	s.sessions[ss] = struct{}{}
	s.smu.Unlock()
	s.requestPublish()
	return okBody(), nil
}
