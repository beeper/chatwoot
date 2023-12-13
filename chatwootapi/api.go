package chatwootapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"strings"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/id"
)

type MessageType string

const (
	IncomingMessage MessageType = "incoming"
	OutgoingMessage MessageType = "outgoing"
)

type ConversationStatus string

const (
	ConversationStatusOpen     ConversationStatus = "open"
	ConversationStatusResolved ConversationStatus = "resolved"
	ConversationStatusPending  ConversationStatus = "pending"
)

type ChatwootAPI struct {
	BaseURL     string
	AccountID   int
	InboxID     int
	AccessToken string

	Client *http.Client
}

func CreateChatwootAPI(baseURL string, accountID int, inboxID int, accessToken string) *ChatwootAPI {
	return &ChatwootAPI{
		BaseURL:     baseURL,
		AccountID:   accountID,
		InboxID:     inboxID,
		AccessToken: accessToken,
		Client: &http.Client{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return errors.New("too many (>=10) redirects, cancelling request")
				}
				return nil
			},
		},
	}
}

func (api *ChatwootAPI) DoRequest(req *http.Request) (*http.Response, error) {
	req.Header.Add("API_ACCESS_TOKEN", api.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	return api.Client.Do(req)
}

func (api *ChatwootAPI) MakeUri(endpoint string) string {
	url, err := url.Parse(api.BaseURL)
	if err != nil {
		panic(err)
	}
	url.Path = path.Join(url.Path, fmt.Sprintf("api/v1/accounts/%d", api.AccountID), endpoint)
	return url.String()
}

func (api *ChatwootAPI) CreateContact(ctx context.Context, userID id.UserID, name string) (int, error) {
	log := zerolog.Ctx(ctx).With().
		Str("component", "create_contact").
		Str("user_id", userID.String()).
		Logger()

	if name == "" {
		name = userID.String()
		if userID.Homeserver() == "beeper.local" && strings.HasPrefix(userID.Localpart(), "imessagego_1.") {
			decoded, err := id.DecodeUserLocalpart(strings.TrimPrefix(userID.Localpart(), "imessagego_1."))
			if err == nil {
				name = decoded
			}
		}
	}

	log.Info().Str("name", name).Msg("Creating contact")
	payload := CreateContactPayload{
		InboxID:    api.InboxID,
		Name:       name,
		Identifier: name,
	}
	jsonValue, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, api.MakeUri("contacts"), bytes.NewBuffer(jsonValue))
	if err != nil {
		log.Err(err).Msg("Failed to create request")
		return 0, err
	}

	resp, err := api.DoRequest(req)
	if err != nil {
		log.Err(err).Msg("Failed to make request")
		return 0, err
	}
	if resp.StatusCode != 200 {
		data, err := io.ReadAll(resp.Body)
		if err == nil {
			log.Error().Str("data", string(data)).Msg("got non-200 status code")
		}
		return 0, fmt.Errorf("POST contacts returned non-200 status code: %d", resp.StatusCode)
	}

	var contactPayload ContactPayload
	if err := json.NewDecoder(resp.Body).Decode(&contactPayload); err != nil {
		return 0, err
	}

	log.Debug().Interface("contact_payload", contactPayload).Msg("Got contact payload")
	return contactPayload.Payload.Contact.ID, nil
}

func (api *ChatwootAPI) ContactIDForMXID(ctx context.Context, userID id.UserID) (int, error) {
	log := zerolog.Ctx(ctx)
	query := userID.String()
	if userID.Homeserver() == "beeper.local" {
		// Special handling for bridged iMessages.
		if strings.HasPrefix(userID.Localpart(), "imessagego_1.") {
			decoded, err := id.DecodeUserLocalpart(strings.TrimPrefix(userID.Localpart(), "imessagego_1."))
			if err == nil {
				query = decoded
			}
		}
	}

	log.Info().Str("query", query).Msg("Searching for contact")

	req, err := http.NewRequest(http.MethodGet, api.MakeUri("contacts/search"), nil)
	if err != nil {
		return 0, err
	}

	q := req.URL.Query()
	q.Add("q", query)
	req.URL.RawQuery = q.Encode()

	resp, err := api.DoRequest(req)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("GET contacts/search returned non-200 status code: %d", resp.StatusCode)
	}

	var contactsPayload ContactsPayload
	if err := json.NewDecoder(resp.Body).Decode(&contactsPayload); err != nil {
		return 0, err
	}

	for _, contact := range contactsPayload.Payload {
		if contact.Identifier == query {
			return contact.ID, nil
		} else if contact.Email == query {
			return contact.ID, nil
		} else if contact.PhoneNumber == query {
			return contact.ID, nil
		}
	}

	return 0, fmt.Errorf("couldn't find user with user ID %s", query)
}

func (api *ChatwootAPI) GetChatwootConversation(conversationID int) (*Conversation, error) {
	req, err := http.NewRequest(http.MethodGet, api.MakeUri(fmt.Sprintf("conversations/%d", conversationID)), nil)
	if err != nil {
		return nil, err
	}

	resp, err := api.DoRequest(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET conversations/%d returned non-200 status code: %d", conversationID, resp.StatusCode)
	}

	var conversation Conversation
	err = json.NewDecoder(resp.Body).Decode(&conversation)
	return &conversation, err
}

func (api *ChatwootAPI) CreateConversation(sourceID string, contactID int, additionalAttrs map[string]string) (*Conversation, error) {
	values := map[string]any{
		"source_id":             sourceID,
		"inbox_id":              api.InboxID,
		"contact_id":            contactID,
		"status":                "open",
		"additional_attributes": additionalAttrs,
	}
	jsonValue, _ := json.Marshal(values)
	req, err := http.NewRequest(http.MethodPost, api.MakeUri("conversations"), bytes.NewBuffer(jsonValue))
	if err != nil {
		return nil, err
	}

	resp, err := api.DoRequest(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		content, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("POST conversations returned non-200 status code: %d: %s", resp.StatusCode, string(content))
	}

	var conversation Conversation
	err = json.NewDecoder(resp.Body).Decode(&conversation)
	return &conversation, err
}

func (api *ChatwootAPI) GetConversationLabels(conversationID int) ([]string, error) {
	req, err := http.NewRequest(http.MethodGet, api.MakeUri(fmt.Sprintf("conversations/%d/labels", conversationID)), nil)
	if err != nil {
		return nil, err
	}

	resp, err := api.DoRequest(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		content, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("POST conversations returned non-200 status code: %d: %s", resp.StatusCode, string(content))
	}

	var labels ConversationLabelsPayload
	err = json.NewDecoder(resp.Body).Decode(&labels)
	return labels.Payload, err
}

func (api *ChatwootAPI) SetConversationLabels(conversationID int, labels []string) error {
	jsonValue, err := json.Marshal(map[string]any{"labels": labels})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, api.MakeUri(fmt.Sprintf("conversations/%d/labels", conversationID)), bytes.NewBuffer(jsonValue))
	if err != nil {
		return err
	}

	resp, err := api.DoRequest(req)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		content, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("POST conversations returned non-200 status code: %d: %s", resp.StatusCode, string(content))
	}
	return nil
}

func (api *ChatwootAPI) SetConversationCustomAttributes(conversationID int, customAttrs map[string]string) error {
	jsonValue, _ := json.Marshal(map[string]any{
		"custom_attributes": customAttrs,
	})
	req, err := http.NewRequest(http.MethodPost, api.MakeUri(fmt.Sprintf("conversations/%d/custom_attributes", conversationID)), bytes.NewBuffer(jsonValue))
	if err != nil {
		return err
	}

	resp, err := api.DoRequest(req)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("POST conversations/%d/custom_attributes returned non-200 status code: %d", conversationID, resp.StatusCode)
	}
	return nil
}

func (api *ChatwootAPI) doSendTextMessage(ctx context.Context, conversationID int, jsonValues map[string]any) (*Message, error) {
	log := zerolog.Ctx(ctx).With().Str("component", "send_text_message").Logger()
	jsonValue, err := json.Marshal(jsonValues)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, api.MakeUri(fmt.Sprintf("conversations/%d/messages", conversationID)), bytes.NewBuffer(jsonValue))
	if err != nil {
		log.Err(err).Msg("Failed to create request")
		return nil, err
	}

	resp, err := api.DoRequest(req)
	if err != nil {
		log.Err(err).Msg("failed to send request")
		return nil, err
	}
	if resp.StatusCode != 200 {
		content, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("POST conversations/%d/messages returned non-200 status code: %d: %s", conversationID, resp.StatusCode, string(content))
	}

	decoder := json.NewDecoder(resp.Body)
	var message Message
	err = decoder.Decode(&message)
	return &message, err
}

func (api *ChatwootAPI) SendTextMessage(ctx context.Context, conversationID int, content string, messageType MessageType) (*Message, error) {
	values := map[string]any{"content": content, "message_type": messageType, "private": false}
	return api.doSendTextMessage(ctx, conversationID, values)
}

func (api *ChatwootAPI) SendPrivateMessage(ctx context.Context, conversationID int, content string) (*Message, error) {
	values := map[string]any{"content": content, "message_type": OutgoingMessage, "private": true}
	return api.doSendTextMessage(ctx, conversationID, values)
}

func (api *ChatwootAPI) ToggleStatus(ctx context.Context, conversationID int, status ConversationStatus) error {
	jsonValue, err := json.Marshal(map[string]any{"status": status})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, api.MakeUri(fmt.Sprintf("conversations/%d/toggle_status", conversationID)), bytes.NewBuffer(jsonValue))
	if err != nil {
		return err
	}
	resp, err := api.DoRequest(req)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		content, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("POST conversations/%d/toggle_status returned non-200 status code: %d: %s", conversationID, resp.StatusCode, string(content))
	}
	return nil
}

var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"")

func (api *ChatwootAPI) SendAttachmentMessage(conversationID int, filename string, mimeType string, fileData io.Reader, messageType MessageType) (*Message, error) {
	bodyBuf := &bytes.Buffer{}
	bodyWriter := multipart.NewWriter(bodyBuf)

	contentFieldWriter, err := bodyWriter.CreateFormField("content")
	if err != nil {
		return nil, err
	}
	contentFieldWriter.Write([]byte{})

	privateFieldWriter, err := bodyWriter.CreateFormField("private")
	if err != nil {
		return nil, err
	}
	privateFieldWriter.Write([]byte("false"))

	messageTypeFieldWriter, err := bodyWriter.CreateFormField("message_type")
	if err != nil {
		return nil, err
	}
	messageTypeFieldWriter.Write([]byte(messageType))

	h := make(textproto.MIMEHeader)
	h.Set(
		"Content-Disposition",
		fmt.Sprintf(`form-data; name="attachments[]"; filename="%s"`, quoteEscaper.Replace(filename)))
	if mimeType != "" {
		h.Set("Content-Type", mimeType)
	} else {
		h.Set("Content-Type", "application/octet-stream")
	}
	fileWriter, err := bodyWriter.CreatePart(h)
	if err != nil {
		return nil, err
	}

	// Copy the file data into the form.
	io.Copy(fileWriter, fileData)

	bodyWriter.Close()

	req, err := http.NewRequest(http.MethodPost, api.MakeUri(fmt.Sprintf("conversations/%d/messages", conversationID)), bodyBuf)
	if err != nil {
		return nil, err
	}
	req.Header.Add("API_ACCESS_TOKEN", api.AccessToken)
	req.Header.Set("Content-Type", bodyWriter.FormDataContentType())

	resp, err := api.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		content, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("POST conversations/%d/messages returned non-200 status code: %d: %s", conversationID, resp.StatusCode, string(content))
	}

	decoder := json.NewDecoder(resp.Body)
	var message Message
	err = decoder.Decode(&message)
	if err != nil {
		return nil, err
	}
	return &message, nil
}

func (api *ChatwootAPI) DownloadAttachment(ctx context.Context, url string) ([]byte, error) {
	log := zerolog.Ctx(ctx)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		log.Err(err).Msg("failed to create request")
		return nil, err
	}
	resp, err := api.DoRequest(req)
	if err != nil {
		log.Err(err).Msg("failed to do request")
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET attachment returned non-200 status code: %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Err(err).Msg("failed to read response body")
		return nil, err
	}
	return data, err
}

func (api *ChatwootAPI) DeleteMessage(conversationID int, messageID int) error {
	req, err := http.NewRequest(http.MethodDelete, api.MakeUri(fmt.Sprintf("conversations/%d/messages/%d", conversationID, messageID)), nil)
	if err != nil {
		return err
	}

	resp, err := api.DoRequest(req)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET attachment returned non-200 status code: %d", resp.StatusCode)
	}

	return nil
}
