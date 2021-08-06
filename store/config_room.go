package store

import (
	"time"

	log "github.com/sirupsen/logrus"
	mid "maunium.net/go/mautrix/id"
)

// Setting which room to look for as the config room for a given user.
func (store *StateStore) SetConfigRoom(userID mid.UserID, roomID mid.RoomID) {
	log.Debugf("Setting config room for %s to %s", userID, roomID)
	tx, err := store.DB.Begin()
	if err != nil {
		tx.Rollback()
		return
	}

	update := "UPDATE user_config_room SET room_id = ? WHERE user_id = ?"
	if _, err := tx.Exec(update, roomID, userID); err != nil {
		tx.Rollback()
		return
	}

	insert := "INSERT OR IGNORE INTO user_config_room (room_id, user_id) VALUES (?, ?)"
	if _, err := tx.Exec(insert, roomID, userID); err != nil {
		tx.Rollback()
		return
	}

	tx.Commit()
}

func (store *StateStore) RemoveConfigRoom(roomID mid.RoomID) {
	log.Debugf("Removing all instances of %s from user_config_room", roomID)
	tx, err := store.DB.Begin()
	if err != nil {
		tx.Rollback()
		return
	}

	update := "DELETE FROM user_config_room WHERE room_id = ?"
	if _, err := tx.Exec(update, roomID); err != nil {
		tx.Rollback()
		return
	}

	tx.Commit()
}

// Notification time handling

func (store *StateStore) SetTimezone(userID mid.UserID, timezone string) {
	log.Debugf("Setting timezone for %s to %s", userID, timezone)
	tx, err := store.DB.Begin()
	if err != nil {
		tx.Rollback()
		return
	}

	update := "UPDATE user_config_room SET timezone = ? WHERE user_id = ?"
	if _, err := tx.Exec(update, timezone, userID); err != nil {
		tx.Rollback()
		return
	}

	tx.Commit()
}

func (store *StateStore) SetNotify(userID mid.UserID, minutesAfterMidnight int) {
	log.Debugf("Setting timezone for %s to %d", userID, minutesAfterMidnight)
	tx, err := store.DB.Begin()
	if err != nil {
		tx.Rollback()
		return
	}

	update := "UPDATE user_config_room SET minutes_after_midnight = ? WHERE user_id = ?"
	if _, err := tx.Exec(update, minutesAfterMidnight, userID); err != nil {
		tx.Rollback()
		return
	}

	tx.Commit()
}

func (store *StateStore) SetSendRoomId(userID mid.UserID, sendRoomID mid.RoomID) {
	log.Debugf("Setting send room ID for %s to %s", userID, sendRoomID.String())
	tx, err := store.DB.Begin()
	if err != nil {
		tx.Rollback()
		return
	}

	update := "UPDATE user_config_room SET send_room_id = ? WHERE user_id = ?"
	if _, err := tx.Exec(update, sendRoomID.String(), userID); err != nil {
		tx.Rollback()
		return
	}

	tx.Commit()
}

func (store *StateStore) GetSendRoomId(userID mid.UserID) mid.RoomID {
	row := store.DB.QueryRow("SELECT send_room_id FROM user_config_room WHERE user_id = ?", userID)
	var sendRoomId mid.RoomID
	if err := row.Scan(&sendRoomId); err != nil {
		return ""
	}
	return sendRoomId
}

func (store *StateStore) GetCurrentWeekdayInUserTimezone(userID mid.UserID) time.Weekday {
	row := store.DB.QueryRow("SELECT timezone FROM user_config_room WHERE user_id = ?", userID)
	var timezone string
	if err := row.Scan(&timezone); err != nil {
		return time.Now().UTC().Weekday()
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Now().UTC().Weekday()
	}
	return time.Now().In(location).Weekday()
}

func (store *StateStore) GetNotifyUsersForMinutesAfterUtcForToday() map[int]map[mid.UserID]mid.RoomID {
	notifyTimes := make(map[int]map[mid.UserID]mid.RoomID)

	query := `
			SELECT user_id, room_id, timezone, minutes_after_midnight
			FROM user_config_room
		`
	rows, err := store.DB.Query(query)
	if err != nil {
		return notifyTimes
	}
	defer rows.Close()

	var userID mid.UserID
	var roomID mid.RoomID
	var timezone string
	var minutesAfterMidnight int
	for rows.Next() {
		if err := rows.Scan(&userID, &roomID, &timezone, &minutesAfterMidnight); err == nil {
			location, err := time.LoadLocation(timezone)
			if err != nil {
				continue
			}
			now := time.Now()
			midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)

			// Don't add a notification time if it's on the weekend
			if midnight.Weekday() == time.Saturday || midnight.Weekday() == time.Sunday {
				log.Debugf("It is the weekend in %s, not including the notification time in the dictionary.", location)
				continue
			}

			notifyTime := midnight.Add(time.Duration(minutesAfterMidnight) * time.Minute)

			h, m, _ := notifyTime.UTC().Clock()
			minutesAfterUtcMidnight := h*60 + m

			if _, exists := notifyTimes[minutesAfterUtcMidnight]; !exists {
				notifyTimes[minutesAfterUtcMidnight] = make(map[mid.UserID]mid.RoomID)
			}
			notifyTimes[minutesAfterUtcMidnight][userID] = roomID
		}
	}

	return notifyTimes
}
