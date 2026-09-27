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
	return s.setStream(on, streamCamera)
}

// CameraOn reports whether this side's camera is being sent.
func (s *Session) CameraOn() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cam
}

// Share starts or stops sharing this side's entire screen inside an Active
// Call, and tells the other side either way — nobody should have to wonder
// whether their desktop is still being watched. Stopping releases the display,
// and the Call carries on with whatever else was running.
//
// Like Camera it blocks while the device opens, and is never called with the
// Session's lock held.
func (s *Session) Share(on bool) error {
	return s.setStream(on, streamScreen)
}

// Sharing reports whether this side's screen is being shared.
func (s *Session) Sharing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.screen
}

// Frames is the other side's camera video, decoded, one picture at a time; a
// UI reads it and paints whatever it finds. A UI that falls behind misses
// frames rather than holding the Call up, because a picture that is late is
// worth less than the one after it.
//
// The channel is never closed: a Call ending simply stops delivering, and
// the Session's events are what say it ended.
func (s *Session) Frames() <-chan *image.RGBA { return s.frames }

// ScreenFrames is the other side's shared screen on exactly the same terms,
// and on its own channel: a screen and a camera arrive at different rates and
// neither should wait for the other.
func (s *Session) ScreenFrames() <-chan *image.RGBA { return s.screenFrames }

// videoStream names which of a Call's two video streams a command is about.
// The two are identical in shape — open a device, send it, say so — and differ
// only in what they are called and which flags they set, so they share one
// implementation rather than two that must be kept in step.
type videoStream int

const (
	streamCamera videoStream = iota
	streamScreen
)

// setStream turns one of this side's video streams on or off, and tells the
// other side. A device that does not exist on this machine is remembered, so
// that asking again is refused rather than retried; anything else — a camera
// another application is holding, say — is worth trying again in a moment.
func (s *Session) setStream(on bool, which videoStream) error {
	s.mu.Lock()
	if s.closed || s.callState == NoCall {
		s.mu.Unlock()
		return fmt.Errorf("session: there is no Call to %s in", turningOn(which))
	}
	if on && s.missing(which) {
		// There is nothing to turn on, and announcing one that could not be
		// opened would have the other side waiting on a black picture.
		s.mu.Unlock()
		return errors.New("session: " + nothingHere(which))
	}
	video, state := s.video, s.callState
	s.mu.Unlock()
	if video == nil {
		return fmt.Errorf("session: the Call is %s — its video is not open yet", state)
	}

	var err error
	switch which {
	case streamCamera:
		err = video.Camera(on)
	case streamScreen:
		err = video.Screen(on)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		switch {
		case which == streamCamera && errors.Is(err, media.ErrNoCamera):
			s.noCam = true
		case which == streamScreen && errors.Is(err, media.ErrNoScreen):
			s.noScreen = true
		}
		return err
	}
	s.setStreamLocked(which, on)
	s.announceMediaLocked()
	return nil
}

// setStreamLocked records one stream's new state.
func (s *Session) setStreamLocked(which videoStream, on bool) {
	switch which {
	case streamCamera:
		s.cam = on
	case streamScreen:
		s.screen = on
	}
}

// missing reports that this machine has no such device, as a previous attempt
// to open one discovered.
func (s *Session) missing(which videoStream) bool {
	switch which {
	case streamCamera:
		return s.noCam
	case streamScreen:
		return s.noScreen
	}
	return false
}

// turningOn and nothingHere are what a refusal says. They exist so that the
// two streams share an implementation without sharing their words: "there is
// no camera on this machine" and "there is no screen to share" are different
// problems to the person reading them.
func turningOn(which videoStream) string {
	if which == streamScreen {
		return "share a screen"
	}
	return "turn a camera on"
}

func nothingHere(which videoStream) string {
	if which == streamScreen {
		return "there is no screen to share on this machine"
	}
	return "there is no camera on this machine"
}

// startVideoLocked builds the Call's video pipeline — camera and screen both.
// It opens no device, so unlike the microphone it cannot fail for want of
// hardware, and a Call that stays audio-only never touches either. The only
// thing it can refuse is a pipeline with nowhere to send or show frames, which
// is not a thing this call can be.
func (s *Session) startVideoLocked(gen int) {
	video, err := media.StartVideo(media.VideoOptions{
		Devices: s.devices,
		Camera: media.StreamOptions{
			Send:         func(frame []byte, d time.Duration) { s.sendVideo(gen, frame, d) },
			Frame:        s.deliverFrame,
			NeedKeyframe: func() { s.requestKeyframe(gen) },
			Stopped:      func() { s.streamStopped(gen, streamCamera) },
		},
		Screen: media.StreamOptions{
			Send:         func(frame []byte, d time.Duration) { s.sendScreen(gen, frame, d) },
			Frame:        s.deliverScreenFrame,
			NeedKeyframe: func() { s.requestScreenKeyframe(gen) },
			Stopped:      func() { s.streamStopped(gen, streamScreen) },
		},
	})
	if err != nil {
		return
	}
	s.video = video
}

// streamStopped is one of this side's devices going away by itself — a camera
// unplugged, a display that went. The other side is told, because a device that
// has gone is off however it went.
func (s *Session) streamStopped(gen int, which videoStream) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || gen != s.gen {
		return
	}
	switch which {
	case streamCamera:
		if !s.cam {
			return
		}
	case streamScreen:
		if !s.screen {
			return
		}
	}
	s.setStreamLocked(which, false)
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

// sendVideo hands one encoded camera frame to the transport, on the capture
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

// sendScreen does the same for one frame of the shared screen.
func (s *Session) sendScreen(gen int, frame []byte, d time.Duration) {
	trans, _, live := s.liveCall(gen)
	if !live || trans == nil {
		return
	}
	_ = trans.WriteScreen(frame, d)
}

// onVideo decodes one received camera frame. Decoding happens inside the
// pipeline, off the Session's lock, because it takes milliseconds.
func (s *Session) onVideo(gen int, frame []byte) {
	_, video, live := s.liveCall(gen)
	if !live || video == nil {
		return
	}
	video.PlayCamera(frame)
}

// onScreen decodes one received frame of the other side's shared screen.
func (s *Session) onScreen(gen int, frame []byte) {
	_, video, live := s.liveCall(gen)
	if !live || video == nil {
		return
	}
	video.PlayScreen(frame)
}

// onKeyframeWanted is the other side saying it cannot decode this side's
// camera. The next frame out is a keyframe.
func (s *Session) onKeyframeWanted(gen int) {
	_, video, live := s.liveCall(gen)
	if !live || video == nil {
		return
	}
	video.ForceCameraKeyframe()
}

// onScreenKeyframeWanted is the same for the shared screen — and matters more
// there: a screen that arrives undecodable stays wrong until it changes, which
// on a desktop may be a long time.
func (s *Session) onScreenKeyframeWanted(gen int) {
	_, video, live := s.liveCall(gen)
	if !live || video == nil {
		return
	}
	video.ForceScreenKeyframe()
}

// requestKeyframe asks the other side for one on the camera stream, because
// nothing arriving here can be decoded without it.
func (s *Session) requestKeyframe(gen int) {
	trans, _, live := s.liveCall(gen)
	if !live || trans == nil {
		return
	}
	trans.RequestKeyframe()
}

// requestScreenKeyframe asks for one on the screen stream.
func (s *Session) requestScreenKeyframe(gen int) {
	trans, _, live := s.liveCall(gen)
	if !live || trans == nil {
		return
	}
	trans.RequestScreenKeyframe()
}

// deliverFrame puts one decoded camera picture in front of the UI, keeping only
// the newest: a UI that is mid-frame when the next one arrives should paint the
// latest picture, not a queue of stale ones.
func (s *Session) deliverFrame(img *image.RGBA) { newest(s.frames, img) }

// deliverScreenFrame does the same for the other side's shared screen.
func (s *Session) deliverScreenFrame(img *image.RGBA) { newest(s.screenFrames, img) }

// newest replaces whatever is waiting on a one-deep frame channel.
func newest(frames chan *image.RGBA, img *image.RGBA) {
	select {
	case <-frames:
	default:
	}
	select {
	case frames <- img:
	default:
	}
}
