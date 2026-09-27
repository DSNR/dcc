package transport

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	pionmedia "github.com/pion/webrtc/v4/pkg/media"
	"github.com/thesyncim/govpx"

	"github.com/DSNR/dcc/internal/media"
)

// The streams a Call negotiates: a microphone track, a camera track and a
// screen track. All three go up together, once, the first time a Call in this
// Session is accepted — so muting, turning a camera off and starting a screen
// share are a matter of writing frames or not, never of renegotiating, and a
// second Call needs no negotiation at all.
const (
	streamID    = "dcc"
	trackAudio  = "mic"
	trackCamera = "cam"
	trackScreen = "screen"
)

// videoMTU is how much of one RTP packet a VP8 payload may fill, descriptor
// included but RTP header excluded. It leaves room for the RTP header, the
// SRTP tag and an IP header inside a 1500-byte path, which is what keeps a
// video frame off the fragmentation path.
const videoMTU = 1200

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
	// The camera and screen tracks are negotiated now and written to when
	// someone turns a camera on or starts sharing — that is the whole point
	// of doing all three at once. A Windows build that can never send a
	// camera frame still negotiates the track, so the protocol is the same on
	// both platforms; it simply never writes to it. Each carries its own name
	// so the other side knows which of its two video streams is which.
	camera, err := videoTrack(trackCamera)
	if err != nil {
		return err
	}
	screen, err := videoTrack(trackScreen)
	if err != nil {
		return err
	}
	for _, track := range []*webrtc.TrackLocalStaticRTP{camera, screen} {
		sender, err := t.pc.AddTrack(track)
		if err != nil {
			return fmt.Errorf("transport: adding the %s track: %w", track.ID(), err)
		}
		// Somebody has to read each sender's RTCP, or pion's receive buffer
		// backs up. It also matters what is in it: a PLI is the other side
		// saying it cannot decode what we are sending on that stream, and the
		// only answer is a keyframe on that same stream.
		wanted := func(o Options) func() { return o.KeyframeWanted }
		if track.ID() == trackScreen {
			wanted = func(o Options) func() { return o.ScreenKeyframeWanted }
		}
		go t.readSenderRTCP(sender, wanted)
	}

	t.mu.Lock()
	t.audio = audio
	t.camera.track, t.screen.track = camera, screen
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

// videoTrack makes one of the Call's video tracks. They are StaticRTP rather
// than StaticSample because dcc packetizes VP8 itself — RFC 7741 descriptors
// with a PictureID, which is what pion's sample writer will not produce.
func videoTrack(id string) (*webrtc.TrackLocalStaticRTP, error) {
	track, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: media.VideoClockRate},
		id, streamID,
	)
	if err != nil {
		return nil, fmt.Errorf("transport: making the %s track: %w", id, err)
	}
	return track, nil
}

// videoStream is one of the Call's two video streams as the transport sees it:
// the local track frames are written to, the RTP state that belongs to the
// stream rather than to any one frame, and the SSRC of the other side's
// equivalent, which is what a PLI has to name. Every field is held under the
// Transport's lock.
type videoStream struct {
	track *webrtc.TrackLocalStaticRTP
	// sequence and timestamp are what every packet of this stream carries;
	// pictureID is what each of its frames is stamped with.
	sequence  uint16
	timestamp uint32
	pictureID uint16
	// remote is the other side's SSRC for this stream, zero until their first
	// packet on it arrives.
	remote webrtc.SSRC
}

// WriteVideo sends one encoded frame of camera video: packetized into RTP
// payloads at videoMTU, each with a 15-bit PictureID, the last one carrying
// the marker bit that says the frame is complete. Like WriteAudio it is an
// error before StartMedia and after the Call's transport has gone.
func (t *Transport) WriteVideo(frame []byte, d time.Duration) error {
	return t.writeVideo(&t.camera, "video", frame, d)
}

// WriteScreen sends one encoded frame of this side's shared screen, the same
// way, on the stream that is only ever the screen — which is how the other
// side knows a picture of a desktop from a picture of a face.
func (t *Transport) WriteScreen(frame []byte, d time.Duration) error {
	return t.writeVideo(&t.screen, "a shared screen", frame, d)
}

// writeVideo packetizes and sends one frame on one of the video streams. what
// names the stream in the error a caller reads.
func (t *Transport) writeVideo(stream *videoStream, what string, frame []byte, d time.Duration) error {
	t.mu.Lock()
	track := stream.track
	if track == nil {
		t.mu.Unlock()
		return fmt.Errorf("transport: there is no Call to send %s to", what)
	}
	// The descriptor's PictureID and the RTP timestamp belong to the stream,
	// not the frame, so they are minted here under the lock that owns them.
	picture := stream.pictureID
	stream.pictureID = govpx.NextVP8RTPPictureID(stream.pictureID)
	timestamp := stream.timestamp
	stream.timestamp += uint32(d.Seconds() * media.VideoClockRate)
	t.mu.Unlock()

	packets, err := govpx.PacketizeVP8RTPFrame(govpx.VP8RTPPayloadDescriptor{
		PictureIDPresent: true,
		PictureID15Bit:   true,
		PictureID:        picture,
	}, frame, videoMTU)
	if err != nil {
		return fmt.Errorf("transport: packetizing a frame of %s: %w", what, err)
	}
	for _, packet := range packets {
		t.mu.Lock()
		sequence := stream.sequence
		stream.sequence++
		t.mu.Unlock()
		if err := track.WriteRTP(&rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				Marker:         packet.Marker,
				SequenceNumber: sequence,
				Timestamp:      timestamp,
			},
			Payload: packet.Payload,
		}); err != nil {
			return fmt.Errorf("transport: sending %s: %w", what, err)
		}
	}
	return nil
}

// RequestKeyframe asks the other side for a keyframe on the camera stream,
// which is the only way back from a stream whose reference frames were lost. A
// Call with no video in it yet has nothing to ask.
func (t *Transport) RequestKeyframe() { t.requestKeyframe(&t.camera) }

// RequestScreenKeyframe is RequestKeyframe for the shared screen.
func (t *Transport) RequestScreenKeyframe() { t.requestKeyframe(&t.screen) }

// requestKeyframe sends a PLI naming one stream's remote SSRC.
func (t *Transport) requestKeyframe(stream *videoStream) {
	t.mu.Lock()
	ssrc, ended := stream.remote, t.ended
	t.mu.Unlock()
	if ended || ssrc == 0 {
		return
	}
	// One lost PLI is one more black second, not a dead Call: the decoder
	// asks again.
	_ = t.pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(ssrc)}})
}

// readSenderRTCP drains one sender's RTCP and answers the part that needs
// answering: a PLI or a FIR is the other side asking for a keyframe on that
// sender's stream. wanted picks the callback that belongs to it, read fresh
// each time because the Options may have been replaced by then.
func (t *Transport) readSenderRTCP(sender *webrtc.RTPSender, wanted func(Options) func()) {
	buf := make([]byte, 1500)
	for {
		n, _, err := sender.Read(buf)
		if err != nil {
			return
		}
		packets, err := rtcp.Unmarshal(buf[:n])
		if err != nil {
			continue
		}
		for _, packet := range packets {
			switch packet.(type) {
			case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
				t.mu.Lock()
				ask, ended := wanted(t.opts), t.ended
				t.mu.Unlock()
				if !ended && ask != nil {
					ask()
				}
			}
		}
	}
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
// packet for it does. Each of the three is read by whoever wants it; anything
// else is drained, so a peer that sends a stream this build knows nothing about
// does not back pion's receiver up.
func (t *Transport) onTrack(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
	switch {
	case track.Kind() == webrtc.RTPCodecTypeAudio:
		go t.readAudio(track)
	case track.ID() == trackCamera:
		go t.readVideo(track, &t.camera, func(o Options) func([]byte) { return o.Video })
	case track.ID() == trackScreen:
		go t.readVideo(track, &t.screen, func(o Options) func([]byte) { return o.Screen })
	default:
		go drain(track)
	}
}

// readVideo reassembles one of the other side's video streams: RTP payloads
// collect until the marker bit says the frame is complete, and the frame goes
// to the Call. A gap in the sequence numbers means a fragment is missing, so
// the frame it belonged to is dropped whole — half a VP8 frame is not a
// picture, and the decoder asks for a keyframe when it notices what it lost.
func (t *Transport) readVideo(track *webrtc.TrackRemote, stream *videoStream, deliver func(Options) func([]byte)) {
	t.mu.Lock()
	stream.remote = track.SSRC()
	t.mu.Unlock()

	var (
		frame    []govpx.RTPPayloadFragment
		previous uint16
		started  bool
		broken   bool
	)
	for {
		pkt, _, err := track.ReadRTP()
		if err != nil {
			return
		}
		if started && pkt.SequenceNumber != previous+1 {
			broken = true
		}
		previous, started = pkt.SequenceNumber, true
		if len(pkt.Payload) > 0 {
			frame = append(frame, govpx.RTPPayloadFragment{Payload: pkt.Payload, Marker: pkt.Marker})
		}
		if !pkt.Marker {
			continue
		}
		complete, whole := frame, !broken
		frame, broken = nil, false
		if !whole || len(complete) == 0 {
			continue
		}
		assembled, err := govpx.AssembleVP8RTPFrame(complete)
		if err != nil {
			continue
		}
		t.mu.Lock()
		show := deliver(t.opts)
		ended := t.ended
		t.mu.Unlock()
		if ended {
			return
		}
		if show != nil {
			show(assembled)
		}
	}
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
// SDP says what a Call can actually do: PCMU for audio, VP8 for the camera
// and the screen.
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
			ClockRate: media.VideoClockRate,
			// The feedback dcc's video actually relies on, negotiated rather
			// than assumed: retransmission of lost packets, and the PLI that
			// asks for a keyframe when a stream cannot be decoded. Two dcc
			// installs would manage without the SDP saying so; a browser on
			// the other end would not.
			RTCPFeedback: []webrtc.RTCPFeedback{
				{Type: "nack"},
				{Type: "nack", Parameter: "pli"},
			},
		},
		PayloadType: media.PayloadTypeVP8,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, fmt.Errorf("transport: registering the video codec: %w", err)
	}
	return engine, nil
}
