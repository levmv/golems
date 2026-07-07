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

// NotifySink is the engine capability the notify tool depends on: an immediate
// push to the user outside the normal reply flow.
type NotifySink interface {
	Notify(ctx context.Context, text string) error
}

type notifyArgs struct {
	Text string `json:"text"`
}

// Notify returns the `notify` tool: push a message to the user out of band.
// Intended for extra out-of-band messages inside scheduled turns. The final
// scheduled-turn reply gets its own system notification; this tool is not needed
// just to announce completion.
func Notify(sink NotifySink) golem.Tool {
	schema := jsonschema.Obj(
		jsonschema.Required("text", jsonschema.Str{
			Description: "Message to push to the user immediately, outside the normal reply.",
		}),
	)
	return golem.FunctionTool("notify",
		"Push a message to the user out of band (e.g. from a scheduled turn). "+
			"In a normal reply, just answer instead of calling this. Do not use it only to announce scheduled-turn completion.",
		schema,
		func(ctx context.Context, call llm.ToolCall) (golem.ToolResult, error) {
			var args notifyArgs
			if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
				return golem.ToolResult{}, fmt.Errorf("invalid arguments: %w", err)
			}
			if strings.TrimSpace(args.Text) == "" {
				return golem.ToolResult{}, fmt.Errorf("text is required")
			}
			if err := sink.Notify(ctx, args.Text); err != nil {
				return golem.ToolResult{}, err
			}
			return golem.ToolResult{Content: "notified"}, nil
		})
}
