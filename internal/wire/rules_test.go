package wire_test

import (
	"strings"
	"testing"

	"github.com/DSNR/dcc/internal/wire"
)

const (
	sampleID     = "018f4c1e-0b2a-7c3d-8e4f-5a6b7c8d9e0f"
	sampleCallID = "018f4c22-1111-7000-8000-abcdefabcdef"
)

// TestDecodeDisposition pins the receiver's obligations: which bad frames a
// Session shrugs off, and which ones must end the connection.
func TestDecodeDisposition(t *testing.T) {
	cases := []struct {
		name string
		path wire.Path
		in   string
		want wire.Disposition
	}{
		// A type we don't know is a type a newer peer added: ignore it and
		// keep talking.
		{"unknown type", wire.PathData, `{"t":"typing","v":1,"on":true}`, wire.IgnoreFrame},
		{"missing type", wire.PathData, `{"v":1}`, wire.IgnoreFrame},
		{"unknown type at an unknown version", wire.PathData, `{"t":"typing","v":9}`, wire.IgnoreFrame},
		{"signaling type on the data path", wire.PathData, `{"t":"offer","v":1,"sdp":"v=0\r\n"}`, wire.IgnoreFrame},
		{"data type on the signaling path", wire.PathSignal, `{"t":"bye","v":1}`, wire.IgnoreFrame},

		// The version is protocol-wide: a known type at another version means
		// the two sides no longer agree on what the bytes mean.
		{"future version", wire.PathData, `{"t":"bye","v":2}`, wire.CloseConnection},
		{"missing version", wire.PathData, `{"t":"bye"}`, wire.CloseConnection},
		{"mistyped version", wire.PathData, `{"t":"bye","v":"1"}`, wire.CloseConnection},

		// Malformed frames are dropped; the Session survives.
		{"not json", wire.PathData, `this is not json`, wire.DropFrame},
		{"not an object", wire.PathData, `[{"t":"bye","v":1}]`, wire.DropFrame},
		{"truncated json", wire.PathData, `{"t":"bye","v":1`, wire.DropFrame},
		{"text without an id", wire.PathData, `{"t":"text","v":1,"body":"hi"}`, wire.DropFrame},
		{"text with a non-uuid id", wire.PathData, `{"t":"text","v":1,"id":"7","body":"hi"}`, wire.DropFrame},
		{"text without a body", wire.PathData, `{"t":"text","v":1,"id":"` + sampleID + `"}`, wire.DropFrame},
		{"text with a mistyped body", wire.PathData, `{"t":"text","v":1,"id":"` + sampleID + `","body":42}`, wire.DropFrame},
		{"text over the body cap", wire.PathData, `{"t":"text","v":1,"id":"` + sampleID + `","body":"` + strings.Repeat("x", wire.MaxTextBytes+1) + `"}`, wire.DropFrame},
		{"ack without an id", wire.PathData, `{"t":"ack","v":1}`, wire.DropFrame},
		{"call without a call_id", wire.PathData, `{"t":"call","v":1}`, wire.DropFrame},
		{"reject with an unknown reason", wire.PathData, `{"t":"reject","v":1,"call_id":"` + sampleCallID + `","reason":"nope"}`, wire.DropFrame},
		{"media missing a field", wire.PathData, `{"t":"media","v":1,"mic":true,"cam":false}`, wire.DropFrame},
		{"media with a mistyped field", wire.PathData, `{"t":"media","v":1,"mic":"yes","cam":false,"screen":false}`, wire.DropFrame},
		{"ice missing mline", wire.PathSignal, `{"t":"ice","v":1,"candidate":"candidate:1 1 udp 1 192.0.2.1 1 typ host","mid":"0"}`, wire.DropFrame},
		{"ice candidate without a mid", wire.PathSignal, `{"t":"ice","v":1,"candidate":"candidate:1 1 udp 1 192.0.2.1 1 typ host","mid":"","mline":0}`, wire.DropFrame},
		{"rejected with an unknown reason", wire.PathSignal, `{"t":"rejected","v":1,"reason":"busy"}`, wire.DropFrame},

		// Signaling can't recover from a broken offer or answer, so those are
		// the one exception to dropping.
		{"offer without an sdp", wire.PathSignal, `{"t":"offer","v":1}`, wire.CloseConnection},
		{"offer with a mistyped sdp", wire.PathSignal, `{"t":"offer","v":1,"sdp":42}`, wire.CloseConnection},
		{"answer with an empty sdp", wire.PathSignal, `{"t":"answer","v":1,"sdp":""}`, wire.CloseConnection},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := wire.Decode(tc.path, []byte(tc.in))
			if err == nil {
				t.Fatalf("Decode(%s) = %#v, want an error", tc.in, f)
			}
			if got := wire.DispositionOf(err); got != tc.want {
				t.Errorf("Decode(%s): disposition %v, want %v (%v)", tc.in, got, tc.want, err)
			}
		})
	}
}

// TestDecodeFrameCap covers the 64 KiB cap. An oversized frame is a dropped
// message on the DataChannel but a protocol violation on the WebSocket, where
// nothing that large is ever legitimate.
func TestDecodeFrameCap(t *testing.T) {
	padded := func(n int) []byte {
		return []byte(`{"t":"bye","v":1,"pad":"` + strings.Repeat("x", n) + `"}`)
	}
	t.Run("a frame at the cap is accepted", func(t *testing.T) {
		in := padded(wire.MaxFrameSize - len(padded(0)))
		if len(in) != wire.MaxFrameSize {
			t.Fatalf("test built a %d byte frame, want %d", len(in), wire.MaxFrameSize)
		}
		if _, err := wire.Decode(wire.PathData, in); err != nil {
			t.Fatalf("Decode of a %d byte frame: %v", len(in), err)
		}
	})

	over := padded(wire.MaxFrameSize)
	t.Run("oversize on the data path is dropped", func(t *testing.T) {
		_, err := wire.Decode(wire.PathData, over)
		if got := wire.DispositionOf(err); got != wire.DropFrame {
			t.Errorf("disposition %v, want %v (%v)", got, wire.DropFrame, err)
		}
	})
	t.Run("oversize on the signaling path closes the connection", func(t *testing.T) {
		_, err := wire.Decode(wire.PathSignal, over)
		if got := wire.DispositionOf(err); got != wire.CloseConnection {
			t.Errorf("disposition %v, want %v (%v)", got, wire.CloseConnection, err)
		}
	})
}

// TestDecodeIgnoresUnknownFields is what lets a later protocol version add
// fields without a version bump.
func TestDecodeIgnoresUnknownFields(t *testing.T) {
	in := `{"t":"text","v":1,"id":"` + sampleID + `","body":"hi","edited":true,"from":"AAAA","reactions":[":+1:"]}`
	got, err := wire.Decode(wire.PathData, []byte(in))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	want := wire.Text{ID: sampleID, Body: "hi"}
	if got != wire.Frame(want) {
		t.Errorf("Decode = %#v, want %#v", got, want)
	}
}

// TestEncodeRejectsInvalidFrames keeps a malformed frame from ever leaving
// this process: the sender finds out, rather than the receiver.
func TestEncodeRejectsInvalidFrames(t *testing.T) {
	cases := []struct {
		name  string
		frame wire.Frame
	}{
		{"text with no body", wire.Text{ID: sampleID}},
		{"text with no id", wire.Text{Body: "hi"}},
		{"text with a non-uuid id", wire.Text{ID: "abc", Body: "hi"}},
		{"text over the body cap", wire.Text{ID: sampleID, Body: strings.Repeat("x", wire.MaxTextBytes+1)}},
		{"ack with no id", wire.Ack{}},
		{"call with no call_id", wire.Call{}},
		{"reject with no reason", wire.Reject{CallID: sampleCallID}},
		{"reject with an unknown reason", wire.Reject{CallID: sampleCallID, Reason: "nope"}},
		{"offer with no sdp", wire.Offer{}},
		{"ice candidate with no mid", wire.ICE{Candidate: "candidate:1 1 udp 1 192.0.2.1 1 typ host"}},
		{"rejected with an unknown reason", wire.Rejected{Reason: "busy"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if b, err := wire.Encode(tc.frame); err == nil {
				t.Errorf("Encode(%#v) = %s, want an error", tc.frame, b)
			}
		})
	}
}

// TestEncodeDoesNotEscapeHTML: Go escapes <, > and & by default, six bytes
// apiece, which would let a body that is legal at 16 KiB blow past the 64 KiB
// frame cap — and which no other JSON reader needs.
func TestEncodeDoesNotEscapeHTML(t *testing.T) {
	b, err := wire.Encode(wire.Text{ID: sampleID, Body: "<b>tom & jerry</b>"})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if want := `"body":"<b>tom & jerry</b>"`; !strings.Contains(string(b), want) {
		t.Errorf("Encode = %s, want it to contain %s", b, want)
	}

	markup := wire.Text{ID: sampleID, Body: strings.Repeat("<", wire.MaxTextBytes)}
	if _, err := wire.Encode(markup); err != nil {
		t.Errorf("Encode of a body of markup at the cap: %v", err)
	}
}

// TestEncodeRejectsNilFrame: a nil frame is a caller's bug, and it should
// read as one rather than panic deep inside the encoder.
func TestEncodeRejectsNilFrame(t *testing.T) {
	if _, err := wire.Encode(nil); err == nil {
		t.Error("Encode(nil) = nil error, want an error")
	}
}

// TestEncodeDecodeRoundTrip covers the values the golden files don't: the
// edges of what the schema allows.
func TestEncodeDecodeRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		path  wire.Path
		frame wire.Frame
	}{
		{"a body at the cap", wire.PathData, wire.Text{ID: sampleID, Body: strings.Repeat("x", wire.MaxTextBytes)}},
		{"a multi-line body", wire.PathData, wire.Text{ID: sampleID, Body: "one\ntwo\n\nthree"}},
		{"a body of emoji", wire.PathData, wire.Text{ID: sampleID, Body: "👋🏽🇬🇧👨‍👩‍👧‍👦"}},
		{"all media off", wire.PathData, wire.Media{}},
		{"all media on", wire.PathData, wire.Media{Mic: true, Cam: true, Screen: true}},
		{"reject busy", wire.PathData, wire.Reject{CallID: sampleCallID, Reason: wire.ReasonBusy}},
		{"reject timeout", wire.PathData, wire.Reject{CallID: sampleCallID, Reason: wire.ReasonTimeout}},
		{"a high mline", wire.PathSignal, wire.ICE{Candidate: "candidate:1 1 udp 1 192.0.2.1 1 typ host", Mid: "2", MLine: 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := wire.Encode(tc.frame)
			if err != nil {
				t.Fatalf("Encode(%#v): %v", tc.frame, err)
			}
			got, err := wire.Decode(tc.path, b)
			if err != nil {
				t.Fatalf("Decode(%s): %v", b, err)
			}
			if got != tc.frame {
				t.Errorf("round trip = %#v, want %#v", got, tc.frame)
			}
		})
	}
}
