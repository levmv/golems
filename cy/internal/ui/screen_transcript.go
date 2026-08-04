package ui

import (
	"strings"

	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/llm"
)

func (m *cyTUIModel) loadSessionHistory() {
	// Input history belongs to the active session. Reset it before reading so a
	// failed resume cannot leave prompts from the previous session available via
	// Up/Ctrl-P.
	m.resetInputHistory()
	history, err := m.agent.SessionHistory()
	if err != nil {
		m.addBlock(screenBlockError, "history: "+err.Error())
		return
	}
	for index, message := range history {
		switch message.Role {
		case llm.RoleUser:
			m.rememberInput(message.Content)
			if recordedShellTurn(history, index) {
				continue
			}
			m.addBlock(screenBlockUser, message.Content)
		case llm.RoleAI:
			if strings.TrimSpace(message.Content) != "" {
				m.addBlock(screenBlockAssistant, message.Content)
			}
			for _, call := range message.ToolCalls {
				m.addToolCallBlock(golem.Step{ToolName: call.Function.Name, ToolCallID: call.ID, Arguments: call.Function.Arguments})
			}
		case llm.RoleTool:
			if change, ok := fileChangeMetaFrom(message.Meta); ok {
				m.applyFileChangeResult(message.ToolCallID, change)
			} else if result, ok := processResultMetaFrom(message.Meta); ok {
				if result.UserInitiated {
					m.applyLocalShellResult(message.ToolCallID, message.Content, result)
				} else {
					m.applyProcessResult(message.ToolCallID, result)
				}
			}
		}
	}
}

func recordedShellTurn(history []llm.Message, index int) bool {
	if index < 0 || index+2 >= len(history) {
		return false
	}
	command, shell := shellEscapeCommand(history[index].Content)
	assistant := history[index+1]
	result := history[index+2]
	if !shell || command == "" || assistant.Role != llm.RoleAI || len(assistant.ToolCalls) != 1 || result.Role != llm.RoleTool {
		return false
	}
	call := assistant.ToolCalls[0]
	if call.Function.Name != "bash" || result.ToolCallID != call.ID {
		return false
	}
	meta, ok := processResultMetaFrom(result.Meta)
	if !ok || !meta.UserInitiated {
		return false
	}
	var args struct {
		Command string `json:"command"`
	}
	return decodeToolDisplayArgs(call.Function.Arguments, &args) && strings.TrimSpace(args.Command) == command
}

func (m *cyTUIModel) rememberInput(input string) {
	if strings.TrimSpace(input) == "" {
		return
	}
	if len(m.history) == 0 || m.history[len(m.history)-1] != input {
		m.history = append(m.history, input)
	}
	m.historyIndex = len(m.history)
	m.savedInput = ""
}

func (m *cyTUIModel) resetInputHistory() {
	m.history = nil
	m.historyIndex = 0
	m.savedInput = ""
}

func (m *cyTUIModel) historyPrevious() {
	if len(m.history) == 0 {
		return
	}
	if m.historyIndex == len(m.history) {
		m.savedInput = m.input.Value()
	}
	if m.historyIndex > 0 {
		m.historyIndex--
		m.input.SetValue(m.history[m.historyIndex])
		m.input.CursorEnd()
	}
}

func (m *cyTUIModel) historyNext() {
	if len(m.history) == 0 || m.historyIndex >= len(m.history) {
		return
	}
	if m.historyIndex < len(m.history)-1 {
		m.historyIndex++
		m.input.SetValue(m.history[m.historyIndex])
	} else {
		m.historyIndex = len(m.history)
		m.input.SetValue(m.savedInput)
	}
	m.input.CursorEnd()
}

func (m *cyTUIModel) resize(width, height int) {
	m.width = width
	m.height = height
	m.input.MaxHeight = min(composerMaxRows, max(1, height-screenFixedRows-1))
	m.input.SetWidth(m.contentWidth())
	m.secret.SetWidth(m.contentWidth())
}

func (m cyTUIModel) lineWidth() int {
	width := m.width
	if width < 20 {
		width = 20
	}
	lineWidth := width - 1
	if lineWidth < 1 {
		lineWidth = 1
	}
	return lineWidth
}

func (m *cyTUIModel) refreshScreen() {
	// There are two suffix caches by design: renderCache maps source blocks to
	// transcript lines, while transcriptDirtyFrom tells inlineRenderer which
	// normalized terminal rows may have changed. Preserving both prefixes keeps
	// editor and streaming updates independent of total session length.
	m.renderPending = false
	lines, dirtyFrom := m.renderTranscriptLinesFromDirty()
	if dirtyFrom == len(m.transcriptLines) && len(lines) == len(m.transcriptLines) {
		return
	}
	dirtyFrom = min(dirtyFrom, len(m.transcriptLines), len(lines))
	m.transcriptLines = append(m.transcriptLines[:dirtyFrom], lines[dirtyFrom:]...)
	if !m.transcriptDirty || dirtyFrom < m.transcriptDirtyFrom {
		m.transcriptDirtyFrom = dirtyFrom
	}
	m.transcriptDirty = true
}

func (m *cyTUIModel) resetTranscript() {
	// /clear is explicitly destructive: invalidate first so the next frame
	// removes both the visible screen and terminal scrollback.
	m.renderer.Invalidate()
	m.clearTranscript()
}

func (m *cyTUIModel) continueTranscriptBelow() {
	// /resume differs from /clear. The completed transcript becomes unmanaged
	// native scrollback, only its interactive suffix is erased, and the resumed
	// session starts as a new source-backed frame below it. A later resize can
	// rebuild only this new managed frame.
	m.renderer.AppendFrameAfter(len(m.transcriptLines))
	m.clearTranscript()
}

func (m *cyTUIModel) clearTranscript() {
	m.blocks = nil
	m.transcriptLines = nil
	m.renderCache = nil
	m.renderCacheLines = nil
	m.renderDirtyFrom = 0
	m.transcriptDirty = true
	m.transcriptDirtyFrom = 0
}
