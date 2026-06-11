# golem

`pkg/golem` is a small reusable agent loop built on top of `pkg/llm`.

The package owns conversation history, system prompt handling, streaming response
assembly, bounded tool execution, trace steps, and cumulative usage accounting.
Applications provide an `llm.Model` plus optional tool executors.

```go
agent, err := golem.New(golem.Config{
	Model:        model,
	Name:         "golem",
	SystemPrompt: golem.DefaultSystemPrompt,
})
if err != nil {
	return err
}

turn, err := agent.Reply(ctx, "What should I do next?")
if err != nil {
	return err
}
fmt.Println(turn.Reply)
```

## Tools

Applications add concrete tools. `pkg/golem` only knows the generic LLM tool
protocol and how to feed tool results back into the model transcript.

```go
readFile := golem.FunctionTool(
	"read_file",
	"Read a UTF-8 text file under the workspace root.",
	jsonschema.Object(map[string]jsonschema.Schema{
		"path": jsonschema.String("Path under workspace root"),
	}, "path").NoAdditionalProperties(),
	func(ctx context.Context, call llm.ToolCall) (golem.ToolResult, error) {
		return golem.ToolResult{Content: "file contents"}, nil
	},
)

agent, err := golem.New(golem.Config{
	Model: model,
	Tools: []golem.Tool{readFile},
})
```

A finished `Turn` contains the final reply plus tool trace steps. The model-visible
history also keeps assistant tool-call messages and tool result messages, so
later turns have the right context.

## Streaming

For terminals or chat transports that want incremental output, use `Stream`.
Text deltas, reasoning deltas, tool calls, tool results, tool errors, and the
final done event are emitted as `StreamEvent`s. The UI decides which events to
show.

```go
_, err := agent.Stream(ctx, "Draft a plan", func(ev golem.StreamEvent) {
	switch ev.Kind {
	case golem.EventTextDelta:
		fmt.Print(ev.Text)
	case golem.EventToolCall, golem.EventToolResult, golem.EventToolError:
		log.Println(ev.Step)
	}
})
```
