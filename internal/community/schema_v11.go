package community

// Schema 11: roles, channels, categories, emoji and join keys get
// AUTOINCREMENT ids. Their ids are kept elsewhere (members' role lists,
// overrides, join keys' roles, AutoMod exemptions, emoji references in
// messages, members' join key), and nextID used to hand out max(id)+1, so
// deleting the newest of them and creating another gave the new one the
// old id and everything that named the old one. The tables are rebuilt
// under their names (sqlite_sequence then holds each table's largest id);
// nextID reads the sequence.
const schemaV11Tables = `
ALTER TABLE roles RENAME TO roles_v10;
CREATE TABLE roles (id INTEGER PRIMARY KEY AUTOINCREMENT, data BLOB NOT NULL);
INSERT INTO roles (id, data) SELECT id, data FROM roles_v10;
DROP TABLE roles_v10;
ALTER TABLE channels RENAME TO channels_v10;
CREATE TABLE channels (id INTEGER PRIMARY KEY AUTOINCREMENT, data BLOB NOT NULL);
INSERT INTO channels (id, data) SELECT id, data FROM channels_v10;
DROP TABLE channels_v10;
ALTER TABLE categories RENAME TO categories_v10;
CREATE TABLE categories (id INTEGER PRIMARY KEY AUTOINCREMENT, data BLOB NOT NULL);
INSERT INTO categories (id, data) SELECT id, data FROM categories_v10;
DROP TABLE categories_v10;
ALTER TABLE emoji RENAME TO emoji_v10;
CREATE TABLE emoji (id INTEGER PRIMARY KEY AUTOINCREMENT, data BLOB NOT NULL, image BLOB NOT NULL);
INSERT INTO emoji (id, data, image) SELECT id, data, image FROM emoji_v10;
DROP TABLE emoji_v10;
ALTER TABLE join_keys RENAME TO join_keys_v10;
CREATE TABLE join_keys (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	secret_hash BLOB NOT NULL UNIQUE,
	uses INTEGER NOT NULL DEFAULT 0,
	revoked INTEGER NOT NULL DEFAULT 0,
	public INTEGER NOT NULL DEFAULT 0,
	data BLOB NOT NULL,
	bound BLOB
);
INSERT INTO join_keys (id, secret_hash, uses, revoked, public, data, bound)
	SELECT id, secret_hash, uses, revoked, public, data, bound FROM join_keys_v10;
DROP TABLE join_keys_v10;
`
