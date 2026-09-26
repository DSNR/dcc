package chatwindow

import (
	"fmt"
	"image"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/DSNR/dcc/internal/gui"
	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/session"
)

// The window laid out without a window. Gio will draw into an op.Ops with no
// display, no GPU and no event router underneath it, so every state the chat
// window can be in is laid out here — which is what catches a panic, a
// constraint that cannot be satisfied or a nil dereference in a state that is
// awkward to reach by hand, such as a Security Code prompt on a narrow
// window.

// TestLaysOutEveryState draws each state the window has, at a usual size and
// at a cramped one.
func TestLaysOutEveryState(t *testing.T) {
	sizes := []image.Point{
		{X: 900, Y: 640},
		// Small enough that everything has to give way somewhere.
		{X: 320, Y: 240},
	}
	for _, state := range states() {
		for _, size := range sizes {
			t.Run(fmt.Sprintf("%s at %dx%d", state.name, size.X, size.Y), func(t *testing.T) {
				u := newUI(gui.New(gui.Options{Name: "me"}))
				u.showHistory = state.history
				if state.clearing {
					asked := state.screen.Conversations[0]
					u.clearing = &asked
				}
				dims := u.layout(newContext(size), state.screen)
				if dims.Size.X > size.X || dims.Size.Y > size.Y {
					t.Errorf("the window laid out %v in a %v frame", dims.Size, size)
				}
			})
		}
	}
}

// state is one thing the window can be showing.
type state struct {
	name     string
	screen   gui.Screen
	history  bool
	clearing bool
}

func states() []state {
	entries := []gui.Entry{
		{At: time.Now(), Body: "dcc — end-to-end encrypted chat, peer to peer."},
		{At: time.Now(), Who: "you", Mine: true, Body: "hello 👋", Status: session.TextDelivered},
		{At: time.Now(), Who: "Ada", Body: "hello yourself 🎉"},
	}
	conversations := []gui.Conversation{
		{Peer: identity.PublicKey{1}, Name: "Ada", Summary: "12 messages, last 1 Mar 2026 09:30"},
	}
	prompt := &gui.Prompt{
		Groups: []string{"11111", "22222", "33333", "44444", "55555", "66666", "77777", "88888"},
		Name:   "Ada",
		Lines:  []string{"Read this code to \"Ada\" and check that every digit matches theirs."},
	}
	changed := *prompt
	changed.Changed = true
	changed.Lines = append(changed.Lines, "⚠ \"Ada\" has been verified before, and their Identity has changed.")

	idle := gui.Screen{
		Name:     "me",
		Status:   `Idle · you are "me"`,
		Entries:  entries[:1],
		Controls: gui.Controls{Host: true, Join: true},
	}
	hosting := gui.Screen{
		Name:     "me",
		Status:   `Hosting · you are "me"`,
		Entries:  entries[:1],
		Invite:   "dcc://example.invalid#password",
		Controls: gui.Controls{Disconnect: true},
	}
	verifying := hosting
	verifying.Prompt = prompt
	warned := hosting
	warned.Prompt = &changed
	connected := gui.Screen{
		Name:     "me",
		Status:   `Connected · with "Ada" · direct · you are "me"`,
		Peer:     "Ada",
		Entries:  entries,
		Controls: gui.Controls{Send: true, Disconnect: true},
	}
	withHistory := connected
	withHistory.Conversations = conversations
	closing := connected
	closing.Closing = true
	closing.Controls = gui.Controls{}

	return []state{
		{name: "idle", screen: idle},
		{name: "idle with the history panel open", screen: idle, history: true},
		{name: "hosting with an Invite", screen: hosting},
		{name: "a standing Security Code prompt", screen: verifying},
		{name: "a changed Identity", screen: warned},
		{name: "connected", screen: connected},
		{name: "history listed", screen: withHistory, history: true},
		{name: "a delete being confirmed", screen: withHistory, history: true, clearing: true},
		{name: "closing", screen: closing},
	}
}

// newContext is a frame to lay out into: a size, and no input router, which
// is enough to draw but not to click.
func newContext(size image.Point) layout.Context {
	return layout.Context{
		Ops:         new(op.Ops),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(size),
	}
}
