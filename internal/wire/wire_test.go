package wire_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DSNR/dcc/internal/wire"
)

// goldenFrames is the canonical schema: every frame type in the protocol,
// paired with the exact bytes it is encoded as. The files under
// testdata/golden are hand-maintained from the protocol spec — never
// regenerated from this package's output — so they can disagree with the code.
var goldenFrames = []struct {
	name  string
	path  wire.Path
	frame wire.Frame
}{
	{
		name:  "text",
		path:  wire.PathData,
		frame: wire.Text{ID: "018f4c1e-0b2a-7c3d-8e4f-5a6b7c8d9e0f", Body: "hello 👋"},
	},
	{
		name:  "ack",
		path:  wire.PathData,
		frame: wire.Ack{ID: "018f4c1e-0b2a-7c3d-8e4f-5a6b7c8d9e0f"},
	},
	{
		name:  "bye",
		path:  wire.PathData,
		frame: wire.Bye{},
	},
	{
		name:  "call",
		path:  wire.PathData,
		frame: wire.Call{CallID: "018f4c22-1111-7000-8000-abcdefabcdef"},
	},
	{
		name:  "accept",
		path:  wire.PathData,
		frame: wire.Accept{CallID: "018f4c22-1111-7000-8000-abcdefabcdef"},
	},
	{
		name:  "reject",
		path:  wire.PathData,
		frame: wire.Reject{CallID: "018f4c22-1111-7000-8000-abcdefabcdef", Reason: wire.ReasonDeclined},
	},
	{
		name:  "hangup",
		path:  wire.PathData,
		frame: wire.Hangup{CallID: "018f4c22-1111-7000-8000-abcdefabcdef"},
	},
	{
		name:  "media",
		path:  wire.PathData,
		frame: wire.Media{Mic: true, Cam: false, Screen: true},
	},
	{
		name:  "offer",
		path:  wire.PathSignal,
		frame: wire.Offer{SDP: "v=0\r\no=- 4611731400430051336 2 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\na=group:BUNDLE 0\r\n"},
	},
	{
		name:  "answer",
		path:  wire.PathSignal,
		frame: wire.Answer{SDP: "v=0\r\no=- 8123456789012345678 2 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\na=group:BUNDLE 0\r\n"},
	},
	{
		name:  "ice",
		path:  wire.PathSignal,
		frame: wire.ICE{Candidate: "candidate:1 1 udp 2130706431 192.0.2.1 54321 typ host", Mid: "0", MLine: 0},
	},
	{
		// An empty candidate is end-of-candidates, not a malformed one.
		name:  "ice_end",
		path:  wire.PathSignal,
		frame: wire.ICE{Candidate: "", Mid: "", MLine: 0},
	},
	{
		name:  "rejected",
		path:  wire.PathSignal,
		frame: wire.Rejected{Reason: wire.ReasonLocked},
	},
}

func TestEncodeMatchesGolden(t *testing.T) {
	for _, tc := range goldenFrames {
		t.Run(tc.name, func(t *testing.T) {
			got, err := wire.Encode(tc.frame)
			if err != nil {
				t.Fatalf("Encode(%#v) returned error: %v", tc.frame, err)
			}
			want := readGolden(t, tc.name)
			if string(got) != string(want) {
				t.Errorf("Encode(%#v)\n got: %s\nwant: %s", tc.frame, got, want)
			}
		})
	}
}

func TestDecodeMatchesGolden(t *testing.T) {
	for _, tc := range goldenFrames {
		t.Run(tc.name, func(t *testing.T) {
			got, err := wire.Decode(tc.path, readGolden(t, tc.name))
			if err != nil {
				t.Fatalf("Decode(%s) returned error: %v", tc.name, err)
			}
			if got != tc.frame {
				t.Errorf("Decode(%s) = %#v, want %#v", tc.name, got, tc.frame)
			}
		})
	}
}

// readGolden returns the canonical bytes for a frame, without the trailing
// newline the file carries for the benefit of text editors.
func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "golden", name+".json"))
	if err != nil {
		t.Fatalf("reading golden: %v", err)
	}
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
	}
	return b
}
