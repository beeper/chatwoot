package store

import (
	"database/sql"

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

func (store *StateStore) GetMatrixRoomFromChatwootConversation(conversationID int) (mid.RoomID, mid.EventID, error) {
	row := store.DB.QueryRow(`
		SELECT matrix_room_id, most_recent_event_id
		  FROM chatwoot_conversation_to_matrix_room
		 WHERE chatwoot_conversation_id = $1`, conversationID)
	var roomID mid.RoomID
	var mostRecentEventIdStr sql.NullString
	if err := row.Scan(&roomID, &mostRecentEventIdStr); err != nil {
		return "", "", err
	}
	if mostRecentEventIdStr.Valid {
		return roomID, mid.EventID(mostRecentEventIdStr.String), nil
	} else {
		return roomID, mid.EventID(""), nil
	}
}

func (store *StateStore) UpdateMostRecentEventIdForRoom(roomID mid.RoomID, mostRecentEventID mid.EventID) error {
	log.Debugf("Setting most recent event ID for %s to %s", roomID, mostRecentEventID)
	tx, err := store.DB.Begin()
	if err != nil {
		tx.Rollback()
		return err
	}

	update := `
		UPDATE chatwoot_conversation_to_matrix_room
		SET most_recent_event_id = $2
		WHERE matrix_room_id = $1
	`
	if _, err := tx.Exec(update, roomID, mostRecentEventID); err != nil {
		tx.Rollback()
		log.Error(err)
		return err
	}

	return tx.Commit()
}

func (store *StateStore) UpdateConversationIdForRoom(roomID mid.RoomID, conversationID int) error {
	log.Debug("Upserting row into chatwoot_conversation_to_matrix_room")
	tx, err := store.DB.Begin()
	if err != nil {
		tx.Rollback()
		return err
	}

	upsert := `
		INSERT INTO chatwoot_conversation_to_matrix_room (matrix_room_id, chatwoot_conversation_id)
			VALUES ($1, $2)
		ON CONFLICT (matrix_room_id) DO UPDATE
			SET chatwoot_conversation_id = $2
	`
	if _, err := tx.Exec(upsert, roomID, conversationID); err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}
