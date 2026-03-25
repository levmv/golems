package telegram

import (
	"fmt"
	"html"
	"regexp"
	"strings"
)

// markdownV2Escaper contains all characters that must be escaped in MarkdownV2.
var markdownV2Escaper = strings.NewReplacer(
	"_", "\\_", "*", "\\*", "[", "\\[", "]", "\\]", "(", "\\(",
	")", "\\)", "~", "\\~", "`", "\\`", ">", "\\>", "#", "\\#",
	"+", "\\+", "-", "\\-", "=", "\\=", "|", "\\|", "{", "\\{",
	"}", "\\}", ".", "\\.", "!", "\\!",
)

// EscapeMarkdown escapes special characters for Telegram's MarkdownV2 ParseMode.
// Use this to sanitize user input or variables before inserting them into a MarkdownV2 message.
func EscapeMarkdown(s string) string {
	return markdownV2Escaper.Replace(s)
}

var (
	// (?s) allows . to match newlines
	codeBlockRegex  = regexp.MustCompile(`(?s)` + "```" + `(?:([a-zA-Z0-9\-]+)\n)?(.*?)\n?` + "```")
	inlineCodeRegex = regexp.MustCompile(`(?s)` + "`" + `([^` + "`" + `]+)` + "`")

	// Bold: **text**
	boldRegex = regexp.MustCompile(`(?s)\*\*(.*?)\*\*`)

	// Italic with *: *text*. We ensure the first character isn't a space so it doesn't match bullet points!
	// We also forbid matching across newlines to be extra safe against broken markdown.
	italicAsteriskRegex = regexp.MustCompile(`\*([^\s\*][^\*\n]*)\*`)

	// Italic with _: _text_. \b works perfectly here because _ is considered a "word" character in regex.
	// This prevents accidentally matching snake_case_variables.
	italicUnderscoreRegex = regexp.MustCompile(`\b_([^_]+)_\b`)

	// Strikethrough: ~~text~~
	strikethroughRegex = regexp.MustCompile(`(?s)~~(.*?)~~`)

	// Links: [text](url)
	linkRegex = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)

	// Headers: # Header text
	headerRegex = regexp.MustCompile(`(?m)^#+\s+(.*?)$`)
)

// LLMMarkdownToHTML safely converts Standard Markdown from an LLM into Telegram-compatible HTML.
func LLMMarkdownToHTML(text string) string {
	// 1. Telegram HTML requires <, >, and & to be escaped.
	text = html.EscapeString(text)

	// 2. Extract and protect code blocks
	codeBlocks := make(map[string]string)
	blockCounter := 0

	text = codeBlockRegex.ReplaceAllStringFunc(text, func(match string) string {
		submatches := codeBlockRegex.FindStringSubmatch(match)
		language := submatches[1]
		code := submatches[2]

		placeholder := fmt.Sprintf("@@CODE_BLOCK_%d@@", blockCounter)

		if language != "" {
			codeBlocks[placeholder] = fmt.Sprintf(`<pre><code class="language-%s">%s</code></pre>`, language, code)
		} else {
			codeBlocks[placeholder] = fmt.Sprintf(`<pre><code>%s</code></pre>`, code)
		}

		blockCounter++
		return placeholder
	})

	text = inlineCodeRegex.ReplaceAllStringFunc(text, func(match string) string {
		submatches := inlineCodeRegex.FindStringSubmatch(match)
		placeholder := fmt.Sprintf("@@INLINE_BLOCK_%d@@", blockCounter)
		codeBlocks[placeholder] = fmt.Sprintf(`<code>%s</code>`, submatches[1])
		blockCounter++
		return placeholder
	})

	// 3. Apply standard formatting
	text = boldRegex.ReplaceAllString(text, `<b>$1</b>`)
	text = italicAsteriskRegex.ReplaceAllString(text, `<i>$1</i>`)
	text = italicUnderscoreRegex.ReplaceAllString(text, `<i>$1</i>`)
	text = strikethroughRegex.ReplaceAllString(text, `<s>$1</s>`)
	text = linkRegex.ReplaceAllString(text, `<a href="$2">$1</a>`)
	text = headerRegex.ReplaceAllString(text, `<b>$1</b>`)

	// 4. Restore the protected code blocks
	for placeholder, htmlCode := range codeBlocks {
		text = strings.Replace(text, placeholder, htmlCode, 1)
	}

	return text
}
