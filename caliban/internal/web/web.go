// Package web is the web transport: it exposes a caliban conversation to a
// murm-ui frontend over HTTP/SSE. Like the telegram transport, it talks only to
// the engine — submitting user messages and tailing run events — and renders
// those events to murm-ui's wire format via pkg/murm.
//
// caliban is already backend-authoritative (the engine owns durable runs), so
// this transport does not use pkg/murm's standalone run handler. It reuses the
// reusable pieces: the wire types, the Translator, and the SSE writer.
package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/levmv/golems/caliban/internal/engine"
	"github.com/levmv/golems/caliban/internal/store"
	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/llm"
	"github.com/levmv/golems/pkg/murm"
)

const (
	defaultConversationID       = 2
	defaultChatTailLimit        = 200
	maxChatTailLimit            = 500
	maxOlderChatPageTargetLimit = 80
	eventScopeRuns              = "runs"
	eventScopeMessages          = "messages"
	eventBuffer                 = 256
	keepaliveInterval           = 15 * time.Second
	maxSubmitBodyBytes          = 1 << 20
)

//go:embed static/*
var staticFiles embed.FS

// Engine is the slice of the engine the web transport needs. *engine.Engine
// satisfies it.
type Engine interface {
	SubmitUserMessage(ctx context.Context, conversationID int64, text, source string) error
	Subscribe(fn func(engine.Event)) (cancel func())
}

// Store is the transcript read surface the PWA needs.
type Store interface {
	Conversation(ctx context.Context, id int64) (store.Conversation, error)
	ConversationByUUID(ctx context.Context, uuid string) (store.Conversation, bool, error)
	MessagesAfter(ctx context.Context, conversationID, afterID int64) ([]store.Message, error)
	MessagesTail(ctx context.Context, conversationID int64, limit int) ([]store.Message, bool, error)
	MessagesBefore(ctx context.Context, conversationID, beforeID int64, limit int) ([]store.Message, bool, error)
	LastMessage(ctx context.Context, conversationID int64) (store.Message, bool, error)
	UpsertPushSubscription(ctx context.Context, ps store.PushSubscription) error
	PushSubscriptions(ctx context.Context, conversationID int64) ([]store.PushSubscription, error)
	DeletePushSubscription(ctx context.Context, endpoint string) (bool, error)
}

type inputRunLookup interface {
	RunIDsByInputMessage(ctx context.Context, conversationID int64, inputMessageIDs []int64) (map[int64]int64, error)
}

// Logger is the minimal logging surface; nil disables logging.
type Logger interface {
	Error(format string, args ...any)
}

// Config wires the transport.
type Config struct {
	Engine         Engine
	Store          Store
	ConversationID int64 // default 2 (web transport)
	Auth           AuthConfig
	Push           PushConfig
	Logger         Logger
}

// Transport serves the web HTTP API.
type Transport struct {
	engine Engine
	store  Store
	convID int64
	auth   AuthConfig
	push   PushConfig
	log    Logger

	sendPush     pushSender
	loginLimiter loginLimiter
}

func New(cfg Config) *Transport {
	conv := cfg.ConversationID
	if conv == 0 {
		conv = defaultConversationID
	}
	return &Transport{
		engine:   cfg.Engine,
		store:    cfg.Store,
		convID:   conv,
		auth:     cfg.Auth.withDefaults(),
		push:     cfg.Push,
		log:      cfg.Logger,
		sendPush: defaultPushSender(cfg.Push),
	}
}

// Handler returns the HTTP routes. Chat ids are web-facing UUIDs; direct numeric
// store ids are not accepted when storage is enabled.
func (t *Transport) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/chats", t.listChats)
	mux.HandleFunc("POST /api/auth/login", t.login)
	mux.HandleFunc("POST /api/auth/logout", t.logout)
	mux.HandleFunc("GET /api/chats/{chatId}", t.getChat)
	mux.HandleFunc("PUT /api/chats/{chatId}", t.saveChat)
	mux.HandleFunc("POST /api/chats/{chatId}/meta", t.updateChatMeta)
	mux.HandleFunc("DELETE /api/chats/{chatId}", t.deleteChat)
	mux.HandleFunc("POST /api/chats/{chatId}/runs", t.submit)
	mux.HandleFunc("GET /api/chats/{chatId}/events", t.events)
	mux.HandleFunc("GET /api/push/config", t.pushConfig)
	mux.HandleFunc("POST /api/chats/{chatId}/push-subscriptions", t.savePushSubscription)
	mux.HandleFunc("DELETE /api/chats/{chatId}/push-subscriptions", t.deletePushSubscription)
	mux.HandleFunc("GET /login", t.loginPage)
	mux.Handle("GET /", spaHandler())
	return t.authMiddleware(mux)
}

func (t *Transport) listChats(w http.ResponseWriter, r *http.Request) {
	if t.store == nil {
		writeError(w, http.StatusNotFound, "chat storage is not enabled")
		return
	}
	conv, err := t.store.Conversation(r.Context(), t.convID)
	if err != nil {
		t.logf("web: list chats: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load chats")
		return
	}
	chat, err := t.chatMeta(r.Context(), conv.UUID, conv.ID)
	if err != nil {
		t.logf("web: list chats: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load chats")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":   []murmChatMeta{{ID: chat.ID, Title: chat.Title, UpdatedAt: chat.UpdatedAt}},
		"hasMore": false,
	})
}

func (t *Transport) getChat(w http.ResponseWriter, r *http.Request) {
	if t.store == nil {
		writeError(w, http.StatusNotFound, "chat storage is not enabled")
		return
	}
	chatID := r.PathValue("chatId")
	resolved, ok := t.resolveChat(r.Context(), chatID)
	if !ok {
		writeError(w, http.StatusNotFound, "chat not found")
		return
	}
	limit, err := chatMessageLimit(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// ?before=<id> requests an older page for upward pagination ("load more" at
	// the top), instead of the latest tail. The full-session shape is unchanged.
	if before, ok, err := chatBeforeCursor(r); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	} else if ok {
		if limit > maxOlderChatPageTargetLimit {
			limit = maxOlderChatPageTargetLimit
		}
		page, err := t.chatPage(r.Context(), resolved.ConversationID, before, limit)
		if err != nil {
			t.logf("web: get chat page: %v", err)
			writeError(w, http.StatusInternalServerError, "could not load chat")
			return
		}
		writeJSON(w, http.StatusOK, page)
		return
	}
	chat, err := t.chat(r.Context(), resolved.ID, resolved.ConversationID, limit)
	if err != nil {
		t.logf("web: get chat: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load chat")
		return
	}
	writeJSON(w, http.StatusOK, chat)
}

// saveChat/updateChatMeta/deleteChat are intentionally no-ops for now. The
// engine is authoritative; these endpoints exist so murm-ui's RemoteStorage can
// run against caliban without treating every local save/title update as a fatal
// backend error.
func (t *Transport) saveChat(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}
func (t *Transport) updateChatMeta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}
func (t *Transport) deleteChat(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// submit appends a user message and returns immediately; the run executes in the
// engine and its events arrive on the events stream.
func (t *Transport) submit(w http.ResponseWriter, r *http.Request) {
	resolved, ok := t.resolveChat(r.Context(), r.PathValue("chatId"))
	if !ok {
		writeError(w, http.StatusNotFound, "chat not found")
		return
	}
	var req murm.RunRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxSubmitBodyBytes)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Input) == "" {
		writeError(w, http.StatusBadRequest, "input is required")
		return
	}
	if err := t.engine.SubmitUserMessage(r.Context(), resolved.ConversationID, req.Input, "web"); err != nil {
		t.logf("web: submit: %v", err)
		writeError(w, http.StatusInternalServerError, "could not submit message")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
}

// events is a long-lived SSE stream owned by Caliban's web transport. pkg/murm
// supplies only the wire types and SSE writer here; the scope parameter is
// Caliban-specific. By default it tails transient run events translated to
// murm-ui events. With scope=messages it tails persisted transcript messages
// appended outside a run, such as fired reminders.
func (t *Transport) events(w http.ResponseWriter, r *http.Request) {
	resolved, ok := t.resolveChat(r.Context(), r.PathValue("chatId"))
	if !ok {
		writeError(w, http.StatusNotFound, "chat not found")
		return
	}
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = eventScopeRuns
	}
	if scope != eventScopeRuns && scope != eventScopeMessages {
		writeError(w, http.StatusBadRequest, "invalid event scope")
		return
	}
	sse, err := murm.NewSSEWriter(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	// engine.Subscribe must not block, so hand events to this goroutine via a
	// buffered channel. On overflow (a stalled client) we drop and rely on the
	// client to reconnect and reload — desync beats blocking the engine.
	events := make(chan engine.Event, eventBuffer)
	cancel := t.engine.Subscribe(func(ev engine.Event) {
		select {
		case events <- ev:
		default:
			t.logf("web: event buffer full, dropping event for run %d", ev.RunID)
		}
	})
	defer cancel()

	translators := make(map[int64]*murm.Translator)
	runAliases := make(map[int64]string)
	clientRunID := strings.TrimSpace(r.URL.Query().Get("client_run_id"))
	ticker := time.NewTicker(keepaliveInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-events:
			if ev.ConversationID != resolved.ConversationID {
				continue
			}
			if scope == eventScopeMessages {
				if ev.Message == nil {
					continue
				}
				inputRunID := int64(0)
				if ev.Message.Role == llm.RoleUser {
					inputRunID = ev.RunID
				}
				msg := toMurmMessage(*ev.Message, inputRunID)
				if err := sse.Send(murm.StreamEvent{Type: murm.EventMessageStart, Message: &msg}); err != nil {
					t.logf("web: send message event: %v", err)
				}
				continue
			}
			if ev.Message != nil {
				continue
			}
			tr, ok := translators[ev.RunID]
			if !ok {
				runID := strconv.FormatInt(ev.RunID, 10)
				// Best-effort alias for the optimistic run the browser submitted
				// after opening this stream. If another run wins this conversation
				// race, it may consume the alias; events do not carry submit
				// metadata yet, so the stream cannot disambiguate here.
				if clientRunID != "" && len(runAliases) == 0 {
					runID = clientRunID
				}
				runAliases[ev.RunID] = runID
				tr = murm.NewTranslator(runID, func(me murm.StreamEvent) {
					if err := sse.Send(me); err != nil {
						t.logf("web: send event: %v", err)
					}
				})
				translators[ev.RunID] = tr
			}
			tr.Feed(ev.Ev)
			if ev.Ev.Kind == golem.EventDone {
				delete(translators, ev.RunID)
			}
		case <-ticker.C:
			if err := sse.Keepalive(); err != nil {
				return
			}
		}
	}
}

type murmChatMeta struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	UpdatedAt int64  `json:"updatedAt"`
}

type murmChat struct {
	ID              string         `json:"id"`
	Title           string         `json:"title"`
	UpdatedAt       int64          `json:"updatedAt"`
	Messages        []murm.Message `json:"messages"`
	HasMoreMessages bool           `json:"hasMoreMessages,omitempty"`
	// NextOlderMessagesCursor is an opaque cursor for fetching the next older page
	// (?before=<cursor>), independent of Message.id. Set only when older rows
	// exist. Currently a numeric row id, but clients must treat it as opaque.
	NextOlderMessagesCursor string `json:"nextOlderMessagesCursor,omitempty"`
	MessageLimit            int    `json:"messageLimit,omitempty"`
}

func (t *Transport) chatMeta(ctx context.Context, id string, conversationID int64) (murmChatMeta, error) {
	updatedAt := time.Now().UnixMilli()
	last, ok, err := t.store.LastMessage(ctx, conversationID)
	if err != nil {
		return murmChatMeta{}, err
	}
	if ok {
		updatedAt = last.CreatedAt.UnixMilli()
	}
	return murmChatMeta{ID: id, Title: "Caliban", UpdatedAt: updatedAt}, nil
}

// murmMessagePage is the response for an upward-pagination request: a page of
// messages older than the requested cursor, whether even-older rows remain, and
// the opaque cursor for the next older page (set only when more remain).
type murmMessagePage struct {
	Messages                []murm.Message `json:"messages"`
	HasMore                 bool           `json:"hasMore"`
	NextOlderMessagesCursor string         `json:"nextOlderMessagesCursor,omitempty"`
}

func (t *Transport) chatPage(ctx context.Context, conversationID, beforeID int64, limit int) (murmMessagePage, error) {
	msgs, hasMore, err := t.store.MessagesBefore(ctx, conversationID, beforeID, limit)
	if err != nil {
		return murmMessagePage{}, err
	}
	out := make([]murm.Message, 0, len(msgs))
	inputRunIDs := t.inputRunIDs(ctx, conversationID, msgs)
	for _, m := range msgs {
		out = append(out, toMurmMessage(m, inputRunIDs[m.ID]))
	}
	page := murmMessagePage{Messages: out, HasMore: hasMore}
	if hasMore && len(msgs) > 0 {
		page.NextOlderMessagesCursor = strconv.FormatInt(msgs[0].ID, 10)
	}
	return page, nil
}

func (t *Transport) chat(ctx context.Context, id string, conversationID int64, limit int) (murmChat, error) {
	msgs, hasMore, err := t.store.MessagesTail(ctx, conversationID, limit)
	if err != nil {
		return murmChat{}, err
	}
	out := make([]murm.Message, 0, len(msgs))
	inputRunIDs := t.inputRunIDs(ctx, conversationID, msgs)
	for _, m := range msgs {
		out = append(out, toMurmMessage(m, inputRunIDs[m.ID]))
	}
	updatedAt := time.Now().UnixMilli()
	if len(msgs) > 0 {
		updatedAt = msgs[len(msgs)-1].CreatedAt.UnixMilli()
	}
	chat := murmChat{
		ID:              id,
		Title:           "Caliban",
		UpdatedAt:       updatedAt,
		Messages:        out,
		HasMoreMessages: hasMore,
		MessageLimit:    limit,
	}
	if hasMore && len(msgs) > 0 {
		chat.NextOlderMessagesCursor = strconv.FormatInt(msgs[0].ID, 10)
	}
	return chat, nil
}

func (t *Transport) inputRunIDs(ctx context.Context, conversationID int64, msgs []store.Message) map[int64]int64 {
	lookup, ok := t.store.(inputRunLookup)
	if !ok {
		return nil
	}
	ids := make([]int64, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == llm.RoleUser {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	out, err := lookup.RunIDsByInputMessage(ctx, conversationID, ids)
	if err != nil {
		t.logf("web: load input run ids: %v", err)
		return nil
	}
	return out
}

func toMurmMessage(m store.Message, inputRunID int64) murm.Message {
	id := strconv.FormatInt(m.ID, 10)
	tm := murm.TranscriptMessage{
		ID:        id,
		Role:      toMurmRole(string(m.Role)),
		Text:      m.Content.Text,
		Reasoning: m.Content.Reasoning,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.CreatedAt,
	}
	if m.RunID != nil {
		tm.RunID = strconv.FormatInt(*m.RunID, 10)
	} else if inputRunID != 0 {
		tm.RunID = strconv.FormatInt(inputRunID, 10)
	}
	for _, tc := range m.Content.ToolCalls {
		tm.ToolCalls = append(tm.ToolCalls, murm.TranscriptToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}
	if m.Source != "" {
		tm.Meta = map[string]any{"source": m.Source}
	}
	if m.Content.ToolCallID != "" {
		tm.ToolResult = &murm.TranscriptToolResult{
			ToolCallID: m.Content.ToolCallID,
			OutputText: m.Content.Text,
		}
	}
	return murm.MessageFromTranscript(tm)
}

func toMurmRole(role string) murm.Role {
	switch role {
	case "user":
		return murm.RoleUser
	case "tool":
		return murm.RoleTool
	case string(store.RoleEvent):
		return murm.RoleSystem
	default:
		return murm.RoleAssistant
	}
}

type resolvedChat struct {
	ID             string
	ConversationID int64
}

func (t *Transport) resolveChat(ctx context.Context, id string) (resolvedChat, bool) {
	if id == "" {
		return t.resolveConversationID(ctx, t.convID)
	}
	convID, err := strconv.ParseInt(id, 10, 64)
	if err == nil && convID > 0 {
		if t.store != nil {
			return resolvedChat{}, false
		}
		return t.resolveConversationID(ctx, convID)
	}
	if t.store == nil {
		return resolvedChat{}, false
	}
	conv, ok, err := t.store.ConversationByUUID(ctx, id)
	if err != nil {
		t.logf("web: resolve chat uuid %q: %v", id, err)
		return resolvedChat{}, false
	}
	if !ok || conv.ParentRunID != nil || conv.Status != "active" {
		return resolvedChat{}, false
	}
	return resolvedChat{ID: conv.UUID, ConversationID: conv.ID}, true
}

func (t *Transport) resolveConversationID(ctx context.Context, id int64) (resolvedChat, bool) {
	if id <= 0 {
		return resolvedChat{}, false
	}
	if t.store == nil {
		return resolvedChat{ID: strconv.FormatInt(id, 10), ConversationID: id}, true
	}
	conv, err := t.store.Conversation(ctx, id)
	if err != nil {
		t.logf("web: resolve chat %d: %v", id, err)
		return resolvedChat{}, false
	}
	if conv.ParentRunID != nil || conv.Status != "active" {
		return resolvedChat{}, false
	}
	return resolvedChat{ID: conv.UUID, ConversationID: conv.ID}, true
}

func chatMessageLimit(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return defaultChatTailLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, errors.New("limit must be a positive integer")
	}
	if n > maxChatTailLimit {
		n = maxChatTailLimit
	}
	return n, nil
}

// chatBeforeCursor parses the ?before=<id> pagination cursor. ok is false when
// the param is absent (a normal latest-tail request).
func chatBeforeCursor(r *http.Request) (int64, bool, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("before"))
	if raw == "" {
		return 0, false, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, false, errors.New("before must be a positive message id")
	}
	return id, true, nil
}

func (t *Transport) logf(format string, args ...any) {
	if t.log != nil {
		t.log.Error(format, args...)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func spaHandler() http.Handler {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "." || path == "" {
			serveStaticFile(w, r, sub, "index.html")
			return
		}
		if _, err := fs.Stat(sub, path); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		} else if !strings.Contains(r.Header.Get("Accept"), "text/html") {
			http.NotFound(w, r)
			return
		} else if !errors.Is(err, fs.ErrNotExist) {
			writeError(w, http.StatusInternalServerError, "could not serve asset")
			return
		}
		serveStaticFile(w, r, sub, "index.html")
	})
}

func serveStaticFile(w http.ResponseWriter, r *http.Request, filesystem fs.FS, name string) {
	b, err := fs.ReadFile(filesystem, name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not serve asset")
		return
	}
	if ct := mime.TypeByExtension(filepath.Ext(name)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(b))
}
