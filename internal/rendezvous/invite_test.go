package rendezvous_test

import (
	"strings"
	"testing"

	"github.com/DSNR/dcc/internal/rendezvous"
)

// newPassword is the call under test wherever a test needs a Password but is
// not testing how one is minted.
func newPassword(t *testing.T) rendezvous.Password {
	t.Helper()
	p, err := rendezvous.NewPassword()
	if err != nil {
		t.Fatalf("NewPassword(): %v", err)
	}
	return p
}

// The Invite is the whole of what one person pastes to another, so what the
// Host prints has to be what the Peer can parse — every time, unchanged.
func TestInviteRoundTripsThroughItsCanonicalForm(t *testing.T) {
	password := newPassword(t)
	minted := rendezvous.Invite{
		URL:      "https://alt-consumption-humor-themselves.trycloudflare.com",
		Password: password,
	}

	text := minted.String()
	want := "https://alt-consumption-humor-themselves.trycloudflare.com/#1" + password.String()
	if text != want {
		t.Errorf("Invite.String() = %q, want %q", text, want)
	}

	parsed, err := rendezvous.ParseInvite(text)
	if err != nil {
		t.Fatalf("ParseInvite(%q): %v", text, err)
	}
	if parsed != minted {
		t.Errorf("round trip changed the Invite: %+v became %+v", minted, parsed)
	}
	if again := parsed.String(); again != text {
		t.Errorf("reprinting a parsed Invite gave %q, want %q", again, text)
	}
}

// The Password is the Noise PSK, so it is exactly 256 bits, and it is read
// aloud and retyped, so it renders in one case only.
func TestPasswordIsTwoHundredFiftySixFreshRandomBits(t *testing.T) {
	first, second := newPassword(t), newPassword(t)
	if first == second {
		t.Fatal("two Passwords in a row are identical")
	}
	if len(first) != 32 {
		t.Errorf("Password is %d bytes, want 32", len(first))
	}

	text := first.String()
	if len(text) != 52 {
		t.Errorf("Password renders as %d characters, want 52: %q", len(text), text)
	}
	const alphabet = "abcdefghijklmnopqrstuvwxyz234567"
	for _, r := range text {
		if !strings.ContainsRune(alphabet, r) {
			t.Fatalf("Password %q contains %q, which is not lowercase base32", text, r)
		}
	}

	parsed, err := rendezvous.ParsePassword(text)
	if err != nil {
		t.Fatalf("ParsePassword(%q): %v", text, err)
	}
	if parsed != first {
		t.Error("round trip through base32 changed the Password")
	}
}

// Invites arrive through messengers that linkify, lowercase, wrap and trim.
// Parsing is lenient about all of it; printing never is.
func TestParseInviteAcceptsWhatAPersonPastes(t *testing.T) {
	password := newPassword(t)
	const canonical = "https://alt-consumption-humor-themselves.trycloudflare.com"

	cases := []struct {
		name  string
		text  string
		want  string
		plain bool // the Password arrives in some other case
	}{
		{name: "canonical", text: canonical + "/#1" + password.String(), want: canonical},
		{name: "no trailing slash", text: canonical + "#1" + password.String(), want: canonical},
		{
			name: "no scheme",
			text: "alt-consumption-humor-themselves.trycloudflare.com/#1" + password.String(),
			want: canonical,
		},
		{
			name: "surrounding whitespace",
			text: "  " + canonical + "/#1" + password.String() + "\n",
			want: canonical,
		},
		{
			name: "shouted",
			text: strings.ToUpper(canonical + "/#1" + password.String()),
			want: canonical,
		},
		{
			name: "local rendezvous under test",
			text: "http://127.0.0.1:8080/#1" + password.String(),
			want: "http://127.0.0.1:8080",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := rendezvous.ParseInvite(tc.text)
			if err != nil {
				t.Fatalf("ParseInvite(%q): %v", tc.text, err)
			}
			if got.URL != tc.want {
				t.Errorf("ParseInvite(%q).URL = %q, want %q", tc.text, got.URL, tc.want)
			}
			if got.Password != password {
				t.Errorf("ParseInvite(%q) read a different Password", tc.text)
			}
		})
	}
}

// An Invite that is not quite an Invite fails now, with a reason, rather than
// failing later as an unexplained handshake that never completes.
func TestParseInviteRefusesWhatCannotBeJoined(t *testing.T) {
	password := newPassword(t)

	cases := []struct {
		name string
		text string
	}{
		{"empty", ""},
		{"no fragment", "https://a-b-c-d.trycloudflare.com"},
		{"empty fragment", "https://a-b-c-d.trycloudflare.com/#"},
		{"version only", "https://a-b-c-d.trycloudflare.com/#1"},
		{"unknown invite version", "https://a-b-c-d.trycloudflare.com/#2" + password.String()},
		{"truncated password", "https://a-b-c-d.trycloudflare.com/#1" + password.String()[:40]},
		{"password not base32", "https://a-b-c-d.trycloudflare.com/#1" + strings.Repeat("!", 52)},
		{"no host", "/#1" + password.String()},
		{"wrong scheme", "ftp://a-b-c-d.trycloudflare.com/#1" + password.String()},
		{"has a path", "https://a-b-c-d.trycloudflare.com/somewhere#1" + password.String()},
		{"has a query", "https://a-b-c-d.trycloudflare.com/?who=me#1" + password.String()},
		{"has credentials", "https://me@a-b-c-d.trycloudflare.com/#1" + password.String()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := rendezvous.ParseInvite(tc.text)
			if err == nil {
				t.Fatalf("ParseInvite(%q) accepted it as %+v", tc.text, got)
			}
			if !strings.Contains(err.Error(), "rendezvous:") {
				t.Errorf("error %q does not name the package", err)
			}
		})
	}
}

// The Password lives in the fragment precisely so that it is never sent
// anywhere: not to Cloudflare, not to the Rendezvous, not to a browser's
// history. Everything the Invite hands out for making a request is without it.
func TestInviteKeepsThePasswordOutOfEveryRequest(t *testing.T) {
	password := newPassword(t)
	invite := rendezvous.Invite{URL: "https://a-b-c-d.trycloudflare.com", Password: password}

	for name, addressed := range map[string]string{"URL": invite.URL, "SignalURL": invite.SignalURL()} {
		if strings.Contains(strings.ToLower(addressed), password.String()) {
			t.Errorf("%s = %q carries the Password", name, addressed)
		}
	}
	if want := "wss://a-b-c-d.trycloudflare.com/v1/signal"; invite.SignalURL() != want {
		t.Errorf("SignalURL() = %q, want %q", invite.SignalURL(), want)
	}

	local := rendezvous.Invite{URL: "http://127.0.0.1:8080", Password: password}
	if want := "ws://127.0.0.1:8080/v1/signal"; local.SignalURL() != want {
		t.Errorf("SignalURL() = %q, want %q", local.SignalURL(), want)
	}
}
