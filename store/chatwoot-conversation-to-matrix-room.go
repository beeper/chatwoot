package store

import (
	log "github.com/sirupsen/logrus"
	mid "maunium.net/go/mautrix/id"
)

func (store *StateStore) GetChatwootConversationFromMatrixRoom(roomID mid.RoomID) (string, error) {
	row := store.DB.QueryRow(`
		SELECT chatwoot_conversation_id
		  FROM chatwoot_conversation_to_matrix_room
		 WHERE matrix_room_id = ?`, roomID)
	var chatwootConversationId string
	if err := row.Scan(&chatwootConversationId); err != nil {
		return "", err
	}
	return chatwootConversationId, nil
}

func (store *StateStore) GetMatrixRoomFromChatwootConversation(conversationID string) (mid.RoomID, error) {
	row := store.DB.QueryRow(`
		SELECT matrix_room_id
		  FROM chatwoot_conversation_to_matrix_room
		 WHERE chatwoot_conversation_id = ?`, conversationID)
	var roomID string
	if err := row.Scan(&roomID); err != nil {
		return mid.RoomID(roomID), err
	}
	return mid.RoomID(roomID), nil
}

func (store *StateStore) UpdateConversationIdForRoom(roomID mid.RoomID, conversationID string) error {
	log.Debug("Upserting row into chatwoot_conversation_to_matrix_room")
	tx, err := store.DB.Begin()
	if err != nil {
		tx.Rollback()
		return err
	}

	update := "UPDATE chatwoot_conversation_to_matrix_room SET chatwoot_conversation_id = ? WHERE matrix_room_id = ?"
	if _, err := tx.Exec(update, conversationID, roomID); err != nil {
		tx.Rollback()
		return err
	}

	insert := `
		INSERT OR IGNORE INTO chatwoot_conversation_to_matrix_room (matrix_room_id, chatwoot_conversation_id)
		VALUES (?, ?)
	`
	if _, err := tx.Exec(insert, roomID, conversationID); err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}
