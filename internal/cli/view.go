package cli

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The only styling in dcc's terminal interface: the status line stands out,
// the standing Security Code prompt insists, and the key hints recede. Colour
// is avoided entirely — a Session's state is carried by words, so a terminal
// that renders none of this loses nothing.
var (
	statusStyle = lipgloss.NewStyle().Reverse(true)
	promptStyle = lipgloss.NewStyle().Bold(true)
	hintStyle   = lipgloss.NewStyle().Faint(true)
)

// View implements tea.Model: the status line, the conversation, the standing
// Security Code prompt if there is one, the message box, and the key hints —
// in exactly the rows layout gave each of them.
func (m Model) View() string {
	rows := make([]string, 0, m.height)
	rows = append(rows, statusStyle.Width(m.width).Render(ansi.Truncate(m.statusLine(), m.width, "…")))
	if m.view.Height > 0 {
		rows = append(rows, strings.Split(m.view.View(), "\n")...)
	}
	for _, line := range m.panel() {
		rows = append(rows, promptStyle.Render(line))
	}
	rows = append(rows, strings.Split(m.input.View(), "\n")...)
	rows = append(rows, hintStyle.Render(ansi.Truncate(hint, m.width, "…")))
	return strings.Join(fit(rows, m.height), "\n")
}

// fit makes the frame exactly as tall as the terminal. layout already divides
// the rows up, so this only bites on a terminal too small to hold even the
// status line and the message box — where something has to give, and a frame
// that overflows would scroll the whole screen on every keystroke.
func fit(rows []string, height int) []string {
	if height < 1 {
		height = 1
	}
	for len(rows) < height {
		rows = append(rows, "")
	}
	return rows[:height]
}

// panel is the rows the standing Security Code prompt gets, empty when no
// prompt stands. Where layout could not spare all of them, the explanation in
// the middle gives way first, so that the code and the question survive; the
// conversation holds the whole of it either way.
func (m Model) panel() []string {
	rows := m.promptRows()
	switch {
	case len(rows) <= m.promptLimit:
		return rows
	case m.promptLimit < 2:
		return rows[len(rows)-m.promptLimit:]
	}
	kept := make([]string, 0, m.promptLimit)
	kept = append(kept, rows[:m.promptLimit-1]...)
	return append(kept, rows[len(rows)-1])
}

// promptRows is the whole standing prompt, wrapped to the terminal.
func (m Model) promptRows() []string {
	if m.prompt == nil {
		return nil
	}
	var rows []string
	for _, line := range promptPanel(*m.prompt) {
		rows = append(rows, wrap(line, max(m.width, 1))...)
	}
	return rows
}
