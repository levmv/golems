package telegram

type ReplyMarkup interface{}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type InlineKeyboardButton struct {
	Text                         string        `json:"text"`
	URL                          string        `json:"url,omitempty"`
	CallbackData                 string        `json:"callback_data,omitempty"`
	WebApp                       *WebAppInfo   `json:"web_app,omitempty"`
	LoginURL                     *LoginURL     `json:"login_url,omitempty"`
	SwitchInlineQuery            string        `json:"switch_inline_query,omitempty"`
	SwitchInlineQueryCurrentChat string        `json:"switch_inline_query_current_chat,omitempty"`
	CallbackGame                 *CallbackGame `json:"callback_game,omitempty"`
	Pay                          bool          `json:"pay,omitempty"`
}

type WebAppInfo struct {
	URL string `json:"url"`
}

type LoginURL struct {
	URL         string `json:"url"`
	ForwardText string `json:"forward_text,omitempty"`
	BotUsername string `json:"bot_username,omitempty"`
}

type CallbackGame struct{}

type ReplyKeyboardMarkup struct {
	Keyboard              [][]KeyboardButton `json:"keyboard"`
	ResizeKeyboard        bool               `json:"resize_keyboard,omitempty"`
	OneTimeKeyboard       bool               `json:"one_time_keyboard,omitempty"`
	InputFieldPlaceholder string             `json:"input_field_placeholder,omitempty"`
	Selective             bool               `json:"selective,omitempty"`
}

type KeyboardButton struct {
	Text            string      `json:"text"`
	RequestContact  bool        `json:"request_contact,omitempty"`
	RequestLocation bool        `json:"request_location,omitempty"`
	WebApp          *WebAppInfo `json:"web_app,omitempty"`
}

type ReplyKeyboardRemove struct {
	RemoveKeyboard bool `json:"remove_keyboard"`
}

type ForceReply struct {
	ForceReply            bool   `json:"force_reply"`
	InputFieldPlaceholder string `json:"input_field_placeholder,omitempty"`
	Selective             bool   `json:"selective,omitempty"`
}

func NewInlineKeyboardMarkup() *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{
		InlineKeyboard: make([][]InlineKeyboardButton, 0),
	}
}

func (k *InlineKeyboardMarkup) AddRow(buttons ...InlineKeyboardButton) *InlineKeyboardMarkup {
	k.InlineKeyboard = append(k.InlineKeyboard, buttons)
	return k
}

func NewInlineKeyboardButton(text string) InlineKeyboardButton {
	return InlineKeyboardButton{Text: text}
}

func (b InlineKeyboardButton) WithURL(url string) InlineKeyboardButton {
	b.URL = url
	return b
}

func (b InlineKeyboardButton) WithCallback(data string) InlineKeyboardButton {
	b.CallbackData = data
	return b
}

func (b InlineKeyboardButton) WithSwitchInline(query string) InlineKeyboardButton {
	b.SwitchInlineQuery = query
	return b
}

func (b InlineKeyboardButton) WithWebApp(url string) InlineKeyboardButton {
	b.WebApp = &WebAppInfo{URL: url}
	return b
}

func NewReplyKeyboardMarkup(keyboard [][]KeyboardButton) *ReplyKeyboardMarkup {
	return &ReplyKeyboardMarkup{Keyboard: keyboard}
}

func NewReplyKeyboardRemove() *ReplyKeyboardRemove {
	return &ReplyKeyboardRemove{RemoveKeyboard: true}
}

func NewForceReply() *ForceReply {
	return &ForceReply{ForceReply: true}
}

func NewKeyboardButton(text string) KeyboardButton {
	return KeyboardButton{Text: text}
}
