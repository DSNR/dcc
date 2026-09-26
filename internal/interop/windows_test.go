package interop_test

import (
	"strings"
	"testing"
	"time"

	"github.com/DSNR/dcc/internal/gui"
	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/rendezvous"
	"github.com/DSNR/dcc/internal/session"
)

// TestTwoWindowsTalk is the desktop client over the real thing: two Models,
// each on its own *session.Session, meeting over a loopback Rendezvous and
// exchanging text through a real handshake, a real PeerConnection and a real
// DataChannel. It is the proof that the window drives the Session the way the
// Session expects — and, because dcc-cli drives the same API, that a window
// and a terminal can hold the same Conversation.
func TestTwoWindowsTalk(t *testing.T) {
	host := newLive(t, "Ada")
	peer := newLive(t, "Grace")

	host.Host()
	s := waitFor(t, host, "the Invite", func(s gui.Screen) bool { return s.Invite != "" })
	peer.Join(s.Invite)

	// Both sides are asked to compare the same Security Code, and both have
	// to answer before anything either of them types is shown.
	hostPrompt := waitFor(t, host, "the Host's Security Code", func(s gui.Screen) bool { return s.Prompt != nil })
	peerPrompt := waitFor(t, peer, "the Peer's Security Code", func(s gui.Screen) bool { return s.Prompt != nil })
	if strings.Join(hostPrompt.Prompt.Groups, "") != strings.Join(peerPrompt.Prompt.Groups, "") {
		t.Fatalf("the two sides were shown different Security Codes:\n%v\n%v",
			hostPrompt.Prompt.Groups, peerPrompt.Prompt.Groups)
	}
	if hostPrompt.Prompt.Name != "Grace" || peerPrompt.Prompt.Name != "Ada" {
		t.Errorf("the prompts name %q and %q", hostPrompt.Prompt.Name, peerPrompt.Prompt.Name)
	}
	host.Accept()
	peer.Accept()

	waitFor(t, host, "the Host to be able to send", func(s gui.Screen) bool { return s.Controls.Send })
	waitFor(t, peer, "the Peer to be able to send", func(s gui.Screen) bool { return s.Controls.Send })

	if sent := host.Send("hello from the window 👋"); !sent {
		t.Fatal("the Host's window would not send")
	}
	waitSaid(t, peer, "hello from the window 👋")
	waitFor(t, host, "the message to be acknowledged", func(s gui.Screen) bool {
		return strings.Contains(transcript(s), "hello from the window 👋  ✓✓")
	})

	if sent := peer.Send("and back"); !sent {
		t.Fatal("the Peer's window would not send")
	}
	waitSaid(t, host, "and back")
}

// waitTimeout bounds every wait for something to reach a screen. Nothing here
// leaves the machine; hitting this means something is wedged.
const waitTimeout = 10 * time.Second

// tick is how long a wait sleeps between looks.
const tick = time.Millisecond

// newLive builds a window over real Sessions on a loopback Rendezvous — no
// cloudflared, no Cloudflare account, everything else real.
func newLive(t *testing.T, name string) *gui.Model {
	t.Helper()
	id, _, err := identity.Load(t.TempDir())
	if err != nil {
		t.Fatalf("identity.Load: %v", err)
	}
	m := gui.New(gui.Options{
		Name: name,
		New: func() (gui.Session, error) {
			return session.New(session.Options{
				Identity: id,
				Name:     name,
				Tunnel:   rendezvous.Loopback{},
			})
		},
	})
	t.Cleanup(func() {
		m.Quit()
		<-m.Done()
	})
	return m
}

// waitFor polls a window's Screen until it satisfies want.
func waitFor(t *testing.T, m *gui.Model, what string, want func(gui.Screen) bool) gui.Screen {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		s := m.Screen()
		if want(s) {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s\n--- conversation ---\n%s", what, transcript(s))
		}
		time.Sleep(tick)
	}
}

// waitSaid waits for one line to appear in a window's conversation area.
func waitSaid(t *testing.T, m *gui.Model, want string) gui.Screen {
	t.Helper()
	return waitFor(t, m, "the window to say "+want, func(s gui.Screen) bool {
		return strings.Contains(transcript(s), want)
	})
}

// said reports whether a window's conversation area carries a line.
func said(m *gui.Model, want string) bool {
	return strings.Contains(transcript(m.Screen()), want)
}

// transcript is a window's conversation area as plain text.
func transcript(s gui.Screen) string {
	var b strings.Builder
	for _, e := range s.Entries {
		if e.Notice() {
			b.WriteString("· ")
		} else {
			b.WriteString(e.Who + ": ")
		}
		b.WriteString(e.Body)
		if marker := e.Marker(); marker != "" {
			b.WriteString("  " + marker)
		}
		b.WriteString("\n")
	}
	return b.String()
}
