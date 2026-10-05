package wire_test

import (
	"strings"
	"testing"

	"github.com/DSNR/dcc/internal/wire"
)

const (
	hostDTLS    = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
	peerDTLS    = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"
	sampleSessn = "018f4c10-0000-7000-8000-000000000001"
)

// The handshake payloads ride inside Noise messages 2 and 3, where position
// says which is which, so unlike every other message they carry no type.

func TestHostHelloMatchesGolden(t *testing.T) {
	hello := wire.HostHello{SessionID: sampleSessn, Name: "Ada", DTLS: hostDTLS}

	got, err := wire.EncodeHostHello(hello)
	if err != nil {
		t.Fatalf("EncodeHostHello: %v", err)
	}
	if want := readGolden(t, "hello_host"); string(got) != string(want) {
		t.Errorf("EncodeHostHello\n got: %s\nwant: %s", got, want)
	}

	decoded, err := wire.DecodeHostHello(readGolden(t, "hello_host"))
	if err != nil {
		t.Fatalf("DecodeHostHello: %v", err)
	}
	if decoded != hello {
		t.Errorf("DecodeHostHello = %#v, want %#v", decoded, hello)
	}
}

func TestPeerHelloMatchesGolden(t *testing.T) {
	cases := []struct {
		name   string
		golden string
		hello  wire.PeerHello
	}{
		{
			// A fresh join claims no Session; the Host mints one.
			name:   "fresh join",
			golden: "hello_peer",
			hello:  wire.PeerHello{Name: "Grace 👩‍💻", DTLS: peerDTLS},
		},
		{
			// A session_id is the Peer claiming to be resuming that Session.
			name:   "reconnect claim",
			golden: "hello_peer_reconnect",
			hello:  wire.PeerHello{Name: "Grace 👩‍💻", DTLS: peerDTLS, SessionID: sampleSessn},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := wire.EncodePeerHello(tc.hello)
			if err != nil {
				t.Fatalf("EncodePeerHello: %v", err)
			}
			if want := readGolden(t, tc.golden); string(got) != string(want) {
				t.Errorf("EncodePeerHello\n got: %s\nwant: %s", got, want)
			}

			decoded, err := wire.DecodePeerHello(readGolden(t, tc.golden))
			if err != nil {
				t.Fatalf("DecodePeerHello: %v", err)
			}
			if decoded != tc.hello {
				t.Errorf("DecodePeerHello = %#v, want %#v", decoded, tc.hello)
			}
		})
	}
}

// TestDecodeHelloRejects covers the one place the protocol has no tolerance:
// a handshake payload we can't read leaves the Session with no Identity, no
// DTLS binding and no agreed version, so every failure here is fatal.
func TestDecodeHelloRejects(t *testing.T) {
	longName := strings.Repeat("a", wire.MaxNameChars+1)
	cases := []struct {
		name string
		in   string
		host bool // whether to decode as the Host's payload
	}{
		{name: "not json", in: `not json`},
		{name: "missing version", in: `{"name":"Ada","dtls":"` + hostDTLS + `"}`},
		{name: "unknown version", in: `{"v":2,"name":"Ada","dtls":"` + hostDTLS + `"}`},
		{name: "missing name", in: `{"v":1,"dtls":"` + hostDTLS + `"}`},
		{name: "blank name", in: `{"v":1,"name":"   ","dtls":"` + hostDTLS + `"}`},
		{name: "name over the cap", in: `{"v":1,"name":"` + longName + `","dtls":"` + hostDTLS + `"}`},
		{name: "name with a control character", in: `{"v":1,"name":"Ada\u0007","dtls":"` + hostDTLS + `"}`},
		{name: "missing dtls", in: `{"v":1,"name":"Ada"}`},
		{name: "dtls with colons", in: `{"v":1,"name":"Ada","dtls":"A1:B2:C3"}`},
		{name: "uppercase dtls", in: `{"v":1,"name":"Ada","dtls":"` + strings.ToUpper(hostDTLS) + `"}`},
		{name: "truncated dtls", in: `{"v":1,"name":"Ada","dtls":"` + hostDTLS[:62] + `"}`},
		{name: "peer session_id that is not a uuid", in: `{"v":1,"name":"Ada","dtls":"` + hostDTLS + `","session_id":"resume"}`},
		{name: "host without a session_id", host: true, in: `{"v":1,"name":"Ada","dtls":"` + hostDTLS + `"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.host {
				_, err = wire.DecodeHostHello([]byte(tc.in))
			} else {
				_, err = wire.DecodePeerHello([]byte(tc.in))
			}
			if err == nil {
				t.Fatalf("decoding %s: want an error", tc.in)
			}
			if got := wire.DispositionOf(err); got != wire.CloseConnection {
				t.Errorf("decoding %s: disposition %v, want %v", tc.in, got, wire.CloseConnection)
			}
		})
	}
}

// TestHelloIgnoresUnknownFields lets a later version add to the handshake
// without breaking this one.
func TestHelloIgnoresUnknownFields(t *testing.T) {
	in := `{"v":1,"name":"Ada","dtls":"` + hostDTLS + `","avatar":"...","caps":["groups"]}`
	got, err := wire.DecodePeerHello([]byte(in))
	if err != nil {
		t.Fatalf("DecodePeerHello: %v", err)
	}
	want := wire.PeerHello{Name: "Ada", DTLS: hostDTLS}
	if got != want {
		t.Errorf("DecodePeerHello = %#v, want %#v", got, want)
	}
}

// TestHelloNameIsTrimmed: surrounding whitespace is the sender's slip, not a
// different name, so it is removed on the way out and on the way in.
func TestHelloNameIsTrimmed(t *testing.T) {
	got, err := wire.EncodePeerHello(wire.PeerHello{Name: "  Ada  ", DTLS: peerDTLS})
	if err != nil {
		t.Fatalf("EncodePeerHello: %v", err)
	}
	if !strings.Contains(string(got), `"name":"Ada"`) {
		t.Errorf("EncodePeerHello = %s, want a trimmed name", got)
	}

	decoded, err := wire.DecodePeerHello([]byte(`{"v":1,"name":"  Ada  ","dtls":"` + peerDTLS + `"}`))
	if err != nil {
		t.Fatalf("DecodePeerHello: %v", err)
	}
	if decoded.Name != "Ada" {
		t.Errorf("DecodePeerHello name = %q, want %q", decoded.Name, "Ada")
	}
}

// TestValidateName is the rule the UIs enforce on a Display Name before a
// Session ever starts, so a name is rejected while it can still be edited.
func TestValidateName(t *testing.T) {
	valid := []string{"Ada", "A", "👩‍💻", strings.Repeat("é", wire.MaxNameChars), "Ada Lovelace"}
	for _, name := range valid {
		if err := wire.ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", name, err)
		}
	}
	invalid := []string{"", "   ", strings.Repeat("a", wire.MaxNameChars+1), "Ada\x07", "Ada\nLovelace"}
	for _, name := range invalid {
		if err := wire.ValidateName(name); err == nil {
			t.Errorf("ValidateName(%q) = nil, want an error", name)
		}
	}
}
