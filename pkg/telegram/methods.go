package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type SetWebhookParams struct {
	URL                string    `json:"url"`
	Certificate        InputFile `json:"certificate,omitempty"`
	IPAddress          string    `json:"ip_address,omitempty"`
	MaxConnections     int       `json:"max_connections,omitempty"`
	AllowedUpdates     []string  `json:"allowed_updates,omitempty"`
	DropPendingUpdates bool      `json:"drop_pending_updates,omitempty"`
	SecretToken        string    `json:"secret_token,omitempty"`
}

type DeleteWebhookParams struct {
	DropPendingUpdates bool `json:"drop_pending_updates,omitempty"`
}

type GetUpdatesParams struct {
	Offset         int64    `json:"offset,omitempty"`
	Limit          int      `json:"limit,omitempty"`
	Timeout        int      `json:"timeout,omitempty"`
	AllowedUpdates []string `json:"allowed_updates,omitempty"`
}

type SendMessageParams struct {
	ChatID                int64            `json:"chat_id"`
	MessageThreadID       int              `json:"message_thread_id,omitempty"`
	Text                  string           `json:"text"`
	ParseMode             string           `json:"parse_mode,omitempty"`
	Entities              []MessageEntity  `json:"entities,omitempty"`
	DisableWebPagePreview bool             `json:"disable_web_page_preview,omitempty"`
	DisableNotification   bool             `json:"disable_notification,omitempty"`
	ProtectContent        bool             `json:"protect_content,omitempty"`
	ReplyParameters       *ReplyParameters `json:"reply_parameters,omitempty"`
	ReplyMarkup           ReplyMarkup      `json:"reply_markup,omitempty"`
}

type EditMessageTextParams struct {
	ChatID          int64  `json:"chat_id"`
	MessageID       int    `json:"message_id,omitempty"`
	InlineMessageID string `json:"inline_message_id,omitempty"`
	// Text is the new plain text (1-4096 chars). Required unless RichMessage is set.
	Text      string          `json:"text,omitempty"`
	ParseMode string          `json:"parse_mode,omitempty"`
	Entities  []MessageEntity `json:"entities,omitempty"`
	// RichMessage is the new rich content. Mutually exclusive with Text.
	RichMessage *InputRichMessage     `json:"rich_message,omitempty"`
	ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

type AnswerCallbackQueryParams struct {
	CallbackQueryID string `json:"callback_query_id"`
	Text            string `json:"text,omitempty"`
	ShowAlert       bool   `json:"show_alert,omitempty"`
	URL             string `json:"url,omitempty"`
	CacheTime       int    `json:"cache_time,omitempty"`
}

type SendPhotoParams struct {
	ChatID              int64            `json:"chat_id"`
	MessageThreadID     int              `json:"message_thread_id,omitempty"`
	Photo               InputFile        `json:"photo"`
	Caption             string           `json:"caption,omitempty"`
	ParseMode           string           `json:"parse_mode,omitempty"`
	CaptionEntities     []MessageEntity  `json:"caption_entities,omitempty"`
	DisableNotification bool             `json:"disable_notification,omitempty"`
	ProtectContent      bool             `json:"protect_content,omitempty"`
	ReplyParameters     *ReplyParameters `json:"reply_parameters,omitempty"`
	ReplyMarkup         ReplyMarkup      `json:"reply_markup,omitempty"`
}

type SendDocumentParams struct {
	ChatID                      int64            `json:"chat_id"`
	MessageThreadID             int              `json:"message_thread_id,omitempty"`
	Document                    InputFile        `json:"document"`
	Thumbnail                   InputFile        `json:"thumbnail,omitempty"`
	Caption                     string           `json:"caption,omitempty"`
	ParseMode                   string           `json:"parse_mode,omitempty"`
	CaptionEntities             []MessageEntity  `json:"caption_entities,omitempty"`
	DisableContentTypeDetection bool             `json:"disable_content_type_detection,omitempty"`
	DisableNotification         bool             `json:"disable_notification,omitempty"`
	ProtectContent              bool             `json:"protect_content,omitempty"`
	ReplyParameters             *ReplyParameters `json:"reply_parameters,omitempty"`
	ReplyMarkup                 ReplyMarkup      `json:"reply_markup,omitempty"`
}

type ReplyParameters struct {
	MessageID                int             `json:"message_id"`
	ChatID                   any             `json:"chat_id,omitempty"`
	AllowSendingWithoutReply bool            `json:"allow_sending_without_reply,omitempty"`
	Quote                    string          `json:"quote,omitempty"`
	QuoteParseMode           string          `json:"quote_parse_mode,omitempty"`
	QuoteEntities            []MessageEntity `json:"quote_entities,omitempty"`
	QuotePosition            int             `json:"quote_position,omitempty"`
}

type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

type BotCommandScope struct {
	Type   string `json:"type"`
	ChatID any    `json:"chat_id,omitempty"`
	UserID int64  `json:"user_id,omitempty"`
}

type SetMyCommandsRequest struct {
	Commands     []BotCommand     `json:"commands"`
	Scope        *BotCommandScope `json:"scope,omitempty"`
	LanguageCode string           `json:"language_code,omitempty"`
}

// ScopeChat applies the command ONLY to a specific private chat or group.
func ScopeChat(chatID int64) *BotCommandScope {
	return &BotCommandScope{Type: "chat", ChatID: chatID}
}

type ChatAction string

const (
	ActionTyping          ChatAction = "typing"
	ActionUploadPhoto     ChatAction = "upload_photo"
	ActionRecordVideo     ChatAction = "record_video"
	ActionUploadVideo     ChatAction = "upload_video"
	ActionRecordVoice     ChatAction = "record_voice"
	ActionUploadVoice     ChatAction = "upload_voice"
	ActionUploadDocument  ChatAction = "upload_document"
	ActionChooseSticker   ChatAction = "choose_sticker"
	ActionFindLocation    ChatAction = "find_location"
	ActionRecordVideoNote ChatAction = "record_video_note"
	ActionUploadVideoNote ChatAction = "upload_video_note"
)

type SendChatActionRequest struct {
	ChatID int64  `json:"chat_id"`
	Action string `json:"action"`
}

type GetFileParams struct {
	FileID string `json:"file_id"`
}

type DeleteMessagesParams struct {
	ChatID     int64 `json:"chat_id"`
	MessageIDs []int `json:"message_ids"`
}

func (b *Bot) SendMessage(ctx context.Context, params *SendMessageParams) (*Message, error) {
	result := &Message{}

	err := b.rawRequest(ctx, "sendMessage", params, result)
	if err != nil && strings.Contains(err.Error(), "can't parse entities") {
		// Fallback: copy struct to avoid race conditions, strip ParseMode, and retry
		retryParams := *params
		retryParams.ParseMode = ""
		b.logger.Debug("Retrying without ParseMode due to formatting error")
		err = b.rawRequest(ctx, "sendMessage", &retryParams, result)
	}
	if err != nil {
		b.logger.Error("sendMessage failed for chat=%d: %v", params.ChatID, err)
	}
	return result, err
}

func (b *Bot) EditMessageText(ctx context.Context, params *EditMessageTextParams) (*Message, error) {
	result := &Message{}
	err := b.rawRequest(ctx, "editMessageText", params, result)
	if err != nil && strings.Contains(err.Error(), "can't parse entities") {
		retryParams := *params
		retryParams.ParseMode = ""
		b.logger.Debug("Retrying EditMessageText without ParseMode due to formatting error")
		err = b.rawRequest(ctx, "editMessageText", &retryParams, result)
	}
	return result, err
}

func (b *Bot) AnswerCallbackQuery(ctx context.Context, params *AnswerCallbackQueryParams) (bool, error) {
	var result bool
	err := b.rawRequest(ctx, "answerCallbackQuery", params, &result)
	return result, err
}

func (b *Bot) SendPhoto(ctx context.Context, params *SendPhotoParams) (*Message, error) {
	result := &Message{}
	err := b.rawRequest(ctx, "sendPhoto", params, result)
	return result, err
}

func (b *Bot) SendDocument(ctx context.Context, params *SendDocumentParams) (*Message, error) {
	result := &Message{}
	err := b.rawRequest(ctx, "sendDocument", params, result)
	return result, err
}

func (b *Bot) GetMe(ctx context.Context) (*User, error) {
	result := &User{}
	err := b.rawRequest(ctx, "getMe", nil, result)
	return result, err
}

func (b *Bot) SetMyCommands(ctx context.Context, req SetMyCommandsRequest) (bool, error) {
	var result bool
	err := b.rawRequest(ctx, "setMyCommands", req, &result)
	return result, err
}

func (b *Bot) SendChatAction(ctx context.Context, chatID int64, action ChatAction) (bool, error) {
	var result bool
	req := SendChatActionRequest{ChatID: chatID, Action: string(action)}
	err := b.rawRequest(ctx, "sendChatAction", req, &result)
	return result, err
}

func (b *Bot) SetWebhook(ctx context.Context, params *SetWebhookParams) (bool, error) {
	var result bool
	err := b.rawRequest(ctx, "setWebhook", params, &result)
	return result, err
}

func (b *Bot) DeleteWebhook(ctx context.Context, params *DeleteWebhookParams) (bool, error) {
	var result bool
	err := b.rawRequest(ctx, "deleteWebhook", params, &result)
	return result, err
}

func (b *Bot) GetWebhookInfo(ctx context.Context) (*WebhookInfo, error) {
	result := &WebhookInfo{}
	err := b.rawRequest(ctx, "getWebhookInfo", nil, result)
	return result, err
}

func (b *Bot) GetFile(ctx context.Context, fileID string) (*File, error) {
	result := &File{}
	err := b.rawRequest(ctx, "getFile", &GetFileParams{FileID: fileID}, result)
	return result, err
}

func (b *Bot) GetUserProfilePhotos(ctx context.Context, userID int64, offset, limit int) (*UserProfilePhotos, error) {
	result := &UserProfilePhotos{}
	params := map[string]interface{}{"user_id": userID}
	if offset > 0 {
		params["offset"] = offset
	}
	if limit > 0 {
		params["limit"] = limit
	}
	err := b.rawRequest(ctx, "getUserProfilePhotos", params, result)
	return result, err
}

func (b *Bot) FileDownloadLink(f *File) string {
	if f.FilePath == "" {
		return ""
	}
	return b.url + "/file/bot" + b.token + "/" + f.FilePath
}

func (b *Bot) DeleteMessages(ctx context.Context, chatID int64, messageIDs []int) (bool, error) {
	if len(messageIDs) == 0 {
		return true, nil
	}
	var result bool
	req := DeleteMessagesParams{ChatID: chatID, MessageIDs: messageIDs}
	err := b.rawRequest(ctx, "deleteMessages", req, &result)
	return result, err
}

// DownloadFile gets a file from Telegram and returns the raw bytes.
func (b *Bot) DownloadFile(ctx context.Context, fileID string) ([]byte, error) {
	// 1. Get the file path from Telegram
	file, err := b.GetFile(ctx, fileID)
	if err != nil {
		return nil, fmt.Errorf("failed to get file info: %w", err)
	}
	if file.FilePath == "" {
		return nil, errors.New("file path is empty")
	}

	// 2. Download the actual bytes
	url := fmt.Sprintf("%s/file/bot%s/%s", b.url, b.token, file.FilePath)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad download status: %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

// TypingIndicator manages a periodic "typing" (or other) chat action.
type TypingIndicator struct {
	stop context.CancelFunc
	done chan struct{}
	once sync.Once // ensures Stop() is safe to call multiple times
}

// Stop halts the periodic chat action. Safe to call multiple times.
func (t *TypingIndicator) Stop() {
	t.once.Do(func() {
		if t.stop != nil {
			t.stop()
			<-t.done // wait for goroutine to exit cleanly
		}
	})
}

// StartTyping begins sending the given chat action periodically.
// The action is sent immediately, then repeated every ~4 seconds.
func (b *Bot) StartTyping(ctx context.Context, chatID int64, action ChatAction) *TypingIndicator {
	actionCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	// Initial burst
	_, _ = b.SendChatAction(ctx, chatID, action)

	go func() {
		defer close(done)
		ticker := time.NewTicker(4 * time.Second) // Telegram action expires ~5s
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				_, _ = b.SendChatAction(actionCtx, chatID, action)
			case <-actionCtx.Done():
				return
			}
		}
	}()

	return &TypingIndicator{stop: cancel, done: done}
}

// StartTypingIndicator is a convenience wrapper for ActionTyping.
func (b *Bot) StartTypingIndicator(ctx context.Context, chatID int64) *TypingIndicator {
	return b.StartTyping(ctx, chatID, ActionTyping)
}
