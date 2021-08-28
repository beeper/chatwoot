package main

import (
	"sync"

	log "github.com/sirupsen/logrus"

	"maunium.net/go/mautrix"
	mevent "maunium.net/go/mautrix/event"
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

	message, err := chatwootApi.SendTextMessage(conversationID, event.Content.AsMessage().Body)
	if err != nil {
		log.Error(err)
		return
	}
	stateStore.SetChatwootMessageIdForMatrixEvent(event.ID, message.ID)
}

func HandleReaction(_ mautrix.EventSource, event *mevent.Event) {
}

func HandleRedaction(_ mautrix.EventSource, event *mevent.Event) {
}
