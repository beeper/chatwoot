-- v2: Migrate to mautrix crypto store

-- Create all of the tables from the upstream mautrix crypto store

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

UPDATE crypto_account SET sync_token = (
	SELECT next_batch_token
	FROM user_batch_tokens
);
DROP TABLE user_batch_tokens;

INSERT INTO mx_room_state (room_id, encryption)
	SELECT room_id, encryption_event::jsonb
	FROM rooms;

INSERT INTO mx_user_profile (room_id, user_id, membership)
	SELECT room_id, user_id, 'join'
	FROM room_members;
