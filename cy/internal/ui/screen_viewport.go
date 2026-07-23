package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/llm"
)

func (m *cyTUIModel) appendHistoryBlocks() {
	history, err := m.agent.SessionHistory()
	if err != nil {
		m.addBlock(screenBlockError, "history: "+err.Error())
		return
	}
	for _, message := range history {
		switch message.Role {
		case llm.RoleUser:
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
				m.applyProcessResult(message.ToolCallID, result)
			}
		}
	}
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

func (m *cyTUIModel) isViewportMsg(msg tea.Msg) bool {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "pgup", "pgdown", "ctrl+up", "ctrl+down", "ctrl+home", "ctrl+end":
			return true
		}
	case tea.MouseWheelMsg:
		return true
	}
	return false
}

func (m cyTUIModel) updateViewport(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m *cyTUIModel) resize(width, height int) {
	m.width = width
	m.height = height
	lineWidth := m.lineWidth()
	m.viewport.SetWidth(lineWidth)
	m.input.MaxHeight = min(composerMaxRows, max(1, height-screenFixedRows-1))
	m.input.SetWidth(m.contentWidth())
	m.secret.SetWidth(m.contentWidth())
	m.syncViewportHeight()
}

func (m *cyTUIModel) syncViewportHeight() {
	if m.height <= 0 {
		return
	}
	contentHeight := m.height - screenFixedRows - m.composerHeight()
	if contentHeight < 1 {
		contentHeight = 1
	}
	m.viewport.SetHeight(contentHeight)
}

func (m cyTUIModel) composerHeight() int {
	if m.picker.active() {
		return len(m.renderPicker())
	}
	return m.editorHeight() + len(m.renderCommandSuggestions())
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

func (m *cyTUIModel) refreshViewport(follow bool) {
	m.renderPending = false
	m.syncViewportHeight()
	lines := m.renderTranscriptLines()
	if padding := m.viewport.Height() - len(lines); padding > 0 {
		lines = append(make([]string, padding), lines...)
	}
	m.viewport.SetContentLines(lines)
	if follow || m.viewport.PastBottom() {
		m.viewport.GotoBottom()
	}
}
