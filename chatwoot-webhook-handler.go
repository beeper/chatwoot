package main

import (
	"encoding/json"
	"net/http"

	log "github.com/sirupsen/logrus"

	"gitlab.com/beeper/chatwoot/chatwootapi"
)

func HandleWebhook(w http.ResponseWriter, r *http.Request) {
	decoder := json.NewDecoder(r.Body)
	var mc chatwootapi.MessageCreatedEvent
	err := decoder.Decode(&mc)
	if err != nil {
		log.Warn(err)
		return
	}
	log.Info(mc)
}
