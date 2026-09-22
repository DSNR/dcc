package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"log"
	"math"
	"net/http"
	"time"

	"sync/atomic"

	"github.com/pion/opus"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/thesyncim/govpx"
)

//go:embed page.html
var pageHTML []byte

// browser: Go sends govpx VP8 + pion/opus Opus to Chrome over WebRTC; Chrome
// sends its mic (libopus) back, which we decode with pion/opus. Proves
// interop both ways.
func runBrowser(args []string) {
	fs := flag.NewFlagSet("browser", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8080", "listen")
	useScreen := fs.Bool("screen", false, "send live screen instead of synthetic frames")
	fs.StringVar(&packetizerMode, "packetizer", "govpx", "video RTP packetizer: govpx (RFC 7741 w/ PictureID) | pion (codecs.VP8Payloader)")
	play := fs.Bool("play", false, "play Chrome's mic through our speakers (pion/opus decode -> platform audio)")
	fs.Parse(args)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.Write(pageHTML) })
	http.HandleFunc("/offer", func(w http.ResponseWriter, r *http.Request) {
		var offer webrtc.SessionDescription
		if err := json.NewDecoder(r.Body).Decode(&offer); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		ans, err := answer(offer, *useScreen, *play)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		json.NewEncoder(w).Encode(ans)
	})
	http.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		json.NewDecoder(r.Body).Decode(&m)
		fmt.Println("chrome stats:", m)
	})
	fmt.Printf("open http://%s in Chrome (allow mic). Ctrl-C to stop.\n", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

func answer(offer webrtc.SessionDescription, useScreen, play bool) (*webrtc.SessionDescription, error) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, err
	}
	vt, _ := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}, "video", "dcc")
	at, _ := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "dcc")
	vs, err := pc.AddTrack(vt)
	if err != nil {
		return nil, err
	}
	var wantKey atomic.Bool
	go func() { // answer PLI/FIR with a keyframe
		for {
			pkts, _, err := vs.ReadRTCP()
			if err != nil {
				return
			}
			for _, p := range pkts {
				switch p.(type) {
				case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
					fmt.Println("rtcp: keyframe requested")
					wantKey.Store(true)
				}
			}
		}
	}()
	if _, err := pc.AddTrack(at); err != nil {
		return nil, err
	}
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) { fmt.Println("pc:", s) })
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		fmt.Println("remote track:", tr.Codec().MimeType)
		if tr.Kind() != webrtc.RTPCodecTypeAudio {
			return
		}
		go decodeRemoteOpus(tr, play)
	})
	if err := pc.SetRemoteDescription(offer); err != nil {
		return nil, err
	}
	ans, err := pc.CreateAnswer(nil)
	if err != nil {
		return nil, err
	}
	g := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(ans); err != nil {
		return nil, err
	}
	<-g
	go sendVideo(vt, useScreen, &wantKey)
	go sendAudio(at)
	return pc.LocalDescription(), nil
}

var packetizerMode string

func sendVideo(t *webrtc.TrackLocalStaticRTP, useScreen bool, wantKey *atomic.Bool) {
	const w, h = 640, 480
	enc, err := newEncoder(w, h, 30, 1000, 2, -6)
	if err != nil {
		log.Fatal(err)
	}
	frames := synthFrames(w, h, 90)
	buf := make([]byte, 1<<20)
	tick := time.NewTicker(time.Second / 30)
	var scaled *image.RGBA
	live := newI420(w, h)
	encS := &stats{}
	pionPk := rtp.NewPacketizer(1200, 96, 0, &codecs.VP8Payloader{EnablePictureID: true}, rtp.NewRandomSequencer(), 90000)
	var seq uint16
	var picID uint16
	var ts uint32
	fmt.Println("video packetizer:", packetizerMode)
	for i := 0; ; i++ {
		<-tick.C
		src := frames[i%len(frames)]
		if useScreen {
			shot, _, _, err := captureScaled(w, scaled)
			if err == nil {
				scaled = shot
				crop := image.NewRGBA(image.Rect(0, 0, w, h))
				for y := 0; y < h && y < shot.Bounds().Dy(); y++ {
					copy(crop.Pix[y*crop.Stride:(y+1)*crop.Stride], shot.Pix[y*shot.Stride:y*shot.Stride+w*4])
				}
				rgbaToI420(crop, live)
				src = live
			}
		}
		var flags govpx.EncodeFlags
		if wantKey.Swap(false) {
			flags = govpx.EncodeForceKeyFrame
		}
		st := time.Now()
		res, err := enc.EncodeInto(buf, src, uint64(i), 1, flags)
		encS.add(time.Since(st))
		if err != nil {
			log.Println("encode:", err)
			continue
		}
		ts += 3000
		if res.Dropped || len(res.Data) == 0 {
			continue
		}
		var pkts []*rtp.Packet
		switch packetizerMode {
		case "pion":
			pkts = pionPk.Packetize(res.Data, 3000)
		default:
			frags, err := res.PacketizeWebRTCRTP(picID, 1200)
			if err != nil {
				log.Println("packetize:", err)
				continue
			}
			picID = govpx.NextVP8RTPPictureID(picID)
			for _, f := range frags {
				seq++
				pkts = append(pkts, &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: seq, Timestamp: ts, Marker: f.Marker}, Payload: f.Payload})
			}
		}
		if i < 4 || res.KeyFrame {
			p := pkts[0]
			n := min(len(p.Payload), 8)
			fmt.Printf("frame %d key=%v bytes=%d pkts=%d | first pkt seq=%d ts=%d marker(last)=%v payload[0:%d]=% x\n",
				i, res.KeyFrame, len(res.Data), len(pkts), p.SequenceNumber, p.Timestamp, pkts[len(pkts)-1].Marker, n, p.Payload[:n])
		}
		for _, p := range pkts {
			if err := t.WriteRTP(p); err != nil {
				log.Println("write video:", err)
				return
			}
		}
		if i%150 == 149 {
			fmt.Printf("video sent %d frames, encode %s\n", i+1, encS)
		}
	}
}

func sendAudio(t *webrtc.TrackLocalStaticSample) {
	enc, err := opus.NewEncoder(opus.WithSampleRate(48000), opus.WithChannels(1), opus.WithBitrate(32000))
	if err != nil {
		log.Fatal(err)
	}
	pkt := make([]byte, 4000)
	tick := time.NewTicker(20 * time.Millisecond)
	phase := 0
	for i := 0; ; i++ {
		<-tick.C
		// 440 Hz tone, on for 0.5 s every 2 s so it's obviously "ours"
		amp := 0.0
		if (i/25)%4 == 0 {
			amp = 0.3
		}
		pcm := sine(opusFrame, 440, amp, phase)
		phase += opusFrame
		n, err := enc.Encode(int16le(pcm), pkt)
		if err != nil {
			log.Println("opus encode:", err)
			continue
		}
		if err := t.WriteSample(media.Sample{Data: pkt[:n], Duration: 20 * time.Millisecond}); err != nil {
			return
		}
	}
}

func decodeRemoteOpus(tr *webrtc.TrackRemote, play bool) {
	dec, err := opus.NewDecoderWithOutput(48000, 1)
	if err != nil {
		log.Fatal(err)
	}
	var stop func()
	q := make(chan []int16, 64)
	var pending []int16
	if play {
		stop, err = startPlayback(48000, func(out []int16) int {
			n := 0
			for n < len(out) {
				if len(pending) == 0 {
					select {
					case pending = <-q:
					default:
						for ; n < len(out); n++ {
							out[n] = 0
						}
						return n
					}
				}
				k := copy(out[n:], pending)
				pending = pending[k:]
				n += k
			}
			return n
		})
		if err != nil {
			fmt.Println("playback open failed:", err)
		} else {
			defer stop()
		}
	}
	out := make([]int16, 5760)
	var pkts, fails, samples int
	var rms float64
	last := time.Now()
	decS := &stats{}
	for {
		p, _, err := tr.ReadRTP()
		if err != nil {
			fmt.Println("remote audio ended:", err)
			return
		}
		pkts++
		if len(p.Payload) == 0 {
			fmt.Printf("empty audio payload (padding=%v marker=%v seq=%d) — skipped\n", p.Padding, p.Marker, p.SequenceNumber)
			continue
		}
		st := time.Now()
		n, err := dec.DecodeToInt16(p.Payload, out)
		decS.add(time.Since(st))
		if err != nil {
			fails++
			if fails <= 5 {
				fmt.Println("pion/opus decode of libopus packet failed:", err)
			}
			continue
		}
		samples += n
		for _, v := range out[:n] {
			rms += float64(v) * float64(v)
		}
		if play {
			select {
			case q <- append([]int16(nil), out[:n]...):
			default:
			}
		}
		if time.Since(last) > 2*time.Second {
			fmt.Printf("mic from Chrome: %d pkts, %d decode fails, %d samples, rms %.0f, decode %s\n",
				pkts, fails, samples, math.Sqrt(rms/float64(samples+1)), decS)
			rms, samples, last = 0, 0, time.Now()
		}
	}
}
