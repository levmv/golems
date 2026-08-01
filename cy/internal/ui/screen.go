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
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/levmv/golems/cy/internal/engine"
	"github.com/levmv/golems/pkg/golem"
)

const (
	screenFixedRows  = 3 // spacing above/below the composer and status line
	composerMaxRows  = 8
	transcriptGutter = 2
	userMarker       = "›"
	transcriptFrame  = 33 * time.Millisecond
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

type processPollMsg struct{}
type transcriptRenderMsg struct{}

type tuiCommand struct {
	name        string
	description string
}

var tuiCommands = []tuiCommand{
	{name: "/help", description: "show commands and keyboard shortcuts"},
	{name: "/clear", description: "start a new session"},
	{name: "/resume", description: "choose or resume a previous session"},
	{name: "/usage", description: "show token usage"},
	{name: "/context", description: "show context budget"},
	{name: "/compact", description: "compact context, optionally with a focus"},
	{name: "/login", description: "choose a provider and enter an API key"},
	{name: "/logout", description: "remove a provider credential"},
	{name: "/model", description: "list or switch models"},
	{name: "/profile", description: "show or switch the capability profile"},
	{name: "/exit", description: "exit Cy"},
}

type pickerKind int

const (
	pickerNone pickerKind = iota
	pickerSession
	pickerModel
	pickerProfile
	pickerLogin
)

type pickerItem struct {
	value         string
	label         string
	description   string
	section       string
	credentialURL string
	current       bool
	efforts       []string
	effortIndex   int
}

type pickerState struct {
	kind        pickerKind
	items       []pickerItem
	index       int
	loginSwitch bool
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

	viewport viewport.Model
	input    textarea.Model
	secret   textinput.Model
	spinner  spinner.Model

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
	loginProvider       string
	loginSwitchModel    bool
	turnChangedPaths    []string
	processPollPending  bool
	renderPending       bool
	transcriptSelection transcriptSelection

	commandSuggestions        []string
	commandSuggestionIndex    int
	commandSuggestionsEnabled bool

	mutedStyle               lipgloss.Style
	selectionStyle           lipgloss.Style
	transcriptSelectionStyle lipgloss.Style
	accentStyle              lipgloss.Style
	errorStyle               lipgloss.Style
	successStyle             lipgloss.Style
	userStyle                lipgloss.Style
}

func CanUseScreen(in io.Reader, out io.Writer) (*os.File, *os.File, bool) {
	inFile, inOK := in.(*os.File)
	outFile, outOK := out.(*os.File)
	if !inOK || !outOK {
		return nil, nil, false
	}
	return inFile, outFile, isTerminalFile(inFile) && isTerminalFile(outFile)
}

func RunScreen(ctx context.Context, agent Agent, cfg Config, root string, in *os.File, out *os.File) error {
	model := newCyTUIModel(ctx, agent, cfg, root, out)
	program := tea.NewProgram(model, tea.WithInput(in), tea.WithOutput(out), tea.WithContext(ctx))
	finalModel, err := program.Run()
	if tui, ok := finalModel.(cyTUIModel); ok {
		printExitTranscript(out, tui)
	}
	if errors.Is(err, tea.ErrInterrupted) {
		return context.Canceled
	}
	return err
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

	vp := viewport.New()
	vp.FillHeight = true
	vp.MouseWheelEnabled = true

	spin := spinner.New(spinner.WithSpinner(spinner.Line))

	m := cyTUIModel{
		ctx:                      ctx,
		agent:                    agent,
		cfg:                      cfg,
		root:                     root,
		console:                  console,
		viewport:                 vp,
		input:                    input,
		secret:                   secret,
		spinner:                  spin,
		historyIndex:             0,
		mutedStyle:               lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		selectionStyle:           lipgloss.NewStyle().Bold(true),
		transcriptSelectionStyle: lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("4")),
		accentStyle:              lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true),
		errorStyle:               lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
		successStyle:             lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		userStyle:                lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Background(lipgloss.Color("254")),
	}
	m.configureCommandSuggestions()
	m.blocks = append(m.blocks, screenBlock{kind: screenBlockBanner})
	if cfg.SecuritySummary != "" {
		m.addBlock(screenBlockSystem, cfg.SecuritySummary)
	}
	m.appendHistoryBlocks()
	if m.agent.SessionRepaired() {
		m.addBlock(screenBlockSystem, "repaired an incomplete session journal tail")
	}
	m.refreshProcessResults()
	m.openLoginPicker(true)
	m.refreshViewport(true)
	return m
}

func (m cyTUIModel) Init() tea.Cmd {
	return nil
}

func (m cyTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		wasBottom := m.viewport.AtBottom()
		m.transcriptSelection = transcriptSelection{}
		m.resize(msg.Width, msg.Height)
		m.refreshViewport(wasBottom)
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.MouseClickMsg:
		return m.handleTranscriptMouseClick(msg)
	case tea.MouseMotionMsg:
		return m.handleTranscriptMouseMotion(msg)
	case tea.MouseReleaseMsg:
		return m.handleTranscriptMouseRelease(msg)
	case spinner.TickMsg:
		if !m.working && m.maintenance == "" {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		if m.updatePendingToolDurations(time.Now()) {
			m.refreshViewport(m.viewport.AtBottom())
		}
		return m, cmd
	case agentStreamMsg:
		m.applyStreamEvent(msg.event)
		if msg.event.Kind == golem.EventTextDelta {
			return m, tea.Batch(waitAgentMsg(m.events), m.scheduleTranscriptRender())
		}
		m.renderPending = false
		m.refreshViewport(m.viewport.AtBottom())
		return m, tea.Batch(waitAgentMsg(m.events), m.scheduleProcessPoll())
	case agentDoneMsg:
		wasBottom := m.viewport.AtBottom()
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
			m.refreshViewport(true)
			return m, cmd
		}
		m.refreshViewport(wasBottom)
		return m, m.scheduleProcessPoll()
	case transcriptRenderMsg:
		if !m.renderPending {
			return m, nil
		}
		m.renderPending = false
		m.refreshViewport(m.viewport.AtBottom())
		return m, nil
	case compactDoneMsg:
		wasBottom := m.viewport.AtBottom()
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
		m.refreshViewport(wasBottom)
		return m, nil
	case modelSwitchDoneMsg:
		wasBottom := m.viewport.AtBottom()
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
		m.refreshViewport(wasBottom)
		return m, nil
	case processPollMsg:
		m.processPollPending = false
		wasBottom := m.viewport.AtBottom()
		m.refreshProcessResults()
		m.refreshViewport(wasBottom)
		return m, m.scheduleProcessPoll()
	default:
		if m.isViewportMsg(msg) {
			return m.updateViewport(msg)
		}
		if _, ok := msg.(tea.MouseMsg); ok {
			return m, nil
		}
		return m.updateEditor(msg)
	}
}

func (m cyTUIModel) View() tea.View {
	if m.width <= 0 || m.height <= 0 {
		v := tea.NewView("Cy")
		v.AltScreen = true
		return v
	}

	footerMeta := strings.Repeat(" ", transcriptGutter) + truncateANSI(m.footerMetaLine(), m.contentWidth())
	parts := []string{m.transcriptViewportView(), m.workingIndicatorLine()}
	if m.picker.active() {
		parts = append(parts, m.renderPicker()...)
	} else if m.maintenance != "" {
		parts = append(parts, strings.Repeat(" ", transcriptGutter)+m.muted(m.spinner.View()+" "+m.maintenance))
	} else {
		parts = append(parts, m.markedEditorView())
		parts = append(parts, m.renderCommandSuggestions()...)
	}
	parts = append(parts, "", footerMeta)
	content := strings.Join(parts, "\n")

	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	if !m.picker.active() && m.maintenance == "" {
		if cursor := m.editorCursor(); cursor != nil {
			cursor.Position.X += transcriptGutter
			cursor.Position.Y += m.viewport.Height() + 1
			v.Cursor = cursor
		}
	}
	return v
}
