package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

type transcriptPoint struct {
	x int
	y int
}

type transcriptSelection struct {
	start    transcriptPoint
	end      transcriptPoint
	dragging bool
}

func (s transcriptSelection) hasRange() bool {
	return s.start != s.end
}

func (s transcriptSelection) bounds() (transcriptPoint, transcriptPoint) {
	start, end := s.start, s.end
	if end.y < start.y || end.y == start.y && end.x < start.x {
		start, end = end, start
	}
	return start, end
}

func (m cyTUIModel) transcriptMousePoint(x, y int) transcriptPoint {
	x = min(max(0, x), m.viewport.Width())
	y = min(max(0, y), max(0, m.viewport.Height()-1))
	return transcriptPoint{x: x, y: m.viewport.YOffset() + y}
}

func (m cyTUIModel) mouseInTranscript(x, y int) bool {
	return x >= 0 && x < m.viewport.Width() && y >= 0 && y < m.viewport.Height()
}

func (m cyTUIModel) handleTranscriptMouseClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if msg.Button != tea.MouseLeft {
		return m, nil
	}
	if !m.mouseInTranscript(msg.X, msg.Y) {
		m.transcriptSelection = transcriptSelection{}
		return m, nil
	}
	point := m.transcriptMousePoint(msg.X, msg.Y)
	m.transcriptSelection = transcriptSelection{start: point, end: point, dragging: true}
	return m, nil
}

func (m cyTUIModel) handleTranscriptMouseMotion(msg tea.MouseMotionMsg) (tea.Model, tea.Cmd) {
	if !m.transcriptSelection.dragging {
		return m, nil
	}
	m.transcriptSelection.end = m.transcriptMousePoint(msg.X, msg.Y)
	return m, nil
}

func (m cyTUIModel) handleTranscriptMouseRelease(msg tea.MouseReleaseMsg) (tea.Model, tea.Cmd) {
	if !m.transcriptSelection.dragging {
		return m, nil
	}
	m.transcriptSelection.end = m.transcriptMousePoint(msg.X, msg.Y)
	m.transcriptSelection.dragging = false
	selected := m.selectedTranscriptText()
	if selected == "" {
		return m, nil
	}
	return m, tea.SetClipboard(selected)
}

func (m cyTUIModel) selectedTranscriptText() string {
	if !m.transcriptSelection.hasRange() {
		return ""
	}
	lines := strings.Split(m.viewport.GetContent(), "\n")
	if len(lines) == 0 {
		return ""
	}
	start, end := m.transcriptSelection.bounds()
	if start.y >= len(lines) || end.y < 0 {
		return ""
	}
	start.y = min(max(0, start.y), len(lines)-1)
	end.y = min(max(0, end.y), len(lines)-1)

	selected := make([]string, 0, end.y-start.y+1)
	for y := start.y; y <= end.y; y++ {
		left := 0
		right := ansi.StringWidth(lines[y])
		if y == start.y {
			left = start.x
		}
		if y == end.y {
			right = end.x
		}
		if right < left {
			right = left
		}
		line := ansi.Strip(ansi.Cut(lines[y], left, right))
		selected = append(selected, strings.TrimRight(line, " "))
	}
	return strings.TrimRight(strings.Join(selected, "\n"), "\n")
}

func (m cyTUIModel) transcriptViewportView() string {
	view := m.viewport.View()
	if !m.transcriptSelection.hasRange() || m.viewport.Width() <= 0 || m.viewport.Height() <= 0 {
		return view
	}

	screen := uv.NewScreenBuffer(m.viewport.Width(), m.viewport.Height())
	area := uv.Rect(0, 0, m.viewport.Width(), m.viewport.Height())
	uv.NewStyledString(view).Draw(screen, area)

	start, end := m.transcriptSelection.bounds()
	for y := 0; y < m.viewport.Height(); y++ {
		contentY := m.viewport.YOffset() + y
		if contentY < start.y || contentY > end.y {
			continue
		}
		left := 0
		right := m.viewport.Width()
		if contentY == start.y {
			left = start.x
		}
		if contentY == end.y {
			right = end.x
		}
		left = min(max(0, left), m.viewport.Width())
		right = min(max(left, right), m.viewport.Width())
		for x := left; x < right; x++ {
			cell := screen.CellAt(x, y)
			if cell == nil || cell.Content == "" {
				continue
			}
			cell = cell.Clone()
			cell.Style.Attrs &^= uv.AttrReverse
			cell.Style.Fg = m.transcriptSelectionStyle.GetForeground()
			cell.Style.Bg = m.transcriptSelectionStyle.GetBackground()
			screen.SetCell(x, y, cell)
		}
	}
	return screen.Render()
}
