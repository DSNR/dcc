package cli_test

import (
	"regexp"
	"testing"

	"github.com/DSNR/dcc/internal/cli"
	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/media"
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

// TestTwoTerminalsCall is the Call end to end, again through nothing but the
// two terminal interfaces: rung, answered, heard — each side hearing the
// other's tone and not its own — muted, and hung up back to text.
func TestTwoTerminalsCall(t *testing.T) {
	hostDevices := &media.Fake{Tone: 440}
	peerDevices := &media.Fake{Tone: 1100}
	host := terminal(t, "alice", hostDevices)
	peer := terminal(t, "bob", peerDevices)
	link(host, peer)

	host.submit("/invite")
	host.until("the Invite", func() bool { return invitePattern.FindString(host.view()) != "" })
	peer.submit("/connect " + invitePattern.FindString(host.view()))
	host.mustSee("yes / no")
	peer.mustSee("yes / no")
	host.submit("yes")
	peer.submit("yes")
	host.mustSee("Connected")
	peer.mustSee("Connected")

	host.submit("/call")
	peer.mustSee(`"alice" is calling`)
	peer.submit("/answer")
	host.mustSee("In a Call")
	peer.mustSee("In a Call")

	// Each side hears the other's tone, which is the only way to tell real
	// audio from a Call that merely says it is connected.
	host.until("the peer's tone", func() bool { return tone(hostDevices, 1100) })
	peer.until("the host's tone", func() bool { return tone(peerDevices, 440) })

	host.submit("/mute")
	peer.mustSee(`"alice" muted their microphone`)
	host.mustSee("muted")

	host.submit("/hangup")
	host.mustSee("The Call ended")
	peer.mustSee("The Call ended")

	// Text carries on over the same Session.
	peer.submit("still here")
	host.mustSee("bob:", "still here")
}

// tone reports whether a fake speaker is dominated by the given frequency.
func tone(f *media.Fake, freq float64) bool {
	return f.Heard().Power(freq) > 4*f.Heard().Power(freq*2.3)
}

// terminal is one dcc-cli, wired to real Sessions over a loopback Rendezvous:
// real Identity, real Noise handshake, real pion, no cloudflared. Calls run
// on fake devices, so a Call can be heard in a test with no sound card.
func terminal(t *testing.T, name string, devices ...media.Devices) *harness {
	t.Helper()
	var audio media.Devices
	if len(devices) > 0 {
		audio = devices[0]
	}
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
				Devices:  audio,
			})
			if err != nil {
				return nil, err
			}
			live = append(live, s)
			return s, nil
		},
	})
}
