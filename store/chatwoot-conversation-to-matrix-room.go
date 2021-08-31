package store

import (
	log "github.com/sirupsen/logrus"
	mid "maunium.net/go/mautrix/id"
)

func (store *StateStore) GetChatwootConversationFromMatrixRoom(roomID mid.RoomID) (int, error) {
	row := store.DB.QueryRow(`
		SELECT chatwoot_conversation_id
		  FROM chatwoot_conversation_to_matrix_room
		 WHERE matrix_room_id = $1`, roomID)
	var chatwootConversationId int
	if err := row.Scan(&chatwootConversationId); err != nil {
		return -1, err
	}
	return chatwootConversationId, nil
}

func (store *StateStore) GetMatrixRoomFromChatwootConversation(conversationID int) (mid.RoomID, error) {
	row := store.DB.QueryRow(`
		SELECT matrix_room_id
		  FROM chatwoot_conversation_to_matrix_room
		 WHERE chatwoot_conversation_id = $1`, conversationID)
	var roomID string
	if err := row.Scan(&roomID); err != nil {
		return mid.RoomID(roomID), err
	}
	return mid.RoomID(roomID), nil
}

func (store *StateStore) UpdateConversationIdForRoom(roomID mid.RoomID, conversationID int) error {
	log.Debug("Upserting row into chatwoot_conversation_to_matrix_room")
	tx, err := store.DB.Begin()
	if err != nil {
		tx.Rollback()
		return err
	}

	if store.dialect == "pgx" {
		upsert := `
			INSERT INTO chatwoot_conversation_to_matrix_room (matrix_room_id, chatwoot_conversation_id)
				VALUES ($1, $2)
			ON CONFLICT DO
				UPDATE chatwoot_conversation_to_matrix_room
				SET chatwoot_conversation_id = $2
				WHERE matrix_room_id = $1
		`
		if _, err := tx.Exec(upsert, roomID, conversationID); err != nil {
			tx.Rollback()
			return err
		}
	} else {
		update := "UPDATE chatwoot_conversation_to_matrix_room SET chatwoot_conversation_id = $1 WHERE matrix_room_id = $2"
		if _, err := tx.Exec(update, conversationID, roomID); err != nil {
			tx.Rollback()
			return err
		}

		insert := `
			INSERT OR IGNORE INTO chatwoot_conversation_to_matrix_room (matrix_room_id, chatwoot_conversation_id)
			VALUES ($1, $2)
		`
		if _, err := tx.Exec(insert, roomID, conversationID); err != nil {
			tx.Rollback()
			return err
		}
	}

	return tx.Commit()
}
