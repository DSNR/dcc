package cli

import (
	"strconv"
	"strings"

	"github.com/DSNR/dcc/internal/session"
	"github.com/DSNR/dcc/internal/transport"
)

// welcome is the first thing in the conversation, on every run.
const welcome = "dcc — end-to-end encrypted chat, peer to peer. Type /help to get started."

// hint is the standing reminder under the message box, for the keys that
// aren't obvious.
const hint = " enter sends · alt+enter new line · pgup/pgdn scroll back · /help · ctrl+c quit"

// helpLines is what /help says. The commands are the ones docs/mvp.md names;
// the ones that need a screen to share arrive with the work that shares one.
var helpLines = []string{
	"Commands:",
	"  /invite               open a Rendezvous and print an Invite to hand over",
	"  /connect <invite>     join someone else's Invite",
	"  /msg <text>           say something that starts with a slash",
	"  /call                 ring the other person",
	"  /answer  /reject      pick up or turn down a Call ringing here",
	"  /mute    /unmute      stop or resume sending your microphone",
	"  /camera on|off        turn your camera on or off during a Call",
	"  /hangup               end the Call, stay connected for text",
	"  /history [name]       read a stored Conversation, no connection needed",
	"  /clearhistory <name>  delete a Conversation from this device only",
	"  /disconnect           end the Session, stay in dcc",
	"  /quit                 end the Session and leave",
	"  /help                 this",
	"Anything else you type is sent to the other person. While no Session is",
	"connected the commands work without their slash too, so 'invite' is enough.",
}

// stateNotice is what a transition is worth saying in the conversation, or ""
// where the state speaks for itself on the status line. The reason comes
// first: why a Session ended matters more than which terminal state it ended
// in.
func stateNotice(e session.StateChanged) string {
	switch e.Reason {
	case session.ReasonRefused:
		// Answering the prompt already said this.
		return ""
	case session.ReasonRejected:
		return "Turned away: that Invite is already in use by another Identity. Ask for a fresh one."
	case session.ReasonHandshakeFailed:
		return "Could not connect: the handshake failed. The Invite may be stale, mistyped, or the Rendezvous gone."
	case session.ReasonConnectionLost:
		return "The connection was lost."
	case session.ReasonTransportFailed:
		return "Could not reach the other person: no connection could be established."
	case session.ReasonRendezvousGone:
		return "Could not reconnect: the Rendezvous is gone. Ask for a fresh Invite and start again."
	}
	switch e.State {
	case session.Hosting:
		return "Hosting — waiting for the other person to connect."
	case session.Connecting:
		return "Connecting to the Rendezvous…"
	case session.Reconnecting:
		return "The connection dropped — reconnecting…"
	case session.Disconnected:
		return "The Session is over."
	case session.Failed:
		return "The Session failed."
	}
	// Verifying speaks through its prompt, and Connected through the
	// direct-or-relayed notice that follows it.
	return ""
}

// linkNotice is Connected's sub-status in words. Relayed is not a failure —
// content is encrypted either way — so it reads as a quality warning, not a
// security one.
func linkNotice(l transport.Link) string {
	switch l {
	case transport.LinkRelayed:
		return "Connected, relayed — still encrypted end to end, but expect less of it."
	default:
		return "Connected, direct — peer to peer, with nothing in between."
	}
}

// callNotice is what a Call transition is worth saying in the conversation.
// The reason comes first: why a Call ended is the whole of what someone
// wants to know when it does.
func callNotice(e session.CallChanged, peer string) string {
	switch e.Reason {
	case session.CallDeclined:
		return quoted(peer) + " turned the Call down."
	case session.CallBusy:
		return quoted(peer) + " is already in a Call."
	case session.CallTimedOut:
		return "The Call rang out unanswered."
	case session.CallEnded:
		return "The Call ended. You are still connected for text."
	case session.CallLost:
		return "The Call dropped with the connection. Call again once you are back."
	case session.CallFailed:
		return "The Call could not be set up — no media path came together."
	}
	switch e.State {
	case session.Incoming:
		return quoted(peer) + " is calling — /answer to pick up, /reject to turn it down."
	case session.Negotiating:
		return "Setting the Call up…"
	case session.Active:
		return "In a Call — video is in its own window. /camera on to be seen, /mute to stop sending your microphone, /hangup to end it."
	}
	// Ringing is announced by the command that caused it.
	return ""
}

// micNotice is the other side's microphone changing state. Muting is worth
// saying out loud: silence that is deliberate and silence that is broken
// sound exactly the same.
func micNotice(live bool, peer string) string {
	if live {
		return quoted(peer) + " unmuted."
	}
	return quoted(peer) + " muted their microphone."
}

// camNotice is the other side's camera changing state, for the same reason: a
// black video window and a camera nobody turned on look identical.
func camNotice(live bool, peer string) string {
	if live {
		return quoted(peer) + " turned their camera on."
	}
	return quoted(peer) + " turned their camera off."
}

// promptRecord is what a Security Code prompt leaves in the conversation: the
// code in the groups it is meant to be read in, and who is claiming it. The
// question itself is not here — it belongs to the standing panel, and must
// stop being asked the moment it is answered.
func promptRecord(p session.VerifyPrompt) []string {
	groups := p.Code.Groups()
	half := len(groups) / 2
	lines := []string{
		"Security Code — read it to " + quoted(p.Name) + " and check every digit matches:",
		"    " + strings.Join(groups[:half], " "),
		"    " + strings.Join(groups[half:], " "),
	}
	if p.Changed {
		lines = append(lines,
			"⚠ "+quoted(p.Name)+" has been verified before, and their Identity has changed.",
			"  A reinstall looks like this. So does someone sitting in between.",
			"  Accept only if the code above matches the one they read back.",
		)
	}
	return lines
}

// promptPanel is the standing Security Code prompt: the record, plus the
// question. It stays over the message box until it is answered, because no
// content flows until then.
func promptPanel(p session.VerifyPrompt) []string {
	return append(promptRecord(p), "Does their code match yours?  yes / no")
}

// statusLine is where the Session stands, on one line: the state, why it
// ended if it did, who is on the other side, whether content is going direct
// or through the relay, and what this side is calling itself.
func (m Model) statusLine() string {
	parts := []string{m.state.String()}
	if m.reason != session.ReasonNone {
		parts = append(parts, m.reason.String())
	}
	if m.peer != "" {
		parts = append(parts, "with "+quoted(m.peer))
	}
	if m.state == session.Connected && m.link != 0 {
		parts = append(parts, m.link.String())
	}
	if m.call != session.NoCall {
		parts = append(parts, m.callStatus())
	}
	if m.prompt != nil {
		parts = append(parts, "Security Code unanswered")
	}
	if m.name != "" {
		parts = append(parts, "you are "+quoted(m.name))
	}
	return " " + strings.Join(parts, " · ") + " "
}

// callStatus is where the Call stands, on the status line: what it is doing,
// and — once it is running — whose microphone is off and whose camera is on.
func (m Model) callStatus() string {
	switch m.call {
	case session.Ringing:
		return "calling"
	case session.Incoming:
		return "incoming Call — /answer or /reject"
	case session.Negotiating:
		return "Call connecting"
	case session.Active:
		status := "in a Call"
		if m.sess != nil && m.sess.Muted() {
			status += " · muted"
		}
		if m.sess != nil && m.sess.CameraOn() {
			status += " · camera on"
		}
		if !m.remoteMic {
			status += " · they are muted"
		}
		if m.remoteCam {
			status += " · their camera is on"
		}
		return status
	}
	return ""
}

// quoted renders a Display Name in a way that cannot be mistaken for dcc's
// own words — it is a label the other side chose, and proves nothing.
func quoted(name string) string {
	if name == "" {
		return "the other person"
	}
	return strconv.Quote(name)
}
