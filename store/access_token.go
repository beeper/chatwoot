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

	upsert := `
		INSERT INTO chatwoot_meta (meta_id, access_token)
			VALUES (1, $1)
		ON CONFLICT (meta_id) DO UPDATE
			SET access_token = $1
	`
	if _, err := tx.Exec(upsert, accessToken); err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}
