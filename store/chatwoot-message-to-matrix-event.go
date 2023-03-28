package store

import (
	"context"
	"database/sql"

	"github.com/rs/zerolog"
	mid "maunium.net/go/mautrix/id"
)

func (store *StateStore) SetChatwootMessageIdForMatrixEvent(ctx context.Context, eventID mid.EventID, chatwootMessageId int) error {
	log := zerolog.Ctx(ctx).With().
		Str("event_id", eventID.String()).
		Int("chatwoot_message_id", chatwootMessageId).
		Logger()
	ctx = log.WithContext(ctx)

	log.Debug().Msg("setting chatwoot message ID for matrix event")
	tx, err := store.DB.Begin()
	if err != nil {
		tx.Rollback()
		return err
	}

	insert := `
		INSERT INTO chatwoot_message_to_matrix_event (matrix_event_id, chatwoot_message_id)
			VALUES ($1, $2)
	`
	if _, err := tx.ExecContext(ctx, insert, eventID, chatwootMessageId); err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}

func (store *StateStore) GetMatrixEventIdsForChatwootMessage(ctx context.Context, chatwootMessageId int) []mid.EventID {
	log := zerolog.Ctx(ctx).With().Int("message_id", chatwootMessageId).Logger()
	ctx = log.WithContext(ctx)

	log.Debug().Msg("getting Matrix event IDs for chatwoot message")
	rows, err := store.DB.QueryContext(ctx, `
		SELECT matrix_event_id
		  FROM chatwoot_message_to_matrix_event
		 WHERE chatwoot_message_id = $1`, chatwootMessageId)
	eventIDs := make([]mid.EventID, 0)
	if err != nil {
		log.Err(err).Msg("failed to get Matrix event IDs for chatwoot message")
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

func (store *StateStore) GetChatwootMessageIdsForMatrixEventID(ctx context.Context, matrixEventID mid.EventID) (messageIDs []int, err error) {
	log := zerolog.Ctx(ctx).With().Str("event_id", matrixEventID.String()).Logger()
	ctx = log.WithContext(ctx)

	log.Debug().Msg("getting chatwoot message IDs for matrix event ID")
	var rows *sql.Rows
	rows, err = store.DB.QueryContext(ctx, `
		SELECT chatwoot_message_id
		  FROM chatwoot_message_to_matrix_event
		 WHERE matrix_event_id = $1`, matrixEventID)
	if err != nil {
		log.Err(err).Msg("failed to get chatwoot message IDs for matrix event ID")
		return
	}
	defer rows.Close()

	var messageID int
	for rows.Next() {
		if err := rows.Scan(&messageID); err == nil {
			messageIDs = append(messageIDs, messageID)
		}
	}
	log.Debug().Interface("message_ids", messageIDs).Msg("found chatwoot message IDs for matrix event ID")
	return messageIDs, rows.Err()
}
