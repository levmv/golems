package extract

import (
	"html"
	"regexp"
	"strings"
)

type Result struct {
	Title string
	Text  string
}

type Regex struct{}

func (Regex) Extract(raw, contentType string) Result {
	return ReadableText(raw, contentType)
}

var (
	titleRe      = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	ogTitleRe    = regexp.MustCompile(`(?is)<meta[^>]+(?:property|name)\s*=\s*["'](?:og:title|twitter:title)["'][^>]+content\s*=\s*["']([^"']+)["'][^>]*>`)
	ogTitleReAlt = regexp.MustCompile(`(?is)<meta[^>]+content\s*=\s*["']([^"']+)["'][^>]+(?:property|name)\s*=\s*["'](?:og:title|twitter:title)["'][^>]*>`)
	articleRe    = regexp.MustCompile(`(?is)<article\b[^>]*>(.*?)</article>`)
	mainRe       = regexp.MustCompile(`(?is)<main\b[^>]*>(.*?)</main>`)
	bodyRe       = regexp.MustCompile(`(?is)<body\b[^>]*>(.*?)</body>`)
	commentRe    = regexp.MustCompile(`(?is)<!--.*?-->`)
	blockTagRe   = regexp.MustCompile(`(?is)</?(p|div|section|article|main|br|h[1-6]|li|ul|ol|blockquote|pre|table|tr|td|th|figure|figcaption)\b[^>]*>`)
	tagRe        = regexp.MustCompile(`(?is)<[^>]+>`)
	blankLineRe  = regexp.MustCompile(`[ \t]*\n[ \t]*\n[ \t\n]+`)
	horizontalRe = regexp.MustCompile(`[ \t\r\f\v]+`)
	lineSpaceRe  = regexp.MustCompile(`(?m)^[ \t]+|[ \t]+$`)
	dropBlockRes = []*regexp.Regexp{
		regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`),
		regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style>`),
		regexp.MustCompile(`(?is)<noscript\b[^>]*>.*?</noscript>`),
		regexp.MustCompile(`(?is)<svg\b[^>]*>.*?</svg>`),
		regexp.MustCompile(`(?is)<canvas\b[^>]*>.*?</canvas>`),
		regexp.MustCompile(`(?is)<iframe\b[^>]*>.*?</iframe>`),
		regexp.MustCompile(`(?is)<form\b[^>]*>.*?</form>`),
		regexp.MustCompile(`(?is)<header\b[^>]*>.*?</header>`),
		regexp.MustCompile(`(?is)<nav\b[^>]*>.*?</nav>`),
		regexp.MustCompile(`(?is)<footer\b[^>]*>.*?</footer>`),
		regexp.MustCompile(`(?is)<aside\b[^>]*>.*?</aside>`),
	}
)

func ReadableText(raw, contentType string) Result {
	if !strings.Contains(strings.ToLower(contentType), "html") && !looksLikeHTML(raw) {
		return Result{Text: normalizeText(raw)}
	}

	title := extractTitle(raw)
	chunk := largestMatch(articleRe, raw)
	if chunk == "" {
		chunk = largestMatch(mainRe, raw)
	}
	if chunk == "" {
		chunk = firstMatch(bodyRe, raw)
	}
	if chunk == "" {
		chunk = raw
	}

	chunk = commentRe.ReplaceAllString(chunk, " ")
	for _, re := range dropBlockRes {
		chunk = re.ReplaceAllString(chunk, " ")
	}
	chunk = blockTagRe.ReplaceAllString(chunk, "\n")
	chunk = tagRe.ReplaceAllString(chunk, " ")
	chunk = html.UnescapeString(chunk)
	return Result{Title: title, Text: normalizeText(chunk)}
}

func looksLikeHTML(s string) bool {
	head := strings.ToLower(s)
	if len(head) > 500 {
		head = head[:500]
	}
	return strings.Contains(head, "<html") ||
		strings.Contains(head, "<body") ||
		strings.Contains(head, "<article") ||
		strings.Contains(head, "<!doctype html")
}

func extractTitle(raw string) string {
	for _, re := range []*regexp.Regexp{ogTitleRe, ogTitleReAlt, titleRe} {
		if m := re.FindStringSubmatch(raw); len(m) > 1 {
			return normalizeTitle(html.UnescapeString(stripTags(m[1])))
		}
	}
	return ""
}

func normalizeTitle(s string) string {
	s = strings.ReplaceAll(s, "\u00a0", " ")
	s = horizontalRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func stripTags(s string) string {
	return tagRe.ReplaceAllString(s, "")
}

func largestMatch(re *regexp.Regexp, s string) string {
	matches := re.FindAllStringSubmatch(s, -1)
	var best string
	for _, m := range matches {
		if len(m) > 1 && len(m[1]) > len(best) {
			best = m[1]
		}
	}
	return best
}

func firstMatch(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); len(m) > 1 {
		return m[1]
	}
	return ""
}

func normalizeText(s string) string {
	s = strings.ReplaceAll(s, "\u00a0", " ")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = html.UnescapeString(s)
	s = horizontalRe.ReplaceAllString(s, " ")
	s = lineSpaceRe.ReplaceAllString(s, "")
	s = blankLineRe.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
