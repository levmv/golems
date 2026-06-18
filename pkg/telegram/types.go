package telegram

const (
	ParseModeHTML       = "HTML"
	ParseModeMarkdownV2 = "MarkdownV2"
)

type Update struct {
	ID            int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	EditedMessage *Message       `json:"edited_message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

type Message struct {
	ID              int64           `json:"message_id"`
	From            *User           `json:"from,omitempty"`
	Chat            Chat            `json:"chat"`
	Date            int             `json:"date"`
	Text            string          `json:"text,omitempty"`
	Entities        []MessageEntity `json:"entities,omitempty"`
	RichMessage     *RichMessage    `json:"rich_message,omitempty"`
	Photo           []PhotoSize     `json:"photo,omitempty"`
	Document        *Document       `json:"document,omitempty"`
	Caption         string          `json:"caption,omitempty"`
	CaptionEntities []MessageEntity `json:"caption_entities,omitempty"`
	ReplyToMessage  *Message        `json:"reply_to_message,omitempty"`
}

// PlainText returns the text-like content of the message. For rich messages it
// returns a readable plain-text projection of the rich block tree.
func (m *Message) PlainText() string {
	if m == nil {
		return ""
	}
	if m.Text != "" {
		return m.Text
	}
	if m.RichMessage != nil {
		return m.RichMessage.PlainText()
	}
	return m.Caption
}

type User struct {
	ID           int64  `json:"id"`
	IsBot        bool   `json:"is_bot"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name,omitempty"`
	Username     string `json:"username,omitempty"`
	LanguageCode string `json:"language_code,omitempty"`
}

type Chat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title,omitempty"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
}

type MessageEntity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	URL    string `json:"url,omitempty"`
	User   *User  `json:"user,omitempty"`
}

type CallbackQuery struct {
	ID            string   `json:"id"`
	From          *User    `json:"from"`
	Message       *Message `json:"message,omitempty"`
	Data          string   `json:"data,omitempty"`
	GameShortName string   `json:"game_short_name,omitempty"`
}

type PhotoSize struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FileSize int    `json:"file_size,omitempty"`
}

type Document struct {
	FileID   string     `json:"file_id"`
	FileName string     `json:"file_name,omitempty"`
	MimeType string     `json:"mime_type,omitempty"`
	FileSize int        `json:"file_size,omitempty"`
	Thumb    *PhotoSize `json:"thumb,omitempty"`
}

type File struct {
	FileID   string `json:"file_id"`
	FileSize int    `json:"file_size,omitempty"`
	FilePath string `json:"file_path,omitempty"`
}

type WebhookInfo struct {
	URL                  string   `json:"url"`
	HasCustomCertificate bool     `json:"has_custom_certificate"`
	PendingUpdateCount   int      `json:"pending_update_count"`
	IPAddress            string   `json:"ip_address,omitempty"`
	LastErrorDate        int      `json:"last_error_date,omitempty"`
	LastError            string   `json:"last_error,omitempty"`
	MaxConnections       int      `json:"max_connections,omitempty"`
	AllowedUpdates       []string `json:"allowed_updates,omitempty"`
}

type UserProfilePhotos struct {
	TotalCount int           `json:"total_count"`
	Photos     [][]PhotoSize `json:"photos"`
}

type ChatMember struct {
	Status    string `json:"status"`
	User      *User  `json:"user"`
	UntilDate int    `json:"until_date,omitempty"`
}

type MessageOrigin struct {
	Type            string `json:"type"`
	From            *User  `json:"from"`
	Chat            *Chat  `json:"chat,omitempty"`
	ForwardFrom     *User  `json:"forward_from,omitempty"`
	ForwardFromChat *Chat  `json:"forward_from_chat,omitempty"`
}
