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
	row := store.DB.QueryRow("SELECT encryption_event FROM rooms WHERE room_id = ?", roomId)

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
	rows, err := store.DB.Query("SELECT room_id FROM room_members WHERE user_id = ?", userId)
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
		insert := "INSERT OR IGNORE INTO room_members VALUES (?, ?)"
		if _, err := tx.Exec(insert, event.RoomID, event.GetStateKey()); err != nil {
			log.Errorf("Failed to insert membership row for %s in %s", event.GetStateKey(), event.RoomID)
		}
	} else {
		del := "DELETE FROM room_members WHERE room_id = ? AND user_id = ?"
		if _, err := tx.Exec(del, event.RoomID, event.GetStateKey()); err != nil {
			log.Errorf("Failed to delete membership row for %s in %s", event.GetStateKey(), event.RoomID)
		}
	}
	tx.Commit()
}

func (store *StateStore) upsertEncryptionEvent(roomId mid.RoomID, encryptionEvent *mevent.Event) error {
	tx, err := store.DB.Begin()
	if err != nil {
		tx.Rollback()
		return nil
	}

	update := "UPDATE rooms SET encryption_event = ? WHERE room_id = ?"
	var encryptionEventJson []byte
	if encryptionEvent == nil {
		encryptionEventJson = nil
	}
	encryptionEventJson, err = json.Marshal(encryptionEvent)
	if err != nil {
		encryptionEventJson = nil
	}

	if _, err := tx.Exec(update, encryptionEventJson, roomId); err != nil {
		tx.Rollback()
		return err
	}

	insert := "INSERT OR IGNORE INTO rooms VALUES (?, ?)"
	if _, err := tx.Exec(insert, roomId, encryptionEventJson); err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}

func (store *StateStore) SetEncryptionEvent(event *mevent.Event) {
	log.Debugf("Updating encryption_event for %s", event.RoomID)
	tx, err := store.DB.Begin()
	if err != nil {
		tx.Rollback()
		return
	}
	err = store.upsertEncryptionEvent(event.RoomID, event)
	if err != nil {
		log.Errorf("Upsert encryption event failed %s", err)
	}

	tx.Commit()
}
