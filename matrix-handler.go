package main

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"maunium.net/go/mautrix"
	mevent "maunium.net/go/mautrix/event"
	mid "maunium.net/go/mautrix/id"

	"github.com/beeper/chatwoot/chatwootapi"
)

var createRoomLock sync.Mutex = sync.Mutex{}

func createChatwootConversation(roomID mid.RoomID, contactMxid mid.UserID, customAttrs map[string]string) (int, error) {
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
	conversation, err := chatwootApi.CreateConversation(roomID.String(), contactID, customAttrs)
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

func GetCustomAttrForDevice(event *mevent.Event) (string, string) {
	clientType, exists := event.Content.Raw["com.beeper.origin_client_type"]
	if !exists || clientType == nil {
		return "", ""
	}

	var clientTypeString, clientVersionString string
	if ct, ok := clientType.(string); ok {
		clientTypeString = fmt.Sprintf("%s version", ct)
	} else {
		return "", ""
	}

	clientVersion, exists := event.Content.Raw["com.beeper.origin_client_version"]
	if !exists && clientVersion == nil {
		return "", ""
	}

	if cv, ok := clientVersion.(string); ok {
		clientVersionString = cv
	} else {
		return "", ""
	}

	log.Debugf("Got client type '%s' and client version '%s'", clientTypeString, clientVersionString)
	return clientTypeString, clientVersionString
}

var deviceVersionRegex = regexp.MustCompile(`(\S+)( \(last updated at .*\))?`)

func HandleBeeperClientInfo(event *mevent.Event) error {
	conversationID, err := stateStore.GetChatwootConversationIDFromMatrixRoom(event.RoomID)
	if err != nil {
		return err
	}

	deviceTypeKey, deviceVersion := GetCustomAttrForDevice(event)
	if deviceTypeKey != "" && deviceVersion != "" {
		conv, err := chatwootApi.GetChatwootConversation(conversationID)
		if err != nil {
			log.Error("Failed to get Chatwoot conversation", err)
			return err
		}
		customAttrs := conv.CustomAttributes
		currentDeviceVersion := customAttrs[deviceTypeKey]

		version := deviceVersionRegex.FindStringSubmatch(customAttrs[deviceTypeKey])
		if version != nil {
			currentDeviceVersion = version[1]
		}

		if currentDeviceVersion != deviceVersion {
			now := time.Now().Format("2006-01-02 15:04:05 UTC")
			versionWithLastUpdated := fmt.Sprintf("%s (last updated at %s)", deviceVersion, now)
			customAttrs[deviceTypeKey] = versionWithLastUpdated

			log.Debugf("Setting custom attribute on the conversation %d / %s :: %s", conversationID, deviceTypeKey, versionWithLastUpdated)
			err := chatwootApi.SetConversationCustomAttributes(conversationID, customAttrs)
			if err != nil {
				log.Errorf("Failed to set custom attributes on the conversation %s -> %s: %v", deviceTypeKey, deviceVersion, customAttrs)
				return err
			}
		}
	}

	return nil
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

	if messageIDs, err := stateStore.GetChatwootMessageIdsForMatrixEventId(event.ID); err == nil && len(messageIDs) > 0 {
		log.Info("Matrix Event ID ", event.ID, " already has a Chatwoot message(s) with ID(s) ", messageIDs)
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
		var customAttrs map[string]string
		deviceTypeKey, deviceVersion := GetCustomAttrForDevice(event)
		if deviceTypeKey != "" && deviceVersion != "" {
			customAttrs[deviceTypeKey] = deviceVersion
		}
		conversationID, err = createChatwootConversation(event.RoomID, contactMxid, customAttrs)
		if err != nil {
			log.Errorf("Error creating chatwoot conversation: %+v", err)
			return
		}
	}

	cm, err := DoRetry(fmt.Sprintf("handle matrix event %s in conversation %d", event.ID, conversationID), func() (*[]*chatwootapi.Message, error) {
		content := event.Content.AsMessage()
		messages, err := HandleMatrixMessageContent(event, conversationID, content)
		return &messages, err
	})
	if err != nil {
		DoRetry(fmt.Sprintf("send private error message to %d for %+v", conversationID, err), func() (*chatwootapi.Message, error) {
			return chatwootApi.SendPrivateMessage(
				conversationID,
				fmt.Sprintf("**Error occurred while receiving a Matrix message. You may have missed a message!**\n\nError: %+v", err))
		})
		return
	}
	for _, m := range *cm {
		stateStore.SetChatwootMessageIdForMatrixEvent(event.ID, m.ID)
	}
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

	if messageIDs, err := stateStore.GetChatwootMessageIdsForMatrixEventId(event.ID); err == nil && len(messageIDs) > 0 {
		log.Info("Matrix Event ID ", event.ID, " already has a Chatwoot message(s) with ID(s) ", messageIDs)
		return
	}

	conversationID, err := stateStore.GetChatwootConversationIDFromMatrixRoom(event.RoomID)
	if err != nil {
		log.Errorf("Chatwoot conversation not found for %s: %+v", event.RoomID, err)
		return
	}

	cm, err := DoRetry(fmt.Sprintf("send notification of reaction to %d", conversationID), func() (*chatwootapi.Message, error) {
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
		DoRetry(fmt.Sprintf("send private error message to %d for %+v", conversationID, err), func() (*chatwootapi.Message, error) {
			return chatwootApi.SendPrivateMessage(
				conversationID,
				fmt.Sprintf("**Error occurred while receiving a Matrix reaction. You may have missed a message reaction!**\n\nError: %+v", err))
		})
		return
	}
	stateStore.SetChatwootMessageIdForMatrixEvent(event.ID, (*cm).ID)
}

func HandleMatrixMessageContent(event *mevent.Event, conversationID int, content *mevent.MessageEventContent) ([]*chatwootapi.Message, error) {
	messageType := chatwootapi.IncomingMessage
	if configuration.Username == event.Sender.String() {
		messageType = chatwootapi.OutgoingMessage
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
		cm, err := chatwootApi.SendTextMessage(conversationID, body, messageType)
		return []*chatwootapi.Message{cm}, err

	case mevent.MsgEmote:
		localpart, _, _ := event.Sender.Parse()
		cm, err := chatwootApi.SendTextMessage(conversationID, fmt.Sprintf(" \\* %s %s", localpart, content.Body), messageType)
		return []*chatwootapi.Message{cm}, err

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

		filename := content.Body
		caption := ""
		if content.FileName != "" {
			filename = content.FileName
			caption = content.Body
		}

		cm, err := chatwootApi.SendAttachmentMessage(conversationID, filename, content.Info.MimeType, bytes.NewReader(data), messageType)
		if err != nil {
			return nil, errors.New(fmt.Sprintf("Failed to send attachment message. Error: %+v", err))
		}
		messages := []*chatwootapi.Message{cm}

		if caption != "" {
			captionMessage, captionErr := chatwootApi.SendTextMessage(conversationID, fmt.Sprintf("Caption: %s", caption), messageType)
			if captionErr != nil {
				log.Errorf("Failed to send caption message. Error: %+v", captionErr)
			} else {
				messages = append(messages, captionMessage)
			}
		}

		return messages, err

	default:
		return nil, errors.New(fmt.Sprintf("Unsupported message type %s in %s", content.MsgType, event.ID))
	}
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

	messageIDs, err := stateStore.GetChatwootMessageIdsForMatrixEventId(event.ID)
	if err != nil || len(messageIDs) == 0 {
		log.Info("[redaction handler] No Chatwoot message for Matrix event ", event.Redacts)
		return
	}

	conversationID, err := stateStore.GetChatwootConversationIDFromMatrixRoom(event.RoomID)
	if err != nil {
		log.Warn("[redaction handler] No Chatwoot conversation associated with ", event.RoomID)
		return
	}

	for _, messageID := range messageIDs {
		err = chatwootApi.DeleteMessage(conversationID, messageID)
		if err != nil {
			log.Infof("[redaction handler] Failed to delete Chatwoot message: %+v", err)
		}
	}
}
