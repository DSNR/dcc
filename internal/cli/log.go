package cli

import (
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/DSNR/dcc/internal/session"
)

// stamp is how a time is shown against every line. Seconds are noise in a
// conversation, and the date belongs to the history view that #24 brings.
const stamp = "15:04"

// me is how this side is labelled in the conversation. The Display Name is on
// the status line; in a conversation between two people, "you" reads better
// than a name you chose for yourself.
const me = "you"

// entry is one thing that happened, in the order it happened: a message
// either side sent, or a notice from dcc itself.
type entry struct {
	// at is when it happened here.
	at time.Time
	// who is the Display Name that said it; empty makes this a notice from
	// dcc rather than from a person.
	who string
	// mine reports that this side sent it, which is what gives it a status.
	mine bool
	body string
	// id is the message id a status arrives against. Empty for notices and
	// for whatever the Peer sent.
	id     string
	status session.DeliveryStatus
}

// notice makes an entry for something dcc itself has to say.
func notice(body string) entry { return entry{at: time.Now(), body: body} }

// render lays the conversation out for a view of the given width, in plain
// text: colour belongs to the view, and keeping it out of here means the
// conversation is exactly as testable as it looks.
func render(entries []entry, width int) string {
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		lines = append(lines, e.lines(width)...)
	}
	return strings.Join(lines, "\n")
}

// lines renders one entry, wrapped to width, with continuations indented under
// the timestamp so that the left edge always reads as a column of times.
func (e entry) lines(width int) []string {
	prefix := e.at.Format(stamp) + " "
	body := e.who + ": " + e.body
	if e.who == "" {
		// A notice is dcc talking, and is marked as such rather than
		// impersonating a participant.
		body = "· " + e.body
	}
	if marker := statusMarker(e); marker != "" {
		body += "  " + marker
	}

	indent := strings.Repeat(" ", len(prefix))
	wrapped := wrap(body, max(width-len(prefix), 1))
	lines := make([]string, 0, len(wrapped))
	for i, line := range wrapped {
		if i == 0 {
			lines = append(lines, prefix+line)
			continue
		}
		lines = append(lines, indent+line)
	}
	return lines
}

// statusMarker is how far a message this side sent has got. Only our own
// messages carry one: there is nothing to say about the delivery of a message
// that arrived.
func statusMarker(e entry) string {
	if !e.mine {
		return ""
	}
	switch e.status {
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

// wrap breaks one piece of text to width, keeping words whole where it can and
// breaking them where it must — an Invite is one long word, and hiding half of
// it off the edge of the terminal would make it unusable.
func wrap(s string, width int) []string {
	return strings.Split(ansi.Wrap(ansi.Wordwrap(s, width, ""), width, ""), "\n")
}
