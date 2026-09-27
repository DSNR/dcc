package gui

import (
	"image"
	"time"

	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/session"
	"github.com/DSNR/dcc/internal/storage"
	"github.com/DSNR/dcc/internal/words"
)

// Entry is one thing in the conversation area: a message either side sent, or
// a notice from dcc itself.
type Entry struct {
	// At is when it happened here.
	At time.Time
	// Who is the Display Name that said it; empty makes this a notice from
	// dcc rather than from a person.
	Who string
	// Mine reports that this side sent it, which is what gives it a status.
	Mine bool
	Body string
	// ID is the message id a status arrives against. Empty for notices and
	// for whatever the Peer sent.
	ID     string
	Status session.DeliveryStatus
}

// Notice reports an entry that is dcc talking rather than a participant.
func (e Entry) Notice() bool { return e.Who == "" }

// Stamp is the time this entry is shown against.
func (e Entry) Stamp() string { return e.At.Format(words.Stamp) }

// Marker is how far a message this side sent has got. Only our own messages
// carry one: there is nothing to say about the delivery of a message that
// arrived.
func (e Entry) Marker() string {
	if !e.Mine {
		return ""
	}
	return words.Delivery(e.Status)
}

// notice makes an entry for something dcc itself has to say.
func notice(body string) Entry { return Entry{At: time.Now(), Body: body} }

// Prompt is the standing Security Code prompt, as the window shows it. It
// stands over the message box until it is answered, and nothing can be said
// or received behind it, because no content flows until then — but the rest
// of the window stays live: someone who does not like the look of a code must
// be able to disconnect without answering it.
type Prompt struct {
	// Groups is the Security Code in the groups it is meant to be read in.
	Groups []string
	// Name is the Display Name the other side announced — a label, not
	// proof.
	Name string
	// Changed reports a Peer verified before under a different Identity,
	// which is the one case where refusing is the likelier right answer.
	Changed bool
	// Lines is the prompt in words: what to do, and the warning if the
	// Identity changed.
	Lines []string
}

// Conversation is one stored Conversation as the history panel lists it.
type Conversation struct {
	Peer identity.PublicKey
	Name string
	// Summary is how much is stored and when it was last added to.
	Summary string
}

// conversation renders one stored Conversation for the panel.
func conversation(c storage.Conversation) Conversation {
	return Conversation{Peer: c.Peer, Name: c.Name, Summary: words.Stored(c.Messages, c.LastAt)}
}

// Call is the Call inside the Session as the window shows it: where it
// stands, what each side has turned on, and the pictures to paint. A Session
// carrying nothing but text has one of these too, in state NoCall — the Call
// controls are on screen whether or not there is a Call, greyed like every
// other control that would do nothing.
type Call struct {
	// State is where the Call stands.
	State session.CallState
	// Status is the Call in one line, for the bar the Call's buttons are on.
	Status string
	// Muted, CameraOn and Sharing are this side's own streams, as the Session
	// answers for them — a camera that was unplugged reads as off here, the
	// same as one that was turned off.
	Muted, CameraOn, Sharing bool
	// TheirMic, TheirCam and TheirScreen are the other side's streams, as
	// they last announced them.
	TheirMic, TheirCam, TheirScreen bool
	// Large is the picture that fills the video area: the other side's shared
	// screen while they are sharing one, their camera otherwise. Nil when
	// there is no picture to paint, and then Waiting says why.
	Large *image.RGBA
	// Small is this side's own camera, the picture-in-picture. Nil while this
	// side's camera is off, because a corner showing a frozen last frame is
	// worse than an empty one.
	Small *image.RGBA
	// Waiting is what to say where a Large picture would be, and "" when
	// there is one to paint instead.
	Waiting string
}

// Video reports a Call with a video area to show. It opens with the Call
// itself — Active — whether or not anybody has turned a camera on yet, and not
// while the media is still coming up: a video area that appeared halfway
// through would move the conversation out from under whoever was reading it.
func (c Call) Video() bool { return c.State == session.Active }

// Controls is which of the window's buttons can be pressed right now. The
// window shows every control it has at all times and greys out the ones that
// would do nothing, so that what dcc can do is visible without a manual.
type Controls struct {
	// Host opens a Rendezvous; Join connects to an Invite. Neither is
	// available while a Session is running.
	Host bool
	Join bool
	// Send is a message box worth submitting: Connected, with the Security
	// Code answered.
	Send bool
	// Disconnect ends the running Session.
	Disconnect bool
	// Call rings the other person, and needs a Connected Session with no Call
	// already running. Answer and Reject are the two ways out of one ringing
	// here, and Hangup ends whichever Call there is.
	Call, Answer, Reject, Hangup bool
	// Mute is available for as long as there is a Call at all; Camera and
	// Share need one whose media is up, because there is nothing to open a
	// device into until then.
	Mute, Camera, Share bool
}

// Screen is one snapshot of the window: everything the Gio layer paints, and
// nothing it has to work out for itself.
type Screen struct {
	// Name is this side's Display Name — a reminder of what the other side
	// is being told.
	Name string
	// Status is where the Session stands, in one line.
	Status string
	// Peer is the other side's Display Name, empty when there is nobody.
	Peer string
	// Entries is the conversation area, oldest first.
	Entries []Entry
	// Invite is the Invite to hand over, empty when there is none to hand.
	// The window offers to copy it, because an Invite that has to be
	// retyped is an Invite that gets mistyped.
	Invite string
	// Prompt is the standing Security Code prompt, nil when none stands.
	Prompt *Prompt
	// Call is the Call inside the Session, and the pictures it carries.
	Call Call
	// Conversations is what the history panel lists.
	Conversations []Conversation
	// Controls is which buttons are live.
	Controls Controls
	// Closing reports that the participant has asked to leave and the
	// Session is being torn down, which is the one time input is ignored.
	Closing bool
}
