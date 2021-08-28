package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"regexp"
	"strings"

	log "github.com/sirupsen/logrus"
	"maunium.net/go/mautrix"
	mcrypto "maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/attachment"
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

func RetrieveAndUploadMediaToMatrix(url string) ([]byte, mevent.EncryptedFileInfo, string) {
	// Download the attachment
	attachmentResp, err := DoRetry(fmt.Sprintf("Download attachment: %s", url), func() (interface{}, error) {
		return chatwootApi.DownloadAttachment(url)
	})
	if err != nil {
		log.Error(err)
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

	resp, err := client.UploadMedia(mautrix.ReqUploadMedia{
		Content:       bytes.NewReader(encryptedFileData),
		ContentLength: int64(len(encryptedFileData)),
		ContentType:   "application/octet-stream",
		FileName:      filename,
	})
	file.URL = resp.ContentURI.CUString()

	return attachmentPlainData, file, filename
}

func HandleWebhook(w http.ResponseWriter, r *http.Request) {
	decoder := json.NewDecoder(r.Body)
	var mc chatwootapi.MessageCreated
	err := decoder.Decode(&mc)
	if err != nil {
		log.Error(err)
		return
	}

	// Skip private messages
	if mc.Private {
		return
	}

	if mc.ContentAttributes != nil && mc.ContentAttributes.Deleted {
		log.Infof("Message %d deleted", mc.ID)
		roomID, err := stateStore.GetMatrixRoomFromChatwootConversation(mc.Conversation.ID)
		if err != nil {
			log.Error("No room for ", mc.Conversation.ID)
			log.Error(err)
			return
		}

		for _, eventID := range stateStore.GetMatrixEventIdsForChatwootMessage(mc.ID) {
			event, err := client.GetEvent(roomID, eventID)
			if err == nil && event.Unsigned.RedactedBecause != nil {
				// Already redacted
				log.Infof("Message %d was already redacted", mc.ID)
				continue
			}
			client.RedactEvent(roomID, eventID)
		}
		return
	}

	if eventIDs := stateStore.GetMatrixEventIdsForChatwootMessage(mc.ID); len(eventIDs) > 0 {
		log.Infof("Chatwoot message with ID %d already has a Matrix Event ID(s): %v", mc.ID, eventIDs)
		return
	}

	roomID, err := stateStore.GetMatrixRoomFromChatwootConversation(mc.Conversation.ID)
	if err != nil {
		log.Error("No room for ", mc.Conversation.ID)
		log.Error(err)
		return
	}

	message := mc.Conversation.Messages[0]

	if message.Content != nil {
		resp, err := SendMessage(roomID, mevent.MessageEventContent{
			MsgType: mevent.MsgText,
			Body:    *message.Content,
		})
		if err != nil {
			log.Error(err)
		}
		stateStore.SetChatwootMessageIdForMatrixEvent(resp.EventID, mc.ID)
	}

	for _, a := range message.Attachments {
		attachmentPlainData, file, filename := RetrieveAndUploadMediaToMatrix(a.DataURL)

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
			thumbnailPlainData, thumbnail, _ := RetrieveAndUploadMediaToMatrix(a.ThumbURL)
			mtype := http.DetectContentType(thumbnailPlainData)
			fileInfo.ThumbnailFile = &thumbnail
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

		resp2, err := SendMessage(roomID, mevent.MessageEventContent{
			Body:    filename,
			MsgType: messageType,
			Info:    &fileInfo,
			File:    &file,
		})
		if err != nil {
			log.Error(err)
		} else {
			stateStore.SetChatwootMessageIdForMatrixEvent(resp2.EventID, mc.ID)
		}
	}
}
