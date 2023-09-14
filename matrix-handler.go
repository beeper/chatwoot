package main

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/sqlstatestore"

	"github.com/beeper/chatwoot/chatwootapi"
)

var createRoomLock sync.Mutex = sync.Mutex{}

func createChatwootConversation(ctx context.Context, roomID id.RoomID, contactMxid id.UserID, customAttrs map[string]string) (int, error) {
	log := zerolog.Ctx(ctx).With().
		Str("component", "create_chatwoot_conversation").
		Str("room_id", roomID.String()).
		Str("contact_mxid", contactMxid.String()).
		Interface("custom_attrs", customAttrs).
		Logger()
	ctx = log.WithContext(ctx)

	log.Debug().Msg("Acquired create room lock")
	createRoomLock.Lock()
	defer log.Debug().Msg("Released create room lock")
	defer createRoomLock.Unlock()

	if conversationID, err := stateStore.GetChatwootConversationIDFromMatrixRoom(ctx, roomID); err == nil {
		return conversationID, nil
	}

	contactID, err := chatwootAPI.ContactIDForMxid(contactMxid)
	if err != nil {
		log.Warn().Err(err).Msg("contact ID not found for user, will attempt to create one")

		contactID, err = chatwootAPI.CreateContact(ctx, contactMxid)
		if err != nil {
			return 0, fmt.Errorf("create contact failed for %s: %w", contactMxid, err)
		}
		log.Info().Int("contact_id", contactID).Msg("Contact created")
	}

	log = log.With().Int("contact_id", contactID).Logger()

	log.Info().Msg("creating Chatwoot conversation")
	conversation, err := chatwootAPI.CreateConversation(roomID.String(), contactID, customAttrs)
	if err != nil {
		return 0, fmt.Errorf("failed to create chatwoot conversation for %s: %w", roomID, err)
	}
	log = log.With().Int("conversation_id", conversation.ID).Logger()
	ctx = log.WithContext(ctx)

	err = stateStore.UpdateConversationIdForRoom(ctx, roomID, conversation.ID)
	if err != nil {
		return 0, err
	}

	_, err = client.SendStateEvent(roomID, chatwootConversationIDType, "", ChatwootConversationIDEventContent{
		ConversationID: conversation.ID,
	})
	if err != nil {
		log.Warn().Err(err).Msg("Failed to send conversation_id state event")
	}

	// Detect if this is the canonical DM
	if configuration.CanonicalDMPrefix != "" {
		var roomNameEvent event.RoomNameEventContent
		err = client.StateEvent(roomID, event.StateRoomName, "", &roomNameEvent)
		if err == nil {
			if strings.HasPrefix(roomNameEvent.Name, configuration.CanonicalDMPrefix) {
				go func() {
					// Wait 30 seconds so that the new-user automation works
					// and we don't race when adding canonical-dm.
					time.Sleep(30 * time.Second)
					log.Info().Msg("Adding canonical-dm label to conversation")

					labels, err := chatwootAPI.GetConversationLabels(conversation.ID)
					if err != nil {
						log.Err(err).Msg("Failed to list conversation labels")
					}
					log.Debug().Msg("Got current conversation labels")
					labels = append(labels, "canonical-dm")

					log.Info().Strs("labels", labels).Msg("Setting conversation labels")
					err = chatwootAPI.SetConversationLabels(conversation.ID, labels)
					if err != nil {
						log.Err(err).Msg("failed to add canonical-dm label to conversation")
					}
				}()
			}
		}
	}

	return conversation.ID, nil
}

func GetCustomAttrForDevice(ctx context.Context, evt *event.Event) (string, string) {
	log := zerolog.Ctx(ctx).With().
		Str("component", "get_custom_attr_for_device").
		Logger()

	clientType, exists := evt.Content.Raw["com.beeper.origin_client_type"]
	if !exists || clientType == nil {
		log.Debug().Msg("No client type found")
		return "", ""
	}

	var clientTypeString, clientVersionString string
	if ct, ok := clientType.(string); ok {
		clientTypeString = fmt.Sprintf("%s version", ct)
	} else {
		log.Warn().Msg("Client type is not a string")
		return "", ""
	}

	clientVersion, exists := evt.Content.Raw["com.beeper.origin_client_version"]
	if !exists && clientVersion == nil {
		log.Debug().Msg("No client version found")
		return "", ""
	}

	if cv, ok := clientVersion.(string); ok {
		clientVersionString = cv
	} else {
		log.Warn().Msg("Client version is not a string")
		return "", ""
	}

	log.Debug().
		Str("client_type", clientTypeString).
		Str("client_version", clientVersionString).
		Msg("got client type and version")
	return clientTypeString, clientVersionString
}

var deviceVersionRegex = regexp.MustCompile(`(\S+)( \(last updated at .*\))?`)

func HandleBeeperClientInfo(ctx context.Context, evt *event.Event) error {
	log := zerolog.Ctx(ctx).With().
		Str("component", "handle_beeper_client_info").
		Str("room_id", evt.RoomID.String()).
		Str("event_id", evt.ID.String()).
		Logger()
	ctx = log.WithContext(ctx)

	conversationID, err := stateStore.GetChatwootConversationIDFromMatrixRoom(ctx, evt.RoomID)
	if err != nil {
		return err
	}
	log = log.With().Int("conversation_id", conversationID).Logger()

	deviceTypeKey, deviceVersion := GetCustomAttrForDevice(ctx, evt)
	if deviceTypeKey != "" && deviceVersion != "" {
		conv, err := chatwootAPI.GetChatwootConversation(conversationID)
		if err != nil {
			log.Err(err).Msg("failed to get Chatwoot conversation")
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

			log = log.With().
				Str("device_type", deviceTypeKey).
				Str("device_version", deviceVersion).
				Str("last_updated", now).
				Logger()

			log.Debug().Msg("setting device custom attribute on conversation")

			err := chatwootAPI.SetConversationCustomAttributes(conversationID, customAttrs)
			if err != nil {
				log.Err(err).Msg("failed to set device custom attribute on conversation")
				return err
			}
		}
	}

	return nil
}

var rageshakeIssueRegex = regexp.MustCompile(`[A-Z]{1,5}-\d+`)

func HandleMessage(ctx context.Context, _ mautrix.EventSource, evt *event.Event) {
	log := zerolog.Ctx(ctx).With().
		Str("component", "handle_message").
		Str("room_id", evt.RoomID.String()).
		Str("event_id", evt.ID.String()).
		Logger()
	ctx = log.WithContext(ctx)

	// Acquire the lock, so that we don't have race conditions with the
	// Chatwoot handler.
	if _, found := roomSendlocks[evt.RoomID]; !found {
		log.Debug().Msg("creating send lock")
		roomSendlocks[evt.RoomID] = &sync.Mutex{}
	}
	roomSendlocks[evt.RoomID].Lock()
	log.Debug().Msg("acquired send lock")
	defer log.Debug().Msg("released send lock")
	defer roomSendlocks[evt.RoomID].Unlock()

	if messageIDs, err := stateStore.GetChatwootMessageIDsForMatrixEventID(ctx, evt.ID); err == nil && len(messageIDs) > 0 {
		log.Info().Interface("message_ids", messageIDs).Msg("event already has chatwoot messages")
		return
	}

	conversationID, err := GetOrCreateChatwootConversation(ctx, evt.RoomID, evt)
	if err != nil {
		log.Err(err).Msg("failed to get or create Chatwoot conversation")
		return
	}

	cm, err := DoRetryArr(ctx, fmt.Sprintf("handle matrix event %s in conversation %d", evt.ID, conversationID), func(context.Context) ([]*chatwootapi.Message, error) {
		content := evt.Content.AsMessage()
		messages, err := HandleMatrixMessageContent(ctx, evt, conversationID, content)
		return messages, err
	})
	if err != nil {
		DoRetry(ctx, fmt.Sprintf("send private error message to %d for %+v", conversationID, err), func(ctx context.Context) (*chatwootapi.Message, error) {
			msg, err := chatwootAPI.SendPrivateMessage(
				ctx,
				conversationID,
				fmt.Sprintf("**Error occurred while receiving a Matrix message. You may have missed a message!**\n\nError: %+v", err))
			if err != nil {
				return nil, err
			}
			err = chatwootAPI.ToggleStatus(ctx, conversationID, chatwootapi.ConversationStatusOpen)
			return msg, err
		})
		return
	}
	for _, m := range cm {
		stateStore.SetChatwootMessageIdForMatrixEvent(ctx, evt.ID, m.ID)
	}
	content := evt.Content.AsMessage()
	if content.MsgType == event.MsgText || content.MsgType == event.MsgNotice {
		linearLinks := []string{}
		for _, match := range rageshakeIssueRegex.FindAllString(content.Body, -1) {
			linearLinks = append(linearLinks, fmt.Sprintf("https://linear.app/beeper/issue/%s", match))
		}
		if len(linearLinks) > 0 {
			chatwootAPI.SendPrivateMessage(ctx, conversationID, strings.Join(linearLinks, "\n\n"))
		}
	}
}

func GetOrCreateChatwootConversation(ctx context.Context, roomID id.RoomID, evt *event.Event) (int, error) {
	log := zerolog.Ctx(ctx).With().Str("method", "GetOrCreateChatwootConversation").Logger()

	conversationID, err := stateStore.GetChatwootConversationIDFromMatrixRoom(ctx, roomID)
	if err == nil {
		return conversationID, nil
	}

	for i := 0; i < 2; i++ {
		joinedMembers := client.StateStore.(*sqlstatestore.SQLStateStore).GetRoomMembers(roomID, event.MembershipJoin)
		memberCount := len(joinedMembers)

		if configuration.BridgeIfMembersLessThan >= 0 && memberCount >= configuration.BridgeIfMembersLessThan {
			log.Info().
				Int("member_count", memberCount).
				Int("bridge_if_members_less_than", configuration.BridgeIfMembersLessThan).
				Msg("not creating Chatwoot conversation for room with too many members")
			return -1, fmt.Errorf("not creating Chatwoot conversation for room with %d members", memberCount)
		}

		contactMxid := evt.Sender
		if configuration.Username == evt.Sender {
			// This message came from the bot. Look for the other
			// users in the room, and use them instead.
			delete(joinedMembers, evt.Sender)
			if len(joinedMembers) != 1 {
				log.Warn().Msg("not creating Chatwoot conversation for non-DM room, re-fetching joined members")

				// TODO: this is a hack because sometimes the database state is not
				// correct. We re-fetch the joined members from the server to get
				// an updated set of users.
				membersResp, err := client.JoinedMembers(roomID)
				if err != nil {
					return -1, fmt.Errorf("failed to get joined members to verify if this conversation is a non-DM room: %w", err)
				}

				if len(membersResp.Joined) == 1 {
					// Only the bot is in the room, leave it
					log.Warn().Msg("leaving room because it was a non-DM room with only the bot in it")
					client.LeaveRoom(roomID)
					break
				}
				continue
			}
			for k := range joinedMembers {
				contactMxid = k
			}
		}

		log.Warn().Err(err).Msg("no existing Chatwoot conversation found")
		customAttrs := map[string]string{}
		deviceTypeKey, deviceVersion := GetCustomAttrForDevice(ctx, evt)
		if deviceTypeKey != "" && deviceVersion != "" {
			customAttrs[deviceTypeKey] = deviceVersion
		}
		return createChatwootConversation(ctx, evt.RoomID, contactMxid, customAttrs)
	}
	return -1, fmt.Errorf("failed to create Chatwoot conversation for room %s", roomID)
}

func HandleReaction(ctx context.Context, _ mautrix.EventSource, evt *event.Event) {
	log := zerolog.Ctx(ctx).With().
		Str("component", "handle_reaction").
		Str("room_id", evt.RoomID.String()).
		Str("event_id", evt.ID.String()).
		Logger()
	ctx = log.WithContext(ctx)

	// Acquire the lock, so that we don't have race conditions with the
	// Chatwoot handler.
	if _, found := roomSendlocks[evt.RoomID]; !found {
		log.Debug().Msg("creating send lock")
		roomSendlocks[evt.RoomID] = &sync.Mutex{}
	}
	roomSendlocks[evt.RoomID].Lock()
	log.Debug().Msg("acquiring send lock")
	defer log.Debug().Msg("released send lock")
	defer roomSendlocks[evt.RoomID].Unlock()

	if messageIDs, err := stateStore.GetChatwootMessageIDsForMatrixEventID(ctx, evt.ID); err == nil && len(messageIDs) > 0 {
		log.Info().Interface("message_ids", messageIDs).Msg("event already has chatwoot messages")
		return
	}

	conversationID, err := stateStore.GetChatwootConversationIDFromMatrixRoom(ctx, evt.RoomID)
	if err != nil {
		log.Err(err).Msg("no existing Chatwoot conversation found")
		return
	}

	cm, err := DoRetry(ctx, fmt.Sprintf("send notification of reaction to %d", conversationID), func(context.Context) (*chatwootapi.Message, error) {
		reaction := evt.Content.AsReaction()
		reactedEvent, err := client.GetEvent(evt.RoomID, reaction.RelatesTo.EventID)
		if err != nil {
			return nil, fmt.Errorf("couldn't find reacted to event %s: %w", reaction.RelatesTo.EventID, err)
		}

		if reactedEvent.Type == event.EventEncrypted {
			err := reactedEvent.Content.ParseRaw(reactedEvent.Type)
			if err != nil {
				return nil, err
			}

			decryptedEvent, err := client.Crypto.Decrypt(reactedEvent)
			if err != nil {
				return nil, err
			}
			reactedEvent = decryptedEvent
		}

		reactedMessage := reactedEvent.Content.AsMessage()
		var reactedMessageText string
		switch reactedMessage.MsgType {
		case event.MsgText, event.MsgNotice, event.MsgAudio, event.MsgFile, event.MsgImage, event.MsgVideo:
			reactedMessageText = reactedMessage.Body
		case event.MsgEmote:
			localpart, _, _ := evt.Sender.Parse()
			reactedMessageText = fmt.Sprintf(" \\* %s %s", localpart, reactedMessage.Body)
		}
		return chatwootAPI.SendTextMessage(
			ctx,
			conversationID,
			fmt.Sprintf("%s reacted with %s to \"%s\"", evt.Sender, reaction.RelatesTo.Key, reactedMessageText),
			chatwootapi.IncomingMessage)
	})
	if err != nil {
		DoRetry(ctx, fmt.Sprintf("send private error message to %d for %+v", conversationID, err), func(ctx context.Context) (*chatwootapi.Message, error) {
			return chatwootAPI.SendPrivateMessage(
				ctx,
				conversationID,
				fmt.Sprintf("**Error occurred while receiving a Matrix reaction. You may have missed a message reaction!**\n\nError: %+v", err))
		})
		return
	}
	stateStore.SetChatwootMessageIdForMatrixEvent(ctx, evt.ID, (*cm).ID)
}

func HandleMatrixMessageContent(ctx context.Context, evt *event.Event, conversationID int, content *event.MessageEventContent) ([]*chatwootapi.Message, error) {
	log := zerolog.Ctx(ctx).With().
		Str("component", "handle_matrix_message_content").
		Int("conversation_id", conversationID).
		Str("room_id", evt.RoomID.String()).
		Str("event_id", evt.ID.String()).
		Logger()
	ctx = log.WithContext(ctx)

	messageType := chatwootapi.IncomingMessage
	if configuration.Username == evt.Sender {
		messageType = chatwootapi.OutgoingMessage
	}

	switch content.MsgType {
	case event.MsgText, event.MsgNotice:
		relatesTo := content.RelatesTo
		body := content.Body
		if relatesTo != nil && relatesTo.Type == event.RelReplace {
			if strings.HasPrefix(body, " * ") {
				body = " \\* " + body[3:]
			}
		}
		cm, err := chatwootAPI.SendTextMessage(ctx, conversationID, body, messageType)
		return []*chatwootapi.Message{cm}, err

	case event.MsgEmote:
		localpart, _, _ := evt.Sender.Parse()
		cm, err := chatwootAPI.SendTextMessage(ctx, conversationID, fmt.Sprintf(" \\* %s %s", localpart, content.Body), messageType)
		return []*chatwootapi.Message{cm}, err

	case event.MsgAudio, event.MsgFile, event.MsgImage, event.MsgVideo:
		var file *event.EncryptedFileInfo
		rawMXC := content.URL
		if content.File != nil {
			file = content.File
			rawMXC = file.URL
		}
		mxc, err := rawMXC.Parse()
		if err != nil {
			return nil, fmt.Errorf("malformed content URL in %s: %w", evt.ID, err)
		}

		data, err := client.DownloadBytes(mxc)
		if err != nil {
			return nil, fmt.Errorf("failed to download media in %s: %w", evt.ID, err)
		}

		if file != nil {
			err = file.DecryptInPlace(data)
			if err != nil {
				return nil, fmt.Errorf("failed to decrypt media in %s: %w", evt.ID, err)
			}
		}

		filename := content.Body
		caption := ""
		if content.FileName != "" {
			filename = content.FileName
			caption = content.Body
		}

		mimeType := "application/octet-stream"
		if content.Info != nil {
			mimeType = content.Info.MimeType
		}

		cm, err := chatwootAPI.SendAttachmentMessage(conversationID, filename, mimeType, bytes.NewReader(data), messageType)
		if err != nil {
			return nil, fmt.Errorf("failed to send attachment message. Error: %w", err)
		}
		messages := []*chatwootapi.Message{cm}

		if caption != "" {
			captionMessage, captionErr := chatwootAPI.SendTextMessage(ctx, conversationID, fmt.Sprintf("Caption: %s", caption), messageType)
			if captionErr != nil {
				log.Err(captionErr).Msg("failed to send caption message")
			} else {
				messages = append(messages, captionMessage)
			}
		}

		return messages, err

	default:
		return nil, fmt.Errorf("unsupported message type %s in %s", content.MsgType, evt.ID)
	}
}

func HandleRedaction(ctx context.Context, _ mautrix.EventSource, evt *event.Event) {
	log := zerolog.Ctx(ctx).With().
		Str("room_id", evt.RoomID.String()).
		Str("event_id", evt.ID.String()).
		Logger()
	ctx = log.WithContext(ctx)

	// Acquire the lock, so that we don't have race conditions with the
	// Chatwoot handler.
	if _, found := roomSendlocks[evt.RoomID]; !found {
		log.Debug().Msg("creating send lock")
		roomSendlocks[evt.RoomID] = &sync.Mutex{}
	}
	roomSendlocks[evt.RoomID].Lock()
	log.Debug().Msg("acquired send lock")
	defer log.Debug().Msg("released send lock")
	defer roomSendlocks[evt.RoomID].Unlock()

	messageIDs, err := stateStore.GetChatwootMessageIDsForMatrixEventID(ctx, evt.Redacts)
	if err != nil || len(messageIDs) == 0 {
		log.Err(err).Str("redacts", evt.Redacts.String()).Msg("no Chatwoot message for redacted event")
		return
	}

	conversationID, err := stateStore.GetChatwootConversationIDFromMatrixRoom(ctx, evt.RoomID)
	if err != nil {
		log.Err(err).Msg("no Chatwoot conversation associated with room")
		return
	}

	for _, messageID := range messageIDs {
		err = chatwootAPI.DeleteMessage(conversationID, messageID)
		if err != nil {
			log.Err(err).Msg("failed to delete Chatwoot message")
		}
	}
}
