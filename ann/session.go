package main

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/levmv/golems/pkg/llm"
)

const CompactionThreshold = 30

type SessionType string

const (
	SessionTypePrivate SessionType = "private"
	SessionTypeGroup   SessionType = "group"
)

type ReplyInfo struct {
	Name string `json:"name"`
	Text string `json:"text,omitempty"`
}

type Message struct {
	Role      llm.Role   `json:"role"`
	Content   string     `json:"content"`
	Name      string     `json:"name,omitempty"`
	Timestamp time.Time  `json:"timestamp,omitempty"`
	ReplyTo   *ReplyInfo `json:"reply_to,omitempty"`
}

// SessionKey represents a unique routing address for a conversation.
type SessionKey struct {
	Platform string
	Type     SessionType
	ChatID   string
}

// String generates a safe identifier for storage filenames or logging.
// e.g., "tg_group_123456789"
func (k SessionKey) String() string {
	return fmt.Sprintf("%s_%s_%s", k.Platform, k.Type, k.ChatID)
}

// SessionRegistry caches active sessions in RAM and falls back to Storage on cache-miss.
// Note: Sessions are intentionally never evicted from RAM. This is designed for small scale
type SessionRegistry struct {
	storage  *Storage
	llm      *llm.Model
	sessions sync.Map // map[string]*Session
}

func NewSessionRegistry(storage *Storage, llmClient *llm.Model) *SessionRegistry {
	return &SessionRegistry{
		storage: storage,
		llm:     llmClient,
	}
}

// GetSession returns a session from cache or bootstraps it from storage.
func (r *SessionRegistry) GetSession(key SessionKey) (*Session, error) {
	if val, ok := r.sessions.Load(key); ok {
		return val.(*Session), nil
	}

	msgs, err := r.storage.GetActiveContext(key)
	if err != nil {
		return nil, fmt.Errorf("failed to load session context: %w", err)
	}

	session := &Session{
		key:        key,
		storage:    r.storage,
		llm:        r.llm,
		summary:    r.storage.GetSummary(key),
		messages:   msgs,
		messageSeq: int64(len(msgs)),
	}

	actual, _ := r.sessions.LoadOrStore(key, session)
	return actual.(*Session), nil
}

// GetGroupSessions returns all active group sessions.
func (r *SessionRegistry) GetGroupSessions() []*Session {
	var groups []*Session
	r.sessions.Range(func(key, value any) bool {
		s := value.(*Session)
		if s.key.Type == SessionTypeGroup {
			groups = append(groups, s)
		}
		return true
	})
	return groups
}

// Session models the thread-safe conversation state for a single chat thread.
type Session struct {
	key          SessionKey
	mu           sync.Mutex
	storage      *Storage
	llm          *llm.Model
	summary      string
	messages     []Message
	messageSeq   int64
	judgeSeq     int64
	isCompacting bool
}

// AddMessage appends a message to RAM/disk and returns true if compaction is needed.
func (s *Session) AddMessage(msg Message) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.messageSeq++
	s.messages = append(s.messages, msg)

	if err := s.storage.AppendMessage(s.key, msg); err != nil {
		return false, fmt.Errorf("failed to save message to disk: %w", err)
	}

	needsCompaction := len(s.messages) >= CompactionThreshold && !s.isCompacting
	return needsCompaction, nil
}

type CompactorFunc func(convoText, currentSoul string) (newSummary, newSoul string, err error)

// DoCompaction safely extracts state, runs the slow LLM function without blocking the session,
// and commits the results. It returns the new soul string if it was updated.
func (s *Session) DoCompaction(compactor CompactorFunc) (string, error) {
	s.mu.Lock()

	if len(s.messages) < CompactionThreshold || s.isCompacting {
		s.mu.Unlock()
		return "", nil
	}

	s.isCompacting = true
	checkpointIdx := max(0, len(s.messages)-2)

	var sb strings.Builder
	for _, msg := range s.messages[:checkpointIdx] {
		sb.WriteString(msg.AsLogLine() + "\n")
	}
	convoText := sb.String()
	currentSoul := s.storage.GetSoul()

	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.isCompacting = false
		s.mu.Unlock()
	}()

	newSummary, newSoul, err := compactor(convoText, currentSoul)
	if err != nil {
		return "", fmt.Errorf("compaction failed: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if newSummary != "" {
		s.summary += "\n- " + newSummary
		if len(s.summary) > 5000 {
			runes := []rune(s.summary)
			s.summary = "... [older memories truncated] ..." + string(runes[len(runes)-4950:])
		}

		s.messages = s.messages[checkpointIdx:]
		_ = s.storage.AppendSummary(s.key, newSummary)
		_ = s.storage.IncrementCheckpoint(s.key, checkpointIdx)
	}

	if newSoul != "" {
		_ = s.storage.SetSoul(newSoul)
	}

	return newSoul, nil
}

// GetPrompt constructs the final message array: system prompt + summary + active messages.
func (s *Session) GetPrompt(systemPrompt string) []llm.Message {
	s.mu.Lock()
	defer s.mu.Unlock()

	sysPrompt := systemPrompt

	if soul := s.storage.GetSoul(); soul != "" {
		sysPrompt += "\n\n--- Your persona ---\n" + soul
	}

	if s.summary != "" {
		sysPrompt += "\n\n--- Background Memory ---\n" + s.summary
	}

	if s.key.Type == SessionTypeGroup {
		sysPrompt += "\n\n--- Rules ---\n" +
			"You're participating in a group chat. Input messages formatted as [TIME] NAME: TEXT. But DO NOT use it yourself, do not add prefix at the beginning of messages!" +
			"Refer to users by name, keep time-aware context in mind (e.g., replies should feel timely). " +
			"Respond in a natural, conversational tone."
	}

	req := make([]llm.Message, 0, len(s.messages)+1)
	req = append(req, llm.Message{Role: llm.RoleSystem, Content: sysPrompt})

	var lastRole llm.Role
	var lastName string

	for _, msg := range s.messages {
		text := msg.Content

		// Hide the prefix from AI messages so it doesn't learn to type it
		if msg.Role == llm.RoleUser {
			text = msg.AsLogLine()
		}
		// For same author - merge messages together
		if len(req) > 1 && msg.Role == lastRole && msg.Name == lastName {
			req[len(req)-1].Content += "\n" + text
		} else {
			req = append(req, llm.Message{Role: llm.Role(msg.Role), Content: text})
			lastRole = msg.Role
			lastName = msg.Name
		}
	}

	return req
}

// AsLogLine formats any message (User or AI) with its time, name, and reply context.
func (m *Message) AsLogLine() string {
	name := m.Name
	if name == "" {
		if m.Role == llm.RoleUser {
			name = "unknown"
		} else {
			name = string(m.Role) // Fallback for AI/System
		}
	}

	if m.ReplyTo != nil {
		if m.ReplyTo.Text != "" {
			quote := strings.ReplaceAll(m.ReplyTo.Text, "\n", " ")
			runes := []rune(quote)
			if len(runes) > 40 {
				quote = string(runes[:37]) + "..."
			}
			name = fmt.Sprintf("%s (replying to %s: %q)", name, m.ReplyTo.Name, quote)
		} else {
			name = fmt.Sprintf("%s (replying to %s)", name, m.ReplyTo.Name)
		}
	}

	timeStr := m.Timestamp.Format("15:04")
	return fmt.Sprintf("[%s] %s: %s", timeStr, name, m.Content)
}

func (s *Session) RecentLogLines(limit int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 || len(s.messages) == 0 {
		return nil
	}

	start := max(0, len(s.messages)-limit)
	lines := make([]string, 0, len(s.messages)-start)
	for _, msg := range s.messages[start:] {
		lines = append(lines, msg.AsLogLine())
	}
	return lines
}

func (s *Session) ClaimProactiveReplyCandidate(baseChance float32) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.key.Type != SessionTypeGroup {
		return false
	}

	if len(s.messages) == 0 {
		return false
	}

	if s.judgeSeq == s.messageSeq {
		return false
	}

	// Count messages since the last AI reply
	msgsSinceAI := 0
	for i := len(s.messages) - 1; i >= 0; i-- {
		if s.messages[i].Role == llm.RoleAI {
			break
		}
		msgsSinceAI++
	}

	lastMsg := s.messages[len(s.messages)-1]

	// Don't reply if the bot was the last one to speak (prevents monologues)
	if lastMsg.Role == llm.RoleAI {
		return false
	}

	timeSinceLast := time.Since(lastMsg.Timestamp)

	// Too early: Give meatbags a chance to type their next message or reply to each other
	if timeSinceLast < 30*time.Second {
		return false
	}

	// Too late: The conversation is dead, don't awkwardly wake it up
	if timeSinceLast > 4*time.Minute {
		return false
	}

	multiplier := float32(1.0)

	// Direct Replies: If users are directly replying to each other, back off.
	if lastMsg.ReplyTo != nil {
		multiplier *= 0.2
	}

	// If users have been talking a lot without the bot, increase the chance to chime in
	if msgsSinceAI > 6 {
		multiplier *= 2
	} else if msgsSinceAI > 3 {
		multiplier *= 1.2
	}

	// Hanging Questions: If a question is left hanging for >30 seconds,
	// it's a great time for the bot to offer an answer or opinion!
	lastText := strings.TrimSpace(lastMsg.Content)
	if strings.HasSuffix(lastText, "?") {
		multiplier *= 2
	}

	finalChance := baseChance * multiplier
	if finalChance > 1.0 {
		finalChance = 1.0
	}
	Log.Debug("[Session] Evaluating proactive reply for %s. msgsSinceAI=%d, chance=%.2f", s.key, msgsSinceAI, finalChance)

	if rand.Float32() >= finalChance {
		return false
	}

	s.judgeSeq = s.messageSeq
	return true
}
