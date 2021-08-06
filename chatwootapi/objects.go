package chatwootapi

type Conversation struct {
	ID int `json:"id"`
}

type MessageCreated struct {
	ID           int          `json:"id"`
	Content      string       `json:"content"`
	CreatedAt    string       `json:"created_at"`
	MessageType  string       `json:"message_type"`
	ContentType  string       `json:"content_type"`
	Private      bool         `json:"private"`
	Conversation Conversation `json:"conversation"`
}
