package database

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/id"

	"github.com/beeper/chatwoot/chatwootapi"
)

func (store *Database) GetChatwootConversationIDFromMatrixRoom(ctx context.Context, roomID id.RoomID) (chatwootapi.ConversationID, error) {
	row := store.DB.QueryRow(ctx, `
		SELECT chatwoot_conversation_id
		  FROM chatwoot_conversation_to_matrix_room
		 WHERE matrix_room_id = $1`, roomID)
	var chatwootConversationID chatwootapi.ConversationID
	if err := row.Scan(&chatwootConversationID); err != nil {
		return -1, err
	}
	return chatwootConversationID, nil
}

func (store *Database) GetMatrixRoomFromChatwootConversation(ctx context.Context, conversationID chatwootapi.ConversationID) (id.RoomID, id.EventID, error) {
	row := store.DB.QueryRow(ctx, `
		SELECT matrix_room_id, most_recent_event_id
		  FROM chatwoot_conversation_to_matrix_room
		 WHERE chatwoot_conversation_id = $1`, conversationID)
	var roomID id.RoomID
	var mostRecentEventIDStr sql.NullString
	if err := row.Scan(&roomID, &mostRecentEventIDStr); err != nil {
		return "", "", err
	}
	if mostRecentEventIDStr.Valid {
		return roomID, id.EventID(mostRecentEventIDStr.String), nil
	} else {
		return roomID, id.EventID(""), nil
	}
}

func (store *Database) UpdateMostRecentEventIDForRoom(ctx context.Context, roomID id.RoomID, mostRecentEventID id.EventID) error {
	log := zerolog.Ctx(ctx).With().
		Str("component", "update_most_recent_event_id_for_room").
		Stringer("most_recent_event_id", mostRecentEventID).
		Logger()
	ctx = log.WithContext(ctx)

	log.Debug().Msg("setting most recent event ID for room")
	return store.DB.DoTxn(ctx, nil, func(ctx context.Context) error {
		update := `
			UPDATE chatwoot_conversation_to_matrix_room
			SET most_recent_event_id = $2
			WHERE matrix_room_id = $1
		`
		if _, err := store.DB.Exec(ctx, update, roomID, mostRecentEventID); err != nil {
			return fmt.Errorf("failed to update most recent event ID: %w", err)
		}
		return nil
	})
}

func (store *Database) UpdateConversationIDForRoom(ctx context.Context, roomID id.RoomID, conversationID chatwootapi.ConversationID) error {
	log := zerolog.Ctx(ctx).With().
		Str("component", "update_conversation_id_for_room").
		Int("conversation_id", int(conversationID)).
		Logger()
	ctx = log.WithContext(ctx)

	log.Debug().Msg("setting conversation ID for room")
	return store.DB.DoTxn(ctx, nil, func(ctx context.Context) error {
		upsert := `
			INSERT INTO chatwoot_conversation_to_matrix_room (matrix_room_id, chatwoot_conversation_id)
				VALUES ($1, $2)
			ON CONFLICT (matrix_room_id) DO UPDATE
				SET chatwoot_conversation_id = $2
		`
		_, err := store.DB.Exec(ctx, upsert, roomID, conversationID)
		return err
	})
}
