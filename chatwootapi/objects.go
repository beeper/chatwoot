package chatwootapi

// Contact
type Contact struct {
	ID    int    `json:"id"`
	Email string `json:"email"`
}

type ContactsPayload struct {
	Payload []Contact `json:"payload"`
}

// Attachment

type Attachment struct {
	ID        int    `json:"id"`
	FileType  string `json:"file_type"`
	AccountID int    `json:"account_id"`
	DataURL   string `json:"data_url"`
	ThumbURL  string `json:"thumb_url"`
}

// Message

type Message struct {
	ID          int          `json:"id"`
	Content     *string      `json:"content"`
	Private     bool         `json:"private"`
	Attachments []Attachment `json:"attachments"`
}

// Conversation

type Conversation struct {
	ID        int       `json:"id"`
	AccountID int       `json:"account_id"`
	InboxID   int       `json:"inbox_id"`
	Messages  []Message `json:"messages"`
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
