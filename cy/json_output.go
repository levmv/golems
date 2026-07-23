package main

import (
	"encoding/json"
	"io"

	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/llm"
)

const jsonResultVersion = 1

type jsonResult struct {
	Version      int              `json:"version"`
	Reply        string           `json:"reply"`
	Usage        llm.Usage        `json:"usage"`
	FinishReason llm.FinishReason `json:"finish_reason"`
	SessionID    string           `json:"session_id,omitempty"`
}

type sessionIdentifier interface {
	SessionID() string
}

func writeJSONResult(out io.Writer, agent agentRunner, cfg Config, turn *golem.Turn) error {
	result := jsonResult{
		Version:      jsonResultVersion,
		Reply:        turn.Reply,
		Usage:        turn.Usage,
		FinishReason: turn.FinishReason,
	}
	if source, ok := agent.(sessionIdentifier); ok && !cfg.Ephemeral {
		result.SessionID = source.SessionID()
	}
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(result)
}
