//
// Implements the mautrix.crypto.StateStore interface on StateStore
//

package store

import (
	"encoding/json"

	log "github.com/sirupsen/logrus"
	mevent "maunium.net/go/mautrix/event"
	mid "maunium.net/go/mautrix/id"
)

// IsEncrypted returns whether a room is encrypted.
func (store *StateStore) IsEncrypted(roomID mid.RoomID) bool {
	return store.GetEncryptionEvent(roomID) != nil
}

func (store *StateStore) GetEncryptionEvent(roomId mid.RoomID) *mevent.EncryptionEventContent {
	row := store.DB.QueryRow("SELECT encryption_event FROM rooms WHERE room_id = $1", roomId)

	var encryptionEventJson []byte
	if err := row.Scan(&encryptionEventJson); err != nil {
		log.Errorf("Failed to find encryption event JSON: %s. Error: %s", encryptionEventJson, err)
		return nil
	}
	var encryptionEvent mevent.EncryptionEventContent
	if err := json.Unmarshal(encryptionEventJson, &encryptionEvent); err != nil {
		log.Errorf("Failed to unmarshal encryption event JSON: %s. Error: %s", encryptionEventJson, err)
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

func (store *StateStore) SetMembership(event *mevent.Event) {
	log.Debugf("Updating room_members for %s", event.RoomID)
	tx, err := store.DB.Begin()
	if err != nil {
		tx.Rollback()
		return
	}
	membershipEvent := event.Content.AsMember()
	if membershipEvent.Membership.IsInviteOrJoin() {
		insert := ""
		if store.dialect == "postgres" {
			insert = "INSERT INTO room_members (room_id, user_id) VALUES ($1, $2) ON CONFLICT (room_id, user_id) DO NOTHING"
		} else {
			insert = "INSERT OR IGNORE INTO room_members (room_id, user_id) VALUES ($1, $2)"
		}
		if _, err := tx.Exec(insert, event.RoomID, event.GetStateKey()); err != nil {
			log.Errorf("Failed to insert membership row for %s in %s: %+v", event.GetStateKey(), event.RoomID, err)
		}
	} else {
		del := "DELETE FROM room_members WHERE room_id = $1 AND user_id = $2"
		if _, err := tx.Exec(del, event.RoomID, event.GetStateKey()); err != nil {
			log.Errorf("Failed to delete membership row for %s in %s", event.GetStateKey(), event.RoomID)
		}
	}
	tx.Commit()
}

func (store *StateStore) SetEncryptionEvent(event *mevent.Event) {
	log.Debugf("Updating encryption_event for %s", event.RoomID)
	tx, err := store.DB.Begin()
	if err != nil {
		tx.Rollback()
		return
	}

	var encryptionEventJson []byte
	if event == nil {
		encryptionEventJson = nil
	}
	encryptionEventJson, err = json.Marshal(event)
	if err != nil {
		encryptionEventJson = nil
	}

	if store.dialect == "postgres" {
		upsert := `
			INSERT INTO rooms (room_id, encryption_event)
				VALUES ($1, $2)
			ON CONFLICT (room_id) DO UPDATE
				SET encryption_event = $2
		`
		if _, err := tx.Exec(upsert, event.RoomID, encryptionEventJson); err != nil {
			tx.Rollback()
			log.Error(err)
		}
	} else {
		update := "UPDATE rooms SET encryption_event = $1 WHERE room_id = $2"
		if _, err := tx.Exec(update, encryptionEventJson, event.RoomID); err != nil {
			tx.Rollback()
			log.Error(err)
		}

		insert := "INSERT OR IGNORE INTO rooms (room_id, encryption_event) VALUES ($1, $2)"
		if _, err := tx.Exec(insert, event.RoomID, encryptionEventJson); err != nil {
			tx.Rollback()
			log.Error(err)
		}
	}

	tx.Commit()
}
