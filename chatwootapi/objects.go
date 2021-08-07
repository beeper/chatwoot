package chatwootapi

// Contact
type Contact struct {
	ID    int    `json:"id"`
	Email string `json:"email"`
}

type ContactsPayload struct {
	Payload []Contact `json:"payload"`
}

// Conversation

type Conversation struct {
	ID        int `json:"id"`
	AccountID int `json:"account_id"`
	InboxID   int `json:"inbox_id"`
}

// Message

type Message struct {
	ID int `json:"id"`
}

// Webhook

type MessageCreated struct {
	ID           int          `json:"id"`
	Content      string       `json:"content"`
	CreatedAt    string       `json:"created_at"`
	MessageType  string       `json:"message_type"`
	ContentType  string       `json:"content_type"`
	Private      bool         `json:"private"`
	Conversation Conversation `json:"conversation"`
}
