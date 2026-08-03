package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/levmv/golems/brevity/internal/source"
)

const telegraphAPIBase = "https://api.telegra.ph"

var ErrTelegraphDisabled = errors.New("telegraph publishing is disabled")

type TelegraphClient struct {
	accessToken string
	authorName  string
	authorURL   string
	httpClient  *http.Client
}

type TelegraphAccount struct {
	ShortName   string `json:"short_name"`
	AuthorName  string `json:"author_name,omitempty"`
	AuthorURL   string `json:"author_url,omitempty"`
	AccessToken string `json:"access_token,omitempty"`
	AuthURL     string `json:"auth_url,omitempty"`
}

type telegraphResponse[T any] struct {
	OK     bool   `json:"ok"`
	Result T      `json:"result"`
	Error  string `json:"error,omitempty"`
}

type telegraphPage struct {
	Path string `json:"path"`
	URL  string `json:"url"`
}

type telegraphNode struct {
	Tag      string          `json:"tag,omitempty"`
	Attrs    map[string]any  `json:"attrs,omitempty"`
	Children []telegraphNode `json:"children,omitempty"`
	Text     string          `json:"-"`
}

func NewTelegraphClient(accessToken, authorName, authorURL string, timeout time.Duration) *TelegraphClient {
	return &TelegraphClient{
		accessToken: accessToken,
		authorName:  authorName,
		authorURL:   authorURL,
		httpClient:  &http.Client{Timeout: timeout},
	}
}

func (c *TelegraphClient) CreateAccount(ctx context.Context, shortName string) (TelegraphAccount, error) {
	if strings.TrimSpace(shortName) == "" {
		shortName = "Brevity"
	}

	values := url.Values{}
	values.Set("short_name", shortName)
	if c.authorName != "" {
		values.Set("author_name", c.authorName)
	}
	if c.authorURL != "" {
		values.Set("author_url", c.authorURL)
	}

	var resp telegraphResponse[TelegraphAccount]
	if err := c.postForm(ctx, "/createAccount", values, &resp); err != nil {
		return TelegraphAccount{}, err
	}
	if !resp.OK {
		return TelegraphAccount{}, fmt.Errorf("telegraph error: %s", resp.Error)
	}
	return resp.Result, nil
}

func (c *TelegraphClient) Publish(ctx context.Context, source source.Document, summary Summary) (PublishedPage, error) {
	if strings.TrimSpace(c.accessToken) == "" {
		return PublishedPage{}, ErrTelegraphDisabled
	}

	title := telegraphTitle(summary.Title)
	content, err := json.Marshal(telegraphContent(source, summary))
	if err != nil {
		return PublishedPage{}, err
	}
	if len(content) > 64*1024 {
		return PublishedPage{}, fmt.Errorf("telegraph content is too large: %d bytes", len(content))
	}

	values := url.Values{}
	values.Set("access_token", c.accessToken)
	values.Set("title", title)
	values.Set("content", string(content))
	if c.authorName != "" {
		values.Set("author_name", c.authorName)
	}
	if c.authorURL != "" {
		values.Set("author_url", c.authorURL)
	}

	var resp telegraphResponse[telegraphPage]
	if err := c.postForm(ctx, "/createPage", values, &resp); err != nil {
		return PublishedPage{}, err
	}
	if !resp.OK {
		return PublishedPage{}, fmt.Errorf("telegraph error: %s", resp.Error)
	}
	return PublishedPage{URL: resp.Result.URL, Path: resp.Result.Path}, nil
}

func (c *TelegraphClient) postForm(ctx context.Context, path string, values url.Values, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, telegraphAPIBase+path, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "BrevityBot/0.1")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("telegraph HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if err = json.Unmarshal(body, result); err != nil {
		return err
	}
	return nil
}

func telegraphContent(source source.Document, summary Summary) []telegraphNode {
	nodes := markdownishToTelegraph(summary.FullSummary)
	nodes = append(nodes,
		paragraph("Источник"),
		linkParagraph("Открыть оригинал", source.FinalURL),
	)
	return nodes
}

func telegraphTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Summary"
	}
	runes := []rune(title)
	if len(runes) > 256 {
		title = string(runes[:256])
	}
	return title
}

func markdownishToTelegraph(text string) []telegraphNode {
	var nodes []telegraphNode
	var listItems []telegraphNode
	var para bytes.Buffer

	flushParagraph := func() {
		s := strings.TrimSpace(para.String())
		para.Reset()
		if s != "" {
			nodes = append(nodes, paragraph(s))
		}
	}
	flushList := func() {
		if len(listItems) == 0 {
			return
		}
		nodes = append(nodes, telegraphNode{Tag: "ul", Children: listItems})
		listItems = nil
	}

	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			flushParagraph()
			flushList()
		case strings.HasPrefix(line, "### "):
			flushParagraph()
			flushList()
			nodes = append(nodes, heading("h4", strings.TrimSpace(strings.TrimPrefix(line, "### "))))
		case strings.HasPrefix(line, "## "):
			flushParagraph()
			flushList()
			nodes = append(nodes, heading("h3", strings.TrimSpace(strings.TrimPrefix(line, "## "))))
		case strings.HasPrefix(line, "# "):
			flushParagraph()
			flushList()
			nodes = append(nodes, heading("h3", strings.TrimSpace(strings.TrimPrefix(line, "# "))))
		case strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* "):
			flushParagraph()
			item := strings.TrimSpace(line[2:])
			if item != "" {
				listItems = append(listItems, telegraphNode{Tag: "li", Children: textChildren(item)})
			}
		default:
			flushList()
			if para.Len() > 0 {
				para.WriteByte(' ')
			}
			para.WriteString(line)
		}
	}

	flushParagraph()
	flushList()
	return nodes
}

func paragraph(text string) telegraphNode {
	return telegraphNode{Tag: "p", Children: textChildren(text)}
}

func heading(tag, text string) telegraphNode {
	return telegraphNode{Tag: tag, Children: textChildren(text)}
}

func linkParagraph(label, href string) telegraphNode {
	return telegraphNode{
		Tag: "p",
		Children: []telegraphNode{{
			Tag:   "a",
			Attrs: map[string]any{"href": href},
			Children: []telegraphNode{{
				Text: label,
			}},
		}},
	}
}

func textChildren(text string) []telegraphNode {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	return []telegraphNode{{Text: text}}
}

func (n telegraphNode) MarshalJSON() ([]byte, error) {
	if n.Text != "" || n.Tag == "" {
		return json.Marshal(n.Text)
	}
	type node telegraphNode
	return json.Marshal(node(n))
}
