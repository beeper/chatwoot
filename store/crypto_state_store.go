//
// Implements the mautrix.crypto.StateStore interface on StateStore
//

package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/rs/zerolog/log"
	mevent "maunium.net/go/mautrix/event"
	mid "maunium.net/go/mautrix/id"
)

// IsEncrypted returns whether a room is encrypted.
func (store *StateStore) IsEncrypted(roomID mid.RoomID) bool {
	return store.GetEncryptionEvent(roomID) != nil
}

func (store *StateStore) GetEncryptionEvent(roomID mid.RoomID) *mevent.EncryptionEventContent {
	row := store.DB.QueryRow("SELECT encryption_event FROM rooms WHERE room_id = $1", roomID)

	var encryptionEventJson []byte
	if err := row.Scan(&encryptionEventJson); err != nil {
		if err != sql.ErrNoRows {
			log.Err(err).
				Str("room_id", roomID.String()).
				Msg("Couldn't to find encryption event JSON for room")
		}
		return nil
	}
	var encryptionEvent mevent.EncryptionEventContent
	if err := json.Unmarshal(encryptionEventJson, &encryptionEvent); err != nil {
		log.Err(err).
			Str("room_id", roomID.String()).
			Str("encryption_event_json", string(encryptionEventJson)).
			Msg("failed to unmarshal encryption event JSON")
		return nil
	}
	return &encryptionEvent
}

func (store *StateStore) FindSharedRooms(userId mid.UserID) []mid.RoomID {
	rows, err := store.DB.Query("SELECT room_id FROM room_members WHERE user_id = $1", userId)
	rooms := make([]mid.RoomID, 0)
	if err != nil {
		return rooms
	}
	defer rows.Close()

	var roomId mid.RoomID
	for rows.Next() {
		if err := rows.Scan(&roomId); err != nil {
			rooms = append(rooms, roomId)
		}
	}
	return rooms
}

func (store *StateStore) SetMembership(ctx context.Context, event *mevent.Event) {
	log.Debug().Str("room_id", event.RoomID.String()).Msg("updating room_members for room")
	tx, err := store.DB.Begin()
	if err != nil {
		tx.Rollback()
		return
	}
	membershipEvent := event.Content.AsMember()
	if membershipEvent.Membership.IsInviteOrJoin() {
		insert := "INSERT INTO room_members (room_id, user_id) VALUES ($1, $2) ON CONFLICT (room_id, user_id) DO NOTHING"
		if _, err := tx.ExecContext(ctx, insert, event.RoomID, event.GetStateKey()); err != nil {
			log.Err(err).
				Str("user_id", event.GetStateKey()).
				Str("room_id", event.RoomID.String()).
				Msg("failed to insert membership row for user")
			tx.Rollback()
			return
		}
	} else {
		del := "DELETE FROM room_members WHERE room_id = $1 AND user_id = $2"
		if _, err := tx.ExecContext(ctx, del, event.RoomID, event.GetStateKey()); err != nil {
			log.Err(err).
				Str("user_id", event.GetStateKey()).
				Str("room_id", event.RoomID.String()).
				Msg("failed to delete membership row for user")
			tx.Rollback()
			return
		}
	}
	tx.Commit()
}

func (store *StateStore) SetEncryptionEvent(ctx context.Context, event *mevent.Event) {
	if event == nil {
		return
	}

	log.Debug().Str("room_id", event.RoomID.String()).Msg("updating encryption_event for room")
	tx, err := store.DB.Begin()
	if err != nil {
		tx.Rollback()
		return
	}

	var encryptionEventJson []byte
	encryptionEventJson, err = json.Marshal(event)
	if err != nil {
		encryptionEventJson = nil
	}

	upsert := `
		INSERT INTO rooms (room_id, encryption_event)
			VALUES ($1, $2)
		ON CONFLICT (room_id) DO UPDATE
			SET encryption_event = $2
	`
	if _, err := tx.ExecContext(ctx, upsert, event.RoomID, encryptionEventJson); err != nil {
		tx.Rollback()
		log.Err(err).Msg("failed to update encryption event for room")
		return
	}

	tx.Commit()
}
