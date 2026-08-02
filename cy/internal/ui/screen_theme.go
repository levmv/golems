package ui

import "charm.land/lipgloss/v2"

func (m *cyTUIModel) applyTerminalTheme(dark bool) {
	m.darkTheme = dark
	m.selectionStyle = lipgloss.NewStyle().Bold(true)
	m.mutedStyle = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(8))

	if dark {
		m.accentStyle = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(14)).Bold(true)
		m.errorStyle = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(9))
		m.successStyle = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(10))
		m.userStyle = lipgloss.NewStyle().
			Foreground(lipgloss.ANSIColor(252)).
			Background(lipgloss.ANSIColor(236))
	} else {
		m.accentStyle = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(6)).Bold(true)
		m.errorStyle = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(1))
		m.successStyle = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(2))
		m.userStyle = lipgloss.NewStyle().
			Foreground(lipgloss.ANSIColor(240)).
			Background(lipgloss.ANSIColor(254))
	}

	// Styled transcript blocks are cached independently from their source.
	// A theme change must invalidate that cache before the first themed frame.
	m.renderDirtyFrom = 0
}
