package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
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

func SendMessage(ctx context.Context, roomID id.RoomID, content *event.MessageEventContent, extraContent ...map[string]any) (resp *mautrix.RespSendEvent, err error) {
	log := zerolog.Ctx(ctx).With().Stringer("room_id", roomID).Logger()
	ctx = log.WithContext(ctx)

	wrappedContent := event.Content{Parsed: content}
	if len(extraContent) == 1 {
		wrappedContent.Raw = extraContent[0]
	}

	r, err := DoRetry(ctx, "send message to "+roomID.String(), func(ctx context.Context) (*mautrix.RespSendEvent, error) {
		return client.SendMessageEvent(ctx, roomID, event.EventMessage, &wrappedContent)
	})
	if err != nil {
		// give up
		log.Err(err).Msg("failed to send message")
		return nil, err
	}
	return r, err
}

func HandleWebhook(_ http.ResponseWriter, r *http.Request) {
	log := hlog.FromRequest(r)
	ctx := log.WithContext(context.Background())

	webhookBody, err := io.ReadAll(r.Body)
	if err != nil {
		log.Err(err).Msg("failed to read webhook body")
	}

	var eventJSON map[string]any
	err = json.Unmarshal(webhookBody, &eventJSON)
	if err != nil {
		log.Err(err).Msg("error decoding webhook body")
		return
	}

	if eventType, found := eventJSON["event"]; found {
		switch eventType {
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
					return chatwootAPI.SendPrivateMessage(
						ctx,
						conversationID,
						fmt.Sprintf("**Error occurred while handling Chatwoot message. The message may not have been sent to Matrix!**\n\nError: %+v", err))
				})
			}
		}
	}
}

func handleAttachment(ctx context.Context, roomID id.RoomID, chatwootMessageID chatwootapi.MessageID, chatwootAttachment chatwootapi.Attachment) (*mautrix.RespSendEvent, error) {
	log := zerolog.Ctx(ctx).With().
		Str("func", "handleAttachment").
		Int("attachment_id", int(chatwootAttachment.ID)).
		Str("attachment_file_type", chatwootAttachment.FileType).
		Logger()
	ctx = log.WithContext(ctx)

	// Download the attachment
	attachmentData, err := DoRetryArr(ctx, fmt.Sprintf("Download attachment: %s", chatwootAttachment.DataURL), func(ctx context.Context) ([]byte, error) {
		return chatwootAPI.DownloadAttachment(ctx, chatwootAttachment.DataURL)
	})
	if err != nil {
		return nil, err
	}

	if len(attachmentData) != chatwootAttachment.FileSize {
		return nil, fmt.Errorf("downloaded attachment size (%d) does not match expected size (%d)", len(attachmentData), chatwootAttachment.FileSize)
	}

	// Construct the file info before encrypting the attachment.
	mimeType := http.DetectContentType(attachmentData)
	log.Info().Str("mime_type", mimeType).Msg("downloaded attachment")
	info := &event.FileInfo{
		MimeType: mimeType,
		Size:     chatwootAttachment.FileSize,
	}

	// Calculate the width and height of the image
	if strings.HasPrefix(mimeType, "image/") {
		image, _, err := image.Decode(bytes.NewReader(attachmentData))
		if err != nil {
			log.Warn().Err(err).Msg("failed to decode image")
		} else {
			bounds := image.Bounds()
			info.Width = bounds.Dx()
			info.Height = bounds.Dy()
		}
	}

	// Handle the thumbnail if it exists.
	if len(chatwootAttachment.ThumbURL) > 0 {
		// Download the thumbnail
		thumbnailData, err := DoRetryArr(ctx, fmt.Sprintf("Download attachment thumbnail: %s", chatwootAttachment.ThumbURL), func(ctx context.Context) ([]byte, error) {
			return chatwootAPI.DownloadAttachment(ctx, chatwootAttachment.ThumbURL)
		})
		if err != nil {
			return nil, err
		}

		// Calculate the info for the thumbnail
		thumbnailMimeType := http.DetectContentType(thumbnailData)
		info.ThumbnailInfo = &event.FileInfo{
			MimeType: thumbnailMimeType,
			Size:     len(thumbnailData),
		}

		thumbnailImage, _, err := image.Decode(bytes.NewReader(thumbnailData))
		if err != nil {
			log.Warn().Err(err).Msg("failed to decode image")
		} else {
			bounds := thumbnailImage.Bounds()
			info.ThumbnailInfo.Width = bounds.Dx()
			info.ThumbnailInfo.Height = bounds.Dy()
		}

		// Encrypt the thumbnail
		info.ThumbnailFile = &event.EncryptedFileInfo{
			EncryptedFile: *attachment.NewEncryptedFile(),
			URL:           "",
		}
		info.ThumbnailFile.EncryptInPlace(thumbnailData)

		// Upload the thumbnail
		uploadedThumbnail, err := DoRetry(ctx, "upload thumbnail to Matrix", func(ctx context.Context) (*mautrix.RespMediaUpload, error) {
			return client.UploadMedia(ctx, mautrix.ReqUploadMedia{
				ContentBytes:  thumbnailData,
				ContentLength: int64(len(thumbnailData)),
				ContentType:   "application/octet-stream",
			})
		})
		if err != nil {
			return nil, err
		}
		info.ThumbnailFile.URL = uploadedThumbnail.ContentURI.CUString()
	}

	// Encrypt the file
	file := &event.EncryptedFileInfo{
		EncryptedFile: *attachment.NewEncryptedFile(),
		URL:           "",
	}
	file.EncryptInPlace(attachmentData)

	// Extract the filename from the data URL. It should be the last part of
	// the path before the query string.
	filename := "unknown"
	parsed, err := url.Parse(chatwootAttachment.DataURL)
	if err != nil {
		log.Warn().Err(err).Msg("failed to parse attachment URL")
	} else {
		parts := strings.Split(parsed.Path, "/")
		if len(parts) == 0 {
			log.Warn().Err(err).Msg("failed to parse attachment URL")
		} else {
			filename = parts[len(parts)-1]
		}
	}

	// Upload it to the media repo
	uploaded, err := DoRetry(ctx, fmt.Sprintf("upload %s to Matrix", filename), func(ctx context.Context) (*mautrix.RespMediaUpload, error) {
		return client.UploadMedia(ctx, mautrix.ReqUploadMedia{
			ContentBytes:  attachmentData,
			ContentLength: int64(len(attachmentData)),
			ContentType:   "application/octet-stream",
			FileName:      filename,
		})
	})
	if err != nil {
		return nil, err
	}
	file.URL = uploaded.ContentURI.CUString()

	messageType := event.MsgFile
	switch chatwootAttachment.FileType {
	case "image":
		messageType = event.MsgImage
	case "video":
		messageType = event.MsgVideo
	case "audio":
		messageType = event.MsgAudio
	}

	return SendMessage(ctx, roomID, &event.MessageEventContent{
		Body:    filename,
		MsgType: messageType,
		Info:    info,
		File:    file,
	}, map[string]any{
		"com.beeper.chatwoot.message_id":    chatwootMessageID,
		"com.beeper.chatwoot.attachment_id": chatwootAttachment.ID,
	})
}

type StartNewChatResp struct {
	RoomID id.RoomID `json:"room_id,omitempty"`
	Error  string    `json:"error,omitempty"`
}

func HandleMessageCreated(ctx context.Context, mc chatwootapi.MessageCreated) error {
	log := zerolog.Ctx(ctx).With().
		Str("component", "handle_message_created").
		Int("message_id", int(mc.ID)).
		Int("conversation_id", int(mc.Conversation.ID)).Logger()
	ctx = log.WithContext(ctx)

	// Skip private messages
	if mc.Private {
		return nil
	}

	roomID, _, err := stateStore.GetMatrixRoomFromChatwootConversation(ctx, mc.Conversation.ID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Err(err).Msg("couldn't find room for conversation")
			return err
		}

		if !configuration.StartNewChat.Enable {
			log.Err(err).Msg("couldn't find room for conversation")
			return err
		}

		log := log.With().Bool("snc_enabled", true).Logger()

		// Create a new room for this conversation using the start new chat
		// endpoint.
		body, err := json.Marshal(mc.Conversation.Meta.Sender)
		if err != nil {
			log.Err(err).Msg("failed to marshal sender to JSON")
			return err
		}
		req, err := http.NewRequest(http.MethodPost, configuration.StartNewChat.Endpoint, bytes.NewReader(body))
		if err != nil {
			log.Err(err).Msg("failed to create request")
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", configuration.StartNewChat.Token))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Err(err).Msg("failed to make request")
			return err
		}
		defer resp.Body.Close()
		var sncResp StartNewChatResp
		err = json.NewDecoder(resp.Body).Decode(&sncResp)
		if err != nil {
			log.Err(err).Msg("failed to read response body")
			return err
		}

		if resp.StatusCode != http.StatusOK {
			log.Warn().Int("status_code", resp.StatusCode).Any("resp", sncResp).Msg("failed to create new chat")
			return fmt.Errorf("failed to create new chat: %s", sncResp.Error)
		} else if sncResp.RoomID == "" {
			log.Warn().Any("resp", sncResp).Msg("invalid start new chat response")
			return fmt.Errorf("invalid start new chat response: %s", sncResp.Error)
		}

		log = log.With().Stringer("room_id", sncResp.RoomID).Logger()
		log.Info().Msg("created new chat for conversation")
		roomID = sncResp.RoomID

		err = stateStore.UpdateConversationIDForRoom(ctx, sncResp.RoomID, mc.Conversation.ID)
		if err != nil {
			log.Err(err).Msg("failed to update conversation ID for room")
			return err
		}

		_, err = client.State(ctx, sncResp.RoomID)
		if err != nil {
			log.Err(err).Msg("failed to get room state")
			return err
		}
	}
	log = log.With().Stringer("room_id", roomID).Logger()
	ctx = log.WithContext(ctx)

	// Acquire the lock, so that we don't have race conditions with the matrix
	// handler.
	if _, found := roomSendlocks[roomID]; !found {
		log.Debug().Msg("creating send lock")
		roomSendlocks[roomID] = &sync.Mutex{}
	}
	roomSendlocks[roomID].Lock()
	log.Debug().Msg("acquired send lock")
	defer log.Debug().Msg("released send lock")
	defer roomSendlocks[roomID].Unlock()

	eventIDs := stateStore.GetMatrixEventIDsForChatwootMessage(ctx, mc.ID)

	// Handle deletions first.
	if mc.ContentAttributes != nil && mc.ContentAttributes.Deleted {
		log.Info().Int("message_id", int(mc.ID)).Msg("message deleted")
		var errs []error
		for _, eventID := range eventIDs {
			event, err := client.GetEvent(ctx, roomID, eventID)
			if err == nil && event.Unsigned.RedactedBecause != nil {
				// Already redacted
				log.Info().Int("message_id", int(mc.ID)).Msg("message was already redacted")
				continue
			}
			_, err = client.RedactEvent(ctx, roomID, eventID)
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
		log.Info().
			Any("event_ids", eventIDs).
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
		resp, err = SendMessage(ctx, roomID, &messageEventContent, map[string]any{
			"com.beeper.chatwoot.message_id": mc.ID,
		})
		if err != nil {
			return err
		}
		stateStore.SetChatwootMessageIDForMatrixEvent(ctx, resp.EventID, mc.ID)
	}

	for _, a := range message.Attachments {
		resp, err = handleAttachment(ctx, roomID, mc.ID, a)
		if err != nil {
			return err
		}
		stateStore.SetChatwootMessageIDForMatrixEvent(ctx, resp.EventID, mc.ID)
	}

	return nil
}
