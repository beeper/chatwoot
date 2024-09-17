-- v2: Migrate to mautrix crypto store

-- Create all of the tables from the upstream mautrix crypto store.
-- This is only necessary for old installations of the chatwoot bot.

CREATE TABLE mx_registrations (
	user_id TEXT PRIMARY KEY
);

-- only: postgres
CREATE TYPE membership AS ENUM ('join', 'leave', 'invite', 'ban', 'knock');

CREATE TABLE mx_user_profile (
	room_id     TEXT,
	user_id     TEXT,
	membership  membership NOT NULL,
	displayname TEXT NOT NULL DEFAULT '',
	avatar_url  TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (room_id, user_id)
);

CREATE TABLE mx_room_state (
	room_id      TEXT PRIMARY KEY,
	power_levels jsonb,
	encryption   jsonb
);

CREATE TABLE mx_version (
	version INTEGER
);

INSERT INTO mx_version (version) VALUES (4);

-- Migrate the existing data to the new crypto store
DROP TABLE user_filter_ids;

CREATE TABLE IF NOT EXISTS crypto_account (
	account_id TEXT    PRIMARY KEY,
	device_id  TEXT    NOT NULL,
	shared     BOOLEAN NOT NULL,
	sync_token TEXT    NOT NULL,
	account    bytea   NOT NULL
);

UPDATE crypto_account SET sync_token = (
	SELECT next_batch_token
	FROM user_batch_tokens
);
DROP TABLE user_batch_tokens;

CREATE TABLE IF NOT EXISTS mx_room_state (
	room_id      TEXT PRIMARY KEY,
	power_levels jsonb,
	encryption   jsonb
);

INSERT INTO mx_room_state (room_id, encryption)
	SELECT room_id, encryption_event::jsonb
	FROM rooms;

CREATE TABLE IF NOT EXISTS mx_user_profile (
	room_id     TEXT,
	user_id     TEXT,
	membership  membership NOT NULL,
	displayname TEXT NOT NULL DEFAULT '',
	avatar_url  TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (room_id, user_id)
);

INSERT INTO mx_user_profile (room_id, user_id, membership)
	SELECT room_id, user_id, 'join'
	FROM room_members;
