package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type Storage struct {
	basePath string
	soulMu   sync.RWMutex
}

func NewStorage(basePath string) *Storage {
	os.MkdirAll(basePath, 0755)
	return &Storage{basePath: basePath}
}

func (s *Storage) GetSoul() string {
	s.soulMu.RLock()
	defer s.soulMu.RUnlock()

	path := filepath.Join(s.basePath, "soul.md")
	data, err := os.ReadFile(path)
	content := strings.TrimSpace(string(data))

	if err != nil || content == "" {
		return "You are concise."
	}
	return content
}

// SetSoul overwrites the persona file with new evolved traits.
func (s *Storage) SetSoul(newSoul string) error {
	s.soulMu.Lock()
	defer s.soulMu.Unlock()

	path := filepath.Join(s.basePath, "soul.md")
	tempPath := path + ".tmp"

	if err := os.WriteFile(tempPath, []byte(newSoul), 0644); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

// AppendMessage appends a single JSON object line to the session log.
func (s *Storage) AppendMessage(key SessionKey, msg Message) error {
	path := filepath.Join(s.basePath, key.String()+"_messages.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	data, _ := json.Marshal(msg)
	_, err = f.Write(append(data, '\n'))
	return err
}

// GetActiveContext reads the messages log, skipping lines that have already been compacted.
func (s *Storage) GetActiveContext(key SessionKey) ([]Message, error) {
	offset := s.getCheckpoint(key)
	path := filepath.Join(s.basePath, key.String()+"_messages.jsonl")

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var msgs []Message
	scanner := bufio.NewScanner(f)

	for lineNum := 1; scanner.Scan(); lineNum++ {
		if lineNum <= offset {
			continue
		}

		var m Message
		if err := json.Unmarshal(scanner.Bytes(), &m); err == nil {
			msgs = append(msgs, m)
		} else {
			Log.Warn("GetActiveContext: skipped corrupted line %d in %s: %v", lineNum+1, key, err)
		}
	}
	return msgs, scanner.Err()
}

// IncrementCheckpoint advances the read offset for subsequent cold loads.
func (s *Storage) IncrementCheckpoint(key SessionKey, added int) error {
	current := s.getCheckpoint(key)
	path := filepath.Join(s.basePath, key.String()+"_checkpoint.txt")
	return os.WriteFile(path, []byte(strconv.Itoa(current+added)), 0644)
}

func (s *Storage) getCheckpoint(key SessionKey) int {
	path := filepath.Join(s.basePath, key.String()+"_checkpoint.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	val, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return val
}

func (s *Storage) GetSummary(key SessionKey) string {
	path := filepath.Join(s.basePath, key.String()+"_summary.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func (s *Storage) AppendSummary(key SessionKey, newSummary string) error {
	path := filepath.Join(s.basePath, key.String()+"_summary.txt")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.WriteString("- " + strings.TrimSpace(newSummary) + "\n")
	return err
}
