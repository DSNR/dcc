package session

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/DSNR/dcc/internal/media"
	"github.com/DSNR/dcc/internal/wire"
)

// RingTimeout is how long an unanswered Call rings before it gives up. Both
// sides count it, so a Call ends at roughly the same moment at both ends
// even if the frame saying so is the one that goes missing.
const RingTimeout = 60 * time.Second

// CallState is where the Call inside a Session stands. There is at most one
// Call at a time, and a Session that is not Connected has none.
type CallState int

const (
	// NoCall: the Session is carrying text and nothing else.
	NoCall CallState = iota
	// Ringing: this side called and is waiting to be answered.
	Ringing
	// Incoming: the other side called and this side has not answered.
	Incoming
	// Negotiating: the Call was answered and its media is coming up.
	Negotiating
	// Active: media is negotiated and the Call is running.
	Active
)

// String implements fmt.Stringer.
func (c CallState) String() string {
	switch c {
	case NoCall:
		return "NoCall"
	case Ringing:
		return "Ringing"
	case Incoming:
		return "Incoming"
	case Negotiating:
		return "Negotiating"
	case Active:
		return "Active"
	}
	return fmt.Sprintf("CallState(%d)", int(c))
}

// CallReason says why a Call ended; it is CallReasonNone on every other
// transition.
type CallReason int

const (
	// CallReasonNone: the transition needs no explaining.
	CallReasonNone CallReason = iota
	// CallDeclined: the other side chose not to answer.
	CallDeclined
	// CallBusy: the other side is already in a Call.
	CallBusy
	// CallTimedOut: it rang for RingTimeout and nobody answered.
	CallTimedOut
	// CallEnded: one side hung up.
	CallEnded
	// CallLost: the Session dropped underneath the Call. A Call does not
	// survive a reconnect — the media path is rebuilt from nothing, and
	// silently reviving it would be worse than ringing again.
	CallLost
	// CallFailed: the media could not be negotiated.
	CallFailed
)

// String implements fmt.Stringer.
func (r CallReason) String() string {
	switch r {
	case CallReasonNone:
		return "none"
	case CallDeclined:
		return "declined"
	case CallBusy:
		return "busy"
	case CallTimedOut:
		return "timed out"
	case CallEnded:
		return "ended"
	case CallLost:
		return "connection lost"
	case CallFailed:
		return "media failed"
	}
	return fmt.Sprintf("CallReason(%d)", int(r))
}

// CallChanged announces every Call transition. Reason is meaningful only on
// the way back to NoCall.
type CallChanged struct {
	State  CallState
	CallID string
	Reason CallReason
}

func (CallChanged) event() {}

// MediaChanged is the other side's stream state, as they last announced it —
// which of their microphone, camera and screen share are live. It arrives
// only while a Call is Active.
type MediaChanged struct {
	Mic, Cam, Screen bool
}

func (MediaChanged) event() {}

// Call rings the other person and returns the Call's id. It needs a
// Connected Session and no Call already in progress.
func (s *Session) Call() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.state != Connected {
		return "", fmt.Errorf("session: cannot call while %s", s.state)
	}
	if s.callState != NoCall {
		return "", fmt.Errorf("session: there is already a Call, and it is %s", s.callState)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("session: minting a call id: %w", err)
	}
	if err := s.trans.Send(wire.Call{CallID: id.String()}); err != nil {
		return "", fmt.Errorf("session: placing the Call: %w", err)
	}
	s.callID = id.String()
	s.setCallLocked(Ringing, CallReasonNone)
	s.ringLocked()
	return s.callID, nil
}

// Answer accepts the Call that is ringing here. The media comes up behind
// it: the Call is Negotiating until both sides' transceivers are in place,
// and Active after.
func (s *Session) Answer() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.callState != Incoming {
		return fmt.Errorf("session: there is no Call to answer while %s", s.callState)
	}
	if err := s.trans.Send(wire.Accept{CallID: s.callID}); err != nil {
		return fmt.Errorf("session: answering the Call: %w", err)
	}
	s.negotiateLocked()
	return nil
}

// Reject turns down the Call that is ringing here.
func (s *Session) Reject() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.callState != Incoming {
		return fmt.Errorf("session: there is no Call to reject while %s", s.callState)
	}
	_ = s.trans.Send(wire.Reject{CallID: s.callID, Reason: wire.ReasonDeclined})
	s.endCallLocked(CallDeclined)
	return nil
}

// Hangup ends the Call — answered, still ringing here, or still ringing
// there — and leaves the Session up for text.
func (s *Session) Hangup() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.callState == NoCall {
		return errors.New("session: there is no Call to hang up")
	}
	if s.trans != nil {
		_ = s.trans.Send(wire.Hangup{CallID: s.callID})
	}
	s.endCallLocked(CallEnded)
	return nil
}

// Mute stops or resumes sending this side's microphone, and tells the other
// side either way — a muted microphone the other person cannot see is how
// people end up talking to nobody.
func (s *Session) Mute(muted bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.callState == NoCall {
		return errors.New("session: there is no Call to mute")
	}
	if !muted && s.noMic {
		// There is no microphone to unmute, and saying mic:true over one
		// that could not be opened would have the other side waiting on
		// silence. A microphone that is merely still opening is fine to
		// unmute: openDevices applies whatever was asked for meanwhile.
		return errors.New("session: there is no microphone on this machine")
	}
	s.muted = muted
	if s.audio != nil {
		s.audio.Mute(muted)
	}
	s.announceMediaLocked()
	return nil
}

// Muted reports whether this side's microphone is being sent.
func (s *Session) Muted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.muted
}

// handleCallLocked acts on one Call frame from the other side. A frame for a
// Call this side has already forgotten — an accept that lost the race with a
// timeout, say — is discarded on its call_id, which is what call_id is for.
func (s *Session) handleCallLocked(f wire.Frame) {
	switch f := f.(type) {
	case wire.Call:
		if s.callState != NoCall {
			// Already in a Call, or ringing into one: theirs is refused and
			// ours carries on untouched.
			_ = s.trans.Send(wire.Reject{CallID: f.CallID, Reason: wire.ReasonBusy})
			return
		}
		s.callID = f.CallID
		s.setCallLocked(Incoming, CallReasonNone)
		s.ringLocked()

	case wire.Accept:
		if s.callState != Ringing || f.CallID != s.callID {
			return
		}
		s.negotiateLocked()

	case wire.Reject:
		if s.callState != Ringing || f.CallID != s.callID {
			return
		}
		switch f.Reason {
		case wire.ReasonBusy:
			s.endCallLocked(CallBusy)
		case wire.ReasonTimeout:
			s.endCallLocked(CallTimedOut)
		default:
			s.endCallLocked(CallDeclined)
		}

	case wire.Hangup:
		if s.callState == NoCall || f.CallID != s.callID {
			return
		}
		s.endCallLocked(CallEnded)

	case wire.Media:
		// A media frame can arrive while this side is still Negotiating —
		// the other side reached Active first — and it is still the truth
		// about their streams. It is kept and announced once there is a
		// Call for it to be about.
		if s.callState != Negotiating && s.callState != Active {
			return
		}
		s.remoteMedia = MediaChanged{Mic: f.Mic, Cam: f.Cam, Screen: f.Screen}
		s.haveRemoteMedia = true
		if s.callState == Active {
			s.events.emit(s.remoteMedia)
		}
	}
}

// negotiateLocked moves an answered Call into Negotiating and asks the
// transport for the Call's transceivers. Both sides do this; only one of
// them renegotiates, and the transport sorts out which.
func (s *Session) negotiateLocked() {
	s.stopRingLocked()
	s.setCallLocked(Negotiating, CallReasonNone)
	if err := s.trans.StartMedia(); err != nil {
		s.endCallLocked(CallFailed)
	}
}

// onMediaUp is the transport reporting the Call's media negotiated. The Call
// is Active from here; the devices follow.
func (s *Session) onMediaUp(gen int) {
	s.mu.Lock()
	if s.closed || gen != s.gen || s.callState != Negotiating {
		s.mu.Unlock()
		return
	}
	s.setCallLocked(Active, CallReasonNone)
	if s.haveRemoteMedia {
		// Their state arrived while this side was still negotiating.
		s.events.emit(s.remoteMedia)
	}
	muted := s.muted
	s.mu.Unlock()

	go s.openDevices(gen, muted)
}

// openDevices opens the microphone and speaker for an Active Call — not
// before, so that a Session only ever used for text never touches the sound
// card, and off the Session's lock, so that every other command is not made
// to wait on one. The video pipeline is built alongside them and opens no
// camera until somebody turns one on.
func (s *Session) openDevices(gen int, muted bool) {
	audio, err := media.StartAudio(media.AudioOptions{
		Devices: s.devices,
		Muted:   muted,
		Send:    func(payload []byte, d time.Duration) { s.sendAudio(gen, payload, d) },
	})

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || gen != s.gen || s.callState != Active {
		// The Call ended while the devices were opening.
		if err == nil {
			go func() { _ = audio.Close() }()
		}
		return
	}
	if err != nil {
		// No microphone is a worse Call, not a failed one: the other side is
		// still heard, and the media state says this side is not speaking.
		s.muted, s.noMic = true, true
	} else {
		s.audio = audio
		// Muting while the devices were opening still counts.
		audio.Mute(s.muted)
	}
	s.startVideoLocked(gen)
	s.announceMediaLocked()
}

// sendAudio hands one captured frame to the transport. It runs on the
// capture goroutine, so it takes the lock only long enough to find the
// transport the frame belongs to.
func (s *Session) sendAudio(gen int, payload []byte, d time.Duration) {
	s.mu.Lock()
	trans := s.trans
	stale := s.closed || gen != s.gen || s.callState != Active
	s.mu.Unlock()
	if stale || trans == nil {
		return
	}
	// A refused frame is 20 ms of audio nobody hears. The Call is not worth
	// ending over it; the transport dying is reported through its own path.
	_ = trans.WriteAudio(payload, d)
}

// onAudio plays one received frame.
func (s *Session) onAudio(gen int, payload []byte) {
	s.mu.Lock()
	audio := s.audio
	stale := s.closed || gen != s.gen || s.callState != Active
	s.mu.Unlock()
	if stale || audio == nil {
		return
	}
	audio.Play(payload)
}

// announceMediaLocked tells the other side which of this side's streams are
// live. Screen is always off until the work that shares one lands, and saying
// so explicitly is what the protocol asks for.
func (s *Session) announceMediaLocked() {
	if s.callState != Active || s.trans == nil {
		return
	}
	_ = s.trans.Send(wire.Media{Mic: !s.muted, Cam: s.cam})
}

// ringLocked starts the ring timeout, which both sides run.
func (s *Session) ringLocked() {
	id, ringing := s.callID, s.callState
	s.ringTimer = time.AfterFunc(s.ringWait, func() { s.ringExpired(id, ringing) })
}

// stopRingLocked disarms the ring timeout.
func (s *Session) stopRingLocked() {
	if s.ringTimer != nil {
		s.ringTimer.Stop()
		s.ringTimer = nil
	}
}

// ringExpired is RingTimeout passing with the Call still unanswered. The
// side that was rung says so on the wire; the side that called simply stops
// waiting, so a lost reject still ends the Call at both ends.
func (s *Session) ringExpired(id string, was CallState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.callID != id || s.callState != was {
		return
	}
	if was == Incoming && s.trans != nil {
		_ = s.trans.Send(wire.Reject{CallID: id, Reason: wire.ReasonTimeout})
	}
	s.endCallLocked(CallTimedOut)
}

// setCallLocked moves the Call machine and tells the UI.
func (s *Session) setCallLocked(state CallState, reason CallReason) {
	s.callState = state
	s.events.emit(CallChanged{State: state, CallID: s.callID, Reason: reason})
}

// endCallLocked returns the Session to text: the ring stops, the devices —
// microphone, speaker and camera — are released, and the Call's id is
// forgotten so a straggling frame for it is discarded. Closing the audio
// waits for its capture goroutine, which takes the Session's lock, so it
// happens off this one.
func (s *Session) endCallLocked(reason CallReason) {
	if s.callState == NoCall {
		return
	}
	s.stopRingLocked()
	audio, video := s.audio, s.video
	s.audio, s.video = nil, nil
	s.muted, s.noMic = false, false
	s.cam, s.noCam = false, false
	s.remoteMedia, s.haveRemoteMedia = MediaChanged{}, false
	if audio != nil {
		go func() { _ = audio.Close() }()
	}
	// Closing the video releases the camera and waits for its capture
	// goroutine, which is why it too happens off this lock.
	if video != nil {
		go func() { _ = video.Close() }()
	}
	s.setCallLocked(NoCall, reason)
	s.callID = ""
}
