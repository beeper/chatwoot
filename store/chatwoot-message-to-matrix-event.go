package store

import (
	log "github.com/sirupsen/logrus"
	mid "maunium.net/go/mautrix/id"
)

func (store *StateStore) SetChatwootMessageIdForMatrixEvent(eventID mid.EventID, chatwootMessageId int) error {
	log.Debug("Upserting row into chatwoot_message_to_matrix_event")
	tx, err := store.DB.Begin()
	if err != nil {
		tx.Rollback()
		return err
	}

	update := "UPDATE chatwoot_message_to_matrix_event SET chatwoot_message_id = $1 WHERE matrix_event_id = $2"
	if _, err := tx.Exec(update, chatwootMessageId, eventID.String()); err != nil {
		tx.Rollback()
		return err
	}

	insert := `
		INSERT OR IGNORE INTO chatwoot_message_to_matrix_event (matrix_event_id, chatwoot_message_id)
		VALUES ($1, $2)
	`
	if _, err := tx.Exec(insert, eventID.String(), chatwootMessageId); err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}

func (store *StateStore) GetMatrixEventIdsForChatwootMessage(chatwootMessageId int) []mid.EventID {
	rows, err := store.DB.Query(`
		SELECT matrix_event_id
		  FROM chatwoot_message_to_matrix_event
		 WHERE chatwoot_message_id = $1`, chatwootMessageId)
	eventIDs := make([]mid.EventID, 0)
	if err != nil {
		log.Error(err)
		return eventIDs
	}
	defer rows.Close()

	var eventID mid.EventID
	for rows.Next() {
		if err := rows.Scan(&eventID); err == nil {
			eventIDs = append(eventIDs, eventID)
		}
	}
	return eventIDs
}

func (store *StateStore) GetChatwootMessageIdForMatrixEventId(matrixEventId mid.EventID) (int, error) {
	row := store.DB.QueryRow(`
		SELECT chatwoot_message_id
		  FROM chatwoot_message_to_matrix_event
		 WHERE matrix_event_id = $1`, matrixEventId)
	var messageID int
	if err := row.Scan(&messageID); err != nil {
		return messageID, err
	}
	return messageID, nil
}
