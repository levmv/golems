package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Summary struct {
	ID        string
	Title     string
	UpdatedAt time.Time
}

const maxSessionRecordBytes = 10 * 1024 * 1024

// List reads only enough of each journal to find its workspace and first user
// prompt. File modification time supplies the picker timestamp; replay is
// reserved for the session the user actually opens.
func List(home, currentWorkspace string) ([]Summary, error) {
	home, err := resolveHome(home)
	if err != nil {
		return nil, err
	}
	sessionsDir := filepath.Join(home, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read sessions: %w", err)
	}
	var summaries []Summary
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(sessionsDir, entry.Name(), journalName)
		summary, include := summarizeJournal(entry.Name(), path, currentWorkspace)
		if include {
			summaries = append(summaries, summary)
		}
	}
	sort.SliceStable(summaries, func(i, j int) bool {
		if !summaries[i].UpdatedAt.Equal(summaries[j].UpdatedAt) {
			return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt)
		}
		return summaries[i].ID < summaries[j].ID
	})
	return summaries, nil
}

func summarizeJournal(id, path, currentWorkspace string) (Summary, bool) {
	summary := Summary{ID: id}
	if info, err := os.Stat(path); err == nil {
		summary.UpdatedAt = info.ModTime()
	}
	file, err := os.Open(path)
	if err != nil {
		return summary, false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxSessionRecordBytes)
	if !scanner.Scan() {
		return summary, false
	}
	var headerRecord Record
	if err := json.Unmarshal(scanner.Bytes(), &headerRecord); err != nil || headerRecord.Type != RecordSessionStarted {
		return summary, false
	}
	header, err := DecodePayload[SessionStarted](headerRecord)
	if err != nil || header.Workspace != currentWorkspace {
		return summary, false
	}

	for scanner.Scan() {
		var record Record
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			break
		}
		if record.Type != RecordUserMessage {
			continue
		}
		message, err := DecodePayload[UserMessage](record)
		if err == nil {
			summary.Title = titleFromContent(message.Content)
		}
		break
	}
	return summary, true
}

func titleFromContent(content string) string {
	const maxTitleRunes = 72
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	for _, line := range strings.Split(normalized, "\n") {
		title := strings.Join(strings.Fields(line), " ")
		if title == "" {
			continue
		}
		runes := []rune(title)
		if len(runes) > maxTitleRunes {
			title = string(runes[:maxTitleRunes-1]) + "…"
		}
		return title
	}
	return ""
}
