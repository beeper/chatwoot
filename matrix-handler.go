package main

import (
	"bytes"
	"errors"
	"fmt"
	"sync"

	log "github.com/sirupsen/logrus"
	"maunium.net/go/mautrix"
	mevent "maunium.net/go/mautrix/event"

	"gitlab.com/beeper/chatwoot/chatwootapi"
)

var createRoomLock sync.Mutex = sync.Mutex{}

func createChatwootConversation(event *mevent.Event) (int, error) {
	log.Debug("Acquired create room lock")
	createRoomLock.Lock()
	defer log.Debug("Released create room lock")
	defer createRoomLock.Unlock()

	conversationID, err := stateStore.GetChatwootConversationFromMatrixRoom(event.RoomID)
	if err == nil {
		return conversationID, nil
	}

	contactID, err := chatwootApi.ContactIDForMxid(event.Sender)
	if err != nil {
		log.Errorf("Contact ID not found for user with MXID: %s. Error: %s", event.Sender, err)

		contactID, err = chatwootApi.CreateContact(event.Sender)
		if err != nil {
			return 0, errors.New(fmt.Sprintf("Create contact failed for %s: %s", event.Sender, err))
		}
		log.Infof("Contact with ID %d created", contactID)
	}

	// Try and find an existing conversation with the user in the
	// configured inbox.
	var conversation *chatwootapi.Conversation = nil
	conversations, err := chatwootApi.GetContactConversations(contactID)
	if err == nil {
		for _, c := range conversations {
			if c.InboxID == configuration.ChatwootInboxID {
				conversation = &c
				break
			}
		}
	}

	if conversation == nil {
		log.Infof("Creating conversation for room %s for contact %d", event.RoomID, contactID)
		conversation, err = chatwootApi.CreateConversation(event.RoomID.String(), contactID)
		if err != nil {
			return 0, errors.New(fmt.Sprintf("Failed to create chatwoot conversation for %s: %+v", event.RoomID, err))
		}
	}

	err = stateStore.UpdateConversationIdForRoom(event.RoomID, conversation.ID)
	if err != nil {
		return 0, err
	}
	return conversation.ID, nil
}

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
		log.Errorf("Chatwoot conversation not found for %s: %s", event.RoomID, err)
		conversationID, err = createChatwootConversation(event)
		if err != nil {
			log.Errorf("Error creating chatwoot conversation: %+v", err)
			// TODO notify somehow
			return
		}
	}

	// Ensure that if the webhook event comes through before the message ID
	// is persisted to the database it will be properly deduplicated.
	_, found := userSendlocks[event.Sender]
	if !found {
		log.Debugf("Creating send lock for %s", event.Sender)
		userSendlocks[event.Sender] = &sync.Mutex{}
	}
	userSendlocks[event.Sender].Lock()
	log.Debugf("[matrix-handler] Acquired send lock for %s", event.Sender)
	defer log.Debugf("[matrix-handler] Released send lock for %s", event.Sender)
	defer userSendlocks[event.Sender].Unlock()

	content := event.Content.AsMessage()
	cm, err := DoRetry(fmt.Sprintf("handle matrix event %s in conversation %d", event.ID, conversationID), func() (interface{}, error) {
		return HandleMatrixMessageContent(event, conversationID, content)
	})
	if err != nil {
		DoRetry(fmt.Sprintf("send private error message to %d for %+v", conversationID, err), func() (interface{}, error) {
			return chatwootApi.SendPrivateMessage(
				conversationID,
				fmt.Sprintf("**Error occurred while receiving a Matrix message. You may have missed a message!**\n\nError: %+v", err))
		})
		// TODO figure out what to do about the error here.
		return
	}
	stateStore.SetChatwootMessageIdForMatrixEvent(event.ID, cm.(*chatwootapi.Message).ID)
}

func HandleMatrixMessageContent(event *mevent.Event, conversationID int, content *mevent.MessageEventContent) (*chatwootapi.Message, error) {
	messageType := chatwootapi.IncomingMessage
	if configuration.Username == event.Sender.String() {
		messageType = chatwootapi.OutgoingMessage
	}

	var cm *chatwootapi.Message
	var err error

	switch content.MsgType {
	case mevent.MsgText, mevent.MsgNotice:
		cm, err = chatwootApi.SendTextMessage(conversationID, content.Body, messageType)
		break

	case mevent.MsgEmote:
		localpart, _, _ := event.Sender.Parse()
		cm, err = chatwootApi.SendTextMessage(conversationID, fmt.Sprintf(" \\* %s %s", localpart, content.Body), messageType)
		break

	case mevent.MsgAudio, mevent.MsgFile, mevent.MsgImage, mevent.MsgVideo:
		log.Debug(content)

		var file *mevent.EncryptedFileInfo
		rawMXC := content.URL
		if content.File != nil {
			file = content.File
			rawMXC = file.URL
		}
		mxc, err := rawMXC.Parse()
		if err != nil {
			return nil, errors.New(fmt.Sprintf("Malformed content URL in %s: %+v", event.ID, err))
		}

		data, err := client.DownloadBytes(mxc)
		if err != nil {
			return nil, errors.New(fmt.Sprintf("Failed to download media in %s: %+v", event.ID, err))
		}

		if file != nil {
			data, err = file.Decrypt(data)
			if err != nil {
				return nil, errors.New(fmt.Sprintf("Failed to decrypt media in %s: %+v", event.ID, err))
			}
		}

		cm, err = chatwootApi.SendAttachmentMessage(conversationID, content.Body, bytes.NewReader(data), messageType)
		if err != nil {
			return nil, errors.New(fmt.Sprintf("Failed to send attachment message. Error: %+v", err))
		}
		break
	}

	return cm, err
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
