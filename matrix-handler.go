package main

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
	"maunium.net/go/mautrix"
	mevent "maunium.net/go/mautrix/event"
	mid "maunium.net/go/mautrix/id"

	"gitlab.com/beeper/chatwoot/chatwootapi"
)

var createRoomLock sync.Mutex = sync.Mutex{}

func createChatwootConversation(roomID mid.RoomID, contactMxid mid.UserID) (int, error) {
	log.Debug("Acquired create room lock")
	createRoomLock.Lock()
	defer log.Debug("Released create room lock")
	defer createRoomLock.Unlock()

	if conversationID, err := stateStore.GetChatwootConversationIDFromMatrixRoom(roomID); err == nil {
		return conversationID, nil
	}

	contactID, err := chatwootApi.ContactIDForMxid(contactMxid)
	if err != nil {
		log.Errorf("Contact ID not found for user with MXID: %s. Error: %s", contactMxid, err)

		contactID, err = chatwootApi.CreateContact(contactMxid)
		if err != nil {
			return 0, errors.New(fmt.Sprintf("Create contact failed for %s: %s", contactMxid, err))
		}
		log.Infof("Contact with ID %d created", contactID)
	}

	log.Infof("Creating conversation for room %s for contact %d", roomID, contactID)
	conversation, err := chatwootApi.CreateConversation(roomID.String(), contactID)
	if err != nil {
		return 0, errors.New(fmt.Sprintf("Failed to create chatwoot conversation for %s: %+v", roomID, err))
	}

	err = stateStore.UpdateConversationIdForRoom(roomID, conversation.ID)
	if err != nil {
		return 0, err
	}

	// Detect if this is the canonical DM
	if configuration.CanonicalDMPrefix != "" {
		var roomNameEvent mevent.RoomNameEventContent
		err = client.StateEvent(roomID, mevent.StateRoomName, "", &roomNameEvent)
		if err == nil {
			if strings.HasPrefix(roomNameEvent.Name, configuration.CanonicalDMPrefix) {
				err = chatwootApi.AddConversationLabel(conversation.ID, []string{"canonical-dm"})
				if err != nil {
					log.Error(err)
				}
			}
		}
	}

	return conversation.ID, nil
}

var rageshakeIssueRegex = regexp.MustCompile(`[A-Z]{1,5}-\d+`)

func HandleMessage(_ mautrix.EventSource, event *mevent.Event) {
	// Acquire the lock, so that we don't have race conditions with the
	// Chatwoot handler.
	if _, found := roomSendlocks[event.RoomID]; !found {
		log.Debugf("[message handler] creating send lock for %s", event.RoomID)
		roomSendlocks[event.RoomID] = &sync.Mutex{}
	}
	roomSendlocks[event.RoomID].Lock()
	log.Debugf("[message handler] Acquired send lock for %s", event.RoomID)
	defer log.Debugf("[message handler] Released send lock for %s", event.RoomID)
	defer roomSendlocks[event.RoomID].Unlock()

	if messageID, err := stateStore.GetChatwootMessageIdForMatrixEventId(event.ID); err == nil {
		log.Info("Matrix Event ID ", event.ID, " already has a Chatwoot message with ID ", messageID)
		return
	}

	conversationID, err := stateStore.GetChatwootConversationIDFromMatrixRoom(event.RoomID)
	if err != nil {
		if configuration.BridgeIfMembersLessThan >= 0 && len(stateStore.GetRoomMembers(event.RoomID)) >= configuration.BridgeIfMembersLessThan {
			log.Warnf("Not creating Chatwoot conversation for %s because there are not less than %d members.", event.RoomID, configuration.BridgeIfMembersLessThan)
			return
		}

		contactMxid := event.Sender
		if configuration.Username == event.Sender.String() {
			// This message came from the bot. Look for the other
			// users in the room, and use them instead.
			nonBotMembers := stateStore.GetNonBotRoomMembers(event.RoomID)
			if len(nonBotMembers) != 1 {
				log.Warnf("Not creating Chatwoot conversation for %s", event.RoomID)
				return
			}
			contactMxid = nonBotMembers[0]
		}

		log.Errorf("Chatwoot conversation not found for %s: %s", event.RoomID, err)
		conversationID, err = createChatwootConversation(event.RoomID, contactMxid)
		if err != nil {
			log.Errorf("Error creating chatwoot conversation: %+v", err)
			return
		}
	}

	cm, err := DoRetry(fmt.Sprintf("handle matrix event %s in conversation %d", event.ID, conversationID), func() (interface{}, error) {
		return HandleMatrixMessageContent(event, conversationID, &event.Content)
	})
	if err != nil {
		DoRetry(fmt.Sprintf("send private error message to %d for %+v", conversationID, err), func() (interface{}, error) {
			return chatwootApi.SendPrivateMessage(
				conversationID,
				fmt.Sprintf("**Error occurred while receiving a Matrix message. You may have missed a message!**\n\nError: %+v", err))
		})
		return
	}
	stateStore.SetChatwootMessageIdForMatrixEvent(event.ID, cm.(*chatwootapi.Message).ID)
	content := event.Content.AsMessage()
	if content.MsgType == mevent.MsgText || content.MsgType == mevent.MsgNotice {
		linearLinks := []string{}
		for _, match := range rageshakeIssueRegex.FindAllString(content.Body, -1) {
			linearLinks = append(linearLinks, fmt.Sprintf("https://linear.app/beeper/issue/%s", match))
		}
		if len(linearLinks) > 0 {
			chatwootApi.SendPrivateMessage(conversationID, strings.Join(linearLinks, "\n\n"))
		}
	}
}

func HandleReaction(_ mautrix.EventSource, event *mevent.Event) {
	// Acquire the lock, so that we don't have race conditions with the
	// Chatwoot handler.
	if _, found := roomSendlocks[event.RoomID]; !found {
		log.Debugf("[reaction handler] creating send lock for %s", event.RoomID)
		roomSendlocks[event.RoomID] = &sync.Mutex{}
	}
	roomSendlocks[event.RoomID].Lock()
	log.Debugf("[reaction handler] Acquired send lock for %s", event.RoomID)
	defer log.Debugf("[reaction handler] Released send lock for %s", event.RoomID)
	defer roomSendlocks[event.RoomID].Unlock()

	if messageID, err := stateStore.GetChatwootMessageIdForMatrixEventId(event.ID); err == nil {
		log.Info("Matrix Event ID ", event.ID, " already has a Chatwoot message with ID ", messageID)
		return
	}

	conversationID, err := stateStore.GetChatwootConversationIDFromMatrixRoom(event.RoomID)
	if err != nil {
		log.Errorf("Chatwoot conversation not found for %s: %+v", event.RoomID, err)
		return
	}

	cm, err := DoRetry(fmt.Sprintf("send notification of reaction to %d", conversationID), func() (interface{}, error) {
		reaction := event.Content.AsReaction()
		reactedEvent, err := client.GetEvent(event.RoomID, reaction.RelatesTo.EventID)
		if err != nil {
			return nil, errors.New(fmt.Sprintf("Couldn't find reacted to event %s: %+v", reaction.RelatesTo.EventID, err))
		}

		if reactedEvent.Type == mevent.EventEncrypted {
			err := reactedEvent.Content.ParseRaw(reactedEvent.Type)
			if err != nil {
				return nil, err
			}

			decryptedEvent, err := olmMachine.DecryptMegolmEvent(reactedEvent)
			if err != nil {
				return nil, err
			}
			reactedEvent = decryptedEvent
		}

		reactedMessage := reactedEvent.Content.AsMessage()
		var reactedMessageText string
		switch reactedMessage.MsgType {
		case mevent.MsgText, mevent.MsgNotice, mevent.MsgAudio, mevent.MsgFile, mevent.MsgImage, mevent.MsgVideo:
			reactedMessageText = reactedMessage.Body
		case mevent.MsgEmote:
			localpart, _, _ := event.Sender.Parse()
			reactedMessageText = fmt.Sprintf(" \\* %s %s", localpart, reactedMessage.Body)
		}
		return chatwootApi.SendTextMessage(
			conversationID,
			fmt.Sprintf("%s reacted with %s to \"%s\"", event.Sender, reaction.RelatesTo.Key, reactedMessageText),
			chatwootapi.IncomingMessage)
	})
	if err != nil {
		DoRetry(fmt.Sprintf("send private error message to %d for %+v", conversationID, err), func() (interface{}, error) {
			return chatwootApi.SendPrivateMessage(
				conversationID,
				fmt.Sprintf("**Error occurred while receiving a Matrix reaction. You may have missed a message reaction!**\n\nError: %+v", err))
		})
		return
	}
	stateStore.SetChatwootMessageIdForMatrixEvent(event.ID, cm.(*chatwootapi.Message).ID)
}

func HandleMatrixMessageContent(event *mevent.Event, conversationID int, rawContent *mevent.Content) (*chatwootapi.Message, error) {
	content := event.Content.AsMessage()
	messageType := chatwootapi.IncomingMessage
	if configuration.Username == event.Sender.String() {
		messageType = chatwootapi.OutgoingMessage
	}

	var cm *chatwootapi.Message
	var err error

	versionInfo := []string{}
	if clientType, exists := rawContent.Raw["com.beeper.origin_client_type"]; exists && clientType != nil {
		versionInfo = append(versionInfo, fmt.Sprintf("Client Type: %s", clientType))
	}
	if clientVersion, exists := rawContent.Raw["com.beeper.origin_client_version"]; exists && clientVersion != nil {
		versionInfo = append(versionInfo, fmt.Sprintf("Client Version: %s", clientVersion))
	}

	var versionString string
	if len(versionInfo) > 0 {
		versionString = "\n\n" + strings.Join(versionInfo, "\n")
	}

	switch content.MsgType {
	case mevent.MsgText, mevent.MsgNotice:
		relatesTo := content.RelatesTo
		body := content.Body
		if relatesTo != nil && relatesTo.Type == mevent.RelReplace {
			if strings.HasPrefix(body, " * ") {
				body = " \\* " + body[3:]
			}
		}
		messageText := fmt.Sprintf("%s%s", body, versionString)
		cm, err = chatwootApi.SendTextMessage(conversationID, messageText, messageType)

	case mevent.MsgEmote:
		localpart, _, _ := event.Sender.Parse()
		messageText := fmt.Sprintf(" \\* %s %s%s", localpart, content.Body, versionString)
		cm, err = chatwootApi.SendTextMessage(conversationID, messageText, messageType)

	case mevent.MsgAudio, mevent.MsgFile, mevent.MsgImage, mevent.MsgVideo:
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

		messageText := fmt.Sprintf("%s%s", content.Body, versionString)
		cm, err = chatwootApi.SendAttachmentMessage(conversationID, messageText, content.Info.MimeType, bytes.NewReader(data), messageType)
		if err != nil {
			return nil, errors.New(fmt.Sprintf("Failed to send attachment message. Error: %+v", err))
		}
	}

	return cm, err
}

func HandleRedaction(_ mautrix.EventSource, event *mevent.Event) {
	// Acquire the lock, so that we don't have race conditions with the
	// Chatwoot handler.
	if _, found := roomSendlocks[event.RoomID]; !found {
		log.Debugf("[redaction handler] creating send lock for %s", event.RoomID)
		roomSendlocks[event.RoomID] = &sync.Mutex{}
	}
	roomSendlocks[event.RoomID].Lock()
	log.Debugf("[redaction handler] Acquired send lock for %s", event.RoomID)
	defer log.Debugf("[redaction handler] Released send lock for %s", event.RoomID)
	defer roomSendlocks[event.RoomID].Unlock()

	messageID, err := stateStore.GetChatwootMessageIdForMatrixEventId(event.Redacts)
	if err != nil {
		log.Info("[redaction handler] No Chatwoot message for Matrix event ", event.Redacts)
		return
	}

	conversationID, err := stateStore.GetChatwootConversationIDFromMatrixRoom(event.RoomID)
	if err != nil {
		log.Warn("[redaction handler] No Chatwoot conversation associated with ", event.RoomID)
		return
	}

	err = chatwootApi.DeleteMessage(conversationID, messageID)
	if err != nil {
		log.Infof("[redaction handler] Failed to delete Chatwoot message: %+v", err)
	}
}
