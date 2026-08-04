package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/term"
	"github.com/levmv/golems/cy/internal/engine"
	"github.com/levmv/golems/pkg/golem"
)

const (
	screenFixedRows   = 3 // spacing above/below the composer and status line
	composerMaxRows   = 8
	transcriptGutter  = 2
	userMarker        = "›"
	transcriptFrame   = 33 * time.Millisecond
	themeQueryTimeout = 150 * time.Millisecond
)

const (
	terminalThemeAuto  = "auto"
	terminalThemeLight = "light"
	terminalThemeDark  = "dark"
)

type screenBlockKind int

const (
	screenBlockBanner screenBlockKind = iota
	screenBlockSystem
	screenBlockInfo
	screenBlockUser
	screenBlockAssistant
	screenBlockTool
	screenBlockError
	screenBlockTurnDuration
)

type screenBlock struct {
	kind              screenBlockKind
	text              string
	toolName          string
	toolCallID        string
	toolStartedAt     time.Time
	toolElapsedMillis int64
	toolGroupKey      string
	toolGroupItems    string
	retryKey          string
	turnDuration      time.Duration
	fileChange        *fileChangeMeta
	processResult     *processResultMeta
	processSuperseded bool
	processOutput     string
	userInitiated     bool
}

type agentStreamMsg struct {
	event golem.StreamEvent
}

type agentDoneMsg struct {
	err error
}

type compactDoneMsg struct {
	report engine.ContextReport
	err    error
}

type modelSwitchDoneMsg struct {
	uri    string
	effort string
	err    error
}

type shellDoneMsg struct {
	content string
	result  processResultMeta
	err     error
}

type resumeSessionMsg struct {
	idOrPrefix string
}

type processPollMsg struct{}
type transcriptRenderMsg struct{}
type themeQueryTimeoutMsg struct{}

type tuiCommand struct {
	usage       string
	description string
}

var tuiCommands = []tuiCommand{
	{usage: "/help", description: "show commands and keyboard shortcuts"},
	{usage: "/clear", description: "start a new session"},
	{usage: "/resume [id-or-prefix]", description: "choose or resume a previous session"},
	{usage: "/usage", description: "show token usage"},
	{usage: "/context", description: "show context budget"},
	{usage: "/compact [focus]", description: "compact context, optionally with a focus"},
	{usage: "/login [provider]", description: "manage provider credentials"},
	{usage: "/logout [provider]", description: "remove a stored provider credential"},
	{usage: "/model [provider/model]", description: "list or switch models"},
	{usage: "/profile [profile]", description: "show or switch the capability profile"},
	{usage: "/exit", description: "exit Cy"},
}

func (c tuiCommand) name() string {
	name, _, _ := strings.Cut(c.usage, " ")
	return name
}

type pickerKind int

const (
	pickerNone pickerKind = iota
	pickerSession
	pickerModel
	pickerProfile
	pickerLogin
	pickerLogout
)

type pickerItem struct {
	value            string
	label            string
	description      string
	section          string
	credentialURL    string
	credentialSource string
	current          bool
	custom           bool
	efforts          []string
	effortIndex      int
}

type pickerState struct {
	kind         pickerKind
	items        []pickerItem
	index        int
	startupLogin bool
}

func (p pickerState) active() bool { return p.kind != pickerNone && len(p.items) > 0 }

type renderedScreenBlock struct {
	block screenBlock
	end   int
}

type cyTUIModel struct {
	ctx     context.Context
	agent   screenAgent
	cfg     Config
	root    string
	console *Console

	input   textarea.Model
	secret  textinput.Model
	spinner spinner.Model

	blocks              []screenBlock
	width               int
	height              int
	working             bool
	maintenance         string
	maintenanceCancel   context.CancelFunc
	events              chan tea.Msg
	turnCancel          context.CancelFunc
	turnStartedAt       time.Time
	history             []string
	historyIndex        int
	savedInput          string
	picker              pickerState
	renderCache         []renderedScreenBlock
	renderCacheLines    []string
	renderCacheWidth    int
	renderCacheStyled   bool
	renderDirtyFrom     int
	loginProvider       string
	loginSwitchModel    bool
	turnChangedPaths    []string
	processPollPending  bool
	renderPending       bool
	transcriptLines     []string
	transcriptDirty     bool
	transcriptDirtyFrom int
	quitting            bool
	renderer            *inlineRenderer
	renderErr           error
	themePending        bool
	darkTheme           bool

	commandSuggestions        []string
	commandSuggestionIndex    int
	commandSuggestionsEnabled bool

	mutedStyle     lipgloss.Style
	selectionStyle lipgloss.Style
	accentStyle    lipgloss.Style
	errorStyle     lipgloss.Style
	successStyle   lipgloss.Style
	userStyle      lipgloss.Style
}

func CanUseScreen(in io.Reader, out io.Writer) (*os.File, *os.File, bool) {
	inFile, inOK := in.(*os.File)
	outFile, outOK := out.(*os.File)
	if !inOK || !outOK {
		return nil, nil, false
	}
	return inFile, outFile, isTerminalFile(inFile) && isTerminalFile(outFile)
}

func RunScreen(ctx context.Context, agent Agent, cfg Config, root string, in *os.File, out *os.File) (returnErr error) {
	// WithoutRenderer also disables Bubble Tea's terminal initialization. Cy
	// owns rendering, so it must own raw mode and restore it after renderer.Stop
	// has returned the cursor and keyboard protocols to their normal state.
	terminalState, err := term.MakeRaw(in.Fd())
	if err != nil {
		return fmt.Errorf("enter terminal raw mode: %w", err)
	}
	defer func() {
		if err := term.Restore(in.Fd(), terminalState); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("restore terminal: %w", err))
		}
	}()

	model := newCyTUIModel(ctx, agent, cfg, root, out)
	defer func() {
		if err := model.renderer.Stop(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("stop terminal renderer: %w", err))
		}
	}()
	width, height, err := term.GetSize(out.Fd())
	if err != nil {
		return fmt.Errorf("get terminal size: %w", err)
	}
	model.resize(width, height)
	model.refreshScreen()
	if !model.themePending {
		if err := model.renderer.RenderFrame(model.inlineFrame(), width, height); err != nil {
			return fmt.Errorf("render terminal: %w", err)
		}
		model.transcriptDirty = false
	}

	program := tea.NewProgram(model, tea.WithInput(in), tea.WithOutput(out), tea.WithContext(ctx), tea.WithoutRenderer())
	resizeCtx, stopResizeWatcher := context.WithCancel(ctx)
	defer stopResizeWatcher()
	go watchTerminalSize(resizeCtx, program, out, width, height)

	finalModel, err := program.Run()
	if errors.Is(err, tea.ErrInterrupted) {
		return context.Canceled
	}
	if final, ok := finalModel.(cyTUIModel); ok && final.renderErr != nil {
		return final.renderErr
	}
	return err
}

func watchTerminalSize(ctx context.Context, program *tea.Program, out *os.File, width, height int) {
	// Bubble Tea does not retain ttyOutput when WithoutRenderer is active, so
	// its SIGWINCH path cannot query a size. Polling keeps this portable while
	// still coalescing bursts of resize events into authoritative redraws.
	ticker := time.NewTicker(75 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			nextWidth, nextHeight, err := term.GetSize(out.Fd())
			if err != nil || nextWidth <= 0 || nextHeight <= 0 || nextWidth == width && nextHeight == height {
				continue
			}
			width, height = nextWidth, nextHeight
			program.Send(tea.WindowSizeMsg{Width: width, Height: height})
		}
	}
}

func newComposerInput() textarea.Model {
	input := textarea.New()
	plain := lipgloss.NewStyle()
	nativeState := textarea.StyleState{
		Base:             plain,
		Text:             plain,
		LineNumber:       plain,
		CursorLineNumber: plain,
		CursorLine:       plain,
		EndOfBuffer:      plain,
		Placeholder:      plain.Faint(true),
		Prompt:           plain,
	}
	styles := input.Styles()
	styles.Focused = nativeState
	styles.Blurred = nativeState
	styles.Cursor.Color = nil
	input.SetStyles(styles)
	input.Prompt = ""
	input.Placeholder = ""
	input.ShowLineNumbers = false
	input.DynamicHeight = true
	input.MinHeight = 1
	input.MaxHeight = composerMaxRows
	input.MaxWidth = 0
	input.MaxContentHeight = 10000
	input.SetHeight(1)
	input.SetVirtualCursor(false)
	input.SetWidth(1)
	_ = input.Focus()
	return input
}

func newSecretInput() textinput.Model {
	secret := textinput.New()
	secret.Prompt = ""
	secret.SetWidth(1)
	secret.SetVirtualCursor(false)
	_ = secret.Focus()
	return secret
}

func newCyTUIModel(ctx context.Context, agent screenAgent, cfg Config, root string, out io.Writer) cyTUIModel {
	console := NewConsole(out)
	input := newComposerInput()
	secret := newSecretInput()

	spin := spinner.New(spinner.WithSpinner(spinner.Line))
	requestedTheme := strings.ToLower(strings.TrimSpace(cfg.TerminalTheme))
	if requestedTheme == "" {
		requestedTheme = terminalThemeLight
	}
	darkTheme := requestedTheme == terminalThemeDark

	m := cyTUIModel{
		ctx:          ctx,
		agent:        agent,
		cfg:          cfg,
		root:         root,
		console:      console,
		input:        input,
		secret:       secret,
		spinner:      spin,
		renderer:     newInlineRenderer(out),
		historyIndex: 0,
		themePending: requestedTheme == terminalThemeAuto,
		darkTheme:    darkTheme,
	}
	m.applyTerminalTheme(darkTheme)
	m.configureCommandSuggestions()
	m.appendBlock(screenBlock{kind: screenBlockBanner})
	if cfg.SecuritySummary != "" {
		m.addBlock(screenBlockSystem, cfg.SecuritySummary)
	}
	m.loadSessionHistory()
	if m.agent.SessionRepaired() {
		m.addBlock(screenBlockSystem, "repaired an incomplete session journal tail")
	}
	m.refreshProcessResults()
	m.openLoginPicker(true)
	m.refreshScreen()
	return m
}

func (m cyTUIModel) Init() tea.Cmd {
	if m.themePending {
		// Wait for OSC 11 before drawing the first frame. Recoloring after a long
		// resumed transcript has entered scrollback would require erasing it.
		return tea.Batch(
			tea.RequestBackgroundColor,
			tea.Tick(themeQueryTimeout, func(time.Time) tea.Msg { return themeQueryTimeoutMsg{} }),
		)
	}
	return nil
}

func (m cyTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	updated, cmd := m.update(msg)
	next, ok := updated.(cyTUIModel)
	if !ok {
		return updated, cmd
	}
	if next.quitting {
		if err := next.renderer.Stop(); err != nil {
			next.renderErr = fmt.Errorf("stop terminal renderer: %w", err)
		}
		return next, cmd
	}
	if next.themePending {
		return next, cmd
	}
	if err := next.renderer.RenderFrame(next.inlineFrame(), next.width, next.height); err != nil {
		next.renderErr = fmt.Errorf("render terminal: %w", err)
		next.quitting = true
		_ = next.renderer.Stop()
		return next, tea.Quit
	}
	next.transcriptDirty = false
	return next, cmd
}

func (m cyTUIModel) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		if !m.themePending {
			return m, nil
		}
		m.themePending = false
		m.applyTerminalTheme(msg.IsDark())
		m.refreshScreen()
		return m, nil
	case themeQueryTimeoutMsg:
		// OSC 11 is widely supported but optional and may be filtered by a
		// multiplexer. Keep the existing light palette as the auto fallback;
		// --theme/CY_THEME provides a deterministic override.
		m.themePending = false
		return m, nil
	case tea.WindowSizeMsg:
		// WithoutRenderer makes Bubble Tea emit an initial zero-sized message;
		// the real initial size and subsequent changes are managed by RunScreen.
		if msg.Width <= 0 || msg.Height <= 0 {
			return m, nil
		}
		m.resize(msg.Width, msg.Height)
		m.refreshScreen()
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case spinner.TickMsg:
		if !m.working && m.maintenance == "" {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		if m.updatePendingToolDurations(time.Now()) {
			m.refreshScreen()
		}
		return m, cmd
	case agentStreamMsg:
		m.applyStreamEvent(msg.event)
		if msg.event.Kind == golem.EventTextDelta {
			return m, tea.Batch(waitAgentMsg(m.events), m.scheduleTranscriptRender())
		}
		m.renderPending = false
		m.refreshScreen()
		return m, tea.Batch(waitAgentMsg(m.events), m.scheduleProcessPoll())
	case agentDoneMsg:
		m.renderPending = false
		m.working = false
		m.turnCancel = nil
		m.finishTurnChanges()
		if msg.err != nil && !errors.Is(msg.err, golem.ErrEmptyInput) && !errors.Is(msg.err, context.Canceled) {
			m.addBlock(screenBlockError, fmt.Sprintf("error: %v", msg.err))
		}
		m.finishTurnDuration(time.Now())
		if input, ok := m.claimQueuedInput(); ok {
			m.addBlock(screenBlockUser, input)
			cmd := m.startTurn(input)
			m.refreshScreen()
			return m, cmd
		}
		m.refreshScreen()
		return m, m.scheduleProcessPoll()
	case shellDoneMsg:
		m.finishMaintenance()
		m.applyLocalShellResult("", msg.content, msg.result)
		if msg.err != nil && !errors.Is(msg.err, context.Canceled) {
			m.addBlock(screenBlockError, "shell: "+msg.err.Error())
		}
		m.refreshScreen()
		return m, m.scheduleProcessPoll()
	case transcriptRenderMsg:
		if !m.renderPending {
			return m, nil
		}
		m.renderPending = false
		m.refreshScreen()
		return m, nil
	case compactDoneMsg:
		m.finishMaintenance()
		if msg.err != nil {
			if errors.Is(msg.err, context.Canceled) {
				m.addBlock(screenBlockSystem, "compaction cancelled")
			} else {
				m.addBlock(screenBlockError, "compact: "+msg.err.Error())
			}
		} else {
			m.addBlock(screenBlockSystem, "compacted; "+compactContextStatus(msg.report))
		}
		m.refreshScreen()
		return m, nil
	case modelSwitchDoneMsg:
		m.finishMaintenance()
		if msg.err != nil {
			m.addBlock(screenBlockError, "model: "+msg.err.Error())
		} else {
			m.cfg.ModelURI = msg.uri
			m.cfg.ReasoningEffort = msg.effort
			label := "model: " + msg.uri
			if msg.effort != "" {
				label += " · effort: " + msg.effort
			}
			m.addBlock(screenBlockSystem, label)
		}
		m.refreshScreen()
		return m, nil
	case resumeSessionMsg:
		cmd := m.resumeSession(msg.idOrPrefix)
		m.refreshScreen()
		return m, cmd
	case processPollMsg:
		m.processPollPending = false
		m.refreshProcessResults()
		m.refreshScreen()
		return m, m.scheduleProcessPoll()
	default:
		if _, ok := msg.(tea.MouseMsg); ok {
			return m, nil
		}
		return m.updateEditor(msg)
	}
}

func (m cyTUIModel) View() tea.View {
	// The inline renderer owns terminal output. Bubble Tea still calls View
	// after every Update even with WithoutRenderer, so materializing the full
	// transcript here would make ordinary input and stream events O(history).
	return tea.NewView("")
}

func (m cyTUIModel) inlineFrame() inlineFrame {
	transcript := m.transcriptLines
	footerMeta := strings.Repeat(" ", transcriptGutter) + truncateANSI(m.footerMetaLine(), m.contentWidth())
	dynamic := []string{m.workingIndicatorLine()}
	if m.picker.active() {
		dynamic = append(dynamic, m.renderPicker()...)
	} else if m.maintenance != "" {
		dynamic = append(dynamic, strings.Repeat(" ", transcriptGutter)+m.muted(m.spinner.View()+" "+m.maintenance))
	} else {
		dynamic = append(dynamic, m.markedEditorLines()...)
		dynamic = append(dynamic, m.renderCommandSuggestions()...)
	}
	dynamic = append(dynamic, "", footerMeta)

	var cursor *tea.Cursor
	if !m.picker.active() && m.maintenance == "" {
		if editorCursor := m.editorCursor(); editorCursor != nil {
			cursorCopy := *editorCursor
			cursor = &cursorCopy
			cursor.Position.X += transcriptGutter
			cursor.Position.Y += len(transcript) + 1
		}
	}
	return inlineFrame{
		transcript:          transcript,
		dynamic:             dynamic,
		cursor:              cursor,
		transcriptChanged:   m.transcriptDirty,
		transcriptDirtyFrom: m.transcriptDirtyFrom,
	}
}
