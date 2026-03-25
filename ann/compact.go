package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/levmv/golems/pkg/llm"
)

// runAsyncCompaction is triggered when a session reaches the message threshold.
func (e *Engine) runAsyncCompaction(s *Session) {
	Log.Debug("[Compact] Triggered background compaction for %s", s.key)

	newSoul, err := s.DoCompaction(func(convoText, currentSoul string) (string, string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()

		var wg sync.WaitGroup
		var newSummaryPoint, evolvedSoul string

		wg.Add(1)
		go func() {
			defer wg.Done()
			newSummaryPoint = e.extractFacts(ctx, s.key, convoText)
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			evolvedSoul = e.evolveSoul(ctx, s.key, convoText, currentSoul)
		}()

		wg.Wait()

		if newSummaryPoint == "" && evolvedSoul == "" {
			return "", "", fmt.Errorf("LLM returned empty results for both facts and soul")
		}

		return newSummaryPoint, evolvedSoul, nil
	})

	if err != nil {
		Log.Error("Compaction error for %s: %v", s.key, err)
		return
	}

	if newSoul != "" {
		e.sendSoulUpdateToControl(s.key, newSoul)
	}
}

func (e *Engine) extractFacts(ctx context.Context, key SessionKey, convoText string) string {
	factPrompt := "Extract factual updates about the USER (preferences, facts) and key narrative events. DO NOT summarize the assistant's personality. Output ONLY brief bullet points."

	if key.Type == SessionTypeGroup {
		factPrompt = "Summarize the key narrative developments from this conversation. Focus on events, decisions, and revealed preferences. " +
			"For each point, mention the user involved. Keep it concise"
	}
	resp, err := e.llm.WithTemperature(0.3).Chat(ctx, llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: factPrompt},
			{Role: llm.RoleUser, Content: convoText},
		},
	})
	if err != nil {
		Log.Error("Fact extraction failed for %s: %v", key, err)
		return ""
	}
	return strings.TrimSpace(resp.Content)
}

func (e *Engine) evolveSoul(ctx context.Context, key SessionKey, convoText string, curSoul string) string {
	prompt := fmt.Sprintf(
		"Current Persona:\n%s\n\n"+
			"Recent Conversation:\n%s\n\n"+
			"Task:\n"+
			"Update the persona based only on clear evidence from the conversation.\n"+
			"Preserve identity anchors and core history.\n"+
			"Allow only small, gradual drift in tone, preferences and habits.\n"+
			"Do not overextend or invent major changes. Keep the result concise and natural.\n"+
			"Output ONLY the raw updated persona text, with no markdown, labels, or explanation.\n\n"+
			curSoul, convoText,
	)

	resp, err := e.llm.WithTemperature(0.4).Chat(ctx, llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "You update an AI persona from conversation evidence. Preserve name and core identity. " +
				"Follow identity constraints exactly and output only the updated persona."},
			{Role: llm.RoleUser, Content: prompt},
		},
	})
	if err != nil {
		Log.Error("Soul evolution failed for %s: %v", key, err)
		return ""
	}
	return strings.TrimSpace(resp.Content)
}
