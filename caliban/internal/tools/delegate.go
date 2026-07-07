package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/jsonschema"
	"github.com/levmv/golems/pkg/llm"
)

// Delegator is the engine capability behind child-conversation subagents.
type Delegator interface {
	Delegate(ctx context.Context, prompt string) (conversationID int64, reply string, err error)
	DelegateContinue(ctx context.Context, conversationID int64, prompt string) (reply string, err error)
}

type delegateArgs struct {
	Prompt string `json:"prompt"`
}

type delegateContinueArgs struct {
	ConversationID int64  `json:"conversation_id"`
	Prompt         string `json:"prompt"`
}

// Delegation returns the subagent tools. v1 is deliberately blocking: a child
// conversation runs to completion and returns only its final text to the parent.
func Delegation(d Delegator) []golem.Tool {
	return []golem.Tool{
		golem.FunctionTool("delegate",
			"Delegate an isolated research/checking task to a child conversation. "+
				"Use this when the work would add noisy intermediate context to the main chat. "+
				"Returns the child conversation id and final answer.",
			jsonschema.Obj(
				jsonschema.Required("prompt", jsonschema.Str{
					Description: "Self-contained task prompt for the child agent.",
				}),
			),
			func(ctx context.Context, call llm.ToolCall) (golem.ToolResult, error) {
				var args delegateArgs
				if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
					return golem.ToolResult{}, fmt.Errorf("invalid arguments: %w", err)
				}
				if strings.TrimSpace(args.Prompt) == "" {
					return golem.ToolResult{}, fmt.Errorf("prompt is required")
				}
				id, reply, err := d.Delegate(ctx, args.Prompt)
				if err != nil {
					return golem.ToolResult{}, err
				}
				return golem.ToolResult{Content: formatDelegateResult(id, reply)}, nil
			}),
		golem.FunctionTool("delegate_continue",
			"Continue a previous delegated child conversation with another prompt. "+
				"Use the conversation_id returned by delegate.",
			jsonschema.Obj(
				jsonschema.Required("conversation_id", jsonschema.Int{
					Description: "Child conversation id returned by delegate.",
				}),
				jsonschema.Required("prompt", jsonschema.Str{
					Description: "Follow-up prompt for that child agent.",
				}),
			),
			func(ctx context.Context, call llm.ToolCall) (golem.ToolResult, error) {
				var args delegateContinueArgs
				if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
					return golem.ToolResult{}, fmt.Errorf("invalid arguments: %w", err)
				}
				if args.ConversationID <= 0 {
					return golem.ToolResult{}, fmt.Errorf("conversation_id must be positive")
				}
				if strings.TrimSpace(args.Prompt) == "" {
					return golem.ToolResult{}, fmt.Errorf("prompt is required")
				}
				reply, err := d.DelegateContinue(ctx, args.ConversationID, args.Prompt)
				if err != nil {
					return golem.ToolResult{}, err
				}
				return golem.ToolResult{Content: formatDelegateResult(args.ConversationID, reply)}, nil
			}),
	}
}

func formatDelegateResult(conversationID int64, reply string) string {
	return fmt.Sprintf("child conversation %d:\n%s", conversationID, strings.TrimSpace(reply))
}
