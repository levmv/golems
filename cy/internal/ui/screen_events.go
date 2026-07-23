package ui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/levmv/golems/pkg/golem"
)

func (m *cyTUIModel) addBlock(kind screenBlockKind, text string) {
	m.blocks = append(m.blocks, screenBlock{kind: kind, text: sanitizeTerminalText(text)})
}

func (m *cyTUIModel) addToolCallBlock(step golem.Step) {
	display := describeToolCall(step.ToolName, step.Arguments)
	if display.GroupKey != "" && len(m.blocks) > 0 {
		last := &m.blocks[len(m.blocks)-1]
		if last.kind == screenBlockTool && last.toolGroupKey == display.GroupKey {
			items := append(splitCompactToolItems(last.toolGroupItems), display.GroupItem)
			last.toolGroupItems = strings.Join(items, compactToolItemSeparator)
			last.text = sanitizeTerminalText(formatReadGroup(display.GroupDir, items))
			return
		}
	}
	m.blocks = append(m.blocks, screenBlock{
		kind:          screenBlockTool,
		text:          sanitizeTerminalText(display.Text),
		toolName:      step.ToolName,
		toolCallID:    step.ToolCallID,
		toolStartedAt: time.Now(),

		toolGroupKey:   display.GroupKey,
		toolGroupItems: display.GroupItem,
	})
}

func (m *cyTUIModel) applyFileChangeResult(toolCallID string, change fileChangeMeta) {
	text := strings.TrimSpace(change.Operation + "  " + change.Path)
	for index := len(m.blocks) - 1; index >= 0; index-- {
		block := &m.blocks[index]
		if block.kind == screenBlockTool && toolCallID != "" && block.toolCallID == toolCallID {
			block.text = sanitizeTerminalText(text)
			block.fileChange = &change
			return
		}
	}
	m.blocks = append(m.blocks, screenBlock{
		kind:       screenBlockTool,
		text:       sanitizeTerminalText(text),
		toolCallID: toolCallID,
		fileChange: &change,
	})
}

func (m *cyTUIModel) applyProcessResult(toolCallID string, result processResultMeta) {
	for index := len(m.blocks) - 1; index >= 0; index-- {
		block := &m.blocks[index]
		if block.kind == screenBlockTool && toolCallID != "" && block.toolCallID == toolCallID {
			block.processResult = &result
			return
		}
	}
	text := "job  " + result.JobID
	if result.JobID == "" {
		text = "process"
	}
	m.blocks = append(m.blocks, screenBlock{
		kind:          screenBlockTool,
		text:          sanitizeTerminalText(text),
		toolCallID:    toolCallID,
		processResult: &result,
	})
}

func (m *cyTUIModel) updatePendingToolDurations(now time.Time) bool {
	changed := false
	for index := range m.blocks {
		block := &m.blocks[index]
		if block.kind != screenBlockTool || block.toolName != "bash" || block.processResult != nil || block.toolStartedAt.IsZero() {
			continue
		}
		elapsed := max(int64(0), now.Sub(block.toolStartedAt).Milliseconds())
		if block.toolElapsedMillis != elapsed {
			block.toolElapsedMillis = elapsed
			changed = true
		}
	}
	return changed
}

func (m *cyTUIModel) failPendingProcess(step golem.Step) {
	if step.ToolName != "bash" {
		return
	}
	for index := len(m.blocks) - 1; index >= 0; index-- {
		block := &m.blocks[index]
		if block.kind != screenBlockTool || block.toolCallID != step.ToolCallID {
			continue
		}
		duration := time.Since(block.toolStartedAt)
		m.applyProcessResult(step.ToolCallID, processResultMeta{
			Type:           processResultMetaType,
			Status:         jobFailed,
			DurationMillis: max(int64(0), duration.Milliseconds()),
			FailureTail:    step.Error,
		})
		return
	}
}

func (m *cyTUIModel) refreshProcessResults() {
	for index := range m.blocks {
		block := &m.blocks[index]
		if block.processResult == nil || !processRunning(block.processResult.Status) || block.processResult.JobID == "" {
			continue
		}
		if latest, found := m.agent.ProcessStatus(block.processResult.JobID); found {
			block.processResult = &latest
		}
	}
}

func (m *cyTUIModel) hasRunningProcesses() bool {
	for _, block := range m.blocks {
		if block.processResult != nil && processRunning(block.processResult.Status) {
			return true
		}
	}
	return false
}

func (m *cyTUIModel) scheduleProcessPoll() tea.Cmd {
	if m.processPollPending || !m.hasRunningProcesses() || m.ctx.Err() != nil {
		return nil
	}
	m.processPollPending = true
	return pollProcessStatusCmd(m.ctx)
}

func pollProcessStatusCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		timer := time.NewTimer(500 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
		return processPollMsg{}
	}
}

func (m *cyTUIModel) scheduleTranscriptRender() tea.Cmd {
	if m.renderPending {
		return nil
	}
	m.renderPending = true
	ctx := m.ctx
	return func() tea.Msg {
		timer := time.NewTimer(transcriptFrame)
		defer timer.Stop()
		if ctx == nil {
			<-timer.C
			return transcriptRenderMsg{}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			return transcriptRenderMsg{}
		}
	}
}

func (m *cyTUIModel) appendAssistant(delta string) {
	delta = sanitizeTerminalText(delta)
	if delta == "" {
		return
	}
	if len(m.blocks) == 0 || m.blocks[len(m.blocks)-1].kind != screenBlockAssistant {
		m.blocks = append(m.blocks, screenBlock{kind: screenBlockAssistant})
	}
	m.blocks[len(m.blocks)-1].text += delta
}

func (m *cyTUIModel) applyStreamEvent(ev golem.StreamEvent) {
	switch ev.Kind {
	case golem.EventTextDelta:
		m.appendAssistant(ev.Text)
	case golem.EventToolCall:
		m.addToolCallBlock(ev.Step)
	case golem.EventToolResult:
		if change, ok := fileChangeMetaFrom(ev.Step.Meta); ok {
			m.applyFileChangeResult(ev.Step.ToolCallID, change)
			if changed, include := changedFilePath(change); include {
				m.turnChangedPaths = appendUniquePath(m.turnChangedPaths, changed)
			}
		}
		if result, ok := processResultMetaFrom(ev.Step.Meta); ok {
			m.applyProcessResult(ev.Step.ToolCallID, result)
		}
	case golem.EventToolError:
		message := compactSingleLine(ev.Step.Error, 180)
		if strings.HasPrefix(message, "tool iteration limit reached") {
			message += "; requesting a final answer without more tools"
			message = sanitizeTerminalText(message)
			if len(m.blocks) == 0 || m.blocks[len(m.blocks)-1].kind != screenBlockError || m.blocks[len(m.blocks)-1].text != message {
				m.addBlock(screenBlockError, message)
			}
			break
		}
		m.failPendingProcess(ev.Step)
		if ev.Step.ToolName == "bash" {
			break
		} else {
			if message == "" {
				message = describeToolCall(ev.Step.ToolName, ev.Step.Arguments).Text + " failed"
			}
			m.addBlock(screenBlockError, message)
		}
	case golem.EventModelRetry:
		text := sanitizeTerminalText("retry: " + ev.Text)
		if ev.RetryKey != "" && len(m.blocks) > 0 {
			last := &m.blocks[len(m.blocks)-1]
			if last.kind == screenBlockError && last.retryKey == ev.RetryKey {
				last.text = text
				break
			}
		}
		m.blocks = append(m.blocks, screenBlock{kind: screenBlockError, text: text, retryKey: ev.RetryKey})
	case golem.EventStatus:
		if !m.processCompletionAlreadyVisible(ev.Text) {
			m.addBlock(screenBlockSystem, ev.Text)
		}
	case golem.EventAttemptReset:
		if ev.Text == "" {
			break
		}
		if len(m.blocks) > 0 && m.blocks[len(m.blocks)-1].kind == screenBlockAssistant {
			m.blocks = m.blocks[:len(m.blocks)-1]
		}
		m.addBlock(screenBlockSystem, "discarded partial model attempt")
	}
}

func (m *cyTUIModel) finishTurnChanges() {
	if summary := formatChangedFiles(m.turnChangedPaths); summary != "" {
		m.addBlock(screenBlockSystem, summary)
	}
	m.turnChangedPaths = nil
}

func (m *cyTUIModel) processCompletionAlreadyVisible(text string) bool {
	fields := strings.Fields(text)
	if len(fields) < 4 || fields[0] != "Background" || fields[1] != "job" || fields[3] != "completed:" {
		return false
	}
	jobID := fields[2]
	for _, block := range m.blocks {
		if block.processResult != nil && block.processResult.JobID == jobID && !processRunning(block.processResult.Status) {
			return true
		}
	}
	return false
}

func (m *cyTUIModel) cancelTurn() {
	if m.turnCancel != nil {
		m.turnCancel()
		m.turnCancel = nil
	}
}

func waitAgentMsg(events <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		if events == nil {
			return nil
		}
		msg, ok := <-events
		if !ok {
			return agentDoneMsg{}
		}
		return msg
	}
}

func runAgentTurnCmd(ctx context.Context, agent agentRunner, input string, events chan<- tea.Msg) tea.Cmd {
	return func() tea.Msg {
		runAgentTurn(ctx, agent, input, events)
		return nil
	}
}

func runAgentTurn(ctx context.Context, agent agentRunner, input string, events chan<- tea.Msg) {
	_, err := agent.Stream(ctx, input, func(ev golem.StreamEvent) {
		sendAgentMsg(ctx, events, agentStreamMsg{event: ev})
	})
	sendAgentDoneMsg(events, agentDoneMsg{err: err})
}

func sendAgentMsg(ctx context.Context, events chan<- tea.Msg, msg tea.Msg) {
	select {
	case events <- msg:
	case <-ctx.Done():
	}
}

func sendAgentDoneMsg(events chan<- tea.Msg, msg agentDoneMsg) {
	// A turn's context is normally cancelled before its terminal message is
	// sent. Do not select on that context here: the TUI must always leave the
	// working state and may need to start queued input.
	events <- msg
}
