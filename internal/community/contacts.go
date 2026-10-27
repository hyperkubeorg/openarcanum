package community

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// Contact cards (docs/protocol.md §9.2). The server stores
// each member's signed card opaquely and hands out one-time keys: one per
// device per request, each deleted as it is handed out. It can't read DMs:
// cards only hold public keys and inbox addresses.
//
// At rest, rows are keyed by lookup hashes (member and DM device) and the
// card and each key package are sealed under the storage key. A sealed key
// package holds the whole KeyPackage (device and package), so the device's
// public key is never stored in the clear.

const (
	maxCardBytes       = 64 << 10
	maxKeyPackageBytes = 2 << 10
	maxDeviceIDBytes   = 64
	// Per device; a card lists at most maxCardDevices devices.
	maxKeyPackages = 100
	maxCardDevices = 20
)

func (s *Server) cardsAllowed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.allowed(FeatureContactCards, nil, nil, 0) == nil
}

func (s *Server) publishContactCard(ctx context.Context, ss *sess, p *wire.PublishContactCard) (*wire.ServerMessage, error) {
	kid := s.kid(ss.member)
	if len(p.Card) == 0 {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM contact_cards WHERE member = ?`, kid); err != nil {
			return nil, err
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM key_packages WHERE member = ?`, kid); err != nil {
			return nil, err
		}
		return okBody(), nil
	}
	if !s.cardsAllowed() {
		return nil, errDisabled("this community doesn't allow contact cards")
	}
	if len(p.Card) > maxCardBytes {
		return nil, errTooLarge("contact card is too large")
	}
	if err := validKeyPackages(p.KeyPackages); err != nil {
		return nil, err
	}
	if len(p.Devices) == 0 || len(p.Devices) > maxCardDevices {
		return nil, errInvalid("a card lists between 1 and 20 devices")
	}
	listed := map[string]bool{}
	for _, d := range p.Devices {
		if len(d) == 0 || len(d) > maxDeviceIDBytes {
			return nil, errInvalid("bad device")
		}
		listed[string(s.dmDevice(d))] = true
	}
	for _, kp := range p.KeyPackages {
		if !listed[string(s.dmDevice(kp.Device))] {
			return nil, errInvalid("key package for a device that isn't in the card")
		}
	}
	if err := s.replaceCard(ctx, kid, p.Card, listed); err != nil {
		return nil, err
	}
	return s.addKeyPackages(ctx, ss, &wire.AddKeyPackages{KeyPackages: p.KeyPackages})
}

// replaceCard stores the card and deletes key packages of devices it no
// longer lists (`devices` holds their lookup hashes).
func (s *Server) replaceCard(ctx context.Context, kid, card []byte, devices map[string]bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO contact_cards (member, card) VALUES (?, ?) ON CONFLICT (member) DO UPDATE SET card = excluded.card`, kid, s.sealCard(kid, card)); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT device FROM key_packages WHERE member = ?`, kid)
	if err != nil {
		return err
	}
	var stale [][]byte
	for rows.Next() {
		var d []byte
		if err := rows.Scan(&d); err != nil {
			_ = rows.Close()
			return err
		}
		if !devices[string(d)] {
			stale = append(stale, d)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, d := range stale {
		if _, err := tx.ExecContext(ctx, `DELETE FROM key_packages WHERE member = ? AND device = ?`, kid, d); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func validKeyPackages(kps []*wire.KeyPackage) error {
	devices := map[string]bool{}
	for _, kp := range kps {
		if len(kp.Device) == 0 || len(kp.Device) > maxDeviceIDBytes || len(kp.Package) == 0 || len(kp.Package) > maxKeyPackageBytes {
			return errInvalid("bad key package")
		}
		devices[string(kp.Device)] = true
	}
	if len(devices) > maxCardDevices {
		return errInvalid("too many devices")
	}
	return nil
}

func (s *Server) addKeyPackages(ctx context.Context, ss *sess, a *wire.AddKeyPackages) (*wire.ServerMessage, error) {
	if err := validKeyPackages(a.KeyPackages); err != nil {
		return nil, err
	}
	kid := s.kid(ss.member)
	var has int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM contact_cards WHERE member = ?`, kid).Scan(&has); err != nil {
		return nil, err
	}
	if has == 0 {
		return nil, errNotFound("publish a contact card first")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var devices int64
	if err := tx.QueryRowContext(ctx, `SELECT count(DISTINCT device) FROM key_packages WHERE member = ?`, kid).Scan(&devices); err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	for _, kp := range a.KeyPackages {
		dev := s.dmDevice(kp.Device)
		n, seen := counts[string(dev)]
		if !seen {
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM key_packages WHERE member = ? AND device = ?`, kid, dev).Scan(&n); err != nil {
				return nil, err
			}
			if n == 0 {
				if devices >= maxCardDevices {
					return nil, errInvalid("too many devices")
				}
				devices++
			}
		}
		if n >= maxKeyPackages {
			counts[string(dev)] = n
			continue
		}
		raw, err := proto.Marshal(kp)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO key_packages (member, device, package) VALUES (?, ?, ?)`, kid, dev, s.sealKeyPackage(kid, raw)); err != nil {
			return nil, err
		}
		counts[string(dev)] = n + 1
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return okBody(), nil
}

func (s *Server) openStoredKeyPackage(kid, sealed []byte) (*wire.KeyPackage, error) {
	raw, err := s.openKeyPackage(kid, sealed)
	if err != nil {
		return nil, err
	}
	kp := &wire.KeyPackage{}
	if err := proto.Unmarshal(raw, kp); err != nil {
		return nil, err
	}
	return kp, nil
}

// Contact-card fetches that hand out one-time key packages, per member:
// cardFetchBurst in all and cardPairBurst for one card per cardFetchWindow.
const (
	cardFetchBurst  = 30
	cardPairBurst   = 3
	cardFetchWindow = time.Hour
)

func (s *Server) getContactCard(ctx context.Context, ss *sess, g *wire.GetContactCard) (*wire.ServerMessage, error) {
	if !s.cardsAllowed() {
		return nil, errDisabled("this community doesn't allow contact cards")
	}
	s.mu.RLock()
	m := s.members[string(g.Member)]
	var screened int64
	var timedOut int64
	if me := s.members[string(ss.member)]; string(g.Member) != string(ss.member) && me != nil {
		// Someone timed out can't start conversations either.
		if s.timedOut(me) {
			timedOut = me.TimeoutUntil
		}
		screened = s.screenedUntil(me)
	}
	s.mu.RUnlock()
	if timedOut != 0 {
		return nil, errDenied(fmt.Sprintf("You're timed out until %s, so you can't start direct messages.", fmtUTC(timedOut)))
	}
	if screened != 0 {
		return nil, errDenied(fmt.Sprintf("New members can start direct messages from %s (member screening).", fmtUTC(screened)))
	}
	if m == nil || m.Status != wire.MemberStatus_MEMBER_STATUS_ACTIVE {
		return nil, errNotFound("no such member")
	}
	kid := s.kid(g.Member)
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, `SELECT card FROM contact_cards WHERE member = ?`, kid).Scan(&sealed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errNotFound("that member hasn't published a contact card")
		}
		return nil, err
	}
	card, err := s.openCard(kid, sealed)
	if err != nil {
		return nil, err
	}
	res := &wire.ContactCardResult{Card: card}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// One package per device, oldest first, and a count of what's left per
	// device. The card's owner asking about itself only learns counts: it
	// must not burn its own keys. Nor may anyone else burn them all: each
	// member's fetches that hand out packages are limited, in all and per
	// card, and beyond that the card comes with counts only (as when the
	// packages have run out), so a client falls back as it does then.
	self := string(g.Member) == string(ss.member)
	if !self {
		now := s.now()
		pair := append(append([]byte(nil), ss.member...), g.Member...)
		if !s.cardFetchLimit.allowN(ss.member, now, cardFetchBurst, cardFetchWindow) ||
			!s.cardPairLimit.allowN(pair, now, cardPairBurst, cardFetchWindow) {
			self = true
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT k.id, k.device, k.package, f.n FROM key_packages k
JOIN (SELECT device, min(id) AS id, count(*) AS n FROM key_packages WHERE member = ? GROUP BY device) f ON k.id = f.id`, kid)
	if err != nil {
		return nil, err
	}
	type first struct {
		id     int64
		device []byte
		sealed []byte
		n      uint32
	}
	var firsts []first
	for rows.Next() {
		var f first
		if err := rows.Scan(&f.id, &f.device, &f.sealed, &f.n); err != nil {
			_ = rows.Close()
			return nil, err
		}
		firsts = append(firsts, f)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, f := range firsts {
		kp, err := s.openStoredKeyPackage(kid, f.sealed)
		if err != nil {
			return nil, err
		}
		// The device column is a lookup hash of the device key sealed in
		// the package; a package moved to another device's row is refused.
		if !bytes.Equal(s.dmDevice(kp.Device), f.device) {
			return nil, &TamperError{"key_packages", "device", rowID(f.id)}
		}
		left := f.n
		if !self {
			// One-time: a package that can't be deleted isn't handed out.
			if _, err := tx.ExecContext(ctx, `DELETE FROM key_packages WHERE id = ?`, f.id); err != nil {
				return nil, err
			}
			res.KeyPackages = append(res.KeyPackages, kp)
			left--
		}
		if left > 0 {
			res.Left = append(res.Left, &wire.DeviceKeyCount{Device: kp.Device, Left: left})
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &wire.ServerMessage{Body: &wire.ServerMessage_ContactCard{ContactCard: res}}, nil
}
