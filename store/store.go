package store

import "database/sql"

type StateStore struct {
	DB *sql.DB
}

func NewStateStore(db *sql.DB) *StateStore {
	return &StateStore{DB: db}
}

func (store *StateStore) CreateTables() error {
	tx, err := store.DB.Begin()
	if err != nil {
		return err
	}

	queries := []string{
		`
		CREATE TABLE IF NOT EXISTS chatwoot_meta (
			meta_id       INTEGER PRIMARY KEY,
			access_token  VARCHAR(255)
		)
		`,
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
			matrix_room_id            VARCHAR(255),
			chatwoot_conversation_id  VARCHAR(255),
			PRIMARY KEY (matrix_room_id, chatwoot_conversation_id)
		)
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
