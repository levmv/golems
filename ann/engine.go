package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/levmv/golems/pkg/llm"
)

type Gateway interface {
	Send(ctx context.Context, sKey SessionKey, text string) error
	StartTyping(ctx context.Context, sKey SessionKey) func() // Returns Stop()
}

// StreamEvent wraps a stream chunk and an error for channel transport.
type StreamEvent struct {
	Chunk llm.StreamChunk
	Err   error
}

// Engine is the core orchestration layer.
// It manages LLM streams, implements a per-session debounce (cancels previous
// ongoing generation when a new message for the same session arrives), and
// coordinates with the SessionRegistry for session state.
type Engine struct {
	registry     *SessionRegistry
	llm          *llm.Model
	proactiveLLM *llm.Model
	botName      string
	systemPrompt string
	// cancelers maps sessionID -> context.CancelFunc for the currently-running
	// generation for that session. We store CancelFunc so new requests can
	// cancel the previous generation quickly.
	cancelers sync.Map

	gateways map[string]Gateway

	controlChats map[string]string
}

func NewEngine(registry *SessionRegistry, llmClient *llm.Model, proactiveLLM *llm.Model, botName string, systemPrompt string, controlChats map[string]string) *Engine {
	if controlChats == nil {
		controlChats = make(map[string]string)
	}
	if proactiveLLM == nil {
		proactiveLLM = llmClient
	}
	return &Engine{
		registry:     registry,
		llm:          llmClient,
		proactiveLLM: proactiveLLM,
		botName:      botName,
		systemPrompt: systemPrompt,
		gateways:     make(map[string]Gateway),
		controlChats: controlChats,
	}
}

func (e *Engine) RegisterGateway(platform string, gw Gateway) {
	e.gateways[platform] = gw
}

// ProcessMessage accepts a user's input, persists it into the session, and
// synchronously generates a complete response from the LLM.
// It implements per-session debounce (cancels previous ongoing generation when
// a new message for the same session arrives).
func (e *Engine) ProcessMessage(ctx context.Context, key SessionKey, userMsg Message) (string, error) {
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	if prev, loaded := e.cancelers.Swap(key, cancel); loaded {
		Log.Debug("[Engine] Canceling previous ongoing generation for %s (debounced)", key)
		prev.(context.CancelFunc)()
	}

	session, err := e.registry.GetSession(key)
	if err != nil {
		return "", err
	}

	if err := e.addMessage(session, userMsg); err != nil {
		return "", err
	}

	reqMessages := session.GetPrompt(e.systemPrompt)

	resp, err := e.llm.Chat(reqCtx, llm.Request{
		Messages: reqMessages,
	})
	if err != nil {
		return "", err
	}

	if resp.ReasoningContent != "" {
		e.sendReasoningToControl(key, string(resp.ReasoningContent), resp.Usage)
	}

	finalText := strings.TrimSpace(resp.Content)

	if finalText != "" && reqCtx.Err() == nil {
		aiMsg := Message{
			Role:      llm.RoleAI,
			Content:   finalText,
			Timestamp: time.Now(),
			Name:      e.botName,
		}
		if err := e.addMessage(session, aiMsg); err != nil {
			Log.Error("Failed to save assistant reply for %s: %v", key, err)
		}
	}

	return finalText, nil
}

func (e *Engine) ObserveMessage(ctx context.Context, sKey SessionKey, userMsg Message) error {
	session, err := e.registry.GetSession(sKey)
	if err != nil {
		return err
	}
	return e.addMessage(session, userMsg)
}

func (e *Engine) StartBackgroundObserver(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			groups := e.registry.GetGroupSessions()

			for _, s := range groups {
				// TODO: move this 0.1 into config ("chatiness" or something)
				if s.ClaimProactiveReplyCandidate(0.1) && e.ShouldProactivelySpeak(ctx, s) {
					go e.processProactiveReply(ctx, s)
				}
			}
		}
	}
}

func (e *Engine) processProactiveReply(ctx context.Context, s *Session) {
	gw, exists := e.gateways[s.key.Platform]
	if !exists {
		Log.Error("Gateway not found for platform: %s", s.key.Platform)
		return
	}

	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	if prev, loaded := e.cancelers.Swap(s.key, cancel); loaded {
		prev.(context.CancelFunc)()
	}

	stopTyping := gw.StartTyping(context.Background(), s.key)
	defer stopTyping()

	finalText, err := e.GenerateDeferredReply(reqCtx, s)

	if err == nil && finalText != "" {
		_ = gw.Send(context.Background(), s.key, finalText)
	} else if err != nil {
		Log.Error("Deferred reply error for %s: %v", s.key, err)
	}
}

func (e *Engine) GenerateDeferredReply(ctx context.Context, session *Session) (string, error) {
	reqMessages := session.GetPrompt(e.systemPrompt)

	/*
		reqMessages = append(reqMessages, llm.Message{
			Role:    llm.RoleSystem,
			Content: "Participate in conversation. Talk naturally.",
		})*/

	resp, err := e.llm.Chat(ctx, llm.Request{
		Messages: reqMessages,
	})
	if err != nil {
		return "", err
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}

	if resp.ReasoningContent != "" {
		e.sendReasoningToControl(session.key, string(resp.ReasoningContent), resp.Usage)
	}
	finalText := strings.TrimSpace(resp.Content)

	if finalText != "" {
		aiMsg := Message{
			Role:      llm.RoleAI,
			Name:      e.botName,
			Content:   finalText,
			Timestamp: time.Now(),
		}
		if err := e.addMessage(session, aiMsg); err != nil {
			Log.Error("Failed to save auto-reply for %s: %v", session.key, err)
		}
	}

	return finalText, nil
}

func (e *Engine) ShouldProactivelySpeak(ctx context.Context, session *Session) bool {
	reqMessages := e.buildProactiveJudgePrompt(session)

	resp, err := e.proactiveLLM.Chat(ctx, llm.Request{
		Messages: reqMessages,
	})
	if err != nil {
		Log.Debug("Proactive judge failed for %s: %v", session.key, err)
		return false
	}
	if ctx.Err() != nil {
		return false
	}

	answer := strings.ToLower(strings.TrimSpace(resp.Content))
	fields := strings.Fields(answer)
	if len(fields) == 0 {
		Log.Debug("Proactive judge returned empty answer for %s", session.key)
		return false
	}

	decision := strings.Trim(fields[0], ".!,;:`'\"")
	shouldSpeak := decision == "yes"
	Log.Debug("Proactive judge for %s returned %q, shouldSpeak=%v", session.key, resp.Content, shouldSpeak)
	return shouldSpeak
}

func (e *Engine) buildProactiveJudgePrompt(session *Session) []llm.Message {
	recentLines := session.RecentLogLines(12)
	conversation := strings.Join(recentLines, "\n")
	soul := session.storage.GetSoul()

	content := fmt.Sprintf(`You decide whether %s should naturally join a Telegram group chat right now.

Persona/system prompt:
%s

Current persona notes:
%s

Recent chat:
%s

Answer only "yes" or "no".

Say yes only if %s has something timely, relevant, or socially natural to add.
Prefer no when users are clearly talking to each other, the bot just spoke, the moment has passed, or a reply would feel forced.`, e.botName, e.systemPrompt, soul, conversation, e.botName)

	return []llm.Message{
		{Role: llm.RoleSystem, Content: content},
	}
}

func (e *Engine) addMessage(session *Session, msg Message) error {
	needsCompaction, err := session.AddMessage(msg)
	if err != nil {
		return err
	}

	if needsCompaction {
		go e.runAsyncCompaction(session)
	}
	return nil
}

func (e *Engine) notifyControlChat(sKey SessionKey, message string) {
	if message == "" {
		return
	}
	targetChatID, ok := e.controlChats[sKey.ChatID]
	if !ok {
		return
	}
	gw, exists := e.gateways[sKey.Platform]
	if !exists {
		return
	}
	targetKey := SessionKey{Platform: sKey.Platform, Type: SessionTypeGroup, ChatID: targetChatID}
	if privateSender, ok := gw.(interface {
		SendWithoutBroadcast(context.Context, SessionKey, string) error
	}); ok {
		go privateSender.SendWithoutBroadcast(context.Background(), targetKey, message)
		return
	}
	go gw.Send(context.Background(), targetKey, message)
}

// sendReasoningToControl formats and routes reasoning logs if a control channel is configured.
func (e *Engine) sendReasoningToControl(sKey SessionKey, reasoning string, usage llm.Usage) {
	stats := fmt.Sprintf("Tokens: %d (Prompt: %d, Cached: %d, Completion: %d)",
		usage.TotalTokens, usage.PromptTokens, usage.CachedTokens, usage.CompletionTokens)
	e.notifyControlChat(sKey, fmt.Sprintf("%s\n\n📊 %s", reasoning, stats))
}

// sendSoulUpdateToControl formats and routes soul evolution logs.
func (e *Engine) sendSoulUpdateToControl(sKey SessionKey, newSoul string) {
	e.notifyControlChat(sKey, fmt.Sprintf("👻 [Soul updated]\n\n%s", newSoul))
}
