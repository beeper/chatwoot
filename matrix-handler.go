package main

import (
	"bytes"
	"sync"

	log "github.com/sirupsen/logrus"
	"maunium.net/go/mautrix"
	mevent "maunium.net/go/mautrix/event"

	"gitlab.com/beeper/chatwoot/chatwootapi"
)

var createRoomLock sync.RWMutex = sync.RWMutex{}

func HandleMessage(_ mautrix.EventSource, event *mevent.Event) {
	if messageID, err := stateStore.GetChatwootMessageIdForMatrixEventId(event.ID); err == nil {
		log.Info("Matrix Event ID ", event.ID, " already has a Chatwoot message with ID ", messageID)
		return
	}

	conversationID, err := stateStore.GetChatwootConversationFromMatrixRoom(event.RoomID)
	if err != nil {
		createRoomLock.Lock()
		email, err := GetEmailForUser(event.Sender)
		if err != nil {
			// TODO do something smart here
			log.Error("Email not found for user: ", event.Sender)
			return
		}

		contactID, err := chatwootApi.ContactIDWithEmail(email)
		if err != nil {
			log.Error("Contact ID not found for user with email: ", email)
			return
		}
		conversation, err := chatwootApi.CreateConversation(event.RoomID.String(), contactID)
		if err != nil {
			log.Error("Failed to create chatwoot conversation for ", event.RoomID)
			return
		}

		stateStore.UpdateConversationIdForRoom(event.RoomID, conversation.ID)
		conversationID = conversation.ID
		createRoomLock.Unlock()
	}

	content := event.Content.AsMessage()
	var cm *chatwootapi.Message
	switch content.MsgType {
	case mevent.MsgText, mevent.MsgNotice:
		cm, err = chatwootApi.SendTextMessage(conversationID, content.Body)
		break

	case mevent.MsgEmote:
		cm, err = chatwootApi.SendTextMessage(conversationID, content.Body)
		break

	case mevent.MsgAudio, mevent.MsgFile, mevent.MsgImage, mevent.MsgVideo:
		log.Info(content)

		var file *mevent.EncryptedFileInfo
		rawMXC := content.URL
		if content.File != nil {
			file = content.File
			rawMXC = file.URL
		}
		mxc, err := rawMXC.Parse()
		if err != nil {
			log.Errorf("Malformed content URL in %s: %v", event.ID, err)
			return
		}

		data, err := client.DownloadBytes(mxc)
		if err != nil {
			log.Errorf("Failed to download media in %s: %v", event.ID, err)
			return
		}

		if file != nil {
			data, err = file.Decrypt(data)
			if err != nil {
				log.Errorf("Failed to decrypt media in %s: %v", event.ID, err)
				return
			}
		}

		cm, err = chatwootApi.SendAttachmentMessage(conversationID, content.Body, bytes.NewReader(data))
		if err != nil {
			log.Errorf("Failed to send attachment message. Error: %v", err)
			return
		}
		break
	}

	if err != nil {
		log.Error(err)
		return
	}
	stateStore.SetChatwootMessageIdForMatrixEvent(event.ID, cm.ID)
}

func HandleRedaction(_ mautrix.EventSource, event *mevent.Event) {
	conversationID, err := stateStore.GetChatwootConversationFromMatrixRoom(event.RoomID)
	if err != nil {
		log.Warn("No Chatwoot conversation associated with ", event.RoomID)
		return
	}

	messageID, err := stateStore.GetChatwootMessageIdForMatrixEventId(event.Redacts)
	if err != nil {
		log.Info("No Chatwoot message for Matrix event ", event.Redacts)
		return
	}

	chatwootApi.DeleteMessage(conversationID, messageID)
}
