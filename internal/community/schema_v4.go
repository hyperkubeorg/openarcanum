package community

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hyperkubeorg/openarcanum/internal/store"
)

// Schema 4: channel categories and the perms.go permission layout.
//
//   - A categories table (id, sealed record), like roles and channels.
//   - Every role's permissions and every channel override's allow and deny
//     move from the schema 1-3 bits to the perms.go layout. Bits keep their
//     meaning (Manage Invites becomes Create Invite), and roles get the
//     permissions that split off what they could already do: View Channel
//     brings Read Message History; Send Messages brings Embed Links, Add
//     Reactions and Use Custom Emoji; Speak brings Use Voice Activity and
//     Video; @everyone gets Change Nickname (every member could set their
//     name before). Overrides are mapped bit for bit only, so an override
//     never starts allowing or denying more than it did.
//
// New channel fields (topic text, category, slowmode, channel toggles) live
// in the sealed channel record and default to empty, and community
// settings get their toggle defaults when loaded (fillFeatureDefaults), so
// neither needs rewriting here.

const schemaV4Tables = `CREATE TABLE categories (id INTEGER PRIMARY KEY, data BLOB NOT NULL);`

// v3Bits maps each schema 1-3 permission bit to its schema 4 bit.
var v3Bits = []struct{ old, new uint64 }{
	{1 << 0, PermViewChannel},
	{1 << 1, PermSendMessages},
	{1 << 2, PermAttachFiles},
	{1 << 3, PermManageMessages},
	{1 << 4, PermManageChannels},
	{1 << 5, PermManageRoles},
	{1 << 6, PermCreateInvite},
	{1 << 7, PermKick},
	{1 << 8, PermBan},
	{1 << 9, PermApproveMembers},
	{1 << 10, PermViewAuditLog},
	{1 << 11, PermManageCommunity},
	{1 << 12, PermConnectVoice},
	{1 << 13, PermSpeak},
	{1 << 30, PermAdministrator},
}

// MapV3Perms converts a schema 1-3 permission mask bit for bit; unknown
// bits are dropped.
func MapV3Perms(old uint64) uint64 {
	var out uint64
	for _, b := range v3Bits {
		if old&b.old != 0 {
			out |= b.new
		}
	}
	return out
}

// MapV3RolePerms converts a role's schema 1-3 permissions and adds the
// permissions that split off what the role could already do.
func MapV3RolePerms(old uint64, everyone bool) uint64 {
	p := MapV3Perms(old)
	if p&PermViewChannel != 0 {
		p |= PermReadMessageHistory
	}
	if p&PermSendMessages != 0 {
		p |= PermEmbedLinks | PermAddReactions | PermUseCustomEmoji
	}
	if p&PermSpeak != 0 {
		p |= PermUseVoiceActivity | PermVideo
	}
	if everyone {
		p |= PermChangeNickname
	}
	return p
}

func (s *Server) migrateV4(ctx context.Context, tx store.Tx) error {
	var check []byte
	if err := tx.QueryRowContext(ctx, `SELECT v FROM meta WHERE k = 'key_check'`).Scan(&check); err != nil {
		return fmt.Errorf("database has no storage key check: %w", err)
	}
	if pt, err := s.open("meta", "v", "key_check", check); err != nil || string(pt) != keyCheckPlaintext {
		return errWrongStorageKey
	}
	if _, err := tx.ExecContext(ctx, schemaV4Tables); err != nil {
		return err
	}
	type row struct {
		id   int64
		data []byte
	}
	read := func(q string) ([]row, error) {
		rs, err := tx.QueryContext(ctx, q)
		if err != nil {
			return nil, err
		}
		defer rs.Close()
		var out []row
		for rs.Next() {
			var r row
			if err := rs.Scan(&r.id, &r.data); err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		return out, rs.Err()
	}
	roles, err := read(`SELECT id, data FROM roles`)
	if err != nil {
		return err
	}
	for _, r := range roles {
		var rec roleRec
		if err := s.openJSON("roles", "data", rowID(r.id), r.data, &rec); err != nil {
			return err
		}
		rec.Perms = MapV3RolePerms(rec.Perms, rec.Builtin && rec.Position == 0)
		if _, err := tx.ExecContext(ctx, `UPDATE roles SET data = ? WHERE id = ?`, s.sealJSON("roles", "data", rowID(r.id), &rec), r.id); err != nil {
			return err
		}
	}
	channels, err := read(`SELECT id, data FROM channels`)
	if err != nil {
		return err
	}
	for _, r := range channels {
		pt, err := s.open("channels", "data", rowID(r.id), r.data)
		if err != nil {
			return err
		}
		var rec channelRec
		if err := json.Unmarshal(pt, &rec); err != nil {
			return errors.Join(&TamperError{"channels", "data", rowID(r.id)}, err)
		}
		for i := range rec.Overrides {
			rec.Overrides[i].Allow = MapV3Perms(rec.Overrides[i].Allow)
			rec.Overrides[i].Deny = MapV3Perms(rec.Overrides[i].Deny)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE channels SET data = ? WHERE id = ?`, s.sealJSON("channels", "data", rowID(r.id), &rec), r.id); err != nil {
			return err
		}
	}
	return nil
}
