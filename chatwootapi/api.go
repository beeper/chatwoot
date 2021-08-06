package chatwootapi

import mid "maunium.net/go/mautrix/id"

type ChatwootAPI struct {
	ChatwootBaseURL string
}

func (api *ChatwootAPI) ContactIDWithEmail(email string) (int, error) {
	api.ChatwootBaseURL
}

func (api *ChatwootAPI) CreateConversation(roomID mid.RoomID, inboxID int, contactID int, additionalAttributes map[string]interface{}) error {
	// {
	//   "source_id": null,
	//   "inbox_id": "string",
	//   "contact_id": "string",
	//   "additional_attributes": {},
	//   "status": "open"
	// }
	return nil
}
