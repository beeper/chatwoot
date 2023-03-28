package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/hlog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"

	"github.com/beeper/chatwoot/chatwootapi"
)

func SendMessage(ctx context.Context, roomId id.RoomID, content event.MessageEventContent) (resp *mautrix.RespSendEvent, err error) {
	log := zerolog.Ctx(ctx).With().Str("room_id", roomId.String()).Logger()
	ctx = log.WithContext(ctx)

	r, err := DoRetry(ctx, "send message to "+roomId.String(), func(ctx context.Context) (*mautrix.RespSendEvent, error) {
		return client.SendMessageEvent(roomId, event.EventMessage, content)
	})
	if err != nil {
		// give up
		log.Err(err).Msg("failed to send message")
		return nil, err
	}
	return r, err
}

func RetrieveAndUploadMediaToMatrix(ctx context.Context, url string) ([]byte, *event.EncryptedFileInfo, string, error) {
	// Download the attachment
	attachmentResp, err := DoRetry(ctx, fmt.Sprintf("Download attachment: %s", url), func(ctx context.Context) (*[]byte, error) {
		return chatwootApi.DownloadAttachment(ctx, url)
	})
	if err != nil {
		return []byte{}, nil, "", err
	}
	attachmentData := *attachmentResp

	file := event.EncryptedFileInfo{
		EncryptedFile: *attachment.NewEncryptedFile(),
		URL:           "",
	}
	file.EncryptInPlace(attachmentData)

	// Extract the filename from the data URL
	re := regexp.MustCompile(`^(.*/)?(?:$|(.+?)(?:(\.[^.]*$)|$))`)
	match := re.FindStringSubmatch(url)
	filename := "unknown"
	if match != nil {
		filename = match[2]
	}

	resp, err := DoRetry(ctx, fmt.Sprintf("upload %s to Matrix", filename), func(context.Context) (*mautrix.RespMediaUpload, error) {
		return client.UploadMedia(mautrix.ReqUploadMedia{
			Content:       bytes.NewReader(attachmentData),
			ContentLength: int64(len(attachmentData)),
			ContentType:   "application/octet-stream",
			FileName:      filename,
		})
	})
	if err != nil {
		return []byte{}, nil, "", err
	}
	file.URL = (*resp).ContentURI.CUString()

	return attachmentData, &file, filename, nil
}

func HandleWebhook(w http.ResponseWriter, r *http.Request) {
	log := hlog.FromRequest(r)
	ctx := r.Context()

	webhookBody, err := io.ReadAll(r.Body)
	if err != nil {
		log.Err(err).Msg("failed to read webhook body")
	}

	var eventJson map[string]any
	err = json.Unmarshal(webhookBody, &eventJson)
	if err != nil {
		log.Err(err).Msg("error decoding webhook body")
		return
	}

	if eventType, found := eventJson["event"]; found {
		switch eventType {
		case "conversation_status_changed":
			var csc chatwootapi.ConversationStatusChanged
			err := json.Unmarshal(webhookBody, &csc)
			if err != nil {
				log.Err(err).Msg("error decoding message created webhook body")
				break
			}
			conversationID := csc.ID
			err = HandleConversationStatusChanged(ctx, csc)
			if err != nil {
				DoRetry(ctx, fmt.Sprintf("send private error message to %d for %+v", conversationID, err), func(ctx context.Context) (*chatwootapi.Message, error) {
					return chatwootApi.SendPrivateMessage(
						ctx,
						conversationID,
						fmt.Sprintf("*Error occurred while handling Chatwoot conversation status changed.*\n\nError: %+v", err))
				})
			}

		case "message_created", "message_updated":
			var mc chatwootapi.MessageCreated
			err = json.Unmarshal(webhookBody, &mc)
			if err != nil {
				log.Err(err).Msg("error decoding message created webhook body")
				break
			}
			conversationID := mc.Conversation.ID
			err = HandleMessageCreated(ctx, mc)
			if err != nil {
				DoRetry(ctx, fmt.Sprintf("send private error message to %d for %+v", conversationID, err), func(ctx context.Context) (*chatwootapi.Message, error) {
					return chatwootApi.SendPrivateMessage(
						ctx,
						conversationID,
						fmt.Sprintf("**Error occurred while handling Chatwoot message. The message may not have been sent to Matrix!**\n\nError: %+v", err))
				})
			}
		}
	}
}

func HandleConversationStatusChanged(ctx context.Context, csc chatwootapi.ConversationStatusChanged) error {
	if csc.Status != "open" {
		// it's backwards, this means that the conversation was re-opened
		return nil
	}

	roomID, mostRecentEventID, err := stateStore.GetMatrixRoomFromChatwootConversation(ctx, csc.ID)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Int("conversation_id", csc.ID).Msg("no room found for conversation")
		return err
	}

	_, err = DoRetry(ctx, fmt.Sprintf("send read receipt to %s for event %s", roomID, mostRecentEventID), func(context.Context) (*any, error) {
		return nil, client.MarkRead(roomID, mostRecentEventID)
	})
	if err != nil {
		return fmt.Errorf("failed to send read receipt to %s for event %s: %w", roomID, mostRecentEventID, err)
	}
	return nil
}

func HandleMessageCreated(ctx context.Context, mc chatwootapi.MessageCreated) error {
	log := zerolog.Ctx(ctx).With().
		Str("component", "handle_message_created").
		Int("conversation_id", mc.Conversation.ID).Logger()
	ctx = log.WithContext(ctx)

	// Skip private messages
	if mc.Private {
		return nil
	}

	roomID, _, err := stateStore.GetMatrixRoomFromChatwootConversation(ctx, mc.Conversation.ID)
	if err != nil {
		log.Err(err).Int("conversation_id", mc.Conversation.ID).Msg("no room found for conversation")
		return err
	}
	log = log.With().Str("room_id", roomID.String()).Logger()
	ctx = log.WithContext(ctx)

	// Acquire the lock, so that we don't have race conditions with the
	// matrix handler.
	if _, found := roomSendlocks[roomID]; !found {
		log.Debug().Msg("creating send lock")
		roomSendlocks[roomID] = &sync.Mutex{}
	}
	roomSendlocks[roomID].Lock()
	log.Debug().Msg("acquired send lock")
	defer log.Debug().Msg("released send lock")
	defer roomSendlocks[roomID].Unlock()

	eventIDs := stateStore.GetMatrixEventIdsForChatwootMessage(ctx, mc.ID)

	// Handle deletions first.
	if mc.ContentAttributes != nil && mc.ContentAttributes.Deleted {
		log.Info().Int("message_id", mc.ID).Msg("message deleted")
		var errs []error
		for _, eventID := range eventIDs {
			event, err := client.GetEvent(roomID, eventID)
			if err == nil && event.Unsigned.RedactedBecause != nil {
				// Already redacted
				log.Info().Int("message_id", mc.ID).Msg("message was already redacted")
				continue
			}
			_, err = client.RedactEvent(roomID, eventID)
			if err != nil {
				errs = append(errs, err)
			}
		}
		if len(errs) > 0 {
			return fmt.Errorf("errors occurred while redacting messages! %v", errs)
		}
		return nil
	}

	// If there are already Matrix event IDs for this Chatwoot message,
	// don't try and actually process the chatwoot message.
	if len(eventIDs) > 0 {
		log.Info().Int("message_id", mc.ID).
			Interface("event_ids", eventIDs).
			Msg("chatwoot message already has matrix event ID(s)")
		return nil
	}

	// keep track of the latest Matrix event so we can mark it read
	var resp *mautrix.RespSendEvent

	message := mc.Conversation.Messages[0]

	if message.Content != nil {
		var messageEventContent event.MessageEventContent
		messageText := fmt.Sprintf("%s - %s", *message.Content, strings.Split(message.Sender.AvailableName, " ")[0])
		if configuration.RenderMarkdown {
			messageEventContent = format.RenderMarkdown(messageText, true, true)
		} else {
			messageEventContent = event.MessageEventContent{MsgType: event.MsgText, Body: messageText}
		}
		resp, err = SendMessage(ctx, roomID, messageEventContent)
		if err != nil {
			return err
		}
		stateStore.SetChatwootMessageIdForMatrixEvent(ctx, resp.EventID, mc.ID)
	}

	for _, a := range message.Attachments {
		attachmentPlainData, file, filename, err := RetrieveAndUploadMediaToMatrix(ctx, a.DataURL)
		if err != nil {
			return err
		}

		// Figure out the type of the file, and if it's an image, determine it's width/height.
		messageType := event.MsgFile
		mtype := http.DetectContentType(attachmentPlainData)
		fileInfo := event.FileInfo{
			Size:     len(attachmentPlainData),
			MimeType: mtype,
		}
		if strings.HasPrefix(mtype, "image/") {
			m, _, err := image.Decode(bytes.NewReader(attachmentPlainData))
			if err != nil {
				log.Warn().Err(err).Msg("failed to decode image")
			} else {
				g := m.Bounds()
				fileInfo.Width = g.Dx()
				fileInfo.Height = g.Dy()
			}
			messageType = event.MsgImage
		}
		if strings.HasPrefix(mtype, "video/") {
			messageType = event.MsgVideo
		}

		// Handle the thumbnail if it exists.
		if len(a.ThumbURL) > 0 {
			thumbnailPlainData, thumbnail, _, err := RetrieveAndUploadMediaToMatrix(ctx, a.ThumbURL)
			if err == nil {
				mtype := http.DetectContentType(thumbnailPlainData)
				fileInfo.ThumbnailFile = thumbnail
				fileInfo.ThumbnailInfo = &event.FileInfo{
					Size:     len(thumbnailPlainData),
					MimeType: mtype,
				}
				if strings.HasPrefix(mtype, "image/") {
					m, _, err := image.Decode(bytes.NewReader(thumbnailPlainData))
					if err != nil {
						log.Warn().Err(err).Msg("failed to decode image")
					} else {
						g := m.Bounds()
						fileInfo.ThumbnailInfo.Width = g.Dx()
						fileInfo.ThumbnailInfo.Height = g.Dy()
					}
				}
			}
		}

		resp, err = SendMessage(ctx, roomID, event.MessageEventContent{
			Body:    filename,
			MsgType: messageType,
			Info:    &fileInfo,
			File:    file,
		})
		if err != nil {
			return err
		}
		stateStore.SetChatwootMessageIdForMatrixEvent(ctx, resp.EventID, mc.ID)
	}

	_, err = DoRetry(ctx, fmt.Sprintf("send read receipt to %s for event %s", roomID, resp.EventID), func(context.Context) (*any, error) {
		return nil, client.MarkRead(roomID, resp.EventID)
	})
	if err != nil {
		log.Err(err).
			Str("event_id", resp.EventID.String()).
			Msg("failed to send read receipt")
	}
	return nil
}
