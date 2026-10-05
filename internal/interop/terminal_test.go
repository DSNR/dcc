package interop_test

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/DSNR/dcc/internal/cli"
	"github.com/DSNR/dcc/internal/gui"
	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/media"
	"github.com/DSNR/dcc/internal/rendezvous"
	"github.com/DSNR/dcc/internal/session"
)

// TestMain forces plain output, so that what the terminal half of the interop
// test reads is exactly what a participant on a colourless terminal reads.
func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.Ascii)
	m.Run()
}

// TestWindowTalksToTerminal is the acceptance test the two clients exist for:
// a dcc-gui window hosting, a dcc-cli terminal joining, and text going both
// ways over a real handshake and a real DataChannel. Neither client has any
// networking of its own — this is what that claim means in practice.
func TestWindowTalksToTerminal(t *testing.T) {
	window := newLive(t, "Ada", nil)
	terminal := newTerminal(t, "Grace", nil)

	window.Host()
	s := waitFor(t, window, "the Invite", func(s gui.Screen) bool { return s.Invite != "" })

	terminal.submit("/connect " + s.Invite)
	terminal.see(t, "Security Code")
	waitFor(t, window, "the Host's Security Code", func(s gui.Screen) bool { return s.Prompt != nil })

	window.Accept()
	terminal.submit("yes")
	terminal.see(t, "Connected")
	waitFor(t, window, "the window to be able to send", func(s gui.Screen) bool { return s.Controls.Send })

	if sent := window.Send("from the window 👋"); !sent {
		t.Fatal("the window would not send")
	}
	terminal.see(t, "from the window 👋")

	terminal.submit("from the terminal 🎉")
	// The window is waiting on the terminal's own goroutine-less loop, so
	// both are pumped while it waits.
	terminal.waitFor(t, "the window to show the terminal's message", func() bool {
		return said(window, "from the terminal 🎉")
	})
}

// TestTwoTerminalsTalk is dcc-cli on both ends: two terminals, each on its
// own *session.Session, meeting over a loopback Rendezvous and exchanging
// text through a real handshake and a real DataChannel. Nothing here is
// GUI-specific, so this is the proof that two terminals alone — no window on
// either end — hold a Conversation the same way the mixed pairing does.
func TestTwoTerminalsTalk(t *testing.T) {
	host := newTerminal(t, "Ada", nil)
	peer := newTerminal(t, "Grace", nil)

	host.submit("/invite")
	host.waitFor(t, "the Host's Invite", func() bool { return strings.Contains(host.model.View(), "#") })
	invite := inviteFrom(host.model.View())

	peer.submit("/connect " + invite)
	bothWaitFor(t, host, peer, "both Security Codes", func() bool {
		return strings.Contains(host.model.View(), "Security Code") && strings.Contains(peer.model.View(), "Security Code")
	})

	host.submit("yes")
	peer.submit("yes")
	bothWaitFor(t, host, peer, "both sides Connected", func() bool {
		return strings.Contains(host.model.View(), "Connected") && strings.Contains(peer.model.View(), "Connected")
	})

	host.submit("hello from the Host")
	bothWaitFor(t, host, peer, "the Peer to see the Host's message", func() bool {
		return strings.Contains(peer.model.View(), "hello from the Host")
	})

	peer.submit("and back from the Peer")
	bothWaitFor(t, host, peer, "the Host to see the Peer's message", func() bool {
		return strings.Contains(host.model.View(), "and back from the Peer")
	})
}

// inviteFrom pulls the Invite URL out of a terminal's rendered screen.
func inviteFrom(screen string) string {
	for _, field := range strings.Fields(screen) {
		if strings.Contains(field, "#") {
			return field
		}
	}
	return ""
}

// bothWaitFor pumps whichever of two terminals has a pending message until
// want reports true — the shape two independently-driven Models need when
// each must be kept moving while the other is what it is waiting on.
func bothWaitFor(t *testing.T, a, b *terminal, what string, want func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		if want() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s\n--- a ---\n%s\n--- b ---\n%s", what, a.model.View(), b.model.View())
		}
		pumped := a.pump()
		pumped = b.pump() || pumped
		if !pumped {
			time.Sleep(tick)
		}
	}
}

// terminal is a dcc-cli Model driven the way bubbletea drives one: messages
// applied one at a time, commands run off this goroutine and fed back in. It
// is the smallest thing that can put a real terminal client on the other end
// of a real Session.
type terminal struct {
	model tea.Model
	msgs  chan tea.Msg
}

// newTerminal starts a terminal client over a real Session on the loopback
// Rendezvous. Nil devices means a terminal that will never be in a Call; a
// media.Fake is what stands in for a microphone, a camera and a display where
// one is.
func newTerminal(t *testing.T, name string, devices media.Devices) *terminal {
	t.Helper()
	id, _, err := identity.Load(t.TempDir())
	if err != nil {
		t.Fatalf("identity.Load: %v", err)
	}
	h := &terminal{msgs: make(chan tea.Msg, 256)}
	h.model = cli.New(cli.Options{
		Name: name,
		New: func() (cli.Session, error) {
			return session.New(session.Options{
				Identity: id,
				Name:     name,
				Tunnel:   rendezvous.Loopback{},
				Devices:  devices,
			})
		},
	})
	h.run(h.model.Init())
	h.do(tea.WindowSizeMsg{Width: 100, Height: 30})
	t.Cleanup(func() { h.submit("/quit") })
	return h
}

// do applies one message to the Model.
func (h *terminal) do(msg tea.Msg) {
	switch msg := msg.(type) {
	case tea.QuitMsg:
		return
	case tea.BatchMsg:
		for _, cmd := range msg {
			h.run(cmd)
		}
		return
	}
	m, cmd := h.model.Update(msg)
	h.model = m
	h.run(cmd)
}

// run performs one command off this goroutine, since a command is exactly the
// work a Model must not block on.
func (h *terminal) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		if msg := cmd(); msg != nil {
			h.msgs <- msg
		}
	}()
}

// pump applies one pending message and reports whether it found one.
func (h *terminal) pump() bool {
	select {
	case msg := <-h.msgs:
		h.do(msg)
		return true
	default:
		return false
	}
}

// submit types a line and presses enter, one key at a time, so that the input
// path itself — emoji and all — is what this exercises.
func (h *terminal) submit(line string) {
	for _, r := range line {
		h.do(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	h.do(tea.KeyMsg{Type: tea.KeyEnter})
}

// see waits for a fragment to appear on the terminal's screen.
func (h *terminal) see(t *testing.T, fragment string) {
	t.Helper()
	h.waitFor(t, "the terminal to show "+fragment, func() bool {
		return strings.Contains(h.model.View(), fragment)
	})
}

// waitFor pumps the terminal until want reports true. Everything either side
// is waiting on arrives as a message here, so this is where both clients are
// kept moving.
func (h *terminal) waitFor(t *testing.T, what string, want func() bool) {
	t.Helper()
	h.wait(t, waitTimeout, what, want)
}

// waitForMedia is waitFor with a Call's media given the time it needs.
func (h *terminal) waitForMedia(t *testing.T, what string, want func() bool) {
	t.Helper()
	h.wait(t, mediaWait, what, want)
}

func (h *terminal) wait(t *testing.T, bound time.Duration, what string, want func() bool) {
	t.Helper()
	deadline := time.Now().Add(bound)
	for {
		if want() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s\n--- terminal ---\n%s", what, h.model.View())
		}
		if !h.pump() {
			time.Sleep(tick)
		}
	}
}
