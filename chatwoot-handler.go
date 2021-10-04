package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io/ioutil"
	"net/http"
	"regexp"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
	"maunium.net/go/mautrix"
	mcrypto "maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
	mevent "maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	mid "maunium.net/go/mautrix/id"

	"gitlab.com/beeper/chatwoot/chatwootapi"
)

func SendMessage(roomId mid.RoomID, content mevent.MessageEventContent) (resp *mautrix.RespSendEvent, err error) {
	r, err := DoRetry("send message to "+roomId.String(), func() (interface{}, error) {
		if stateStore.IsEncrypted(roomId) {
			log.Debugf("Sending encrypted event to %s", roomId)
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
			log.Debugf("Sending unencrypted event to %s", roomId)
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

func RetrieveAndUploadMediaToMatrix(url string) ([]byte, *mevent.EncryptedFileInfo, string, error) {
	// Download the attachment
	attachmentResp, err := DoRetry(fmt.Sprintf("Download attachment: %s", url), func() (interface{}, error) {
		return chatwootApi.DownloadAttachment(url)
	})
	if err != nil {
		return []byte{}, nil, "", err
	}
	attachmentPlainData := attachmentResp.([]byte)

	file := mevent.EncryptedFileInfo{
		EncryptedFile: *attachment.NewEncryptedFile(),
		URL:           "",
	}
	encryptedFileData := file.Encrypt(attachmentPlainData)

	// Extract the filename from the data URL
	re := regexp.MustCompile(`^(.*/)?(?:$|(.+?)(?:(\.[^.]*$)|$))`)
	match := re.FindStringSubmatch(url)
	filename := "unknown"
	if match != nil {
		filename = match[2]
	}

	resp, err := DoRetry(fmt.Sprintf("upload %s to Matrix", filename), func() (interface{}, error) {
		return client.UploadMedia(mautrix.ReqUploadMedia{
			Content:       bytes.NewReader(encryptedFileData),
			ContentLength: int64(len(encryptedFileData)),
			ContentType:   "application/octet-stream",
			FileName:      filename,
		})
	})
	if err != nil {
		return []byte{}, nil, "", err
	}
	file.URL = resp.(*mautrix.RespMediaUpload).ContentURI.CUString()

	return attachmentPlainData, &file, filename, nil
}

func HandleWebhook(w http.ResponseWriter, r *http.Request) {
	webhookBody, _ := ioutil.ReadAll(r.Body)

	var eventJson map[string]interface{}
	err := json.Unmarshal(webhookBody, &eventJson)
	if err != nil {
		log.Errorf("Error decoding webhook body: %+v", err)
		return
	}

	if eventType, found := eventJson["event"]; found {
		switch eventType {
		case "conversation_status_changed":
			var csc chatwootapi.ConversationStatusChanged
			err := json.Unmarshal(webhookBody, &csc)
			if err != nil {
				log.Errorf("Error decoding message created webhook body: %+v", err)
				break
			}
			conversationID := csc.ID
			err = HandleConversationStatusChanged(csc)
			if err != nil {
				DoRetry(fmt.Sprintf("send private error message to %d for %+v", conversationID, err), func() (interface{}, error) {
					return chatwootApi.SendPrivateMessage(
						conversationID,
						fmt.Sprintf("*Error occurred while handling Chatwoot conversation status changed.*\n\nError: %+v", err))
				})
			}
			break
		case "message_created", "message_updated":
			var mc chatwootapi.MessageCreated
			err = json.Unmarshal(webhookBody, &mc)
			if err != nil {
				log.Errorf("Error decoding message created webhook body: %+v", err)
				break
			}
			conversationID := mc.Conversation.ID
			err = HandleMessageCreated(mc)
			if err != nil {
				DoRetry(fmt.Sprintf("send private error message to %d for %+v", conversationID, err), func() (interface{}, error) {
					return chatwootApi.SendPrivateMessage(
						conversationID,
						fmt.Sprintf("**Error occurred while handling Chatwoot message. The message may not have been sent to Matrix!**\n\nError: %+v", err))
				})
			}
			break
		}
	}
}

func HandleConversationStatusChanged(csc chatwootapi.ConversationStatusChanged) error {
	if csc.Status != "open" {
		// it's backwards, this means that the conversation was re-opened
		return nil
	}

	roomID, mostRecentEventID, err := stateStore.GetMatrixRoomFromChatwootConversation(csc.ID)
	if err != nil {
		log.Error("No room for ", csc.ID)
		return err
	}

	_, err = DoRetry(fmt.Sprintf("send read receipt to %s for event %s", roomID, mostRecentEventID), func() (interface{}, error) {
		return nil, client.MarkRead(roomID, mostRecentEventID)
	})
	if err != nil {
		return errors.New(fmt.Sprintf("Failed to send read receipt to %s for event %s: %+v", roomID, mostRecentEventID, err))
	}
	return nil
}

func HandleMessageCreated(mc chatwootapi.MessageCreated) error {
	// Skip private messages
	if mc.Private {
		return nil
	}

	roomID, _, err := stateStore.GetMatrixRoomFromChatwootConversation(mc.Conversation.ID)
	if err != nil {
		log.Error("No room for ", mc.Conversation.ID)
		return err
	}

	// Acquire the lock, so that we don't have race conditions with the
	// matrix handler.
	if _, found := roomSendlocks[roomID]; !found {
		log.Debugf("Creating send lock for %s", roomID)
		roomSendlocks[roomID] = &sync.Mutex{}
	}
	roomSendlocks[roomID].Lock()
	log.Debugf("[chatwoot-handler] Acquired send lock for %s", roomID)
	defer log.Debugf("[chatwoot-handler] Released send lock for %s", roomID)
	defer roomSendlocks[roomID].Unlock()

	eventIDs := stateStore.GetMatrixEventIdsForChatwootMessage(mc.ID)

	// Handle deletions first.
	if mc.ContentAttributes != nil && mc.ContentAttributes.Deleted {
		log.Infof("[chatwoot-handler] message %d deleted", mc.ID)
		var errs []error
		for _, eventID := range eventIDs {
			event, err := client.GetEvent(roomID, eventID)
			if err == nil && event.Unsigned.RedactedBecause != nil {
				// Already redacted
				log.Infof("[chatwoot-handler] message %d was already redacted", mc.ID)
				continue
			}
			_, err = client.RedactEvent(roomID, eventID)
			if err != nil {
				errs = append(errs, err)
			}
		}
		if len(errs) > 0 {
			return errors.New(fmt.Sprintf("Errors occurred while redacting messages! %+v", errs))
		}
		return nil
	}

	// If there are already Matrix event IDs for this Chatwoot message,
	// don't try and actually process the chatwoot message.
	if len(eventIDs) > 0 {
		log.Infof("Chatwoot message with ID %d already has a Matrix Event ID(s): %v", mc.ID, eventIDs)
		return nil
	}

	// keep track of the latest Matrix event so we can mark it read
	var resp *mautrix.RespSendEvent

	message := mc.Conversation.Messages[0]

	if message.Content != nil {
		var messageEventContent mevent.MessageEventContent
		messageText := fmt.Sprintf("%s - %s", *message.Content, strings.Split(message.Sender.AvailableName, " ")[0])
		if configuration.RenderMarkdown {
			messageEventContent = format.RenderMarkdown(messageText, true, true)
		} else {
			messageEventContent = mevent.MessageEventContent{MsgType: event.MsgText, Body: messageText}
		}
		resp, err = SendMessage(roomID, messageEventContent)
		if err != nil {
			return err
		}
		stateStore.SetChatwootMessageIdForMatrixEvent(resp.EventID, mc.ID)
	}

	for _, a := range message.Attachments {
		attachmentPlainData, file, filename, err := RetrieveAndUploadMediaToMatrix(a.DataURL)
		if err != nil {
			return err
		}

		// Figure out the type of the file, and if it's an image, determine it's width/height.
		messageType := mevent.MsgFile
		mtype := http.DetectContentType(attachmentPlainData)
		fileInfo := mevent.FileInfo{
			Size:     len(attachmentPlainData),
			MimeType: mtype,
		}
		if strings.HasPrefix(mtype, "image/") {
			m, _, err := image.Decode(bytes.NewReader(attachmentPlainData))
			if err != nil {
				log.Warn(err)
			} else {
				g := m.Bounds()
				fileInfo.Width = g.Dx()
				fileInfo.Height = g.Dy()
			}
			messageType = mevent.MsgImage
		}
		if strings.HasPrefix(mtype, "video/") {
			messageType = mevent.MsgVideo
		}

		// Handle the thumbnail if it exists.
		if len(a.ThumbURL) > 0 {
			thumbnailPlainData, thumbnail, _, err := RetrieveAndUploadMediaToMatrix(a.ThumbURL)
			if err == nil {
				mtype := http.DetectContentType(thumbnailPlainData)
				fileInfo.ThumbnailFile = thumbnail
				fileInfo.ThumbnailInfo = &mevent.FileInfo{
					Size:     len(thumbnailPlainData),
					MimeType: mtype,
				}
				if strings.HasPrefix(mtype, "image/") {
					m, _, err := image.Decode(bytes.NewReader(thumbnailPlainData))
					if err != nil {
						log.Warn(err)
					} else {
						g := m.Bounds()
						fileInfo.ThumbnailInfo.Width = g.Dx()
						fileInfo.ThumbnailInfo.Height = g.Dy()
					}
				}
			}
		}

		resp, err = SendMessage(roomID, mevent.MessageEventContent{
			Body:    filename,
			MsgType: messageType,
			Info:    &fileInfo,
			File:    file,
		})
		if err != nil {
			return err
		}
		stateStore.SetChatwootMessageIdForMatrixEvent(resp.EventID, mc.ID)
	}

	_, err = DoRetry(fmt.Sprintf("send read receipt to %s for event %s", roomID, resp.EventID), func() (interface{}, error) {
		return nil, client.MarkRead(roomID, resp.EventID)
	})
	if err != nil {
		log.Errorf("Failed to send read receipt to %s for event %s", roomID, resp.EventID)
	}
	return nil
}
