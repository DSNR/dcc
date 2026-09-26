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
