package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/levmv/golems/brevity/internal/source"
	"github.com/levmv/golems/pkg/llm"
)

const summarizerSystemPrompt = `Ты Brevity, аккуратный редактор-суммаризатор.

Твоя задача: по тексту веб-страницы сделать две русскоязычные версии:
1. Короткое саммари для Telegram-поста, до 1800 символов.
2. Подробное саммари на 1-1.5 страницы, примерно 5000-8000 символов, если исходный материал достаточно содержательный.

Правила:
- Пиши только по содержанию источника. Не добавляй факты извне.
- Если источник спорный, рекламный или неполный, обозначай это спокойно и явно.
- Сохраняй важные цифры, имена, даты, причинно-следственные связи и ограничения.
- Не пересказывай технический мусор страницы, меню, cookie banners и навигацию.
- Если документ содержит секцию "Hacker News discussion", отдельно отрази, что добавляет обсуждение: консенсус, сильные возражения, уточнения и практические выводы.
- Не используй Markdown code fence.
- Соблюдай формат ниже буквально. Не возвращай JSON.

Формат ответа:
<<<TITLE>>>
короткий понятный заголовок на русском
<<<SHORT>>>
короткая версия
<<<FULL>>>
подробная версия
<<<END>>>`

type LLMSummarizer struct {
	model llm.Model
}

func NewLLMSummarizer(model llm.Model) *LLMSummarizer {
	return &LLMSummarizer{model: model}
}

func (s *LLMSummarizer) Summarize(ctx context.Context, source source.Document) (Summary, error) {
	userPrompt := fmt.Sprintf(`URL: %s
Заголовок страницы: %s

Текст страницы:
%s`, source.FinalURL, emptyDash(source.Title), source.Text)

	resp, err := s.model.Chat(ctx, llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: summarizerSystemPrompt},
			{Role: llm.RoleUser, Content: userPrompt},
		},
	})
	if err != nil {
		return Summary{}, err
	}

	summary, err := decodeSummary(resp.Content)
	if err != nil {
		return Summary{}, fmt.Errorf("decode model summary: %w; raw response: %q", err, truncateForError(resp.Content, 500))
	}
	if strings.TrimSpace(summary.ShortSummary) == "" || strings.TrimSpace(summary.FullSummary) == "" {
		return Summary{}, fmt.Errorf("model returned an empty summary")
	}
	return summary, nil
}

func decodeSummary(raw string) (Summary, error) {
	raw = strings.TrimSpace(raw)
	if summary, ok := decodeMarkedSummary(raw); ok {
		return summary, nil
	}
	if summary, ok := decodeJSONSummary(raw); ok {
		return summary, nil
	}
	if summary, ok := decodeLabeledSummary(raw); ok {
		return summary, nil
	}
	return Summary{}, fmt.Errorf("unknown summary format")
}

func decodeMarkedSummary(raw string) (Summary, bool) {
	title, okTitle := markerBlock(raw, "<<<TITLE>>>", "<<<SHORT>>>")
	short, okShort := markerBlock(raw, "<<<SHORT>>>", "<<<FULL>>>")
	full, okFull := markerBlock(raw, "<<<FULL>>>", "<<<END>>>")
	if !okFull {
		full, okFull = markerTail(raw, "<<<FULL>>>")
	}
	if !okTitle || !okShort || !okFull {
		return Summary{}, false
	}
	return Summary{
		Title:        strings.TrimSpace(title),
		ShortSummary: strings.TrimSpace(short),
		FullSummary:  strings.TrimSpace(full),
	}, true
}

func markerBlock(raw, startMarker, endMarker string) (string, bool) {
	start := strings.Index(raw, startMarker)
	if start == -1 {
		return "", false
	}
	start += len(startMarker)
	end := strings.Index(raw[start:], endMarker)
	if end == -1 {
		return "", false
	}
	return raw[start : start+end], true
}

func markerTail(raw, startMarker string) (string, bool) {
	start := strings.Index(raw, startMarker)
	if start == -1 {
		return "", false
	}
	return raw[start+len(startMarker):], true
}

func decodeJSONSummary(raw string) (Summary, bool) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var summary Summary
	if err := json.Unmarshal([]byte(raw), &summary); err == nil {
		return summary, true
	}

	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(raw[start:end+1]), &summary); err == nil {
			return summary, true
		}
	}
	return Summary{}, false
}

func decodeLabeledSummary(raw string) (Summary, bool) {
	labels := []string{"TITLE:", "SHORT:", "FULL:"}
	upper := strings.ToUpper(raw)
	positions := make([]int, len(labels))
	for i, label := range labels {
		positions[i] = strings.Index(upper, label)
		if positions[i] == -1 {
			return Summary{}, false
		}
	}
	if !(positions[0] < positions[1] && positions[1] < positions[2]) {
		return Summary{}, false
	}

	titleStart := positions[0] + len(labels[0])
	shortStart := positions[1] + len(labels[1])
	fullStart := positions[2] + len(labels[2])
	return Summary{
		Title:        strings.TrimSpace(raw[titleStart:positions[1]]),
		ShortSummary: strings.TrimSpace(raw[shortStart:positions[2]]),
		FullSummary:  strings.TrimSpace(raw[fullStart:]),
	}, true
}

func emptyDash(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "-"
	}
	return s
}

func truncateForError(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "..."
}
