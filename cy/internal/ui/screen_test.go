package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/color"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/levmv/golems/cy/internal/engine"
	"github.com/levmv/golems/cy/internal/session"
	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/llm"
)

func (m cyTUIModel) frameView() tea.View {
	frame := m.inlineFrame()
	parts := append([]string(nil), frame.transcript...)
	parts = append(parts, frame.dynamic...)
	view := tea.NewView(strings.Join(parts, "\n"))
	view.Cursor = frame.cursor
	return view
}

func TestTUIKeepsFullSourceBackedTranscript(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.resize(40, 6)
	model.blocks = nil
	for i := 0; i < 6; i++ {
		model.addBlock(screenBlockSystem, fmt.Sprintf("line %d", i))
	}
	model.refreshScreen()

	visible := strings.Join(model.transcriptLines, "\n")
	if !strings.Contains(visible, "line 0") || !strings.Contains(visible, "line 5") {
		t.Fatalf("source-backed transcript = %q, want all lines", visible)
	}
}

func TestTUIViewDoesNotMaterializeTranscript(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.resize(80, 24)
	model.addBlock(screenBlockAssistant, "large transcript sentinel")
	model.refreshScreen()

	if view := model.View(); view.Content != "" || view.Cursor != nil {
		t.Fatalf("Bubble Tea view materialized the custom-rendered frame: %#v", view)
	}
	if rendered := model.frameView().Content; !strings.Contains(rendered, "large transcript sentinel") {
		t.Fatalf("explicit frame view missed transcript: %q", rendered)
	}
}

func TestTUITextDeltasRenderAtFrameBoundary(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.resize(40, 12)
	model.blocks = []screenBlock{{kind: screenBlockAssistant, text: "before"}}
	model.refreshScreen()

	updated, _ := model.Update(agentStreamMsg{event: golem.StreamEvent{Kind: golem.EventTextDelta, Text: " after"}})
	got := updated.(cyTUIModel)
	if !got.renderPending {
		t.Fatal("text delta did not schedule a transcript frame")
	}
	if strings.Contains(strings.Join(got.transcriptLines, "\n"), "before after") {
		t.Fatal("text delta rebuilt the live transcript before the frame boundary")
	}

	updated, _ = got.Update(transcriptRenderMsg{})
	got = updated.(cyTUIModel)
	if got.renderPending || !strings.Contains(strings.Join(got.transcriptLines, "\n"), "before after") {
		t.Fatalf("frame did not flush text delta: pending=%v content=%q", got.renderPending, got.transcriptLines)
	}
}

func TestTUIRefreshTracksDirtyTranscriptSuffix(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.resize(80, 24)
	model.clearTranscript()
	model.addBlock(screenBlockSystem, "stable prefix")
	model.addBlock(screenBlockAssistant, "old tail")
	model.refreshScreen()
	prefixLines := model.renderCache[0].end
	model.transcriptDirty = false

	model.markBlockDirty(1)
	model.blocks[1].text = "new tail"
	model.refreshScreen()

	if !model.transcriptDirty || model.transcriptDirtyFrom != prefixLines {
		t.Fatalf("dirty transcript starts at line %d (changed=%v), want %d", model.transcriptDirtyFrom, model.transcriptDirty, prefixLines)
	}
	if got := strings.Join(model.transcriptLines, "\n"); !strings.Contains(got, "stable prefix") || !strings.Contains(got, "new tail") || strings.Contains(got, "old tail") {
		t.Fatalf("updated transcript = %q", got)
	}
}

func TestTUILongInputWrapsAndGrowsComposer(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.resize(24, 14)
	model.input.SetValue(strings.Repeat("long message ", 8))
	model.refreshScreen()

	if model.input.Height() <= 1 {
		t.Fatalf("composer height = %d, want wrapped multi-line input", model.input.Height())
	}
	for _, line := range strings.Split(model.input.View(), "\n") {
		if visibleLen(line) > model.lineWidth() {
			t.Fatalf("wrapped input line width = %d, limit = %d: %q", visibleLen(line), model.lineWidth(), line)
		}
	}
	for index, line := range model.inlineFrame().dynamic {
		if strings.Contains(line, "\n") {
			t.Fatalf("dynamic row %d contains an embedded newline: %q", index, line)
		}
	}
}

func TestTUIMultilinePasteKeepsRendererRowsAligned(t *testing.T) {
	var output bytes.Buffer
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", &output)
	model.resize(32, 14)
	model.clearTranscript()
	for index := range 20 {
		model.addBlock(screenBlockSystem, fmt.Sprintf("transcript line %d", index))
	}
	model.refreshScreen()
	if err := model.renderer.RenderFrame(model.inlineFrame(), model.width, model.height); err != nil {
		t.Fatalf("render initial frame: %v", err)
	}
	model.transcriptDirty = false
	output.Reset()

	updated, _ := model.Update(tea.PasteMsg{Content: "alpha\nbeta\ngamma"})
	got := updated.(cyTUIModel)
	if got.input.Value() != "alpha\nbeta\ngamma" {
		t.Fatalf("pasted value = %q", got.input.Value())
	}
	if rendered := output.String(); strings.Contains(rendered, "\x1b[2J") || strings.Contains(rendered, "\x1b[3J") {
		t.Fatalf("paste triggered a full-screen clear: %q", rendered)
	}
	assertEditorFrameRows(t, got, "gamma")

	backspace := tea.KeyPressMsg{Code: tea.KeyBackspace}
	for range len("gamma") + 1 {
		updated, _ = got.Update(backspace)
		got = updated.(cyTUIModel)
	}
	if got.input.Value() != "alpha\nbeta" {
		t.Fatalf("value after deleting the last line = %q", got.input.Value())
	}
	assertEditorFrameRows(t, got, "beta")
}

func assertEditorFrameRows(t *testing.T, model cyTUIModel, cursorLine string) {
	t.Helper()
	frame := model.inlineFrame()
	for index, line := range frame.dynamic {
		if strings.Contains(line, "\n") {
			t.Fatalf("dynamic row %d contains an embedded newline: %q", index, line)
		}
	}
	if frame.cursor == nil {
		t.Fatal("frame has no editor cursor")
	}
	dynamicCursorRow := frame.cursor.Position.Y - len(frame.transcript)
	if dynamicCursorRow < 0 || dynamicCursorRow >= len(frame.dynamic) {
		t.Fatalf("cursor row %d is outside %d dynamic rows", dynamicCursorRow, len(frame.dynamic))
	}
	if line := frame.dynamic[dynamicCursorRow]; !strings.Contains(line, cursorLine) {
		t.Fatalf("cursor points at dynamic row %q, want row containing %q", line, cursorLine)
	}
	if len(model.renderer.previousDynamic) != len(frame.dynamic) {
		t.Fatalf("renderer retained %d dynamic rows, frame has %d", len(model.renderer.previousDynamic), len(frame.dynamic))
	}
}

func TestTUIWidthChangeReflowsManagedMarkdown(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.blocks = []screenBlock{{
		kind: screenBlockAssistant,
		text: "| Name | Description |\n| --- | --- |\n| resize | a deliberately long table cell that must be laid out again |",
	}}
	model.resize(80, 24)
	model.refreshScreen()
	wide := strings.Join(model.transcriptLines, "\n")

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 36, Height: 24})
	got := updated.(cyTUIModel)
	narrow := strings.Join(got.transcriptLines, "\n")
	if narrow == wide || len(strings.Split(narrow, "\n")) <= len(strings.Split(wide, "\n")) {
		t.Fatalf("markdown was not re-rendered for the new width:\nwide:\n%s\n\nnarrow:\n%s", wide, narrow)
	}
}

func TestTUIWorkingIndicatorUsesRowAboveEditor(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.resize(80, 12)
	model.working = true
	model.turnStartedAt = time.Now().Add(-3 * time.Second)
	model.input.SetValue("draft")
	model.refreshScreen()

	lines := strings.Split(model.frameView().Content, "\n")
	workingRow := len(model.transcriptLines)
	if workingRow+1 >= len(lines) {
		t.Fatalf("view has %d lines, want working and editor rows after transcript row %d", len(lines), workingRow)
	}
	if !strings.Contains(lines[workingRow], "Working (3s • esc to interrupt)") {
		t.Fatalf("row above editor = %q, want working indicator", lines[workingRow])
	}
	if !strings.Contains(lines[workingRow+1], "draft") {
		t.Fatalf("editor row = %q, want draft", lines[workingRow+1])
	}
	if strings.Contains(lines[len(lines)-1], "Working") {
		t.Fatalf("footer still contains working indicator: %q", lines[len(lines)-1])
	}
}

func TestTUIShowsJournalRepairNotice(t *testing.T) {
	model := newCyTUIModel(context.Background(), repairedScreenAgent{}, Config{}, ".", nil)
	model.resize(80, 24)
	transcript := strings.Join(model.renderTranscriptLines(), "\n")
	if !strings.Contains(transcript, "repaired an incomplete session journal tail") {
		t.Fatalf("transcript missed journal repair notice: %q", transcript)
	}
}

func TestTUINewlineShortcutsDoNotSubmit(t *testing.T) {
	shortcuts := []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{name: "Shift+Enter", key: tea.KeyPressMsg{Mod: tea.ModShift, Code: tea.KeyEnter}},
		{name: "Alt+Enter", key: tea.KeyPressMsg{Mod: tea.ModAlt, Code: tea.KeyEnter}},
		{name: "Ctrl+J", key: tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'j', BaseCode: 'j'}},
	}
	for _, shortcut := range shortcuts {
		t.Run(shortcut.name, func(t *testing.T) {
			model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
			model.resize(40, 16)
			model.input.SetValue("first line")
			model.input.MoveToEnd()

			updated, cmd := model.Update(shortcut.key)
			got := updated.(cyTUIModel)
			if cmd != nil || got.working {
				t.Fatalf("shortcut submitted input: working=%v cmd=%v", got.working, cmd)
			}
			if got.input.Value() != "first line\n" || got.input.Height() != 2 {
				t.Fatalf("composer = %q, height %d", got.input.Value(), got.input.Height())
			}
		})
	}
}

func TestTUIEnterSubmitsMultilineInput(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.input.SetValue("first line\nsecond line")

	updated, cmd := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := updated.(cyTUIModel)
	if cmd == nil || !got.working {
		t.Fatalf("Enter did not start turn: working=%v cmd=%v", got.working, cmd)
	}
	last := got.blocks[len(got.blocks)-1]
	if last.kind != screenBlockUser || last.text != "first line\nsecond line" {
		t.Fatalf("submitted block = %#v", last)
	}
	if got.input.Value() != "" || got.input.Height() != 1 {
		t.Fatalf("composer did not reset: value=%q height=%d", got.input.Value(), got.input.Height())
	}
}

func TestTUISlashShowsAndNavigatesCommandSuggestions(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.resize(80, 24)

	updated, _ := model.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	got := updated.(cyTUIModel)
	if got.currentCommandSuggestion() != "/help" {
		t.Fatalf("initial command suggestion = %q, want /help", got.currentCommandSuggestion())
	}
	if transcript := strings.Join(got.renderTranscriptLines(), "\n"); strings.Contains(transcript, "/help") {
		t.Fatalf("command suggestions leaked into transcript: %q", transcript)
	}
	lines := strings.Split(got.frameView().Content, "\n")
	gapRow := len(got.transcriptLines)
	inputRow := gapRow + 1
	menuRow := inputRow + got.input.Height()
	if gapRow >= len(lines) || strings.TrimSpace(lines[gapRow]) != "" {
		t.Fatalf("composer gap row = %q, want blank at row %d", lines, gapRow)
	}
	if inputRow >= len(lines) || !strings.Contains(lines[inputRow], "› /") {
		t.Fatalf("input row = %q, want marked slash at row %d", lines, inputRow)
	}
	if menuRow >= len(lines) || !strings.Contains(lines[menuRow], "/help") {
		t.Fatalf("menu did not open below input at row %d: %q", menuRow, lines)
	}
	if menu := strings.Join(lines[menuRow:], "\n"); strings.Contains(menu, "Commands (") || strings.Contains(menu, "Tab complete") {
		t.Fatalf("command menu kept redundant header: %q", menu)
	}

	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	got = updated.(cyTUIModel)
	if got.currentCommandSuggestion() != "/clear" {
		t.Fatalf("command suggestion after Down = %q, want /clear", got.currentCommandSuggestion())
	}
}

func TestTUIEnterRunsSelectedCommandSuggestion(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	updated, _ := model.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	updated, _ = updated.(cyTUIModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := updated.(cyTUIModel)

	if got.input.Value() != "" {
		t.Fatalf("input after selected command = %q, want empty", got.input.Value())
	}
	help := got.blocks[len(got.blocks)-1].text
	for _, command := range tuiCommands {
		if !strings.Contains(help, command.usage) {
			t.Fatalf("/help missed %q: %q", command.usage, help)
		}
	}
	if !strings.Contains(help, "Ctrl+J") {
		t.Fatalf("/help missed newline fallback: %q", help)
	}
}

func TestTUIResumePickerReplacesComposerWithoutHeader(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.resize(80, 24)
	model.picker = pickerState{kind: pickerSession, index: 1, items: []pickerItem{
		{value: "first-session", label: "Investigate flaky tests", description: "4m ago"},
		{value: "second-session", label: "Refactor tool runner", description: "1h ago"},
	}}
	model.refreshScreen()

	transcript := strings.Join(model.renderTranscriptLines(), "\n")
	if strings.Contains(transcript, "Investigate flaky tests") || strings.Contains(transcript, "Refactor tool runner") {
		t.Fatalf("resume picker leaked into transcript: %q", transcript)
	}
	view := model.frameView()
	if view.Cursor != nil {
		t.Fatal("resume picker left the editor cursor visible")
	}
	if strings.Contains(view.Content, "Resume session") || strings.Contains(view.Content, "Enter") {
		t.Fatalf("resume picker kept a redundant header: %q", view.Content)
	}
	if !strings.Contains(view.Content, "Investigate flaky tests") || !strings.Contains(view.Content, "› Refactor tool runner") {
		t.Fatalf("resume picker was not rendered in place of the composer: %q", view.Content)
	}
}

func TestTUIPickerBoldsFocusedItem(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.console.useStyle = true
	model.picker = pickerState{kind: pickerSession, index: 1, items: []pickerItem{
		{value: "current", label: "Current item", current: true},
		{value: "focused", label: "Focused item"},
	}}

	lines := model.renderPicker()
	if len(lines) != 2 {
		t.Fatalf("picker lines = %q", lines)
	}
	if !strings.Contains(lines[0], model.accentStyle.Render("Current item")) {
		t.Fatalf("current item is not accented: %q", lines[0])
	}
	if !strings.Contains(lines[1], model.selectionStyle.Render("Focused item")) {
		t.Fatalf("focused item is not bold: %q", lines[1])
	}
}

func TestTUIDarkThemeUsesDarkUserPanelAndBrightStatuses(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{TerminalTheme: terminalThemeDark}, ".", nil)
	if !model.darkTheme || model.themePending {
		t.Fatalf("dark theme state: dark=%v pending=%v", model.darkTheme, model.themePending)
	}

	wantUser := lipgloss.NewStyle().
		Foreground(lipgloss.ANSIColor(252)).
		Background(lipgloss.ANSIColor(236)).
		Render(" user ")
	if got := model.userStyle.Render(" user "); got != wantUser {
		t.Fatalf("dark user style = %q, want %q", got, wantUser)
	}
	wantError := lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(9)).Render("error")
	if got := model.errorStyle.Render("error"); got != wantError {
		t.Fatalf("dark error style = %q, want %q", got, wantError)
	}
}

func TestTUIAutoThemeWaitsForBackgroundBeforeFirstFrame(t *testing.T) {
	var output bytes.Buffer
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{TerminalTheme: terminalThemeAuto}, ".", &output)
	model.resize(80, 24)
	model.refreshScreen()

	updated, _ := model.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	got := updated.(cyTUIModel)
	if !got.themePending || got.renderer.started || output.Len() != 0 {
		t.Fatalf("auto theme rendered before detection: pending=%v started=%v output=%q", got.themePending, got.renderer.started, output.String())
	}

	updated, _ = got.Update(tea.BackgroundColorMsg{Color: color.Black})
	got = updated.(cyTUIModel)
	if got.themePending || !got.darkTheme || !got.renderer.started || output.Len() == 0 {
		t.Fatalf("detected theme state: pending=%v dark=%v started=%v output=%q", got.themePending, got.darkTheme, got.renderer.started, output.String())
	}
}

func TestTUIAutoThemeFallsBackToLight(t *testing.T) {
	var output bytes.Buffer
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{TerminalTheme: terminalThemeAuto}, ".", &output)
	model.resize(80, 24)
	model.refreshScreen()

	updated, _ := model.Update(themeQueryTimeoutMsg{})
	got := updated.(cyTUIModel)
	if got.themePending || got.darkTheme || !got.renderer.started || output.Len() == 0 {
		t.Fatalf("fallback theme state: pending=%v dark=%v started=%v output=%q", got.themePending, got.darkTheme, got.renderer.started, output.String())
	}

	updated, _ = got.Update(tea.BackgroundColorMsg{Color: color.Black})
	if got = updated.(cyTUIModel); got.darkTheme {
		t.Fatal("a late OSC 11 response changed the fallback theme")
	}
}

func TestTUITabCompletesSlashCommand(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	updated, _ := model.Update(tea.KeyPressMsg{Code: 'l', Text: "/lo"})
	updated, _ = updated.(cyTUIModel).Update(tea.KeyPressMsg{Code: tea.KeyTab})
	got := updated.(cyTUIModel)
	if got.input.Value() != "/login" {
		t.Fatalf("completed input = %q, want /login", got.input.Value())
	}
}

func TestTUICompletesProfileArgument(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.input.SetValue("/profile e")
	model.input.CursorEnd()
	model.syncCommandSuggestions()
	if !model.commandSuggestionsVisible() || model.currentCommandSuggestion() != "/profile edit" {
		t.Fatalf("profile suggestion = %q, matches=%#v", model.currentCommandSuggestion(), model.commandSuggestions)
	}
}

func TestTUIMissingCredentialOpensProviderPicker(t *testing.T) {
	control := &authPickerAgent{
		statuses: []providerStatus{
			{Name: "deepseek", Source: "none"},
			{Name: "openai", Source: "none"},
			{Name: "openrouter", Source: "none"},
		},
	}
	model := newCyTUIModel(context.Background(), control, Config{ModelURI: "deepseek/deepseek-v4-flash"}, ".", nil)
	if model.picker.kind != pickerLogin {
		t.Fatal("missing current-provider credential did not open login picker")
	}
	if item := model.picker.items[model.picker.index]; item.value != "deepseek" {
		t.Fatalf("selected login provider = %#v, want deepseek", item)
	}
	model.resize(80, 24)
	view := model.frameView()
	if strings.Contains(view.Content, "Choose login provider") || strings.Contains(view.Content, "select a provider") {
		t.Fatalf("login picker kept redundant hints: %q", view.Content)
	}
	if view.Cursor != nil {
		t.Fatalf("login picker kept input cursor: %#v", view.Cursor)
	}
	lines := strings.Split(view.Content, "\n")
	if pickerRow := len(model.transcriptLines) + 1; pickerRow >= len(lines) || !strings.Contains(lines[pickerRow], "deepseek") {
		t.Fatalf("login picker did not replace editor at row %d: %q", pickerRow, lines)
	}

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	updated, _ = updated.(cyTUIModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := updated.(cyTUIModel)
	if got.picker.active() || got.loginProvider != "openai" || got.secret.EchoMode != textinput.EchoPassword {
		t.Fatalf("provider selection state: active=%v provider=%q echo=%v", got.picker.active(), got.loginProvider, got.secret.EchoMode)
	}
}

func TestTUIInitialLoginSelectionSwitchesProviderModel(t *testing.T) {
	control := &authPickerAgent{
		statuses: []providerStatus{
			{Name: "deepseek", Source: "none"},
			{Name: "openai", Source: "none"},
		},
		models: []string{"deepseek/deepseek-v4-flash", "openai/test-model"},
	}
	model := newCyTUIModel(context.Background(), control, Config{ModelURI: "deepseek/deepseek-v4-flash"}, ".", nil)
	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	updated, _ = updated.(cyTUIModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(cyTUIModel)
	model.secret.SetValue("test-key")
	updated, cmd := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := updated.(cyTUIModel)
	if got.maintenance != "switching model" || cmd == nil || control.switchedModel != "" {
		t.Fatalf("login model switch ran in Update: maintenance=%q switched=%q cmd=%v", got.maintenance, control.switchedModel, cmd)
	}
	if control.loginProvider != "openai" {
		t.Fatalf("logged-in provider = %q, want openai", control.loginProvider)
	}
	updated, _ = got.Update(cmd())
	got = updated.(cyTUIModel)
	if control.switchedModel != "openai/test-model" || got.cfg.ModelURI != "openai/test-model" {
		t.Fatalf("model switch = %q, cfg model = %q", control.switchedModel, got.cfg.ModelURI)
	}
}

func TestTUIStartupPickerUsesConfiguredProvider(t *testing.T) {
	control := &authPickerAgent{
		statuses: []providerStatus{
			{Name: "deepseek", Source: "none"},
			{Name: "openrouter", Source: "environment override"},
		},
		models: []string{"deepseek/deepseek-v4-flash", "openrouter/test-model"},
	}
	model := newCyTUIModel(context.Background(), control, Config{ModelURI: "deepseek/deepseek-v4-flash"}, ".", nil)
	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	updated, cmd := updated.(cyTUIModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := updated.(cyTUIModel)
	if got.loginProvider != "" || control.loginProvider != "" || got.maintenance != "switching model" || cmd == nil {
		t.Fatalf("configured provider selection = login %q/%q, maintenance %q, cmd %v", got.loginProvider, control.loginProvider, got.maintenance, cmd)
	}
	updated, _ = got.Update(cmd())
	got = updated.(cyTUIModel)
	if control.switchedModel != "openrouter/test-model" || got.cfg.ModelURI != "openrouter/test-model" {
		t.Fatalf("model switch = %q, cfg model = %q", control.switchedModel, got.cfg.ModelURI)
	}
}

func TestTUILoginDoesNotReplaceEnvironmentOverride(t *testing.T) {
	control := &authPickerAgent{statuses: []providerStatus{{Name: "deepseek", Source: "environment override"}}}
	model := newCyTUIModel(context.Background(), control, Config{ModelURI: "deepseek/deepseek-v4-flash"}, ".", nil)
	model.input.SetValue("/login")
	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	updated, _ = updated.(cyTUIModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := updated.(cyTUIModel)
	if got.loginProvider != "" || control.loginProvider != "" {
		t.Fatalf("environment login opened key input: model=%q agent=%q", got.loginProvider, control.loginProvider)
	}
	if last := got.blocks[len(got.blocks)-1]; last.kind != screenBlockSystem {
		t.Fatalf("environment login result = %#v", last)
	}
}

func TestTUIBareLogoutOffersStoredCredentials(t *testing.T) {
	control := &authPickerAgent{statuses: []providerStatus{
		{Name: "deepseek", Source: "auth store", Category: "Model providers"},
		{Name: "openrouter", Source: "environment override", Category: "Model providers"},
	}}
	model := newCyTUIModel(context.Background(), control, Config{ModelURI: "openrouter/test-model"}, ".", nil)
	model.input.SetValue("/logout")
	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := updated.(cyTUIModel)
	if got.picker.kind != pickerLogout || len(got.picker.items) != 1 || got.picker.items[0].value != "deepseek" {
		t.Fatalf("logout picker = %#v", got.picker)
	}
	got.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if control.logoutProvider != "deepseek" {
		t.Fatalf("logged out provider = %q", control.logoutProvider)
	}
}

func TestTUILoginPickerGroupsModelsAndServices(t *testing.T) {
	control := &authPickerAgent{statuses: []providerStatus{
		{Name: "deepseek", Source: "auth store", Category: "Model providers", Description: "model provider"},
		{Name: "tavily", Source: "none", Category: "Services", Description: "web search", CredentialURL: "https://app.tavily.com"},
	}}
	model := newCyTUIModel(context.Background(), control, Config{ModelURI: "deepseek/deepseek-v4-flash", Providers: []string{"deepseek", "tavily"}}, ".", nil)
	if model.picker.active() {
		t.Fatal("configured model unexpectedly opened startup login picker")
	}
	model.input.SetValue("/login")
	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := updated.(cyTUIModel)
	if got.picker.kind != pickerLogin || len(got.picker.items) != 2 {
		t.Fatalf("login picker = %#v", got.picker)
	}
	got.resize(100, 24)
	lines := strings.Join(got.renderPicker(), "\n")
	for _, want := range []string{"Model providers", "deepseek", "Services", "tavily", "web search"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("login picker missed %q: %q", want, lines)
		}
	}
	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	updated, _ = updated.(cyTUIModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got = updated.(cyTUIModel)
	if last := got.blocks[len(got.blocks)-1].text; !strings.Contains(last, "https://app.tavily.com") {
		t.Fatalf("Tavily login prompt missed credential URL: %q", last)
	}
}

func TestTUIStartupLoginPickerExcludesServiceCredentials(t *testing.T) {
	control := &authPickerAgent{statuses: []providerStatus{
		{Name: "deepseek", Source: "none", Category: "Model providers", Description: "model provider"},
		{Name: "tavily", Source: "auth store", Category: "Services", Description: "web search"},
	}}
	model := newCyTUIModel(context.Background(), control, Config{ModelURI: "deepseek/deepseek-v4-flash"}, ".", nil)
	if model.picker.kind != pickerLogin || len(model.picker.items) != 1 || model.picker.items[0].value != "deepseek" {
		t.Fatalf("startup picker = %#v", model.picker)
	}
}

func TestTUICompactRunsAsynchronouslyAndCanBeCancelled(t *testing.T) {
	control := &compactControlAgent{}
	model := newCyTUIModel(context.Background(), control, Config{}, ".", nil)
	model.input.SetValue("/compact keep decisions")
	updated, cmd := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := updated.(cyTUIModel)
	if control.called || got.maintenance != "compacting context" || cmd == nil {
		t.Fatalf("compact ran inside Update: called=%v maintenance=%q cmd=%v", control.called, got.maintenance, cmd)
	}
	updated, _ = got.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'c'})
	got = updated.(cyTUIModel)
	batchMessage := cmd()
	batch, ok := batchMessage.(tea.BatchMsg)
	if !ok {
		t.Fatalf("compact command returned %T, want tea.BatchMsg", batchMessage)
	}
	for _, part := range batch {
		if msg := part(); msg != nil {
			if _, ok := msg.(compactDoneMsg); ok {
				updated, _ = got.Update(msg)
				got = updated.(cyTUIModel)
			}
		}
	}
	if !control.called || got.maintenance != "" {
		t.Fatalf("cancelled compact state: called=%v maintenance=%q", control.called, got.maintenance)
	}
}

func TestTUIModelCommandOpensPickerAndSwitchesSelection(t *testing.T) {
	control := &authPickerAgent{
		models: []string{
			"deepseek/deepseek-v4-flash",
			"openrouter/~moonshotai/kimi-latest",
			"ollama/local-model",
		},
	}
	model := newCyTUIModel(context.Background(), control, Config{ModelURI: "openrouter/~moonshotai/kimi-latest"}, ".", nil)
	model.resize(80, 24)
	model.input.SetValue("/model")
	model.input.MoveToEnd()

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := updated.(cyTUIModel)
	if got.picker.kind != pickerModel || len(got.picker.items) != 4 || got.picker.index != 1 {
		t.Fatalf("model picker = %#v", got.picker)
	}
	if transcript := strings.Join(got.renderTranscriptLines(), "\n"); strings.Contains(transcript, "openrouter/~moonshotai/kimi-latest") {
		t.Fatalf("model picker leaked into transcript: %q", transcript)
	}
	view := got.frameView()
	rendered := view.Content
	if !strings.Contains(rendered, "openrouter/~moonshotai/kimi-latest  current") {
		t.Fatalf("model picker missed current marker: %q", rendered)
	}
	if strings.Contains(rendered, "Choose model") || strings.Contains(rendered, "select a model") {
		t.Fatalf("model picker kept redundant hints: %q", rendered)
	}
	if view.Cursor != nil {
		t.Fatalf("model picker kept input cursor: %#v", view.Cursor)
	}
	lines := strings.Split(rendered, "\n")
	if pickerRow := len(got.transcriptLines) + 1; pickerRow >= len(lines) || !strings.Contains(lines[pickerRow], "deepseek/deepseek-v4-flash") {
		t.Fatalf("model picker did not replace editor at row %d: %q", pickerRow, lines)
	}

	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	updated, cmd := updated.(cyTUIModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got = updated.(cyTUIModel)
	if cmd == nil || got.maintenance != "switching model" || control.switchedModel != "" {
		t.Fatalf("model switch ran in Update: maintenance=%q switched=%q cmd=%v", got.maintenance, control.switchedModel, cmd)
	}
	updated, _ = got.Update(cmd())
	got = updated.(cyTUIModel)
	if control.switchedModel != "deepseek/deepseek-v4-flash" || got.cfg.ModelURI != control.switchedModel {
		t.Fatalf("model switch = %q, cfg model = %q", control.switchedModel, got.cfg.ModelURI)
	}
	if got.picker.active() {
		t.Fatalf("model picker remained open: %#v", got.picker)
	}
}

func TestTUIModelPickerAcceptsCustomURI(t *testing.T) {
	control := &authPickerAgent{models: []string{"openrouter/free"}}
	model := newCyTUIModel(context.Background(), control, Config{ModelURI: "openrouter/free"}, ".", nil)
	model.resize(80, 24)
	model.openModelPicker()
	model.picker.index = len(model.picker.items) - 1

	updated, cmd := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := updated.(cyTUIModel)
	if cmd != nil || got.picker.active() || got.input.Value() != "/model " {
		t.Fatalf("custom model entry state: cmd=%v picker=%#v input=%q", cmd, got.picker, got.input.Value())
	}

	const uri = "openrouter/example/new-model"
	got.input.SetValue("/model " + uri)
	got.input.MoveToEnd()
	updated, cmd = got.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got = updated.(cyTUIModel)
	if cmd == nil || got.maintenance != "switching model" {
		t.Fatalf("custom model switch did not start: maintenance=%q cmd=%v", got.maintenance, cmd)
	}
	updated, _ = got.Update(cmd())
	got = updated.(cyTUIModel)
	if control.switchedModel != uri || got.cfg.ModelURI != uri {
		t.Fatalf("custom model switch = %q, cfg model = %q", control.switchedModel, got.cfg.ModelURI)
	}
}

func TestTUIModelPickerCyclesReasoningEffort(t *testing.T) {
	uri := "deepseek/deepseek-v4-flash"
	control := &authPickerAgent{
		models:  []string{uri},
		efforts: map[string][]string{uri: {"", "high"}},
	}
	model := newCyTUIModel(context.Background(), control, Config{ModelURI: uri}, ".", nil)
	model.resize(80, 24)
	model.input.SetValue("/model")
	model.input.MoveToEnd()

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := updated.(cyTUIModel)
	if rendered := got.frameView().Content; !strings.Contains(rendered, "effort: default") {
		t.Fatalf("model picker missed default effort: %q", rendered)
	}

	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	got = updated.(cyTUIModel)
	if rendered := got.frameView().Content; !strings.Contains(rendered, "effort: high") || !strings.Contains(rendered, uri+"  current") {
		t.Fatalf("model picker did not cycle effort while retaining current model: %q", rendered)
	}
	updated, cmd := got.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got = updated.(cyTUIModel)
	if cmd == nil || got.maintenance != "switching model" {
		t.Fatalf("effort switch did not start: maintenance=%q cmd=%v", got.maintenance, cmd)
	}
	updated, _ = got.Update(cmd())
	got = updated.(cyTUIModel)
	if control.switchedModel != uri || control.switchedEffort != "high" {
		t.Fatalf("selection = %q/%q", control.switchedModel, control.switchedEffort)
	}
	if got.cfg.ModelURI != uri || got.cfg.ReasoningEffort != "high" {
		t.Fatalf("TUI config = %#v", got.cfg)
	}
}

func TestTUIModelPickerShowsEffortOnlyForFocusedModel(t *testing.T) {
	currentURI := "deepseek/deepseek-v4-flash"
	otherURI := "openrouter/free"
	control := &authPickerAgent{
		models: []string{currentURI, otherURI},
		efforts: map[string][]string{
			currentURI: {"", "high"},
			otherURI:   {"", "high"},
		},
	}
	model := newCyTUIModel(context.Background(), control, Config{ModelURI: currentURI}, ".", nil)
	model.resize(80, 24)
	model.console.useStyle = false
	model.openModelPicker()

	lines := model.renderPicker()
	if len(lines) != 3 || !strings.Contains(lines[0], "effort: default") || strings.Contains(lines[1], "effort:") || strings.Contains(lines[2], "effort:") {
		t.Fatalf("initial model effort labels = %q", lines)
	}

	updated, _ := model.handlePickerKey(tea.KeyPressMsg{Code: tea.KeyDown})
	lines = updated.(cyTUIModel).renderPicker()
	if len(lines) != 3 || strings.Contains(lines[0], "effort:") || !strings.Contains(lines[1], "effort: default") || strings.Contains(lines[2], "effort:") {
		t.Fatalf("moved model effort labels = %q", lines)
	}
}

func TestTUIProfileCommandOpensPickerAndSwitchesSelection(t *testing.T) {
	control := &profilePickerAgent{profile: "edit"}
	model := newCyTUIModel(context.Background(), control, Config{CapabilityProfile: "edit"}, ".", nil)
	model.resize(80, 24)
	model.input.SetValue("/profile")
	model.input.MoveToEnd()

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := updated.(cyTUIModel)
	if got.picker.kind != pickerProfile || len(got.picker.items) != len(capabilityProfileCatalog) || got.picker.index != 1 {
		t.Fatalf("profile picker = %#v", got.picker)
	}
	if transcript := strings.Join(got.renderTranscriptLines(), "\n"); strings.Contains(transcript, "read-only") {
		t.Fatalf("profile picker leaked into transcript: %q", transcript)
	}
	got.console.useStyle = false
	view := got.frameView()
	if !strings.Contains(view.Content, "› edit  current  read, write, and available web · no Bash") {
		t.Fatalf("profile picker missed selected current profile: %q", view.Content)
	}
	if view.Cursor != nil {
		t.Fatalf("profile picker kept input cursor: %#v", view.Cursor)
	}
	lines := strings.Split(view.Content, "\n")
	if pickerRow := len(got.transcriptLines) + 1; pickerRow >= len(lines) || !strings.Contains(lines[pickerRow], "read-only  read, search, and available web · no writes") {
		t.Fatalf("profile picker did not replace editor at row %d: %q", pickerRow, lines)
	}

	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	updated, _ = updated.(cyTUIModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got = updated.(cyTUIModel)
	if control.switched != "full" || control.profile != "full" || got.cfg.CapabilityProfile != "full" {
		t.Fatalf("profile switch = %q current=%q cfg=%q", control.switched, control.profile, got.cfg.CapabilityProfile)
	}
	if got.picker.active() {
		t.Fatalf("profile picker remained open: %#v", got.picker)
	}
}

func TestTUIHotkeysUseBaseCode(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.history = []string{"first", "second"}
	model.historyIndex = len(model.history)

	updated, _ := model.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'з', Text: "з", BaseCode: 'p'})
	got := updated.(cyTUIModel).input.Value()
	if got != "second" {
		t.Fatalf("input value = %q, want second", got)
	}
}

func TestTUIHotkeysFallbackToRussianLayout(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.history = []string{"first", "second"}
	model.historyIndex = 0
	model.input.SetValue("first")

	updated, _ := model.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'т', Text: "т"})
	got := updated.(cyTUIModel).input.Value()
	if got != "second" {
		t.Fatalf("input value = %q, want second", got)
	}
}

func TestTUIArrowHistoryStartsOnlyFromBlankEditor(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.resize(40, 16)
	model.history = []string{"older prompt"}
	model.historyIndex = len(model.history)
	model.input.SetValue("first line\nsecond line")
	model.input.MoveToEnd()

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	got := updated.(cyTUIModel)
	if got.input.Value() != "first line\nsecond line" || got.historyIndex != len(got.history) {
		t.Fatalf("Up inside editor opened history: value=%q index=%d", got.input.Value(), got.historyIndex)
	}
	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	got = updated.(cyTUIModel)
	if got.input.Value() != "first line\nsecond line" || got.historyIndex != len(got.history) {
		t.Fatalf("Up at the top replaced a non-empty draft: value=%q index=%d", got.input.Value(), got.historyIndex)
	}

	got.input.Reset()
	got.historyIndex = len(got.history)
	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	got = updated.(cyTUIModel)
	if got.input.Value() != "older prompt" || got.historyIndex != 0 {
		t.Fatalf("Up at first line did not open history: value=%q index=%d", got.input.Value(), got.historyIndex)
	}

	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	got = updated.(cyTUIModel)
	if got.input.Value() != "" || got.historyIndex != len(got.history) {
		t.Fatalf("Down past newest history did not restore blank draft: value=%q index=%d", got.input.Value(), got.historyIndex)
	}
}

func TestTUIHistoryRestoresBlankAfterCommandPrompt(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.history = []string{"/help"}
	model.historyIndex = len(model.history)

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	got := updated.(cyTUIModel)
	if got.input.Value() != "/help" || !got.commandSuggestionsVisible() {
		t.Fatalf("command history was not restored with suggestions: value=%q visible=%v", got.input.Value(), got.commandSuggestionsVisible())
	}

	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	got = updated.(cyTUIModel)
	if got.input.Value() != "" || got.historyIndex != len(got.history) {
		t.Fatalf("Down selected a suggestion instead of restoring blank: value=%q index=%d", got.input.Value(), got.historyIndex)
	}
}

func TestTUICtrlPEntersHistoryFromNonEmptyDraft(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.history = []string{"older prompt"}
	model.historyIndex = len(model.history)
	model.input.SetValue("current draft")

	updated, _ := model.Update(editorCtrlKey('p'))
	got := updated.(cyTUIModel)
	if got.input.Value() != "older prompt" || got.savedInput != "current draft" {
		t.Fatalf("Ctrl-P did not preserve the draft: value=%q saved=%q", got.input.Value(), got.savedInput)
	}

	updated, _ = got.Update(editorCtrlKey('n'))
	got = updated.(cyTUIModel)
	if got.input.Value() != "current draft" || got.historyIndex != len(got.history) {
		t.Fatalf("Ctrl-N did not restore the draft: value=%q index=%d", got.input.Value(), got.historyIndex)
	}
}

func TestTUIRestoresInputHistoryFromSession(t *testing.T) {
	model := newCyTUIModel(context.Background(), &resumeScreenAgent{}, Config{}, ".", nil)

	if len(model.history) != 1 || model.history[0] != "hey" || model.historyIndex != len(model.history) {
		t.Fatalf("restored input history = %#v at %d", model.history, model.historyIndex)
	}
}

func TestTUIClearResetsInputHistory(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.history = []string{"old prompt"}
	model.historyIndex = len(model.history)
	model.savedInput = "draft"
	model.input.SetValue("/clear")

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := updated.(cyTUIModel)
	if len(got.history) != 0 || got.historyIndex != 0 || got.savedInput != "" {
		t.Fatalf("input history after clear = %#v at %d, saved %q", got.history, got.historyIndex, got.savedInput)
	}
}

func TestTUIArrowHistoryDoesNotReplaceSoftWrappedDraft(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.resize(20, 16)
	model.history = []string{"older prompt"}
	model.historyIndex = len(model.history)
	model.input.SetValue(strings.Repeat("wrapped ", 8))
	model.input.MoveToEnd()

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	got := updated.(cyTUIModel)
	if got.input.Value() == "older prompt" {
		t.Fatal("Up from the last visual row of wrapped input opened history")
	}

	got.input.MoveToBegin()
	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	got = updated.(cyTUIModel)
	if value := got.input.Value(); value != strings.Repeat("wrapped ", 8) || got.historyIndex != len(got.history) {
		t.Fatalf("Up from first visual row replaced draft: value=%q index=%d", value, got.historyIndex)
	}
}

func TestTUIInputControlHotkeysFallbackToRussianLayout(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.input.SetValue("prefix suffix")
	model.input.SetCursorColumn(6)

	updated, _ := model.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'г', Text: "г"})
	got := updated.(cyTUIModel)
	if got.input.Value() != " suffix" {
		t.Fatalf("input value = %q, want suffix tail", got.input.Value())
	}
	if got.input.LineInfo().CharOffset != 0 {
		t.Fatalf("cursor = %d, want 0", got.input.LineInfo().CharOffset)
	}
}

func TestTUIEnterWhileWorkingQueuesInput(t *testing.T) {
	agent := &queueScreenAgent{}
	model := newCyTUIModel(context.Background(), agent, Config{}, ".", nil)
	model.working = true
	model.input.SetValue("inspect tests too")

	updated, cmd := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := updated.(cyTUIModel)
	if cmd != nil {
		t.Fatal("queueing input unexpectedly returned a command")
	}
	if len(agent.queued) != 1 || agent.queued[0] != "inspect tests too" {
		t.Fatalf("queued input = %#v", agent.queued)
	}
	if got.input.Value() != "" {
		t.Fatalf("input was not cleared: %q", got.input.Value())
	}
	if strings.Contains(got.blocks[len(got.blocks)-1].text, "already working") {
		t.Fatalf("working input was rejected: %#v", got.blocks)
	}
}

func TestTUISlashCommandWhileWorkingIsNotQueued(t *testing.T) {
	agent := &queueScreenAgent{}
	model := newCyTUIModel(context.Background(), agent, Config{}, ".", nil)
	model.working = true
	model.input.SetValue("/model")

	updated, cmd := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := updated.(cyTUIModel)
	if cmd != nil {
		t.Fatal("rejected command unexpectedly returned a command")
	}
	if len(agent.queued) != 0 {
		t.Fatalf("slash command was queued: %#v", agent.queued)
	}
	if got.input.Value() != "/model" {
		t.Fatalf("rejected command was not preserved: %q", got.input.Value())
	}
	if !strings.Contains(got.blocks[len(got.blocks)-1].text, "commands are unavailable") {
		t.Fatalf("missing command guidance: %#v", got.blocks)
	}
}

func TestTUICtrlCCancelsWorkingTurnWithoutExiting(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.working = true
	cancelled := false
	model.turnCancel = func() { cancelled = true }

	updated, cmd := model.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'c'})
	got := updated.(cyTUIModel)
	if !cancelled {
		t.Fatal("active turn was not cancelled")
	}
	if cmd != nil {
		t.Fatal("Ctrl+C while working requested program exit")
	}
	if !got.working {
		t.Fatal("model left working state before terminal agent message")
	}
}

func TestRunAgentTurnDeliversDoneAfterCancellation(t *testing.T) {
	agent := &cancellableScreenAgent{started: make(chan struct{})}
	events := make(chan tea.Msg, 2)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runAgentTurn(ctx, agent, "review", events)
		close(done)
	}()

	select {
	case <-agent.started:
	case <-time.After(time.Second):
		t.Fatal("agent turn did not start")
	}
	if _, ok := (<-events).(agentStreamMsg); !ok {
		t.Fatal("stream event was not delivered before cancellation")
	}
	cancel()
	select {
	case msg := <-events:
		finished, ok := msg.(agentDoneMsg)
		if !ok || !errors.Is(finished.err, context.Canceled) {
			t.Fatalf("terminal event = %#v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal event was not delivered after cancellation")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("agent command did not return after cancellation")
	}
}

func TestTUIEscapeRestoresUndeliveredQueue(t *testing.T) {
	agent := &queueScreenAgent{queued: []string{"queued text"}}
	model := newCyTUIModel(context.Background(), agent, Config{}, ".", nil)
	model.working = true
	model.input.SetValue("draft")
	cancelled := false
	model.turnCancel = func() { cancelled = true }

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	got := updated.(cyTUIModel)
	if !cancelled {
		t.Fatal("Escape did not cancel the active turn")
	}
	if len(agent.queued) != 0 {
		t.Fatalf("queue was not drained: %#v", agent.queued)
	}
	if got.input.Value() != "queued text draft" {
		t.Fatalf("restored input = %q", got.input.Value())
	}
}

func TestTUIAgentDoneAddsWorkedForDivider(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.resize(60, 20)
	model.working = true
	model.turnStartedAt = time.Now().Add(-91 * time.Second)
	model.blocks = append(model.blocks, screenBlock{kind: screenBlockAssistant, text: "done"})

	updated, _ := model.Update(agentDoneMsg{})
	got := updated.(cyTUIModel)
	if got.working || !got.turnStartedAt.IsZero() {
		t.Fatalf("turn timing did not finish: working=%v started=%s", got.working, got.turnStartedAt)
	}
	last := got.blocks[len(got.blocks)-1]
	if last.kind != screenBlockTurnDuration {
		t.Fatalf("last block = %#v, want turn duration", last)
	}
	rendered := strings.Join(got.renderTranscriptLines(), "\n")
	if !strings.Contains(rendered, "─ Worked for 1m 31s ") {
		t.Fatalf("transcript missed worked-for divider: %q", rendered)
	}
	if line := got.renderTurnDurationLine(last.turnDuration); visibleLen(line) != got.lineWidth() {
		t.Fatalf("worked-for divider width=%d, want %d: %q", visibleLen(line), got.lineWidth(), line)
	}
}

func TestTUICancelledTurnAddsDurationWithoutError(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.working = true
	model.turnStartedAt = time.Now().Add(-2 * time.Second)

	updated, _ := model.Update(agentDoneMsg{err: context.Canceled})
	got := updated.(cyTUIModel)
	if len(got.blocks) == 0 || got.blocks[len(got.blocks)-1].kind != screenBlockTurnDuration {
		t.Fatalf("cancelled turn blocks = %#v, want duration divider", got.blocks)
	}
	for _, block := range got.blocks {
		if block.kind == screenBlockError {
			t.Fatalf("cancelled turn rendered an error: %#v", block)
		}
	}
}

func TestTUIStartsQueuedInputAfterTurnSettles(t *testing.T) {
	agent := &queueScreenAgent{queued: []string{"next turn"}}
	model := newCyTUIModel(context.Background(), agent, Config{}, ".", nil)
	model.working = true

	updated, cmd := model.Update(agentDoneMsg{})
	got := updated.(cyTUIModel)
	if cmd == nil || !got.working {
		t.Fatalf("queued turn did not start: working=%v cmd=%v", got.working, cmd)
	}
	if got.turnStartedAt.IsZero() {
		t.Fatal("queued turn did not start a fresh timer")
	}
	if len(agent.queued) != 0 || got.blocks[len(got.blocks)-1].kind != screenBlockUser || got.blocks[len(got.blocks)-1].text != "next turn" {
		t.Fatalf("queued turn state = queue %#v blocks %#v", agent.queued, got.blocks)
	}
}

type screenAgentStub struct{}

func (screenAgentStub) Stream(context.Context, string, golem.StreamFunc) (*golem.Turn, error) {
	return &golem.Turn{}, nil
}
func (screenAgentStub) SessionHistory() ([]llm.Message, error)   { return nil, nil }
func (screenAgentStub) SessionUsage() (llm.Usage, error)         { return llm.Usage{}, nil }
func (screenAgentStub) SessionID() string                        { return "" }
func (screenAgentStub) SessionRepaired() bool                    { return false }
func (screenAgentStub) QueueInput(string) error                  { return nil }
func (screenAgentStub) ClaimQueued() (string, bool, error)       { return "", false, nil }
func (screenAgentStub) RestoreQueued() ([]string, error)         { return nil, nil }
func (screenAgentStub) ClearSession() (string, error)            { return "", nil }
func (screenAgentStub) ResumeSession(string) (string, error)     { return "", nil }
func (screenAgentStub) ListSessions() ([]session.Summary, error) { return nil, nil }
func (screenAgentStub) ContextReport() (engine.ContextReport, error) {
	return engine.ContextReport{}, nil
}
func (screenAgentStub) CachedContextReport() engine.ContextReport { return engine.ContextReport{} }
func (screenAgentStub) Compact(context.Context, string) (engine.ContextReport, error) {
	return engine.ContextReport{}, nil
}
func (screenAgentStub) ProviderStatuses() ([]providerStatus, error) { return nil, nil }
func (screenAgentStub) Login(string, string) error                  { return nil }
func (screenAgentStub) Logout(string) error                         { return nil }
func (screenAgentStub) SwitchModelWithEffort(string, string) error  { return nil }
func (screenAgentStub) KnownModels() []string                       { return nil }
func (screenAgentStub) CurrentModel() string                        { return "" }
func (screenAgentStub) CurrentReasoningEffort() string              { return "" }
func (screenAgentStub) ReasoningEfforts(string) []string            { return []string{""} }
func (screenAgentStub) CurrentProfile() string                      { return "" }
func (screenAgentStub) SwitchProfile(string) error                  { return nil }
func (screenAgentStub) ProcessStatus(string) (processResultMeta, bool) {
	return processResultMeta{}, false
}

type repairedScreenAgent struct{ screenAgentStub }

func (repairedScreenAgent) SessionRepaired() bool { return true }

type resumeScreenAgent struct {
	screenAgentStub
	resumed string
}

func (a *resumeScreenAgent) ResumeSession(idOrPrefix string) (string, error) {
	a.resumed = idOrPrefix
	return "resolved-session", nil
}

func (a *resumeScreenAgent) SessionHistory() ([]llm.Message, error) {
	return []llm.Message{{Role: llm.RoleUser, Content: "hey"}}, nil
}

type queueScreenAgent struct {
	screenAgentStub
	queued []string
}

type cancellableScreenAgent struct {
	screenAgentStub
	started chan struct{}
}

func (a *cancellableScreenAgent) Stream(ctx context.Context, _ string, emit golem.StreamFunc) (*golem.Turn, error) {
	emit(golem.StreamEvent{Kind: golem.EventStatus, Text: "started"})
	close(a.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (a *queueScreenAgent) QueueInput(input string) error {
	a.queued = append(a.queued, input)
	return nil
}

func (a *queueScreenAgent) ClaimQueued() (string, bool, error) {
	if len(a.queued) == 0 {
		return "", false, nil
	}
	input := a.queued[0]
	a.queued = a.queued[1:]
	return input, true, nil
}

func (a *queueScreenAgent) RestoreQueued() ([]string, error) {
	restored := append([]string(nil), a.queued...)
	a.queued = nil
	return restored, nil
}

type authPickerAgent struct {
	screenAgentStub
	statuses       []providerStatus
	models         []string
	loginProvider  string
	logoutProvider string
	switchedModel  string
	switchedEffort string
	currentEffort  string
	efforts        map[string][]string
}

type compactControlAgent struct {
	screenAgentStub
	called bool
}

func (a *compactControlAgent) ContextReport() (engine.ContextReport, error) {
	return engine.ContextReport{}, nil
}

func (a *compactControlAgent) Compact(ctx context.Context, _ string) (engine.ContextReport, error) {
	a.called = true
	<-ctx.Done()
	return engine.ContextReport{}, ctx.Err()
}

func (a *authPickerAgent) ProviderStatuses() ([]providerStatus, error) {
	return append([]providerStatus(nil), a.statuses...), nil
}

func (a *authPickerAgent) Login(provider, _ string) error {
	a.loginProvider = provider
	return nil
}

func (a *authPickerAgent) Logout(provider string) error {
	a.logoutProvider = provider
	return nil
}
func (a *authPickerAgent) SwitchModelWithEffort(uri, effort string) error {
	a.switchedModel = uri
	a.switchedEffort = effort
	return nil
}
func (a *authPickerAgent) KnownModels() []string          { return append([]string(nil), a.models...) }
func (a *authPickerAgent) CurrentReasoningEffort() string { return a.currentEffort }
func (a *authPickerAgent) ReasoningEfforts(uri string) []string {
	if efforts := a.efforts[uri]; len(efforts) > 0 {
		return append([]string(nil), efforts...)
	}
	return []string{""}
}

type profilePickerAgent struct {
	screenAgentStub
	profile  string
	switched string
}

func (a *profilePickerAgent) CurrentProfile() string { return a.profile }
func (a *profilePickerAgent) SwitchProfile(profile string) error {
	a.switched = profile
	a.profile = profile
	return nil
}

func TestTUITranscriptSnapshot(t *testing.T) {
	model := cyTUIModel{
		console: &Console{useStyle: false},
		width:   80,
		blocks: []screenBlock{
			{kind: screenBlockBanner},
			{kind: screenBlockInfo, text: "model: openrouter/moonshotai/kimi-k3"},
			{kind: screenBlockUser, text: "inspect the project"},
			{kind: screenBlockTool, text: "read  cy/main.go"},
			{kind: screenBlockTool, text: "$ go test ./cy/..."},
			{kind: screenBlockAssistant, text: "Everything passes."},
		},
	}
	want := strings.Join([]string{
		"  Cy  Type / for commands.",
		"  ",
		"  model: openrouter/moonshotai/kimi-k3",
		"  ",
		"› inspect the project",
		"  ",
		"  ",
		"• read  cy/main.go",
		"• $ go test ./cy/...",
		"  ",
		"• Everything passes.",
		"  ",
	}, "\n")
	if got := strings.Join(model.renderTranscriptLines(), "\n"); got != want {
		t.Fatalf("transcript snapshot:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestTUIUserMessageHasOneMarkerAcrossParagraphs(t *testing.T) {
	for _, styled := range []bool{false, true} {
		model := cyTUIModel{
			console: &Console{useStyle: styled},
			width:   40,
		}
		lines := model.renderUserBlock("first paragraph\nsecond paragraph")
		rendered := strings.Join(lines, "\n")
		if got := strings.Count(rendered, userMarker); got != 1 {
			t.Fatalf("styled=%v marker count=%d, want 1:\n%s", styled, got, rendered)
		}
		if styled && strings.TrimSpace(lines[0]) != "" {
			t.Fatalf("styled user block starts without outside spacing: %q", lines[0])
		}
	}
}

func TestTUIRepeatedRetryUpdatesPreviousBlock(t *testing.T) {
	model := cyTUIModel{}
	model.applyStreamEvent(golem.StreamEvent{Kind: golem.EventModelRetry, Text: "#1 in 1s — openrouter 429: busy", RetryKey: "openrouter 429: busy"})
	model.applyStreamEvent(golem.StreamEvent{Kind: golem.EventModelRetry, Text: "#2 in 2s — openrouter 429: busy", RetryKey: "openrouter 429: busy"})
	if len(model.blocks) != 1 || !strings.Contains(model.blocks[0].text, "#2") {
		t.Fatalf("repeated retry blocks = %#v, want one updated block", model.blocks)
	}

	model.applyStreamEvent(golem.StreamEvent{Kind: golem.EventModelRetry, Text: "#3 in 4s — gateway timeout", RetryKey: "gateway timeout"})
	if len(model.blocks) != 2 {
		t.Fatalf("different retry cause replaced prior block: %#v", model.blocks)
	}
}

func TestTUIQuitStopsRenderer(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.resize(80, 24)
	model.resetTranscript()
	model.addBlock(screenBlockSystem, "last message")
	model.refreshScreen()

	updated, cmd := model.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'c'})
	got := updated.(cyTUIModel)
	if cmd == nil || !got.quitting || !got.renderer.stopped {
		t.Fatalf("quit state: cmd=%v quitting=%v stopped=%v", cmd, got.quitting, got.renderer.stopped)
	}
}

func TestTUIResumeStagesCleanOldFrameBeforeAppendingSession(t *testing.T) {
	agent := &resumeScreenAgent{}
	model := newCyTUIModel(context.Background(), agent, Config{}, ".", nil)
	model.resize(80, 24)
	model.addBlock(screenBlockAssistant, "old answer")
	model.history = []string{"old prompt"}
	model.historyIndex = len(model.history)
	model.refreshScreen()
	oldBlockCount := len(model.blocks)

	handled, done, cmd := model.handleCommand("/resume target-prefix")
	if !handled || done || cmd == nil {
		t.Fatalf("resume command state: handled=%v done=%v cmd=%v", handled, done, cmd)
	}
	if agent.resumed != "" || len(model.blocks) != oldBlockCount {
		t.Fatalf("resume ran before the old frame could render cleanly: resumed=%q blocks=%d", agent.resumed, len(model.blocks))
	}

	msg, ok := cmd().(resumeSessionMsg)
	if !ok || msg.idOrPrefix != "target-prefix" {
		t.Fatalf("resume command message = %#v", msg)
	}
	updated, _ := model.update(msg)
	got := updated.(cyTUIModel)
	if agent.resumed != "target-prefix" {
		t.Fatalf("resumed prefix = %q", agent.resumed)
	}
	rendered := strings.Join(got.transcriptLines, "\n")
	if strings.Contains(rendered, "old answer") || !strings.Contains(rendered, "resumed session resolved-session") || !strings.Contains(rendered, "hey") {
		t.Fatalf("resumed transcript = %q", rendered)
	}
	if len(got.history) != 1 || got.history[0] != "hey" || got.historyIndex != len(got.history) {
		t.Fatalf("resumed input history = %#v at %d", got.history, got.historyIndex)
	}
}
