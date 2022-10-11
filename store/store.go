package store

import (
	mid "maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"
)

type StateStore struct {
	DB          *dbutil.Database
	dialect     string
	botUsername mid.UserID
}

func NewStateStore(db *dbutil.Database, dialect string, botUsername mid.UserID) *StateStore {
	return &StateStore{DB: db, dialect: dialect, botUsername: botUsername}
}

func (store *StateStore) CreateTables() error {
	tx, err := store.DB.Begin()
	if err != nil {
		return err
	}

	queries := []string{
		`
		CREATE TABLE IF NOT EXISTS user_filter_ids (
			user_id    VARCHAR(255) PRIMARY KEY,
			filter_id  VARCHAR(255)
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS user_batch_tokens (
			user_id           VARCHAR(255) PRIMARY KEY,
			next_batch_token  VARCHAR(255)
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS rooms (
			room_id           VARCHAR(255) PRIMARY KEY,
			encryption_event  VARCHAR(65535) NULL
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS room_members (
			room_id  VARCHAR(255),
			user_id  VARCHAR(255),
			PRIMARY KEY (room_id, user_id)
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS chatwoot_conversation_to_matrix_room (
			matrix_room_id            VARCHAR(255) UNIQUE,
			chatwoot_conversation_id  INTEGER      UNIQUE,
			PRIMARY KEY (matrix_room_id, chatwoot_conversation_id)
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS chatwoot_message_to_matrix_event (
			matrix_event_id      VARCHAR(255) UNIQUE,
			chatwoot_message_id  INTEGER,
			PRIMARY KEY (matrix_event_id, chatwoot_message_id)
		)
		`,
		`
		ALTER TABLE chatwoot_conversation_to_matrix_room
		ADD COLUMN IF NOT EXISTS most_recent_event_id VARCHAR(255)
		`,
		`
		ALTER TABLE chatwoot_message_to_matrix_event
		DROP CONSTRAINT IF EXISTS chatwoot_message_to_matrix_event_matrix_event_id_key
		`,
	}

	for _, query := range queries {
		if _, err := tx.Exec(query); err != nil {
			_ = tx.Rollback()
			return err
		}
	}

	if err = tx.Commit(); err != nil {
		return err
	}

	return nil
}
