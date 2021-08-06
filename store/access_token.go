package store

import log "github.com/sirupsen/logrus"

func (store *StateStore) GetAccessToken() (string, error) {
	row := store.DB.QueryRow("SELECT access_token FROM chatwoot_meta")
	var accessToken string
	if err := row.Scan(&accessToken); err != nil {
		return "", err
	}

	return accessToken, nil
}

func (store *StateStore) SetAccessToken(accessToken string) error {
	log.Debug("Upserting row into chatwoot_meta")
	tx, err := store.DB.Begin()
	if err != nil {
		tx.Rollback()
		return err
	}

	update := "UPDATE chatwoot_meta SET access_token = ? WHERE meta_id = 1"
	if _, err := tx.Exec(update, accessToken); err != nil {
		tx.Rollback()
		return err
	}

	insert := "INSERT OR IGNORE INTO chatwoot_meta VALUES (1, ?)"
	if _, err := tx.Exec(insert, accessToken); err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}
