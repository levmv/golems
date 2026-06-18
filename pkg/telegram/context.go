package telegram

import (
	"context"
)

type HandlerFunc func(c *Context) error

type Context struct {
	context.Context

	Bot      *Bot
	Update   *Update
	ChatID   int64
	SenderID int64
	Command  string
}

func (c *Context) Text() string {
	if c.Update.Message != nil {
		return c.Update.Message.PlainText()
	}
	if c.Update.EditedMessage != nil {
		return c.Update.EditedMessage.PlainText()
	}
	return ""
}

func (c *Context) Reply(text string) error {
	_, err := c.Bot.SendMessage(c, &SendMessageParams{
		ChatID: c.ChatID,
		Text:   text,
	})
	return err
}

// ReplyChunked safely converts standard markdown to HTML, splits it if it's too long,
// and returns the sent Messages.
//
// For modern clients prefer ReplyRich, which sends GitHub Flavored Markdown
// (e.g. raw LLM output) untouched and renders headings, lists, tables and more.
func (c *Context) ReplyChunked(text string) ([]*Message, error) {
	return c.Bot.SendChunked(c, c.ChatID, text)
}

// ReplyRich sends GitHub Flavored Markdown (e.g. raw LLM output) back to the
// current chat as a rich message, with no conversion. Output longer than the
// rich-message limit is split across messages.
func (c *Context) ReplyRich(md string) ([]*Message, error) {
	return c.Bot.SendRichMarkdown(c, c.ChatID, md)
}
