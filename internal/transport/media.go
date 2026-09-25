package transport

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"
	pionmedia "github.com/pion/webrtc/v4/pkg/media"

	"github.com/DSNR/dcc/internal/media"
)

// The streams a Call negotiates: a microphone track, plus the camera and
// screen transceivers alongside it. All three go up together, once, the
// first time a Call in this Session is accepted — so muting, turning a
// camera off and starting a screen share are a matter of writing frames or
// not, never of renegotiating, and a second Call needs no negotiation at
// all.
const (
	streamID   = "dcc"
	trackAudio = "mic"
	// videoTransceivers is the camera and the screen. They are negotiated
	// now and written to by the work that adds video, so they need no local
	// track yet — only a place in the SDP.
	videoTransceivers = 2
)

// StartMedia negotiates the Call's transceivers and reports through MediaUp
// when they are in place. It is idempotent and safe from either side: the
// transport's initiator renegotiates, the other side only makes its tracks,
// and an offer that arrives before this side has got here starts media on
// its own. Asked again once media is already negotiated — a second Call in
// the same Session, or an accept that lost the race to the offer it belongs
// to — it reports MediaUp straight back, because the transceivers a Call
// needs are already there.
func (t *Transport) StartMedia() error {
	t.mu.Lock()
	if t.ended {
		t.mu.Unlock()
		return errors.New("transport: the connection is gone")
	}
	if t.mediaStarted {
		negotiated, up := t.mediaUp, t.opts.MediaUp
		t.mu.Unlock()
		if negotiated && up != nil {
			// Off this goroutine: the caller may be holding the lock that
			// the callback needs.
			go up()
		}
		return nil
	}
	t.mediaStarted = true
	initiator := t.opts.Initiator
	t.mu.Unlock()

	audio, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: media.MimeTypePCMU, ClockRate: media.SampleRate, Channels: 1},
		trackAudio, streamID,
	)
	if err != nil {
		return fmt.Errorf("transport: making the microphone track: %w", err)
	}
	if _, err := t.pc.AddTrack(audio); err != nil {
		return fmt.Errorf("transport: adding the microphone track: %w", err)
	}
	// The camera and screen transceivers are negotiated now and written to
	// later — that is the whole point of doing all three at once. A Windows
	// build that can never send a camera frame still negotiates the
	// transceiver, so the protocol is the same on both platforms.
	for range videoTransceivers {
		if _, err := t.pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
			webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendrecv}); err != nil {
			return fmt.Errorf("transport: adding a video transceiver: %w", err)
		}
	}

	t.mu.Lock()
	t.audio = audio
	t.mu.Unlock()

	if !initiator {
		// The responder's tracks exist; the initiator's offer is what puts
		// them in an SDP.
		return nil
	}
	return t.offer()
}

// WriteAudio sends one encoded frame of microphone audio. It is an error
// before StartMedia and after the Call's transport has gone — both of which
// mean the same thing to the caller: that frame was not sent.
func (t *Transport) WriteAudio(payload []byte, d time.Duration) error {
	t.mu.Lock()
	audio := t.audio
	t.mu.Unlock()
	if audio == nil {
		return errors.New("transport: there is no Call to send audio to")
	}
	if err := audio.WriteSample(pionmedia.Sample{Data: payload, Duration: d}); err != nil {
		return fmt.Errorf("transport: sending audio: %w", err)
	}
	return nil
}

// mediaNegotiated reports the Call's renegotiation finishing on this side —
// the transceivers are in place and both directions are open. It is the
// moment a Call becomes Active, and deliberately not the moment the first
// RTP packet arrives: a Call answered muted sends nothing at all, and is a
// Call nonetheless. It fires on the one transition; StartMedia is what tells
// a later Call the media is already up.
func (t *Transport) mediaNegotiated() {
	t.mu.Lock()
	if t.mediaUp || t.ended {
		t.mu.Unlock()
		return
	}
	t.mediaUp = true
	up := t.opts.MediaUp
	t.mu.Unlock()
	if up != nil {
		up()
	}
}

// onTrack is a remote track arriving — pion reports it when the first RTP
// packet for it does. Only audio is read; a video track is drained so the
// receiver does not back up.
func (t *Transport) onTrack(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
	if track.Kind() != webrtc.RTPCodecTypeAudio {
		go drain(track)
		return
	}
	go t.readAudio(track)
}

// readAudio feeds each received audio frame to the Call. Padding-only
// packets are skipped: they carry no audio, and handing an empty payload to
// the decoder would play a frame of nothing that was never sent.
func (t *Transport) readAudio(track *webrtc.TrackRemote) {
	for {
		pkt, _, err := track.ReadRTP()
		if err != nil {
			return
		}
		if len(pkt.Payload) == 0 {
			continue
		}
		t.mu.Lock()
		audio := t.opts.Audio
		ended := t.ended
		t.mu.Unlock()
		if ended {
			return
		}
		if audio != nil {
			audio(pkt.Payload)
		}
	}
}

// drain reads a track nobody is listening to, so that pion's receiver does
// not stall, and stops when the track does.
func drain(track *webrtc.TrackRemote) {
	for {
		if _, _, err := track.ReadRTP(); err != nil {
			return
		}
	}
}

// offerHasMedia reports whether an SDP carries the Call's streams. An offer
// that does means the other side accepted a Call and renegotiated, which is
// this side's cue to make its own tracks before answering — the two accepts
// travel on different transports, so neither side can assume it got there
// first.
func offerHasMedia(sdp string) bool {
	return strings.Contains(sdp, "m=audio")
}

// mediaEngine registers exactly the codecs dcc speaks — no more, so that an
// SDP says what a Call can actually do. Audio is PCMU; the video codec is
// registered now so the camera and screen transceivers have something to
// negotiate, and is written to by the work that adds video.
func mediaEngine() (*webrtc.MediaEngine, error) {
	engine := &webrtc.MediaEngine{}
	if err := engine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:    media.MimeTypePCMU,
			ClockRate:   media.SampleRate,
			Channels:    1,
			SDPFmtpLine: "",
		},
		PayloadType: media.PayloadTypePCMU,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, fmt.Errorf("transport: registering the audio codec: %w", err)
	}
	if err := engine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeVP8,
			ClockRate: 90000,
		},
		PayloadType: 96,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, fmt.Errorf("transport: registering the video codec: %w", err)
	}
	return engine, nil
}
