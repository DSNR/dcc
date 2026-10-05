package gui

import (
	"strings"

	"github.com/DSNR/dcc/internal/session"
	"github.com/DSNR/dcc/internal/words"
)

// welcome is the first thing in the conversation area, on every run. The
// window's own buttons say what to do, so this only has to say what dcc is.
const welcome = "dcc — end-to-end encrypted chat, peer to peer. Host a Session and hand the Invite over, or paste an Invite someone sent you."

// prompt is the standing Security Code prompt as the window shows it.
func prompt(p session.VerifyPrompt) Prompt {
	lines := []string{
		"Read this code to " + words.Quoted(p.Name) + " and check that every digit matches theirs.",
	}
	return Prompt{
		Groups:  p.Code.Groups(),
		Name:    p.Name,
		Changed: p.Changed,
		Lines:   append(lines, words.IdentityChanged(p)...),
	}
}

// callNotice is what a Call transition is worth saying in the conversation.
// The reason comes first — why a Call ended is the whole of what someone wants
// to know when it does, and both clients say that the same way; what is left
// here is what to press next, which is this client's alone.
func callNotice(e session.CallChanged, peer string) string {
	if said := words.CallReason(e.Reason, peer); said != "" {
		return said
	}
	switch e.State {
	case session.Incoming:
		return words.Quoted(peer) + " is calling — Answer to pick up, Reject to turn it down."
	case session.Negotiating:
		return "Setting the Call up…"
	case session.Active:
		return "In a Call. Camera shows you, Share shows your screen, Mute stops sending your microphone, Hang up ends it."
	}
	// Ringing is announced by the button that caused it.
	return ""
}

// callStatus is where the Call stands, on the line above its buttons: what it
// is doing, and — once it is running — who has what turned on.
func callStatus(c Call, peer string) string {
	switch c.State {
	case session.Ringing:
		return "Calling " + words.Quoted(peer) + "…"
	case session.Incoming:
		return words.Quoted(peer) + " is calling"
	case session.Negotiating:
		return "Setting the Call up…"
	case session.Active:
		parts := []string{"In a Call"}
		if c.Muted {
			parts = append(parts, "you are muted")
		}
		if c.CameraOn {
			parts = append(parts, "your camera is on")
		}
		if c.Sharing {
			parts = append(parts, "you are sharing your screen")
		}
		if !c.TheirMic {
			parts = append(parts, "they are muted")
		}
		if c.TheirCam {
			parts = append(parts, "their camera is on")
		}
		if c.TheirScreen {
			parts = append(parts, "they are sharing their screen")
		}
		return strings.Join(parts, " · ")
	}
	return "No Call — Call rings them; text works either way."
}

// waiting is what stands in for a picture there is not one of yet, so that an
// empty video area always says why it is empty.
func waiting(c Call) string {
	switch {
	case c.TheirScreen:
		return "Waiting for their screen…"
	case c.TheirCam:
		return "Waiting for their video…"
	}
	return "No video — their camera is off. Turn yours on with Camera, or ask them for theirs."
}

// MuteLabel, CameraLabel and ShareLabel say what pressing the button will do,
// which is the only way a toggle can be honest about which way it points.
func (c Call) MuteLabel() string {
	if c.Muted {
		return "Unmute"
	}
	return "Mute"
}

// CameraLabel is the camera's toggle.
func (c Call) CameraLabel() string {
	if c.CameraOn {
		return "Camera off"
	}
	return "Camera on"
}

// ShareLabel is the screen share's.
func (c Call) ShareLabel() string {
	if c.Sharing {
		return "Stop sharing"
	}
	return "Share screen"
}

// statusLine is where the Session stands, on one line: the state, why it
// ended if it did, who is on the other side, whether content is going direct
// or through the relay, and what this side is calling itself.
func (m *Model) statusLine() string {
	parts := []string{m.state.String()}
	if m.reason != session.ReasonNone {
		parts = append(parts, m.reason.String())
	}
	if m.peer != "" {
		parts = append(parts, "with "+words.Quoted(m.peer))
	}
	if m.state == session.Connected && m.link != 0 {
		parts = append(parts, m.link.String())
	}
	if m.prompt != nil {
		parts = append(parts, "Security Code unanswered")
	}
	if m.name != "" {
		parts = append(parts, "you are "+words.Quoted(m.name))
	}
	return strings.Join(parts, " · ")
}
