package ui

import (
	"context"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/levmv/golems/pkg/golem"
)

func (m *cyTUIModel) handleCommand(input string) (handled bool, done bool, cmd tea.Cmd) {
	fields := strings.Fields(input)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return false, false, nil
	}
	switch strings.ToLower(fields[0]) {
	case "/exit", "/quit", "/q":
		return true, true, nil
	case "/help":
		m.addBlock(screenBlockInfo, "Type / to browse commands; use ↑/↓ to choose, Tab to complete, and Enter to run. Shift+Enter inserts a new line. While working: Enter queues input, Esc cancels and restores undelivered input, Ctrl+C cancels. The transcript uses normal terminal scrollback: use the mouse wheel and terminal-native selection, copying, and context menu.")
	case "/clear":
		id, err := m.agent.ClearSession()
		if err != nil {
			m.addBlock(screenBlockError, "clear session: "+err.Error())
			break
		}
		m.resetTranscript()
		m.addBlock(screenBlockSystem, "new session "+id)
	case "/resume":
		if len(fields) > 1 {
			cmd = resumeSessionCmd(fields[1])
			break
		}
		summaries, err := m.agent.ListSessions()
		if err != nil {
			m.addBlock(screenBlockError, "list sessions: "+err.Error())
			break
		}
		current := m.agent.SessionID()
		items := make([]pickerItem, 0, min(20, len(summaries)))
		for _, summary := range summaries {
			if summary.ID != current {
				items = append(items, pickerItem{
					value:       summary.ID,
					label:       sessionDisplayTitle(summary),
					description: relativeSessionTime(time.Now(), summary.UpdatedAt),
				})
			}
			if len(items) == 20 {
				break
			}
		}
		if len(items) == 0 {
			m.addBlock(screenBlockSystem, "no other sessions")
		} else {
			m.openPicker(pickerSession, items, 0, false)
		}
	case "/usage":
		usage, err := m.agent.SessionUsage()
		if err != nil {
			m.addBlock(screenBlockError, "usage: "+err.Error())
			break
		}
		m.addBlock(screenBlockInfo, golem.FormatUsage(usage))
	case "/context":
		report, err := m.agent.ContextReport()
		if err != nil {
			m.addBlock(screenBlockError, "context: "+err.Error())
			break
		}
		m.addBlock(screenBlockInfo, formatContextReport(report))
	case "/compact":
		focus := strings.TrimSpace(strings.TrimPrefix(input, fields[0]))
		opCtx, cancel := context.WithCancel(m.ctx)
		m.maintenance = "compacting context"
		m.maintenanceCancel = cancel
		cmd = tea.Batch(m.spinner.Tick, compactCmd(opCtx, m.agent, focus))
	case "/login":
		if len(fields) == 1 {
			m.openLoginPicker(false)
			break
		}
		provider := strings.ToLower(fields[1])
		if !m.isLoginProvider(provider) {
			m.addBlock(screenBlockError, "login: unsupported provider "+provider)
			break
		}
		m.beginLogin(provider, false, m.providerCredentialURL(provider))
	case "/logout":
		if len(fields) < 2 {
			m.addBlock(screenBlockError, "usage: /logout <provider>")
			break
		}
		if err := m.agent.Logout(fields[1]); err != nil {
			m.addBlock(screenBlockError, "logout: "+err.Error())
		} else {
			m.addBlock(screenBlockSystem, "logged out of "+fields[1])
		}
	case "/model":
		if len(fields) == 1 {
			m.openModelPicker()
			break
		}
		cmd = m.startModelSwitch(fields[1], "")
	case "/profile":
		if len(fields) == 1 {
			m.openProfilePicker()
			break
		}
		if err := m.agent.SwitchProfile(fields[1]); err != nil {
			m.addBlock(screenBlockError, "profile: "+err.Error())
		} else {
			m.cfg.CapabilityProfile = m.agent.CurrentProfile()
			m.addBlock(screenBlockSystem, "profile: "+m.agent.CurrentProfile())
		}
	default:
		m.addBlock(screenBlockError, "unknown command: "+fields[0])
	}
	return true, false, cmd
}

func (m *cyTUIModel) isLoginProvider(provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	for _, known := range m.cfg.providers() {
		if provider == known {
			return true
		}
	}
	return false
}

func (m *cyTUIModel) openModelPicker() {
	currentEffort := m.agent.CurrentReasoningEffort()
	m.cfg.ReasoningEffort = currentEffort
	seen := make(map[string]struct{})
	models := make([]string, 0, len(m.agent.KnownModels())+1)
	add := func(uri string) {
		uri = strings.TrimSpace(uri)
		if uri == "" {
			return
		}
		if _, exists := seen[uri]; exists {
			return
		}
		seen[uri] = struct{}{}
		models = append(models, uri)
	}
	for _, uri := range m.agent.KnownModels() {
		add(uri)
	}
	if _, exists := seen[m.cfg.ModelURI]; !exists && strings.TrimSpace(m.cfg.ModelURI) != "" {
		models = append([]string{m.cfg.ModelURI}, models...)
	}
	if len(models) == 0 {
		m.addBlock(screenBlockError, "model: no models available")
		return
	}
	items := make([]pickerItem, 0, len(models))
	selected := 0
	for index, uri := range models {
		efforts := m.agent.ReasoningEfforts(uri)
		if len(efforts) == 0 {
			efforts = []string{""}
		}
		effortIndex := 0
		if uri == m.cfg.ModelURI {
			for candidate, effort := range efforts {
				if effort == currentEffort {
					effortIndex = candidate
					break
				}
			}
		}
		current := uri == m.cfg.ModelURI
		item := pickerItem{value: uri, label: uri, current: current, efforts: append([]string(nil), efforts...), effortIndex: effortIndex}
		updateModelEffortDescription(&item)
		items = append(items, item)
		if current {
			selected = index
		}
	}
	m.openPicker(pickerModel, items, selected, false)
}

func (m *cyTUIModel) openProfilePicker() {
	items := make([]pickerItem, 0, len(capabilityProfileCatalog))
	current := m.agent.CurrentProfile()
	m.cfg.CapabilityProfile = current
	selected := 0
	for _, profile := range capabilityProfileCatalog {
		isCurrent := profile.Name == current
		items = append(items, pickerItem{value: profile.Name, label: profile.Name, description: profile.Description, current: isCurrent})
		if isCurrent {
			selected = len(items) - 1
		}
	}
	m.openPicker(pickerProfile, items, selected, false)
}

func (m *cyTUIModel) openLoginPicker(onlyIfMissing bool) {
	statuses, err := m.agent.ProviderStatuses()
	if err != nil {
		m.addBlock(screenBlockError, "login: "+err.Error())
		return
	}
	currentProvider := modelProvider(m.cfg.ModelURI)
	selected := 0
	currentMissing := false
	items := make([]pickerItem, 0, len(statuses))
	for _, status := range statuses {
		if onlyIfMissing && status.Category != "" && status.Category != "Model providers" {
			continue
		}
		if status.Name == currentProvider {
			selected = len(items)
			currentMissing = status.Source == "none"
		}
		source := status.Source
		if source == "none" {
			source = "not configured"
		}
		description := strings.TrimSpace(status.Description)
		if description != "" {
			description += " · "
		}
		description += source
		if status.Name == currentProvider {
			description += " · current model"
		}
		items = append(items, pickerItem{
			value:         status.Name,
			label:         status.Name,
			description:   description,
			section:       status.Category,
			credentialURL: status.CredentialURL,
			current:       status.Name == currentProvider,
		})
	}
	if onlyIfMissing && !currentMissing {
		return
	}
	if len(items) == 0 {
		if !onlyIfMissing {
			m.addBlock(screenBlockError, "login: no providers available")
		}
		return
	}

	m.openPicker(pickerLogin, items, selected, onlyIfMissing)

}

func (m *cyTUIModel) openPicker(kind pickerKind, items []pickerItem, selected int, loginSwitch bool) {
	m.picker = pickerState{kind: kind, items: items, index: min(max(0, selected), len(items)-1), loginSwitch: loginSwitch}
	m.input.Reset()
	m.disableCommandSuggestions()
}

func (m cyTUIModel) handlePickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.Key()
	switch {
	case key.Code == tea.KeyEscape || key.Code == tea.KeyEsc || msg.String() == "esc" || msg.String() == "ctrl+c":
		m.closePicker()
		return m, nil
	case key.Code == tea.KeyUp || key.Code == tea.KeyKpUp || msg.String() == "up":
		if m.picker.index > 0 {
			m.picker.index--
		}
		return m, nil
	case key.Code == tea.KeyDown || key.Code == tea.KeyKpDown || msg.String() == "down":
		if m.picker.index+1 < len(m.picker.items) {
			m.picker.index++
		}
		return m, nil
	case m.picker.kind == pickerModel && (key.Code == tea.KeyLeft || key.Code == tea.KeyKpLeft || msg.String() == "left"):
		m.cycleModelEffort(-1)
		return m, nil
	case m.picker.kind == pickerModel && (key.Code == tea.KeyRight || key.Code == tea.KeyKpRight || msg.String() == "right"):
		m.cycleModelEffort(1)
		return m, nil
	case isEnterKey(msg):
		cmd := m.selectPickerItem()
		m.refreshScreen()
		return m, cmd
	}
	return m, nil
}

func (m *cyTUIModel) selectPickerItem() tea.Cmd {
	picker := m.picker
	item := picker.items[picker.index]
	m.closePicker()
	switch picker.kind {
	case pickerSession:
		return resumeSessionCmd(item.value)
	case pickerModel:
		if item.value == m.cfg.ModelURI && selectedModelEffort(item) == m.cfg.ReasoningEffort {
			return nil
		}
		return m.startModelSwitch(item.value, selectedModelEffort(item))
	case pickerProfile:
		if item.current {
			return nil
		}
		if err := m.agent.SwitchProfile(item.value); err != nil {
			m.addBlock(screenBlockError, "profile: "+err.Error())
		} else {
			m.cfg.CapabilityProfile = m.agent.CurrentProfile()
			m.addBlock(screenBlockSystem, "profile: "+m.cfg.CapabilityProfile)
		}
	case pickerLogin:
		m.beginLogin(item.value, picker.loginSwitch, item.credentialURL)
	}
	return nil
}

func (m *cyTUIModel) cycleModelEffort(delta int) {
	item := &m.picker.items[m.picker.index]
	if len(item.efforts) <= 1 {
		return
	}
	item.effortIndex = (item.effortIndex + delta + len(item.efforts)) % len(item.efforts)
	updateModelEffortDescription(item)
}

func selectedModelEffort(item pickerItem) string {
	if item.effortIndex < 0 || item.effortIndex >= len(item.efforts) {
		return ""
	}
	return item.efforts[item.effortIndex]
}

func updateModelEffortDescription(item *pickerItem) {
	if len(item.efforts) <= 1 {
		item.description = ""
		return
	}
	effort := selectedModelEffort(*item)
	if effort == "" {
		effort = "default"
	}
	item.description = "effort: " + effort + "  ←/→"
}

func (m *cyTUIModel) closePicker() {
	m.picker = pickerState{}
	m.input.Reset()
	m.configureCommandSuggestions()
}

func (m *cyTUIModel) beginLogin(provider string, switchModel bool, credentialURL string) {
	m.loginProvider = provider
	m.loginSwitchModel = switchModel
	m.secret.Reset()
	m.secret.EchoMode = textinput.EchoPassword
	m.secret.Placeholder = provider + " API key"
	m.disableCommandSuggestions()
	message := "enter " + provider + " API key (input is hidden)"
	if credentialURL != "" {
		message += "; create or manage keys at " + credentialURL
	}
	m.addBlock(screenBlockSystem, message)
}

func (m *cyTUIModel) providerCredentialURL(provider string) string {
	statuses, err := m.agent.ProviderStatuses()
	if err != nil {
		return ""
	}
	for _, status := range statuses {
		if status.Name == provider {
			return status.CredentialURL
		}
	}
	return ""
}

func compactCmd(ctx context.Context, agent screenAgent, focus string) tea.Cmd {
	return func() tea.Msg {
		report, err := agent.Compact(ctx, focus)
		return compactDoneMsg{report: report, err: err}
	}
}

func (m *cyTUIModel) startModelSwitch(uri, effort string) tea.Cmd {
	uri = strings.TrimSpace(uri)
	m.maintenance = "switching model"
	m.maintenanceCancel = nil
	return switchModelCmd(m.agent, uri, effort)
}

func switchModelCmd(agent screenAgent, uri, effort string) tea.Cmd {
	return func() tea.Msg {
		return modelSwitchDoneMsg{uri: uri, effort: effort, err: agent.SwitchModelWithEffort(uri, effort)}
	}
}

func (m *cyTUIModel) finishMaintenance() {
	if m.maintenanceCancel != nil {
		m.maintenanceCancel()
	}
	m.maintenanceCancel = nil
	m.maintenance = ""
}

func (m *cyTUIModel) cancelMaintenance() {
	if m.maintenanceCancel != nil {
		m.maintenanceCancel()
	}
}

func firstProviderModel(models []string, provider string) string {
	for _, uri := range models {
		if modelProvider(uri) == provider {
			return uri
		}
	}
	return ""
}

func (m *cyTUIModel) resumeSession(idOrPrefix string) tea.Cmd {
	id, err := m.agent.ResumeSession(idOrPrefix)
	if err != nil {
		m.addBlock(screenBlockError, "resume session: "+err.Error())
		return nil
	}
	m.cfg.ModelURI = m.agent.CurrentModel()
	m.cfg.ReasoningEffort = m.agent.CurrentReasoningEffort()
	m.continueTranscriptBelow()
	m.addBlock(screenBlockSystem, "resumed session "+id)
	m.appendHistoryBlocks()
	if m.agent.SessionRepaired() {
		m.addBlock(screenBlockSystem, "repaired an incomplete session journal tail")
	}
	m.refreshProcessResults()
	return m.scheduleProcessPoll()
}

func resumeSessionCmd(idOrPrefix string) tea.Cmd {
	// Resume on the next Bubble Tea message rather than inside submitInput. This
	// gives the renderer one frame to erase the old picker/editor before
	// continueTranscriptBelow releases the previous transcript to scrollback.
	return func() tea.Msg { return resumeSessionMsg{idOrPrefix: idOrPrefix} }
}
