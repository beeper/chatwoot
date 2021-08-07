package main

import (
	"encoding/json"
	"net/http"

	log "github.com/sirupsen/logrus"
	"maunium.net/go/mautrix"
	mcrypto "maunium.net/go/mautrix/crypto"
	mevent "maunium.net/go/mautrix/event"
	mid "maunium.net/go/mautrix/id"

	"gitlab.com/beeper/chatwoot/chatwootapi"
)

func SendMessage(roomId mid.RoomID, content mevent.MessageEventContent) (resp *mautrix.RespSendEvent, err error) {
	r, err := DoRetry("send message to "+roomId.String(), func() (interface{}, error) {
		if stateStore.IsEncrypted(roomId) {
			log.Debugf("Sending event to %s encrypted: %+v", roomId, content)
			encrypted, err := olmMachine.EncryptMegolmEvent(roomId, mevent.EventMessage, content)

			// These three errors mean we have to make a new Megolm session
			if err == mcrypto.SessionExpired || err == mcrypto.SessionNotShared || err == mcrypto.NoGroupSession {
				err = olmMachine.ShareGroupSession(roomId, stateStore.GetRoomMembers(roomId))
				if err != nil {
					log.Errorf("Failed to share group session to %s: %s", roomId, err)
					return nil, err
				}
				encrypted, err = olmMachine.EncryptMegolmEvent(roomId, mevent.EventMessage, content)
			}

			if err != nil {
				log.Errorf("Failed to encrypt message to %s: %s", roomId, err)
				return nil, err
			}

			encrypted.RelatesTo = content.RelatesTo // The m.relates_to field should be unencrypted, so copy it.
			return client.SendMessageEvent(roomId, mevent.EventEncrypted, encrypted)
		} else {
			log.Debugf("Sending event to %s unencrypted: %+v", roomId, content)
			return client.SendMessageEvent(roomId, mevent.EventMessage, content)
		}
	})
	if err != nil {
		// give up
		log.Errorf("Failed to send message to %s: %s", roomId, err)
		return nil, err
	}
	return r.(*mautrix.RespSendEvent), err
}

func HandleWebhook(w http.ResponseWriter, r *http.Request) {
	decoder := json.NewDecoder(r.Body)
	var mc chatwootapi.MessageCreated
	err := decoder.Decode(&mc)
	if err != nil {
		log.Error(err)
		return
	}

	if eventID, err := stateStore.GetMatrixEventIdForChatwootMessage(mc.ID); err == nil {
		log.Info("Chatwoot message with ID ", mc.ID, " already has a Matrix Event ID ", eventID)
		return
	}

	roomID, err := stateStore.GetMatrixRoomFromChatwootConversation(mc.Conversation.ID)
	if err != nil {
		log.Error("No room for ", mc.Conversation.ID)
		log.Error(err)
		return
	}
	resp, err := SendMessage(roomID, mevent.MessageEventContent{
		MsgType: mevent.MsgText,
		Body:    mc.Content,
	})
	if err != nil {
		log.Error(err)
		return
	}

	stateStore.SetChatwootMessageIdForMatrixEvent(resp.EventID, mc.ID)
}
