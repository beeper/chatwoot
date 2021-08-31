package main

import (
	"bytes"
	"fmt"
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
		if configuration.Username == event.Sender.String() {
			log.Warnf("Not creating Chatwoot conversation for %s", event.Sender)
			return
		}

		createRoomLock.Lock()
		log.Errorf("Chatwoot conversation not found for %s: %s", event.RoomID, err)
		contactID, err := chatwootApi.ContactIDForMxid(event.Sender)
		if err != nil {
			log.Errorf("Contact ID not found for user with MXID: %s. Error: %s", event.Sender, err)

			contactID, err = chatwootApi.CreateContact(event.Sender)
			if err != nil {
				log.Errorf("Create contact failed for %s: %s", event.Sender, err)
				createRoomLock.Unlock()
				return
			}
			log.Infof("Contact with ID %d created", contactID)
		}
		conversation, err := chatwootApi.CreateConversation(event.RoomID.String(), contactID)
		if err != nil {
			log.Error("Failed to create chatwoot conversation for ", event.RoomID)
			createRoomLock.Unlock()
			return
		}

		err = stateStore.UpdateConversationIdForRoom(event.RoomID, conversation.ID)
		if err != nil {
			log.Error(err)
			return
		}
		conversationID = conversation.ID
		createRoomLock.Unlock()
	}

	messageType := chatwootapi.IncomingMessage
	if configuration.Username == event.Sender.String() {
		messageType = chatwootapi.OutgoingMessage
	}

	content := event.Content.AsMessage()
	var cm *chatwootapi.Message
	switch content.MsgType {
	case mevent.MsgText, mevent.MsgNotice:
		cm, err = chatwootApi.SendTextMessage(conversationID, content.Body, messageType)
		break

	case mevent.MsgEmote:
		localpart, _, _ := event.Sender.Parse()
		cm, err = chatwootApi.SendTextMessage(conversationID, fmt.Sprintf(" \\* %s %s", localpart, content.Body), messageType)
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

		cm, err = chatwootApi.SendAttachmentMessage(conversationID, content.Body, bytes.NewReader(data), messageType)
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
