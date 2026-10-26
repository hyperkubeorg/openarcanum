package community

// schemaV1 is the original schema (version 1). It is kept verbatim so that
// fresh databases and databases written by older builds reach the current
// version through the same migration path (migrations in schema_v2.go).
const schemaV1 = `
CREATE TABLE meta (k TEXT PRIMARY KEY, v BLOB NOT NULL);

CREATE TABLE members (
	key BLOB PRIMARY KEY,
	display_name TEXT NOT NULL DEFAULT '',
	avatar BLOB,
	avatar_version INTEGER NOT NULL DEFAULT 0,
	joined_at INTEGER NOT NULL,
	join_key_id INTEGER NOT NULL DEFAULT 0,
	status INTEGER NOT NULL,
	co_owner INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL
);
CREATE INDEX members_join_key ON members (join_key_id, joined_at);

CREATE TABLE member_roles (
	member BLOB NOT NULL REFERENCES members (key) ON DELETE CASCADE,
	role_id INTEGER NOT NULL REFERENCES roles (id) ON DELETE CASCADE,
	PRIMARY KEY (member, role_id)
);

CREATE TABLE devices (
	device_key BLOB PRIMARY KEY,
	member BLOB NOT NULL,
	name TEXT NOT NULL DEFAULT '',
	first_seen INTEGER NOT NULL,
	last_seen INTEGER NOT NULL,
	revoked INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX devices_member ON devices (member);

CREATE TABLE roles (
	id INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	permissions INTEGER NOT NULL,
	position INTEGER NOT NULL,
	color INTEGER NOT NULL DEFAULT 0,
	builtin INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE channels (
	id INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	kind INTEGER NOT NULL,
	position INTEGER NOT NULL,
	topic BLOB NOT NULL,
	key BLOB NOT NULL,
	key_epoch INTEGER NOT NULL,
	msg_retention INTEGER,
	file_retention INTEGER
);

CREATE TABLE channel_overrides (
	channel_id INTEGER NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
	role_id INTEGER NOT NULL REFERENCES roles (id) ON DELETE CASCADE,
	allow INTEGER NOT NULL,
	deny INTEGER NOT NULL,
	PRIMARY KEY (channel_id, role_id)
);

CREATE TABLE join_keys (
	id INTEGER PRIMARY KEY,
	label TEXT NOT NULL DEFAULT '',
	secret_hash BLOB NOT NULL UNIQUE,
	secret_enc BLOB,
	created_by BLOB NOT NULL,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL DEFAULT 0,
	max_uses INTEGER NOT NULL DEFAULT 0,
	uses INTEGER NOT NULL DEFAULT 0,
	request_only INTEGER NOT NULL DEFAULT 0,
	revoked INTEGER NOT NULL DEFAULT 0,
	public INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE join_key_roles (
	join_key_id INTEGER NOT NULL REFERENCES join_keys (id) ON DELETE CASCADE,
	role_id INTEGER NOT NULL REFERENCES roles (id) ON DELETE CASCADE,
	PRIMARY KEY (join_key_id, role_id)
);

CREATE TABLE files (
	id INTEGER PRIMARY KEY,
	channel_id INTEGER NOT NULL,
	uploader BLOB NOT NULL,
	name_enc BLOB NOT NULL,
	mime TEXT NOT NULL DEFAULT '',
	size INTEGER NOT NULL,
	key_enc BLOB NOT NULL,
	chunks INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	complete INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX files_created ON files (channel_id, created_at);

CREATE TABLE file_chunks (
	file_id INTEGER NOT NULL REFERENCES files (id) ON DELETE CASCADE,
	idx INTEGER NOT NULL,
	data BLOB NOT NULL,
	PRIMARY KEY (file_id, idx)
);

CREATE TABLE messages (
	id INTEGER PRIMARY KEY,
	channel_id INTEGER NOT NULL,
	author BLOB NOT NULL,
	created_at INTEGER NOT NULL,
	edited_at INTEGER NOT NULL DEFAULT 0,
	deleted INTEGER NOT NULL DEFAULT 0,
	file_id INTEGER NOT NULL DEFAULT 0,
	body BLOB
);
CREATE INDEX messages_channel ON messages (channel_id, id);
CREATE INDEX messages_created ON messages (channel_id, created_at);

CREATE TABLE bans (
	key BLOB PRIMARY KEY,
	display_name TEXT NOT NULL DEFAULT '',
	reason TEXT NOT NULL DEFAULT '',
	banned_by BLOB NOT NULL,
	at INTEGER NOT NULL
);

CREATE TABLE audit (
	id INTEGER PRIMARY KEY,
	at INTEGER NOT NULL,
	actor BLOB NOT NULL,
	summary TEXT NOT NULL,
	action BLOB NOT NULL,
	signature BLOB NOT NULL
);

CREATE TABLE action_nonces (nonce BLOB PRIMARY KEY, at INTEGER NOT NULL);

CREATE TABLE contact_cards (member BLOB PRIMARY KEY, card BLOB NOT NULL);

CREATE TABLE key_packages (
	id INTEGER PRIMARY KEY,
	member BLOB NOT NULL,
	package BLOB NOT NULL
);
CREATE INDEX key_packages_member ON key_packages (member);
`
