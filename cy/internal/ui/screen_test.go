package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/levmv/golems/cy/internal/engine"
	"github.com/levmv/golems/cy/internal/session"
	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/llm"
)

func TestTUIRefreshViewportFollowsBottom(t *testing.T) {
	model := cyTUIModel{
		console:  &Console{useStyle: false},
		viewport: viewport.New(viewport.WithWidth(40), viewport.WithHeight(3)),
	}
	for i := 0; i < 10; i++ {
		model.blocks = append(model.blocks, screenBlock{kind: screenBlockSystem, text: "line"})
	}

	model.refreshViewport(true)

	if !model.viewport.AtBottom() {
		t.Fatalf("viewport is not at bottom after follow refresh; offset=%d", model.viewport.YOffset())
	}
}

func TestTUITextDeltasRenderAtFrameBoundary(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.resize(40, 12)
	model.blocks = []screenBlock{{kind: screenBlockAssistant, text: "before"}}
	model.refreshViewport(true)

	updated, _ := model.Update(agentStreamMsg{event: golem.StreamEvent{Kind: golem.EventTextDelta, Text: " after"}})
	got := updated.(cyTUIModel)
	if !got.renderPending {
		t.Fatal("text delta did not schedule a transcript frame")
	}
	if strings.Contains(got.viewport.GetContent(), "before after") {
		t.Fatal("text delta rebuilt the viewport before the frame boundary")
	}

	updated, _ = got.Update(transcriptRenderMsg{})
	got = updated.(cyTUIModel)
	if got.renderPending || !strings.Contains(got.viewport.GetContent(), "before after") {
		t.Fatalf("frame did not flush text delta: pending=%v content=%q", got.renderPending, got.viewport.GetContent())
	}
}

func TestTUILongInputWrapsAndGrowsComposer(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.resize(24, 14)
	initialViewportHeight := model.viewport.Height()
	model.input.SetValue(strings.Repeat("long message ", 8))
	model.refreshViewport(true)

	if model.input.Height() <= 1 {
		t.Fatalf("composer height = %d, want wrapped multi-line input", model.input.Height())
	}
	if got, want := model.viewport.Height(), model.height-screenFixedRows-model.input.Height(); got != want {
		t.Fatalf("viewport height = %d, want %d", got, want)
	}
	if model.viewport.Height() >= initialViewportHeight {
		t.Fatalf("viewport did not make room for composer: before=%d after=%d", initialViewportHeight, model.viewport.Height())
	}
	for _, line := range strings.Split(model.input.View(), "\n") {
		if visibleLen(line) > model.lineWidth() {
			t.Fatalf("wrapped input line width = %d, limit = %d: %q", visibleLen(line), model.lineWidth(), line)
		}
	}
}

func TestTUIViewEnablesMouseWheelEvents(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.resize(80, 24)
	if mode := model.View().MouseMode; mode != tea.MouseModeCellMotion {
		t.Fatalf("mouse mode = %v, want wheel event capture", mode)
	}
}

func TestTUIWorkingIndicatorUsesRowAboveEditor(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.resize(80, 12)
	model.working = true
	model.turnStartedAt = time.Now().Add(-3 * time.Second)
	model.input.SetValue("draft")
	model.refreshViewport(true)

	lines := strings.Split(model.View().Content, "\n")
	workingRow := model.viewport.Height()
	if workingRow+1 >= len(lines) {
		t.Fatalf("view has %d lines, want working and editor rows after viewport height %d", len(lines), workingRow)
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

func TestTUIMouseWheelScrollsTranscriptInsteadOfInputHistory(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.viewport = viewport.New(viewport.WithWidth(40), viewport.WithHeight(2))
	model.viewport.MouseWheelEnabled = true
	model.viewport.SetContentLines([]string{"one", "two", "three", "four"})
	model.viewport.GotoBottom()
	model.history = []string{"history"}
	model.historyIndex = len(model.history)

	updated, _ := model.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	got := updated.(cyTUIModel)
	if got.input.Value() != "" || got.historyIndex != len(got.history) {
		t.Fatalf("wheel changed input history: value=%q index=%d", got.input.Value(), got.historyIndex)
	}
	if got.viewport.AtBottom() {
		t.Fatalf("wheel did not scroll transcript; offset=%d", got.viewport.YOffset())
	}
}

func TestTUINonWheelMouseEventsPreserveTranscriptScroll(t *testing.T) {
	events := []struct {
		name string
		msg  tea.Msg
	}{
		{name: "motion", msg: tea.MouseMotionMsg{X: 2, Y: 1}},
		{name: "click", msg: tea.MouseClickMsg{X: 2, Y: 1, Button: tea.MouseLeft}},
		{name: "release", msg: tea.MouseReleaseMsg{X: 2, Y: 1, Button: tea.MouseLeft}},
	}

	for _, event := range events {
		t.Run(event.name, func(t *testing.T) {
			model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
			model.viewport = viewport.New(viewport.WithWidth(40), viewport.WithHeight(2))
			model.viewport.SetContentLines([]string{"one", "two", "three", "four"})
			model.viewport.GotoBottom()
			model.viewport.ScrollUp(1)
			before := model.viewport.YOffset()

			updated, cmd := model.Update(event.msg)
			got := updated.(cyTUIModel)
			if cmd != nil {
				t.Fatalf("mouse event returned command: %v", cmd)
			}
			if got.viewport.YOffset() != before {
				t.Fatalf("viewport offset = %d, want %d", got.viewport.YOffset(), before)
			}
		})
	}
}

func TestTUIMouseDragSelectsAndCopiesTranscript(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.viewport = viewport.New(viewport.WithWidth(20), viewport.WithHeight(2))
	model.viewport.SetContentLines([]string{"\x1b[36mhello\x1b[0m world", "second line"})

	updated, _ := model.Update(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	got := updated.(cyTUIModel)
	updated, _ = got.Update(tea.MouseMotionMsg{X: 5, Y: 0, Button: tea.MouseLeft})
	got = updated.(cyTUIModel)
	if selected := got.selectedTranscriptText(); selected != "hello" {
		t.Fatalf("selected text while dragging = %q, want hello", selected)
	}
	if rendered := got.transcriptViewportView(); strings.Contains(rendered, "\x1b[7m") || strings.Contains(rendered, ";7m") ||
		!strings.Contains(rendered, "\x1b[97;44m") && !strings.Contains(rendered, ";44m") {
		t.Fatalf("drag selection does not use the configured selection colors: %q", rendered)
	}

	updated, cmd := got.Update(tea.MouseReleaseMsg{X: 5, Y: 0, Button: tea.MouseLeft})
	got = updated.(cyTUIModel)
	if cmd == nil {
		t.Fatal("mouse release did not copy selected text")
	}
	if copied := fmt.Sprint(cmd()); copied != "hello" {
		t.Fatalf("clipboard content = %q, want hello", copied)
	}
	if got.transcriptSelection.dragging || !got.transcriptSelection.hasRange() {
		t.Fatalf("selection after release = %#v", got.transcriptSelection)
	}
}

func TestTUIClipboardMessagePreservesTranscriptScroll(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.resize(40, 8)
	model.blocks = nil
	for i := 0; i < 10; i++ {
		model.addBlock(screenBlockSystem, fmt.Sprintf("line %d", i))
	}
	model.refreshViewport(true)
	model.viewport.ScrollUp(1)
	before := model.viewport.YOffset()

	updated, _ := model.Update(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	updated, _ = updated.(cyTUIModel).Update(tea.MouseMotionMsg{X: 3, Y: 0, Button: tea.MouseLeft})
	updated, cmd := updated.(cyTUIModel).Update(tea.MouseReleaseMsg{X: 3, Y: 0, Button: tea.MouseLeft})
	if cmd == nil {
		t.Fatal("mouse release did not return clipboard command")
	}
	updated, _ = updated.(cyTUIModel).Update(cmd())
	got := updated.(cyTUIModel)
	if got.viewport.YOffset() != before {
		t.Fatalf("clipboard message changed viewport offset from %d to %d", before, got.viewport.YOffset())
	}
}

func TestTUIMouseSelectionUsesScrolledTranscriptCoordinates(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.viewport = viewport.New(viewport.WithWidth(20), viewport.WithHeight(2))
	model.viewport.SetContentLines([]string{"first", "second", "third"})
	model.viewport.GotoBottom()
	before := model.viewport.YOffset()

	updated, _ := model.Update(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	updated, _ = updated.(cyTUIModel).Update(tea.MouseMotionMsg{X: 3, Y: 1, Button: tea.MouseLeft})
	got := updated.(cyTUIModel)
	if selected := got.selectedTranscriptText(); selected != "second\nthi" {
		t.Fatalf("selected scrolled text = %q", selected)
	}
	if got.viewport.YOffset() != before {
		t.Fatalf("selection changed viewport offset from %d to %d", before, got.viewport.YOffset())
	}
}

func TestTUIShiftEnterInsertsNewlineWithoutSubmitting(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.resize(40, 16)
	model.input.SetValue("first line")
	model.input.MoveToEnd()

	updated, cmd := model.Update(tea.KeyPressMsg{Mod: tea.ModShift, Code: tea.KeyEnter})
	got := updated.(cyTUIModel)
	if cmd != nil {
		t.Fatal("Shift+Enter unexpectedly started a turn")
	}
	if got.input.Value() != "first line\n" {
		t.Fatalf("input = %q, want explicit newline", got.input.Value())
	}
	if got.working {
		t.Fatal("Shift+Enter unexpectedly marked the model working")
	}
	if got.input.Height() != 2 {
		t.Fatalf("composer height = %d, want 2", got.input.Height())
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
	lines := strings.Split(got.View().Content, "\n")
	gapRow := got.viewport.Height()
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
	if last := got.blocks[len(got.blocks)-1].text; !strings.Contains(last, "Type / to browse commands") {
		t.Fatalf("selected /help did not run: %q", last)
	}
}

func TestTUIResumePickerReplacesComposerWithoutHeader(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.resize(80, 24)
	model.picker = pickerState{kind: pickerSession, index: 1, items: []pickerItem{
		{value: "first-session", label: "Investigate flaky tests", description: "4m ago"},
		{value: "second-session", label: "Refactor tool runner", description: "1h ago"},
	}}
	model.refreshViewport(true)

	transcript := strings.Join(model.renderTranscriptLines(), "\n")
	if strings.Contains(transcript, "Investigate flaky tests") || strings.Contains(transcript, "Refactor tool runner") {
		t.Fatalf("resume picker leaked into transcript: %q", transcript)
	}
	view := model.View()
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
	view := model.View()
	if strings.Contains(view.Content, "Choose login provider") || strings.Contains(view.Content, "select a provider") {
		t.Fatalf("login picker kept redundant hints: %q", view.Content)
	}
	if view.Cursor != nil {
		t.Fatalf("login picker kept input cursor: %#v", view.Cursor)
	}
	lines := strings.Split(view.Content, "\n")
	if pickerRow := model.viewport.Height() + 1; pickerRow >= len(lines) || !strings.Contains(lines[pickerRow], "deepseek") {
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
	if got.picker.kind != pickerModel || len(got.picker.items) != 3 || got.picker.index != 1 {
		t.Fatalf("model picker = %#v", got.picker)
	}
	if transcript := strings.Join(got.renderTranscriptLines(), "\n"); strings.Contains(transcript, "openrouter/~moonshotai/kimi-latest") {
		t.Fatalf("model picker leaked into transcript: %q", transcript)
	}
	view := got.View()
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
	if pickerRow := got.viewport.Height() + 1; pickerRow >= len(lines) || !strings.Contains(lines[pickerRow], "deepseek/deepseek-v4-flash") {
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
	if rendered := got.View().Content; !strings.Contains(rendered, "effort: default") {
		t.Fatalf("model picker missed default effort: %q", rendered)
	}

	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	got = updated.(cyTUIModel)
	if rendered := got.View().Content; !strings.Contains(rendered, "effort: high") || !strings.Contains(rendered, uri+"  current") {
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
	if len(lines) != 2 || !strings.Contains(lines[0], "effort: default") || strings.Contains(lines[1], "effort:") {
		t.Fatalf("initial model effort labels = %q", lines)
	}

	updated, _ := model.handlePickerKey(tea.KeyPressMsg{Code: tea.KeyDown})
	lines = updated.(cyTUIModel).renderPicker()
	if len(lines) != 2 || strings.Contains(lines[0], "effort:") || !strings.Contains(lines[1], "effort: default") {
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
	view := got.View()
	if !strings.Contains(view.Content, "› edit  current  read, write, and available web · no Bash") {
		t.Fatalf("profile picker missed selected current profile: %q", view.Content)
	}
	if view.Cursor != nil {
		t.Fatalf("profile picker kept input cursor: %#v", view.Cursor)
	}
	lines := strings.Split(view.Content, "\n")
	if pickerRow := got.viewport.Height() + 1; pickerRow >= len(lines) || !strings.Contains(lines[pickerRow], "read-only  read, search, and available web · no writes") {
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

func TestTUICtrlDownScrollsViewportInsteadOfHistory(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.viewport = viewport.New(viewport.WithWidth(40), viewport.WithHeight(2))
	model.viewport.SetContentLines([]string{"one", "two", "three", "four"})
	model.history = []string{"history"}
	model.historyIndex = 0
	model.input.SetValue("draft")

	updated, _ := model.Update(tea.KeyPressMsg{Mod: tea.ModCtrl, Code: tea.KeyDown})
	got := updated.(cyTUIModel)
	if got.input.Value() != "draft" {
		t.Fatalf("input value = %q, want draft", got.input.Value())
	}
	if got.viewport.YOffset() == 0 {
		t.Fatalf("viewport offset = %d, want scrolled", got.viewport.YOffset())
	}
}

func TestTUIArrowHistoryOnlyAtEditorBoundariesAndRestoresBlankDraft(t *testing.T) {
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

func TestTUIArrowHistoryUsesSoftWrappedVisualBoundaries(t *testing.T) {
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
	if value := updated.(cyTUIModel).input.Value(); value != "older prompt" {
		t.Fatalf("Up from first visual row = %q, want history", value)
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

func (a *authPickerAgent) Logout(string) error { return nil }
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

func TestPrintExitTranscriptWritesBlocks(t *testing.T) {
	var out bytes.Buffer
	model := cyTUIModel{
		console: &Console{out: &out, useStyle: false},
		blocks: []screenBlock{
			{kind: screenBlockSystem, text: "system"},
			{kind: screenBlockUser, text: "hello"},
			{kind: screenBlockAssistant, text: "done"},
		},
	}

	printExitTranscript(&out, model)

	got := out.String()
	if !strings.Contains(got, "system") || !strings.Contains(got, "› hello") || !strings.Contains(got, "done") {
		t.Fatalf("exit transcript missed content: %q", got)
	}
}

func TestPrintExitTranscriptKeepsBoundedTail(t *testing.T) {
	var out bytes.Buffer
	model := cyTUIModel{
		console: &Console{out: &out, useStyle: false},
		width:   80,
	}
	for index := 0; index < maxExitTranscriptLines+20; index++ {
		model.blocks = append(model.blocks, screenBlock{kind: screenBlockSystem, text: fmt.Sprintf("line-%03d", index)})
	}

	printExitTranscript(&out, model)

	got := out.String()
	if !strings.Contains(got, "earlier transcript lines omitted") || strings.Contains(got, "line-000") {
		t.Fatalf("bounded exit transcript = %q", got)
	}
	if !strings.Contains(got, fmt.Sprintf("line-%03d", maxExitTranscriptLines+19)) {
		t.Fatalf("bounded exit transcript missed newest line: %q", got)
	}
}
