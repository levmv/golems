package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

func commandSuggestionCatalog() []string {
	commands := make([]string, 0, len(tuiCommands))
	for _, command := range tuiCommands {
		commands = append(commands, command.name)
	}
	return commands
}

func (m *cyTUIModel) configureCommandSuggestions() {
	m.commandSuggestionsEnabled = true
	m.commandSuggestionIndex = 0
	m.syncCommandSuggestions()
}

func (m *cyTUIModel) disableCommandSuggestions() {
	m.commandSuggestionsEnabled = false
	m.commandSuggestions = nil
	m.commandSuggestionIndex = 0
}

func (m *cyTUIModel) syncCommandSuggestions() {
	if !m.commandSuggestionsEnabled {
		return
	}
	value := strings.ToLower(strings.TrimLeft(m.input.Value(), " \t"))
	var candidates []string
	switch {
	case strings.HasPrefix(value, "/profile "):
		for _, profile := range capabilityProfileCatalog {
			candidates = append(candidates, "/profile "+profile.Name)
		}
	case strings.HasPrefix(value, "/login "):
		for _, provider := range m.cfg.providers() {
			candidates = append(candidates, "/login "+provider)
		}
	case strings.HasPrefix(value, "/logout "):
		for _, provider := range m.cfg.providers() {
			candidates = append(candidates, "/logout "+provider)
		}
	case strings.HasPrefix(value, "/model "):
		for _, uri := range m.agent.KnownModels() {
			candidates = append(candidates, "/model "+uri)
		}
	default:
		candidates = commandSuggestionCatalog()
	}
	m.commandSuggestions = m.commandSuggestions[:0]
	for _, suggestion := range candidates {
		if strings.HasPrefix(strings.ToLower(suggestion), value) {
			m.commandSuggestions = append(m.commandSuggestions, suggestion)
		}
	}
	if m.commandSuggestionIndex >= len(m.commandSuggestions) {
		m.commandSuggestionIndex = max(0, len(m.commandSuggestions)-1)
	}
}

func (m cyTUIModel) commandSuggestionsVisible() bool {
	if m.working || m.picker.active() || m.loginProvider != "" || !m.commandSuggestionsEnabled {
		return false
	}
	value := strings.TrimSpace(m.input.Value())
	return strings.HasPrefix(value, "/") && len(m.commandSuggestions) > 0
}

func (m cyTUIModel) currentCommandSuggestion() string {
	if len(m.commandSuggestions) == 0 {
		return ""
	}
	index := min(max(0, m.commandSuggestionIndex), len(m.commandSuggestions)-1)
	return m.commandSuggestions[index]
}

func (m *cyTUIModel) moveCommandSuggestion(delta int) {
	if len(m.commandSuggestions) == 0 {
		return
	}
	m.commandSuggestionIndex = (m.commandSuggestionIndex + delta + len(m.commandSuggestions)) % len(m.commandSuggestions)
}

func (m cyTUIModel) selectedInput() string {
	value := strings.TrimSpace(m.editorValue())
	if m.commandSuggestionsVisible() {
		if suggestion := m.currentCommandSuggestion(); strings.HasPrefix(strings.ToLower(suggestion), strings.ToLower(value)) {
			return suggestion
		}
	}
	return value
}

func (m cyTUIModel) editorValue() string {
	if m.loginProvider != "" {
		return m.secret.Value()
	}
	return m.input.Value()
}

func (m cyTUIModel) editorView() string {
	if m.loginProvider != "" {
		return m.secret.View()
	}
	return m.input.View()
}

func (m cyTUIModel) markedEditorView() string {
	lines := strings.Split(strings.TrimSuffix(m.editorView(), "\n"), "\n")
	for index, line := range lines {
		marker := " "
		if index == 0 {
			marker = userMarker
		}
		lines[index] = m.renderMarkedLine(marker, line, m.mutedStyle)
	}
	return strings.Join(lines, "\n")
}

func (m cyTUIModel) editorCursor() *tea.Cursor {
	if m.loginProvider != "" {
		return m.secret.Cursor()
	}
	return m.input.Cursor()
}

func (m cyTUIModel) editorHeight() int {
	if m.loginProvider != "" {
		return 1
	}
	return max(1, m.input.Height())
}

func (m cyTUIModel) updateEditor(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	if m.loginProvider != "" {
		m.secret, cmd = m.secret.Update(msg)
	} else {
		m.input, cmd = m.input.Update(msg)
		m.syncCommandSuggestions()
	}
	m.refreshViewport(true)
	return m, cmd
}

func isEnterKey(msg tea.KeyPressMsg) bool {
	key := msg.Key()
	keyString := msg.String()
	return key.Code == tea.KeyEnter || key.Code == tea.KeyReturn || key.Code == tea.KeyKpEnter ||
		keyString == "enter" || keyString == "shift+enter" || keyString == "alt+enter"
}

func (m cyTUIModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.picker.active() {
		return m.handlePickerKey(msg)
	}
	if m.maintenance != "" {
		if m.maintenanceCancel != nil && (msg.String() == "ctrl+c" || keyIsCtrl(msg, 'c') || msg.Key().Code == tea.KeyEscape || msg.Key().Code == tea.KeyEsc || msg.String() == "esc") {
			label := m.maintenance
			m.cancelMaintenance()
			m.addBlock(screenBlockSystem, "cancelling "+label)
			m.refreshViewport(true)
		}
		return m, nil
	}
	key := msg.Key()
	keyString := msg.String()
	hasCtrl := key.Mod&tea.ModCtrl != 0
	hasShift := key.Mod&tea.ModShift != 0

	switch {
	case keyString == "ctrl+c" || keyIsCtrl(msg, 'c'):
		if m.working {
			m.cancelTurn()
			m.addBlock(screenBlockSystem, "cancelling current turn")
			m.refreshViewport(true)
			return m, nil
		}
		return m, tea.Quit
	case keyString == "ctrl+d" || keyIsCtrl(msg, 'd'):
		if !m.working && strings.TrimSpace(m.editorValue()) == "" {
			return m, tea.Quit
		}
		return m.updateEditor(editorCtrlKey('d'))
	case isEnterKey(msg) && (hasShift || keyString == "shift+enter") && m.loginProvider == "":
		m.input.InsertString("\n")
		m.syncCommandSuggestions()
		m.refreshViewport(true)
		return m, nil
	case isEnterKey(msg):
		return m.submitInput()
	case m.historyIndex >= len(m.history) && m.commandSuggestionsVisible() && ((!hasCtrl && (key.Code == tea.KeyUp || key.Code == tea.KeyKpUp || keyString == "up")) || keyIsCtrl(msg, 'p')):
		m.moveCommandSuggestion(-1)
		m.refreshViewport(true)
		return m, nil
	case m.historyIndex >= len(m.history) && m.commandSuggestionsVisible() && ((!hasCtrl && (key.Code == tea.KeyDown || key.Code == tea.KeyKpDown || keyString == "down")) || keyIsCtrl(msg, 'n')):
		m.moveCommandSuggestion(1)
		m.refreshViewport(true)
		return m, nil
	case m.commandSuggestionsVisible() && (key.Code == tea.KeyTab || keyString == "tab"):
		m.input.SetValue(m.currentCommandSuggestion())
		m.input.MoveToEnd()
		m.syncCommandSuggestions()
		m.refreshViewport(true)
		return m, nil
	case !hasCtrl && m.loginProvider == "" && (key.Code == tea.KeyUp || key.Code == tea.KeyKpUp || keyString == "up"):
		if !m.editorAtFirstVisualLine() {
			return m.updateEditor(msg)
		}
		m.historyPrevious()
		m.syncCommandSuggestions()
		m.refreshViewport(true)
		return m, nil
	case !hasCtrl && m.loginProvider == "" && (key.Code == tea.KeyDown || key.Code == tea.KeyKpDown || keyString == "down"):
		if !m.editorAtLastVisualLine() {
			return m.updateEditor(msg)
		}
		m.historyNext()
		m.syncCommandSuggestions()
		m.refreshViewport(true)
		return m, nil
	case keyIsCtrl(msg, 'p'):
		m.historyPrevious()
		m.syncCommandSuggestions()
		m.refreshViewport(true)
		return m, nil
	case keyIsCtrl(msg, 'n'):
		m.historyNext()
		m.syncCommandSuggestions()
		m.refreshViewport(true)
		return m, nil
	case keyString == "ctrl+u" || keyIsCtrl(msg, 'u'):
		return m.updateEditor(editorCtrlKey('u'))
	case keyString == "ctrl+k" || keyIsCtrl(msg, 'k'):
		return m.updateEditor(editorCtrlKey('k'))
	case keyString == "ctrl+w" || keyIsCtrl(msg, 'w'):
		return m.updateEditor(editorCtrlKey('w'))
	case keyString == "ctrl+a" || keyIsCtrl(msg, 'a'):
		return m.updateEditor(editorCtrlKey('a'))
	case keyString == "ctrl+e" || keyIsCtrl(msg, 'e'):
		return m.updateEditor(editorCtrlKey('e'))
	case keyString == "ctrl+b" || keyIsCtrl(msg, 'b'):
		return m.updateEditor(editorCtrlKey('b'))
	case keyString == "ctrl+f" || keyIsCtrl(msg, 'f'):
		return m.updateEditor(editorCtrlKey('f'))
	case key.Code == tea.KeyPgUp || key.Code == tea.KeyKpPgUp || keyString == "pgup":
		m.viewport.PageUp()
		return m, nil
	case key.Code == tea.KeyPgDown || key.Code == tea.KeyKpPgDown || keyString == "pgdown":
		m.viewport.PageDown()
		return m, nil
	case keyString == "ctrl+up" || hasCtrl && (key.Code == tea.KeyUp || key.Code == tea.KeyKpUp):
		m.viewport.ScrollUp(1)
		return m, nil
	case keyString == "ctrl+down" || hasCtrl && (key.Code == tea.KeyDown || key.Code == tea.KeyKpDown):
		m.viewport.ScrollDown(1)
		return m, nil
	case keyString == "ctrl+home" || hasCtrl && (key.Code == tea.KeyHome || key.Code == tea.KeyKpHome):
		m.viewport.GotoTop()
		return m, nil
	case key.Code == tea.KeyEscape || key.Code == tea.KeyEsc || keyString == "esc":
		if m.loginProvider != "" {
			m.loginProvider = ""
			m.loginSwitchModel = false
			m.secret.Reset()
			m.input.Placeholder = ""
			m.configureCommandSuggestions()
			m.addBlock(screenBlockSystem, "login cancelled")
			m.refreshViewport(true)
			return m, nil
		}
		if m.working {
			m.cancelTurn()
			m.restoreQueuedInput()
			m.refreshViewport(true)
			return m, nil
		}
		m.viewport.GotoBottom()
		return m, nil
	case keyString == "ctrl+end" || hasCtrl && (key.Code == tea.KeyEnd || key.Code == tea.KeyKpEnd):
		m.viewport.GotoBottom()
		return m, nil
	}

	if m.isViewportMsg(msg) {
		return m.updateViewport(msg)
	}
	return m.updateEditor(msg)
}

func (m cyTUIModel) editorAtFirstVisualLine() bool {
	line := m.input.LineInfo()
	return m.input.Line() == 0 && line.RowOffset == 0
}

func (m cyTUIModel) editorAtLastVisualLine() bool {
	line := m.input.LineInfo()
	return m.input.Line() == m.input.LineCount()-1 && line.RowOffset >= line.Height-1
}

func editorCtrlKey(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Mod: tea.ModCtrl, Code: code, BaseCode: code}
}

func (m cyTUIModel) submitInput() (tea.Model, tea.Cmd) {
	input := m.selectedInput()
	if m.loginProvider != "" {
		var cmd tea.Cmd
		m.secret.Reset()
		provider := m.loginProvider
		switchModel := m.loginSwitchModel
		m.loginProvider = ""
		m.loginSwitchModel = false
		m.input.Placeholder = ""
		m.configureCommandSuggestions()
		if input == "" {
			m.addBlock(screenBlockError, "API key is required")
		} else if err := m.agent.Login(provider, input); err != nil {
			m.addBlock(screenBlockError, "login: "+err.Error())
		} else {
			m.addBlock(screenBlockSystem, "logged in to "+provider)
			if switchModel && modelProvider(m.cfg.ModelURI) != provider {
				modelURI := firstProviderModel(m.agent.KnownModels(), provider)
				if modelURI == "" {
					m.addBlock(screenBlockError, fmt.Sprintf("login: no model is known for %s", provider))
				} else {
					cmd = m.startModelSwitch(modelURI)
				}
			}
		}
		m.refreshViewport(true)
		return m, cmd
	}
	if m.working && strings.HasPrefix(strings.TrimSpace(input), "/") {
		m.addBlock(screenBlockError, "commands are unavailable while Cy is working; wait for the turn to finish or cancel it")
		m.input.SetValue(input)
		m.input.CursorEnd()
		m.refreshViewport(true)
		return m, nil
	}
	m.input.Reset()
	m.configureCommandSuggestions()
	m.historyIndex = len(m.history)
	m.savedInput = ""
	if input == "" {
		return m, nil
	}
	if len(m.history) == 0 || m.history[len(m.history)-1] != input {
		m.history = append(m.history, input)
	}
	m.historyIndex = len(m.history)

	if m.working {
		if err := m.agent.QueueInput(input); err != nil {
			m.addBlock(screenBlockError, "queue input: "+err.Error())
			m.input.SetValue(input)
			m.input.CursorEnd()
		} else {
			m.addBlock(screenBlockSystem, "queued: "+preview(input, 120))
		}
		m.refreshViewport(true)
		return m, nil
	}
	if handled, done, cmd := m.handleCommand(input); handled {
		m.refreshViewport(true)
		if done {
			m.cancelTurn()
			return m, tea.Quit
		}
		return m, cmd
	}

	m.addBlock(screenBlockUser, input)
	cmd := m.startTurn(input)
	m.refreshViewport(true)
	return m, cmd
}

func (m *cyTUIModel) startTurn(input string) tea.Cmd {
	m.working = true
	m.turnStartedAt = time.Now()
	m.turnChangedPaths = nil
	m.events = make(chan tea.Msg, 128)
	turnCtx, cancel := context.WithCancel(m.ctx)
	m.turnCancel = cancel
	return tea.Batch(m.spinner.Tick, runAgentTurnCmd(turnCtx, m.agent, input, m.events), waitAgentMsg(m.events))
}

func (m *cyTUIModel) claimQueuedInput() (string, bool) {
	input, found, err := m.agent.ClaimQueued()
	if err != nil {
		m.addBlock(screenBlockError, "claim queued input: "+err.Error())
		return "", false
	}
	return input, found
}

func (m *cyTUIModel) restoreQueuedInput() {
	restored, err := m.agent.RestoreQueued()
	if err != nil {
		m.addBlock(screenBlockError, "restore queued input: "+err.Error())
		return
	}
	if len(restored) == 0 {
		return
	}
	if draft := strings.TrimSpace(m.input.Value()); draft != "" {
		restored = append(restored, draft)
	}
	m.input.SetValue(strings.Join(restored, " "))
	m.input.CursorEnd()
	m.addBlock(screenBlockSystem, "cancelled; queued input restored to editor")
}
