package cli

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Run takes over the terminal and returns when the participant leaves.
//
// The mouse is deliberately left alone: enabling mouse reporting takes
// selection away from the terminal, and the first thing a Host has to do is
// copy an Invite out of this screen and paste it into a messenger.
func Run(opts Options) error {
	_, err := tea.NewProgram(New(opts), tea.WithAltScreen()).Run()
	return err
}
