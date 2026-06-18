package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// RichMessageMaxChars is Telegram's limit on rich message text length: up to
// 32768 UTF-8 characters, including custom emoji alternative text and formula
// source. (Plain messages, by contrast, are capped at 4096.)
//
// Other rich message limits worth knowing: up to 500 blocks (incl. nested
// blocks, list items, table rows, quotations, details), 16 levels of nesting,
// 50 media attachments, and 20 columns per table.
const RichMessageMaxChars = 32768

// InputRichMessage describes a rich message to be sent. Exactly one of Markdown
// or HTML must be set.
//
// Rich Markdown is GitHub Flavored Markdown compatible, so output from an LLM
// can be passed through verbatim — no conversion to Telegram HTML is needed.
// Headings, lists, task lists, tables, blockquotes (incl. expandable), spoilers,
// code blocks and math are all parsed server-side.
type InputRichMessage struct {
	Markdown string `json:"markdown,omitempty"`
	HTML     string `json:"html,omitempty"`
	// IsRTL shows the rich message right-to-left.
	IsRTL bool `json:"is_rtl,omitempty"`
	// SkipEntityDetection disables automatic detection of URLs, e-mails,
	// @mentions, #hashtags, $cashtags, bot commands and phone numbers.
	SkipEntityDetection bool `json:"skip_entity_detection,omitempty"`
}

// RichMessage is Telegram's parsed rich-message representation received in
// incoming updates and send/edit responses.
type RichMessage struct {
	Blocks []RichBlock     `json:"blocks"`
	IsRTL  bool            `json:"is_rtl,omitempty"`
	Raw    json.RawMessage `json:"-"`
}

func (m *RichMessage) UnmarshalJSON(data []byte) error {
	type richMessage RichMessage
	var v richMessage
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*m = RichMessage(v)
	m.Raw = append(m.Raw[:0], data...)
	return nil
}

func (m *RichMessage) PlainText() string {
	if m == nil {
		return ""
	}
	parts := make([]string, 0, len(m.Blocks))
	for _, block := range m.Blocks {
		if text := block.PlainText(); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

type RichBlock struct {
	Type       string                 `json:"type"`
	Text       RichText               `json:"text,omitempty"`
	Blocks     []RichBlock            `json:"blocks,omitempty"`
	Items      []RichBlockListItem    `json:"items,omitempty"`
	Cells      [][]RichBlockTableCell `json:"cells,omitempty"`
	Caption    *RichBlockCaption      `json:"caption,omitempty"`
	Credit     RichText               `json:"credit,omitempty"`
	Summary    RichText               `json:"summary,omitempty"`
	Expression string                 `json:"expression,omitempty"`
}

func (b RichBlock) PlainText() string {
	var parts []string
	appendText := func(text string) {
		if text != "" {
			parts = append(parts, text)
		}
	}

	appendText(b.Text.PlainText())
	appendText(b.Expression)
	for _, item := range b.Items {
		appendText(item.PlainText())
	}
	for _, block := range b.Blocks {
		appendText(block.PlainText())
	}
	for _, row := range b.Cells {
		var cells []string
		for _, cell := range row {
			if text := cell.PlainText(); text != "" {
				cells = append(cells, text)
			}
		}
		appendText(strings.Join(cells, "\t"))
	}
	appendText(b.Summary.PlainText())
	appendText(b.Credit.PlainText())
	if b.Caption != nil {
		appendText(b.Caption.PlainText())
	}

	return strings.Join(parts, "\n")
}

type RichBlockCaption struct {
	Text   RichText `json:"text"`
	Credit RichText `json:"credit,omitempty"`
}

func (c RichBlockCaption) PlainText() string {
	parts := []string{}
	if text := c.Text.PlainText(); text != "" {
		parts = append(parts, text)
	}
	if credit := c.Credit.PlainText(); credit != "" {
		parts = append(parts, credit)
	}
	return strings.Join(parts, "\n")
}

type RichBlockListItem struct {
	Label       string      `json:"label,omitempty"`
	Blocks      []RichBlock `json:"blocks"`
	HasCheckbox bool        `json:"has_checkbox,omitempty"`
	IsChecked   bool        `json:"is_checked,omitempty"`
	Value       int         `json:"value,omitempty"`
	Type        string      `json:"type,omitempty"`
}

func (i RichBlockListItem) PlainText() string {
	parts := make([]string, 0, len(i.Blocks)+1)
	if i.Label != "" {
		parts = append(parts, i.Label)
	}
	for _, block := range i.Blocks {
		if text := block.PlainText(); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, " ")
}

type RichBlockTableCell struct {
	Text     RichText `json:"text,omitempty"`
	IsHeader bool     `json:"is_header,omitempty"`
	Colspan  int      `json:"colspan,omitempty"`
	Rowspan  int      `json:"rowspan,omitempty"`
	Align    string   `json:"align,omitempty"`
	Valign   string   `json:"valign,omitempty"`
}

func (c RichBlockTableCell) PlainText() string {
	return c.Text.PlainText()
}

// RichText is a recursive Telegram value that can be a string, an array of rich
// text parts, or a typed object such as bold, url, mention, or formula.
type RichText struct {
	String          string
	Parts           []RichText
	Type            string
	Text            *RichText
	Expression      string
	AlternativeText string
	Username        string
	Hashtag         string
	Cashtag         string
	BotCommand      string
	EmailAddress    string
	PhoneNumber     string
	BankCardNumber  string
	Name            string
	AnchorName      string
	ReferenceName   string
	Raw             json.RawMessage
}

func (t *RichText) UnmarshalJSON(data []byte) error {
	t.Raw = append(t.Raw[:0], data...)

	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		t.String = s
		return nil
	}

	var parts []RichText
	if err := json.Unmarshal(data, &parts); err == nil {
		t.Parts = parts
		return nil
	}

	var aux struct {
		Type            string          `json:"type"`
		Text            json.RawMessage `json:"text"`
		Expression      string          `json:"expression"`
		AlternativeText string          `json:"alternative_text"`
		Username        string          `json:"username"`
		Hashtag         string          `json:"hashtag"`
		Cashtag         string          `json:"cashtag"`
		BotCommand      string          `json:"bot_command"`
		EmailAddress    string          `json:"email_address"`
		PhoneNumber     string          `json:"phone_number"`
		BankCardNumber  string          `json:"bank_card_number"`
		Name            string          `json:"name"`
		AnchorName      string          `json:"anchor_name"`
		ReferenceName   string          `json:"reference_name"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	t.Type = aux.Type
	t.Expression = aux.Expression
	t.AlternativeText = aux.AlternativeText
	t.Username = aux.Username
	t.Hashtag = aux.Hashtag
	t.Cashtag = aux.Cashtag
	t.BotCommand = aux.BotCommand
	t.EmailAddress = aux.EmailAddress
	t.PhoneNumber = aux.PhoneNumber
	t.BankCardNumber = aux.BankCardNumber
	t.Name = aux.Name
	t.AnchorName = aux.AnchorName
	t.ReferenceName = aux.ReferenceName
	if len(aux.Text) > 0 {
		var text RichText
		if err := json.Unmarshal(aux.Text, &text); err != nil {
			return err
		}
		t.Text = &text
	}
	return nil
}

func (t RichText) PlainText() string {
	if t.String != "" {
		return t.String
	}
	if len(t.Parts) > 0 {
		parts := make([]string, 0, len(t.Parts))
		for _, part := range t.Parts {
			parts = append(parts, part.PlainText())
		}
		return strings.Join(parts, "")
	}
	if t.Text != nil {
		return t.Text.PlainText()
	}
	for _, value := range []string{
		t.Expression,
		t.AlternativeText,
		t.BotCommand,
		t.Username,
		t.Hashtag,
		t.Cashtag,
		t.EmailAddress,
		t.PhoneNumber,
		t.BankCardNumber,
		t.Name,
		t.AnchorName,
		t.ReferenceName,
	} {
		if value != "" {
			return value
		}
	}
	return ""
}

type SendRichMessageParams struct {
	ChatID               int64            `json:"chat_id"`
	BusinessConnectionID string           `json:"business_connection_id,omitempty"`
	MessageThreadID      int              `json:"message_thread_id,omitempty"`
	RichMessage          InputRichMessage `json:"rich_message"`
	DisableNotification  bool             `json:"disable_notification,omitempty"`
	ProtectContent       bool             `json:"protect_content,omitempty"`
	MessageEffectID      string           `json:"message_effect_id,omitempty"`
	ReplyParameters      *ReplyParameters `json:"reply_parameters,omitempty"`
	ReplyMarkup          ReplyMarkup      `json:"reply_markup,omitempty"`
}

// SendRichMessage sends a single rich message. On success the sent Message is
// returned. For long LLM output prefer SendRichMarkdown, which handles the
// 32768-character limit.
func (b *Bot) SendRichMessage(ctx context.Context, params *SendRichMessageParams) (*Message, error) {
	result := &Message{}
	err := b.rawRequest(ctx, "sendRichMessage", params, result)
	if err != nil {
		b.logger.Error("sendRichMessage failed for chat=%d: %v", params.ChatID, err)
	}
	return result, err
}

type SendRichMessageDraftParams struct {
	ChatID          int64            `json:"chat_id"`
	MessageThreadID int              `json:"message_thread_id,omitempty"`
	DraftID         int64            `json:"draft_id"`
	RichMessage     InputRichMessage `json:"rich_message"`
}

// SendRichMessageDraft streams a partial rich message to a private chat while
// it is being generated. The draft is ephemeral — a ~30s preview; updates with
// the same DraftID are animated. Once generation is complete you must call
// SendRichMessage (or SendRichMarkdown) to persist the final message. Most
// callers should use RichStream instead of calling this directly.
func (b *Bot) SendRichMessageDraft(ctx context.Context, params *SendRichMessageDraftParams) (bool, error) {
	var result bool
	err := b.rawRequest(ctx, "sendRichMessageDraft", params, &result)
	return result, err
}

// SendRichMarkdown sends GitHub Flavored Markdown (e.g. raw LLM output) as a
// rich message, passing it through verbatim. If Telegram rejects the rich
// content as a bad request, it falls back to the legacy Markdown-to-HTML sender.
// If the text exceeds RichMessageMaxChars it is split across several messages on
// block boundaries.
func (b *Bot) SendRichMarkdown(ctx context.Context, chatID int64, md string) ([]*Message, error) {
	var sent []*Message
	for _, chunk := range splitRich(md) {
		msg, err := b.SendRichMessage(ctx, &SendRichMessageParams{
			ChatID:      chatID,
			RichMessage: InputRichMessage{Markdown: chunk},
		})
		if err != nil {
			if errors.Is(err, ErrBadRequest) {
				fallback, fallbackErr := b.SendChunked(ctx, chatID, md)
				sent = append(sent, fallback...)
				if fallbackErr != nil {
					return sent, fmt.Errorf("send rich markdown: %w; fallback send chunked: %v", err, fallbackErr)
				}
				return sent, nil
			}
			return sent, err
		}
		if msg != nil {
			sent = append(sent, msg)
		}
	}
	return sent, nil
}

// splitRich splits markdown into chunks no longer than RichMessageMaxChars,
// keeping fenced code blocks atomic. The common case (output that fits) returns
// a single chunk.
func splitRich(md string) []string {
	md = strings.TrimSpace(md)
	if md == "" {
		return nil
	}
	if utf8.RuneCountInString(md) <= RichMessageMaxChars {
		return []string{md}
	}

	var out []string
	var buf strings.Builder
	flush := func() {
		if s := strings.TrimSpace(buf.String()); s != "" {
			out = append(out, s)
		}
		buf.Reset()
	}

	for _, p := range splitPreserveFenced(md) {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if buf.Len() > 0 && utf8.RuneCountInString(buf.String())+utf8.RuneCountInString(p)+2 > RichMessageMaxChars {
			flush()
		}
		if utf8.RuneCountInString(p) > RichMessageMaxChars {
			flush()
			out = append(out, chopRunes(p, RichMessageMaxChars)...)
			continue
		}
		if buf.Len() > 0 {
			buf.WriteString("\n\n")
		}
		buf.WriteString(p)
	}
	flush()
	return out
}

func chopRunes(s string, limit int) []string {
	runes := []rune(s)
	var out []string
	for i := 0; i < len(runes); i += limit {
		out = append(out, string(runes[i:min(i+limit, len(runes))]))
	}
	return out
}

// RichStream streams a rich message to a private chat while it is being
// generated, throttling network updates to at most one per 500ms. Write appends
// generated text; Close finalizes the ephemeral draft into a persistent message.
//
//	stream := bot.StartRichStream(c.ChatID, 12345)
//	for tok := range tokens {
//	    stream.Write(c, tok)
//	}
//	stream.Close(c)
type RichStream struct {
	chatID     int64
	draftID    int64
	bot        *Bot
	mu         sync.Mutex
	buf        strings.Builder
	closed     bool
	lastUpdate time.Time
}

// StartRichStream begins a rich streaming session. draftID is an arbitrary
// non-zero identifier; reusing it across Writes is what makes updates animate.
func (b *Bot) StartRichStream(chatID int64, draftID int64) *RichStream {
	return &RichStream{chatID: chatID, draftID: draftID, bot: b, lastUpdate: time.Now()}
}

// Write appends text to the stream and, subject to throttling, pushes the
// current state as a draft preview. Raw (possibly mid-token) markdown is sent;
// since the draft is only a preview, transiently broken markdown is harmless.
func (s *RichStream) Write(ctx context.Context, text string) error {
	if text == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("stream already closed")
	}
	s.buf.WriteString(text)
	if time.Since(s.lastUpdate) < 500*time.Millisecond {
		return nil
	}

	display := s.buf.String()
	if runes := []rune(display); len(runes) > RichMessageMaxChars {
		display = "…\n" + string(runes[len(runes)-(RichMessageMaxChars-2):])
	}
	_, err := s.bot.SendRichMessageDraft(ctx, &SendRichMessageDraftParams{
		ChatID:      s.chatID,
		DraftID:     s.draftID,
		RichMessage: InputRichMessage{Markdown: display},
	})
	if err == nil {
		s.lastUpdate = time.Now()
	}
	return err
}

// Close finalizes the stream: it persists the accumulated text as a real rich
// message (splitting if it exceeds the limit) and lets the ephemeral draft
// expire. Safe to call multiple times.
func (s *RichStream) Close(ctx context.Context) ([]*Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil
	}
	s.closed = true
	return s.bot.SendRichMarkdown(ctx, s.chatID, s.buf.String())
}
