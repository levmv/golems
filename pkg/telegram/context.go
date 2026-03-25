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
		return c.Update.Message.Text
	}
	if c.Update.EditedMessage != nil {
		return c.Update.EditedMessage.Text
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
func (c *Context) ReplyChunked(text string) ([]*Message, error) {
	return c.Bot.SendChunked(c, c.ChatID, text)
}
