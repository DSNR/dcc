// Package words is what dcc's two clients say. The terminal and the window
// show the same Session in very different shapes, but a Session dropped for
// the same reason in both, and a Security Code has to be read out
// the same way wherever it is shown — so the lines that describe a Session,
// and the small formatting decisions that go with them, live here and are
// written once.
//
// What only one client can say stays with that client: the terminal talks
// about its commands, the window about its buttons, and neither of those
// belongs to the other.
package words

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/DSNR/dcc/internal/session"
	"github.com/DSNR/dcc/internal/transport"
)

// Stamp is how a time is shown against a message. Seconds are noise in a
// conversation; dates appear as their own lines where history spans days.
const Stamp = "15:04"

// DateStamp is how a day is named where one has to be — over restored
// history, and against a stored Conversation's last message.
const DateStamp = "2 Jan 2006"

// Me is how this side is labelled in a conversation. The Display Name is on
// the status line; between two people, "you" reads better than a name you
// chose for yourself.
const Me = "you"

// Quoted renders a Display Name in a way that cannot be mistaken for dcc's
// own words — it is a label the other side chose, and proves nothing.
func Quoted(name string) string {
	if name == "" {
		return "the other person"
	}
	return strconv.Quote(name)
}

// State is what a transition is worth saying out loud, or "" where the state
// speaks for itself wherever the client shows it. The reason comes first: why
// a Session ended matters more than which terminal state it ended in.
func State(e session.StateChanged) string {
	switch e.Reason {
	case session.ReasonRefused:
		// Answering the Security Code prompt already said this.
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
	// direct-or-relayed line that follows it.
	return ""
}

// Link is Connected's sub-status in words. Relayed is not a failure — content
// is encrypted either way — so it reads as a quality warning, not a security
// one.
func Link(l transport.Link) string {
	if l == transport.LinkRelayed {
		return "Connected, relayed — still encrypted end to end, but expect less of it."
	}
	return "Connected, direct — peer to peer, with nothing in between."
}

// Delivery is how far a message this side sent has got, or "" where there is
// nothing to say yet. Only our own messages carry one: there is nothing to
// say about the delivery of a message that arrived.
func Delivery(status session.DeliveryStatus) string {
	switch status {
	case session.TextPending:
		return "…"
	case session.TextSent:
		return "✓"
	case session.TextDelivered:
		return "✓✓"
	case session.TextFailed:
		return "✗ not delivered"
	}
	return ""
}

// SecurityCode is the record a Security Code prompt leaves behind it: the
// code in the groups it is meant to be read in, and who is claiming it. The
// question itself is not here — how a client asks it, and how it stops asking
// once it is answered, is the client's own business.
func SecurityCode(p session.VerifyPrompt) []string {
	groups := p.Code.Groups()
	half := len(groups) / 2
	return []string{
		"Security Code — read it to " + Quoted(p.Name) + " and check every digit matches:",
		"    " + strings.Join(groups[:half], " "),
		"    " + strings.Join(groups[half:], " "),
	}
}

// IdentityChanged is the part of a prompt that only a Peer verified before
// under a different Identity gets, and nothing at all for anyone else. A
// reinstall and a stranger in the middle look identical from here, so it says
// both and leaves the answer to the two people comparing digits.
func IdentityChanged(p session.VerifyPrompt) []string {
	if !p.Changed {
		return nil
	}
	return []string{
		"⚠ " + Quoted(p.Name) + " has been verified before, and their Identity has changed.",
		"  A reinstall looks like this. So does someone sitting in between.",
		"  Accept only if the code above matches the one they read back.",
	}
}

// Day names the calendar day history has reached, which is what makes a
// timestamp of 09:30 mean something once a Conversation spans more than one.
func Day(day time.Time) string { return "— " + day.Format(DateStamp) + " —" }

// SameDay reports whether two times fall on the same calendar day, which is
// where reading history back stops needing a new Day line.
func SameDay(a, b time.Time) bool {
	y1, m1, d1 := a.Date()
	y2, m2, d2 := b.Date()
	return y1 == y2 && m1 == m2 && d1 == d2
}

// Stored is how much of a Conversation is kept, for a list of the ones there
// are to read back.
func Stored(messages int, last time.Time) string {
	if messages == 0 {
		return "no messages"
	}
	return fmt.Sprintf("%d messages, last %s", messages, last.Format(DateStamp+" "+Stamp))
}
