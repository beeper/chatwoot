-- v0 -> v2: Latest revision

CREATE TABLE IF NOT EXISTS user_filter_ids (
	user_id    TEXT PRIMARY KEY,
	filter_id  TEXT
);

CREATE TABLE IF NOT EXISTS user_batch_tokens (
	user_id           TEXT PRIMARY KEY,
	next_batch_token  TEXT
);

CREATE TABLE IF NOT EXISTS rooms (
	room_id           TEXT PRIMARY KEY,
	encryption_event  TEXT
);

CREATE TABLE IF NOT EXISTS room_members (
	room_id  TEXT,
	user_id  TEXT,
	PRIMARY KEY (room_id, user_id)
);

CREATE TABLE IF NOT EXISTS chatwoot_conversation_to_matrix_room (
	matrix_room_id            TEXT     UNIQUE,
	chatwoot_conversation_id  INTEGER  UNIQUE,
	most_recent_event_id      TEXT,
	PRIMARY KEY (matrix_room_id, chatwoot_conversation_id)
);

CREATE TABLE IF NOT EXISTS chatwoot_message_to_matrix_event (
	matrix_event_id      TEXT,
	chatwoot_message_id  INTEGER,
	PRIMARY KEY (matrix_event_id, chatwoot_message_id)
);
