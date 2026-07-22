package hackernews

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

func cleanHTML(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	document, err := html.Parse(strings.NewReader("<html><body>" + value + "</body></html>"))
	if err != nil {
		return cleanOneLine(value)
	}
	var out strings.Builder
	var walk func(*html.Node, bool)
	walk = func(node *html.Node, skipped bool) {
		if node.Type == html.ElementNode {
			switch node.Data {
			case "script", "style", "noscript", "svg", "canvas":
				skipped = true
			case "br", "p", "pre", "li", "blockquote":
				writeBreak(&out)
			}
		}
		if node.Type == html.TextNode && !skipped {
			out.WriteString(node.Data)
			out.WriteByte(' ')
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child, skipped)
		}
		if node.Type == html.ElementNode {
			switch node.Data {
			case "p", "pre", "li", "blockquote":
				writeBreak(&out)
			}
		}
	}
	walk(document, false)
	return cleanMultiline(out.String())
}

func writeBreak(out *strings.Builder) {
	if out.Len() > 0 {
		out.WriteByte('\n')
	}
}

func cleanMultiline(value string) string {
	value = sanitizeText(strings.ReplaceAll(value, "\r\n", "\n"))
	lines := strings.Split(value, "\n")
	cleaned := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			if len(cleaned) > 0 && !blank {
				cleaned = append(cleaned, "")
				blank = true
			}
			continue
		}
		cleaned = append(cleaned, line)
		blank = false
	}
	return strings.TrimSpace(strings.Join(cleaned, "\n"))
}

func cleanOneLine(value string) string {
	return strings.Join(strings.Fields(sanitizeText(value)), " ")
}

func sanitizeText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, value)
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return strings.TrimSpace(string(runes[:limit])) + "\n[…truncated…]"
}

func truncateBytes(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.ValidString(value[:cut]) {
		cut--
	}
	return value[:cut] + "\n[…truncated…]"
}

func cleanArticleURL(raw string) string {
	raw = cleanOneLine(raw)
	target, err := url.Parse(raw)
	if err != nil || target.Hostname() == "" || target.User != nil || target.Scheme != "http" && target.Scheme != "https" {
		return ""
	}
	return target.String()
}
