package session

import (
	"errors"
	"fmt"
	"image"
	"time"

	"github.com/DSNR/dcc/internal/media"
	"github.com/DSNR/dcc/internal/transport"
)

// Camera turns this side's camera on or off inside an Active Call, and tells
// the other side either way — a camera whose state the other person cannot
// see is how people end up talking to a black rectangle. Turning it off
// releases the device: the light beside it going out is the only camera-off
// worth having.
//
// It blocks while the device opens, which on a real camera is a moment, so it
// is deliberately not called with the Session's lock held.
func (s *Session) Camera(on bool) error {
	s.mu.Lock()
	if s.closed || s.callState == NoCall {
		s.mu.Unlock()
		return errors.New("session: there is no Call to turn a camera on in")
	}
	if on && s.noCam {
		// There is no camera to turn on, and saying cam:true over one that
		// could not be opened would have the other side waiting on a black
		// picture.
		s.mu.Unlock()
		return errors.New("session: there is no camera on this machine")
	}
	video, state := s.video, s.callState
	s.mu.Unlock()
	if video == nil {
		return fmt.Errorf("session: the Call is %s — its camera is not open yet", state)
	}

	err := video.Camera(on)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		// A machine with no camera at all is remembered, so that asking again
		// is refused rather than retried. Anything else — a camera another
		// application is holding, say — is worth trying again in a moment.
		if errors.Is(err, media.ErrNoCamera) {
			s.noCam = true
		}
		return err
	}
	s.cam = on
	s.announceMediaLocked()
	return nil
}

// CameraOn reports whether this side's camera is being sent.
func (s *Session) CameraOn() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cam
}

// Frames is the other side's video, decoded, one picture at a time. A UI
// reads it and paints whatever it finds; a UI that falls behind misses
// frames rather than holding the Call up, because a picture that is late is
// worth less than the one after it.
//
// The channel is never closed: a Call ending simply stops delivering, and
// the Session's events are what say it ended.
func (s *Session) Frames() <-chan *image.RGBA { return s.frames }

// startVideoLocked builds the Call's video pipeline. It opens no device — a
// camera is opened only when someone turns one on — so unlike the microphone
// it cannot fail for want of hardware, and a Call that stays audio-only never
// touches the camera at all. The only thing it can refuse is a pipeline with
// nowhere to send or show frames, which is not a thing this call can be.
func (s *Session) startVideoLocked(gen int) {
	video, err := media.StartVideo(media.VideoOptions{
		Devices:      s.devices,
		Send:         func(frame []byte, d time.Duration) { s.sendVideo(gen, frame, d) },
		Frame:        s.deliverFrame,
		NeedKeyframe: func() { s.requestKeyframe(gen) },
		Stopped:      func() { s.cameraStopped(gen) },
	})
	if err != nil {
		return
	}
	s.video = video
}

// cameraStopped is this side's camera going away by itself — unplugged, or a
// driver that gave up. The other side is told, because a camera that has gone
// is off however it went.
func (s *Session) cameraStopped(gen int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || gen != s.gen || !s.cam {
		return
	}
	s.cam = false
	s.announceMediaLocked()
}

// liveCall is the Call's transport and video pipeline, or false where there is
// no longer a Call for them to belong to: the Session has ended, the
// connection has been rebuilt underneath it, or the Call is over. Every video
// callback starts here, because every one of them can arrive late.
func (s *Session) liveCall(gen int) (*transport.Transport, *media.Video, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || gen != s.gen || s.callState != Active {
		return nil, nil, false
	}
	return s.trans, s.video, true
}

// sendVideo hands one encoded frame to the transport, on the capture
// goroutine, the way sendAudio does.
func (s *Session) sendVideo(gen int, frame []byte, d time.Duration) {
	trans, _, live := s.liveCall(gen)
	if !live || trans == nil {
		return
	}
	// A refused frame is one frame of video nobody sees. The Call is not
	// worth ending over it.
	_ = trans.WriteVideo(frame, d)
}

// onVideo decodes one received frame. Decoding happens inside the pipeline,
// off the Session's lock, because it takes milliseconds.
func (s *Session) onVideo(gen int, frame []byte) {
	_, video, live := s.liveCall(gen)
	if !live || video == nil {
		return
	}
	video.Play(frame)
}

// onKeyframeWanted is the other side saying it cannot decode this side's
// video. The next frame out is a keyframe.
func (s *Session) onKeyframeWanted(gen int) {
	_, video, live := s.liveCall(gen)
	if !live || video == nil {
		return
	}
	video.ForceKeyframe()
}

// requestKeyframe asks the other side for one, because nothing arriving here
// can be decoded without it.
func (s *Session) requestKeyframe(gen int) {
	trans, _, live := s.liveCall(gen)
	if !live || trans == nil {
		return
	}
	trans.RequestKeyframe()
}

// deliverFrame puts one decoded picture in front of the UI, keeping only the
// newest: a UI that is mid-frame when the next one arrives should paint the
// latest picture, not a queue of stale ones.
func (s *Session) deliverFrame(img *image.RGBA) {
	select {
	case <-s.frames:
	default:
	}
	select {
	case s.frames <- img:
	default:
	}
}
