package telegram

import (
	"context"
	"regexp"
	"strings"
	"unicode/utf8"
)

// safeLimit is the conservative max display characters per message.
// Telegram limit ~4096; leave small margin for safety/markup effects.
const safeLimit = 4000

var (
	paraSplitRe     = regexp.MustCompile(`\n{2,}`)
	sentenceSplitRe = regexp.MustCompile(`(?m)([^.!?]+[.!?]+)`)
)

// stripHTML removes simple HTML tags to estimate displayed characters.
// It's intentionally simple: it removes <...> sequences.
var stripHTMLRe = regexp.MustCompile(`(?s)<[^>]*>`)

// SendChunked takes markdown 'md' as input, splits it into safe chunks,
// converts each chunk with LLMMarkdownToHTML, and sends them sequentially.
func (b *Bot) SendChunked(ctx context.Context, chatID int64, md string) ([]*Message, error) {
	parts := splitPreserveFenced(md)
	var sentMessages []*Message

	// convert parts (which are paragraphs or fenced blocks) into sequential messages
	var buf strings.Builder
	flush := func() error {
		s := strings.TrimSpace(buf.String())
		if s == "" {
			return nil
		}
		// convert markdown -> HTML using your existing helper
		html := LLMMarkdownToHTML(s)
		// measure display length by stripping tags
		display := stripHTMLRe.ReplaceAllString(html, "")

		if utf8.RuneCountInString(display) > safeLimit {
			// chunk too big: split by sentences or fallback to rune-chopping
			msgs, err := b.splitAndSend(ctx, chatID, s)
			sentMessages = append(sentMessages, msgs...)
			buf.Reset()
			return err
		}

		msg, err := b.SendMessage(ctx, &SendMessageParams{
			ChatID:    chatID,
			Text:      html,
			ParseMode: "HTML",
		})
		if msg != nil {
			sentMessages = append(sentMessages, msg)
		}
		buf.Reset()
		return err
	}

	for _, p := range parts {
		// try to append paragraph p to current buffer; flush if would exceed
		candidate := strings.TrimSpace(buf.String() + "\n\n" + p)
		html := LLMMarkdownToHTML(candidate)
		display := stripHTMLRe.ReplaceAllString(html, "")

		if utf8.RuneCountInString(display) > safeLimit {
			// flush current buffer then start new with p
			if err := flush(); err != nil {
				return sentMessages, err
			}
			// if p itself is too big, handle inside splitAndSend
			html2 := LLMMarkdownToHTML(strings.TrimSpace(p))
			display2 := stripHTMLRe.ReplaceAllString(html2, "")
			if utf8.RuneCountInString(display2) > safeLimit {
				msgs, err := b.splitAndSend(ctx, chatID, p)
				sentMessages = append(sentMessages, msgs...)
				if err != nil {
					return sentMessages, err
				}
				continue
			}
			buf.WriteString(p)
			continue
		}
		// safe to accumulate
		if buf.Len() == 0 {
			buf.WriteString(strings.TrimSpace(p))
		} else {
			buf.WriteString("\n\n")
			buf.WriteString(strings.TrimSpace(p))
		}
	}
	err := flush()
	return sentMessages, err
}

// splitAndSend splits a single big markdown chunk by sentences (or runes fallback)
// and sends each sub-chunk. It uses similar HTML conversion + check logic.
func (b *Bot) splitAndSend(ctx context.Context, chatID int64, md string) ([]*Message, error) {
	var sentMessages []*Message
	// try sentence splitting first
	sents := sentenceSplitRe.FindAllString(md, -1)
	if len(sents) == 0 {
		// fallback: chop by runes
		runes := []rune(md)
		for i := 0; i < len(runes); i += safeLimit {
			end := min(i+safeLimit, len(runes))
			chunk := string(runes[i:end])
			html := LLMMarkdownToHTML(chunk)
			msg, err := b.SendMessage(ctx, &SendMessageParams{
				ChatID:    chatID,
				Text:      html,
				ParseMode: "HTML",
			})
			if msg != nil {
				sentMessages = append(sentMessages, msg)
			}
			if err != nil {
				return sentMessages, err
			}
		}
		return sentMessages, nil
	}

	var buf strings.Builder
	flush := func() error {
		s := strings.TrimSpace(buf.String())
		if s == "" {
			return nil
		}
		html := LLMMarkdownToHTML(s)
		display := stripHTMLRe.ReplaceAllString(html, "")
		if utf8.RuneCountInString(display) > safeLimit {
			// extreme fallback: chop by runes
			runes := []rune(s)
			for i := 0; i < len(runes); i += safeLimit {
				end := i + safeLimit
				if end > len(runes) {
					end = len(runes)
				}
				chunk := string(runes[i:end])
				html2 := LLMMarkdownToHTML(chunk)
				msg, err := b.SendMessage(ctx, &SendMessageParams{ChatID: chatID, Text: html2, ParseMode: "HTML"})
				if err != nil {
					return err
				}
				if msg != nil {
					sentMessages = append(sentMessages, msg)
				}
			}
			buf.Reset()
			return nil
		}
		msg, err := b.SendMessage(ctx, &SendMessageParams{ChatID: chatID, Text: html, ParseMode: "HTML"})
		if err != nil {
			return err
		}
		if msg != nil {
			sentMessages = append(sentMessages, msg)
		}
		buf.Reset()
		return nil
	}

	for _, s := range sents {
		cand := strings.TrimSpace(buf.String() + " " + s)
		html := LLMMarkdownToHTML(cand)
		display := stripHTMLRe.ReplaceAllString(html, "")
		if utf8.RuneCountInString(display) > safeLimit {
			if err := flush(); err != nil {
				return sentMessages, err
			}
			buf.WriteString(strings.TrimSpace(s))
			continue
		}
		if buf.Len() == 0 {
			buf.WriteString(strings.TrimSpace(s))
		} else {
			buf.WriteString(" ")
			buf.WriteString(strings.TrimSpace(s))
		}
	}
	err := flush()
	return sentMessages, err
}

// splitPreserveFenced splits md into "paragraph-like" parts but treats fenced
// code blocks (```...```) as atomic (never split inside them).
func splitPreserveFenced(md string) []string {
	var out []string
	i := 0
	for i < len(md) {
		// look for next fenced block
		idx := strings.Index(md[i:], "```")
		if idx == -1 {
			// no more fenced blocks: split rest by paragraphs
			rest := md[i:]
			paras := paraSplitRe.Split(rest, -1)
			for _, p := range paras {
				if strings.TrimSpace(p) != "" {
					out = append(out, p)
				}
			}
			break
		}
		prefix := md[i : i+idx]
		// split prefix into paragraphs
		paras := paraSplitRe.Split(prefix, -1)
		for _, p := range paras {
			if strings.TrimSpace(p) != "" {
				out = append(out, p)
			}
		}
		// find end of fenced block
		start := i + idx
		endIdx := strings.Index(md[start+3:], "```")
		if endIdx == -1 {
			// unterminated fenced block: take rest as a block
			block := md[start:]
			out = append(out, block)
			break
		}
		end := start + 3 + endIdx + 3 // include closing ```
		block := md[start:end]
		out = append(out, block)
		i = end
	}
	return out
}
