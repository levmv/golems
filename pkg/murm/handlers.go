package murm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/levmv/golems/pkg/golem"
)

// AgentResolver supplies the golem agent for a chat. The package does not own
// agent construction or per-chat lifecycle — the app builds/loads the agent
// (e.g. from a stored transcript) behind this interface.
type AgentResolver interface {
	Resolve(ctx context.Context, chatID string) (*golem.Agent, error)
}

// Logger is the minimal logging surface the handler uses; nil disables logging.
type Logger interface {
	Error(format string, args ...any)
}

// Handler serves the murm-ui backend HTTP API. Phase 1 implements the run
// endpoint with immediate SSE streaming (no persistence).
type Handler struct {
	resolver AgentResolver
	log      Logger
}

// NewHandler creates a Handler backed by the given agent resolver.
func NewHandler(resolver AgentResolver, log Logger) *Handler {
	return &Handler{resolver: resolver, log: log}
}

// Mux returns an http.ServeMux with the murm routes registered. Mount it under
// the app's router (it uses absolute /api/... patterns).
func (h *Handler) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/chats/{chatId}/runs", h.startRun)
	return mux
}

func (h *Handler) startRun(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("chatId")

	var req RunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Input) == "" {
		writeJSONError(w, http.StatusBadRequest, "input is required")
		return
	}
	if req.ClientMessageID == "" {
		writeJSONError(w, http.StatusBadRequest, "clientMessageId is required")
		return
	}

	agent, err := h.resolver.Resolve(r.Context(), chatID)
	if err != nil {
		h.logf("murm: resolve agent for chat %s: %v", chatID, err)
		writeJSONError(w, http.StatusInternalServerError, "could not start run")
		return
	}

	// From here on we stream; pre-flight errors above used normal HTTP codes.
	sse, err := NewSSEWriter(w)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	translator := NewTranslator(req.ClientMessageID, func(ev StreamEvent) {
		if err := sse.Send(ev); err != nil {
			h.logf("murm: send event: %v", err)
		}
	})

	_, err = agent.Stream(r.Context(), req.Input, translator.Feed)
	if err == nil {
		return // golem emitted EventDone -> translator already sent usage+finish
	}
	// The stream errored without a normal finish; tell the client why.
	if errors.Is(err, context.Canceled) || r.Context().Err() != nil {
		translator.Finish(FinishAborted)
		return
	}
	h.logf("murm: run %s failed: %v", req.ClientMessageID, err)
	translator.Finish(FinishError)
}

func (h *Handler) logf(format string, args ...any) {
	if h.log != nil {
		h.log.Error(format, args...)
	}
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
