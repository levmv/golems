package telegram

import "strings"

type RouteType int

const (
	RouteCommand RouteType = iota
	RouteText
	RouteCallback
	RoutePhoto
	RouteDocument
	RouteEditedMessage
	RouteAny
)

type route struct {
	routeType RouteType
	pattern   string
	handler   HandlerFunc
}

func (r *route) match(c *Context) bool {
	upd := c.Update

	switch r.routeType {
	case RouteCommand:
		return c.Command == r.pattern

	case RouteText:
		if upd.Message == nil || upd.Message.PlainText() == "" {
			return false
		}
		// If command, don't trigger normal text handlers
		if c.Command != "" {
			return false
		}
		return true

	case RouteCallback:
		if upd.CallbackQuery == nil {
			return false
		}
		if r.pattern != "" {
			return upd.CallbackQuery.Data == r.pattern
		}
		return true

	case RoutePhoto:
		return upd.Message != nil && len(upd.Message.Photo) > 0

	case RouteDocument:
		return upd.Message != nil && upd.Message.Document != nil

	case RouteEditedMessage:
		return upd.EditedMessage != nil

	case RouteAny:
		return true
	}

	return false
}

func (b *Bot) findHandler(c *Context) HandlerFunc {
	for _, r := range b.handlers {
		if r.match(c) {
			return r.handler
		}
	}

	return func(c *Context) error {
		return nil
	}
}

// OnCommand registers a handler for "/command". Leading slashes are optionally ignored.
func (b *Bot) OnCommand(command string, h HandlerFunc) {
	command = strings.TrimPrefix(command, "/")
	b.handlers = append(b.handlers, route{routeType: RouteCommand, pattern: command, handler: h})
}

// OnText catches ANY text message that isn't a command.
func (b *Bot) OnText(h HandlerFunc) {
	b.handlers = append(b.handlers, route{routeType: RouteText, handler: h})
}

// OnCallback handles inline keyboard button presses.
func (b *Bot) OnCallback(data string, h HandlerFunc) {
	b.handlers = append(b.handlers, route{routeType: RouteCallback, pattern: data, handler: h})
}

func (b *Bot) OnPhoto(h HandlerFunc) {
	b.handlers = append(b.handlers, route{routeType: RoutePhoto, handler: h})
}

func (b *Bot) OnEditedMessage(h HandlerFunc) {
	b.handlers = append(b.handlers, route{routeType: RouteEditedMessage, handler: h})
}

func (b *Bot) OnAny(h HandlerFunc) {
	b.handlers = append(b.handlers, route{routeType: RouteAny, handler: h})
}
