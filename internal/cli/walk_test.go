package cli_test

import (
	"regexp"
	"testing"

	"github.com/DSNR/dcc/internal/cli"
	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/rendezvous"
	"github.com/DSNR/dcc/internal/session"
)

// invitePattern finds the Invite on the Host's screen, which is the only place
// it exists — the test reads it off exactly as the person handing it over has
// to.
var invitePattern = regexp.MustCompile(`http://\S+/#1\S+`)

// TestTwoTerminalsChat is the walking skeleton end to end: two real Sessions
// over real pion on a loopback Rendezvous, each driven only through its own
// terminal interface. Invite handed over, Security Code answered on both
// sides, Unicode and multiline text exchanged and acknowledged, then the
// Session taken down — with no cloudflared, no network and no fakes in
// between.
func TestTwoTerminalsChat(t *testing.T) {
	host := terminal(t, "alice")
	peer := terminal(t, "bob")
	link(host, peer)

	host.submit("/invite")
	host.until("the Invite", func() bool { return invitePattern.FindString(host.view()) != "" })
	invite := invitePattern.FindString(host.view())

	peer.submit("/connect " + invite)

	// Both sides are asked, and each is told who the handshake authenticated.
	host.mustSee("yes / no", `"bob"`)
	peer.mustSee("yes / no", `"alice"`)

	host.submit("yes")
	peer.submit("yes")
	host.mustSee("Connected")
	peer.mustSee("Connected")

	peer.submit("Hello 👋\nand hello again")
	host.mustSee("bob:", "Hello 👋", "and hello again")
	// The Host's application-level ack is what turns the Peer's message
	// delivered.
	peer.mustSee("✓✓")

	host.submit("emoji back 🎉")
	peer.mustSee("alice:", "emoji back 🎉")
	host.mustSee("✓✓")

	host.submit("/disconnect")
	host.mustSee("Disconnected")
	// The Peer isn't told the Host left: it sees the connection die, tries
	// to reconnect, and finds the Rendezvous gone.
	peer.mustSee("the Rendezvous is gone")
}

// terminal is one dcc-cli, wired to real Sessions over a loopback Rendezvous:
// real Identity, real Noise handshake, real pion, no cloudflared.
func terminal(t *testing.T, name string) *harness {
	t.Helper()
	id, reset, err := identity.Load(t.TempDir())
	if err != nil {
		t.Fatalf("identity.Load: %v", err)
	}
	if reset != nil {
		t.Fatalf("a fresh Identity reported a reset: %v", reset.Cause)
	}

	var live []*session.Session
	t.Cleanup(func() {
		for _, s := range live {
			_ = s.Close()
		}
	})
	return newHarness(t, cli.Options{
		Name: name,
		New: func() (cli.Session, error) {
			s, err := session.New(session.Options{
				Identity: id,
				Name:     name,
				Tunnel:   rendezvous.Loopback{},
			})
			if err != nil {
				return nil, err
			}
			live = append(live, s)
			return s, nil
		},
	})
}
