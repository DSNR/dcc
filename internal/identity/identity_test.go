package identity_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/DSNR/dcc/internal/identity"
)

// load is the call under test in the common case: an Identity is expected,
// and neither an error nor a reset is.
func load(t *testing.T, dir string) identity.Identity {
	t.Helper()
	id, reset, err := identity.Load(dir)
	if err != nil {
		t.Fatalf("Load(%q): %v", dir, err)
	}
	if reset != nil {
		t.Fatalf("Load(%q): unexpected reset: %s", dir, reset.Warning())
	}
	return id
}

func TestLoadCreatesIdentityOnFirstRun(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dcc")

	id := load(t, dir)

	if id.Public() == (identity.PublicKey{}) {
		t.Error("new Identity has a zero public key")
	}
	if id.DBKey() == ([32]byte{}) {
		t.Error("new Identity has a zero db key")
	}

	// The file holds the only copy of both keys, and it sits in a directory
	// under the user's home — nothing but this user may read either.
	if runtime.GOOS != "windows" {
		assertMode(t, dir, 0o700)
		assertMode(t, filepath.Join(dir, "identity.json"), 0o600)
	}
}

func TestLoadReusesIdentityOnSecondRun(t *testing.T) {
	dir := t.TempDir()

	first := load(t, dir)
	before := readFile(t, filepath.Join(dir, "identity.json"))
	second := load(t, dir)

	if first.Public() != second.Public() {
		t.Error("second run produced a different Identity key")
	}
	if first.Private() != second.Private() {
		t.Error("second run produced a different private key")
	}
	if first.DBKey() != second.DBKey() {
		t.Error("second run produced a different db key — the database would be unreadable")
	}
	if after := readFile(t, filepath.Join(dir, "identity.json")); after != before {
		t.Error("second run rewrote identity.json")
	}
}

// A damaged file is never continued with and never silently discarded: the
// participant loses recognition either way, and must be told why.
func TestLoadSetsAsideCorruptIdentity(t *testing.T) {
	valid := func(t *testing.T) []byte {
		dir := t.TempDir()
		load(t, dir)
		return []byte(readFile(t, filepath.Join(dir, "identity.json")))
	}
	rewrite := func(t *testing.T, edit func(m map[string]any)) []byte {
		var m map[string]any
		if err := json.Unmarshal(valid(t), &m); err != nil {
			t.Fatalf("parsing a freshly written identity.json: %v", err)
		}
		edit(m)
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("re-encoding identity.json: %v", err)
		}
		return b
	}

	cases := []struct {
		name    string
		content func(t *testing.T) []byte
	}{
		{"empty", func(*testing.T) []byte { return nil }},
		{"not json", func(*testing.T) []byte { return []byte("\x00\xff garbage") }},
		{"truncated json", func(t *testing.T) []byte { return valid(t)[:20] }},
		{"unknown version", func(t *testing.T) []byte {
			return rewrite(t, func(m map[string]any) { m["v"] = 99 })
		}},
		{"missing identity key", func(t *testing.T) []byte {
			return rewrite(t, func(m map[string]any) { delete(m, "identity_key") })
		}},
		{"identity key not base64", func(t *testing.T) []byte {
			return rewrite(t, func(m map[string]any) { m["identity_key"] = "not base64!!" })
		}},
		{"identity key too short", func(t *testing.T) []byte {
			return rewrite(t, func(m map[string]any) { m["identity_key"] = "c2hvcnQ=" })
		}},
		{"db key too short", func(t *testing.T) []byte {
			return rewrite(t, func(m map[string]any) { m["db_key"] = "c2hvcnQ=" })
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "identity.json")
			damaged := tc.content(t)
			if err := os.WriteFile(path, damaged, 0o600); err != nil {
				t.Fatal(err)
			}

			id, reset, err := identity.Load(dir)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if reset == nil {
				t.Fatal("Load accepted a damaged identity.json without reporting a reset")
			}
			if reset.Cause == nil {
				t.Error("reset carries no cause to show the participant")
			}
			if w := reset.Warning(); !strings.Contains(w, reset.SetAside) {
				t.Errorf("warning %q does not name the file set aside (%q)", w, reset.SetAside)
			}

			// The damaged file is kept verbatim: it is the only copy of an
			// Identity the participant may still be able to recover by hand.
			if got := readFile(t, reset.SetAside); got != string(damaged) {
				t.Errorf("set-aside file = %q, want the original %q", got, damaged)
			}
			if filepath.Dir(reset.SetAside) != dir {
				t.Errorf("set-aside file %q left outside the app directory %q", reset.SetAside, dir)
			}

			// The fresh Identity is usable and persisted, so the next run is
			// an ordinary one.
			if id.Public() == (identity.PublicKey{}) {
				t.Error("replacement Identity has a zero public key")
			}
			if next := load(t, dir); next.Public() != id.Public() {
				t.Error("the replacement Identity was not the one written to disk")
			}
		})
	}
}

// Two damaged files in a row must both survive: the second set-aside cannot
// land on the name the first one took.
func TestLoadSetsAsideRepeatedCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identity.json")

	var asides []string
	for i := range 2 {
		content := fmt.Sprintf("damaged %d", i)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		_, reset, err := identity.Load(dir)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if reset == nil {
			t.Fatal("Load accepted a damaged identity.json without reporting a reset")
		}
		if got := readFile(t, reset.SetAside); got != content {
			t.Errorf("set-aside file = %q, want %q", got, content)
		}
		asides = append(asides, reset.SetAside)
	}
	if asides[0] == asides[1] {
		t.Errorf("the second reset overwrote the first set-aside file %q", asides[0])
	}
}

// A file that cannot be read is not the same as a file that is damaged.
// Regenerating here would discard an Identity that is still perfectly good.
func TestLoadFailsRatherThanResetOnUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	// A directory where the file belongs: readable as a name, never as JSON.
	if err := os.Mkdir(filepath.Join(dir, "identity.json"), 0o700); err != nil {
		t.Fatal(err)
	}

	_, reset, err := identity.Load(dir)
	if err == nil {
		t.Fatal("Load succeeded despite an unreadable identity.json")
	}
	if reset != nil {
		t.Errorf("Load reset an Identity it could not even read: %s", reset.Warning())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("Load left %d entries in the directory, want only the unreadable identity.json", len(entries))
	}
}

// Copying identity.json to another install is the whole backup story, so a
// restored file must yield the same Identity and the same database key.
func TestCopiedIdentityFileRestoresRecognition(t *testing.T) {
	original := t.TempDir()
	id := load(t, original)

	restored := t.TempDir()
	content := readFile(t, filepath.Join(original, "identity.json"))
	if err := os.WriteFile(filepath.Join(restored, "identity.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	got := load(t, restored)
	if got.Public() != id.Public() {
		t.Error("the restored install has a different Identity — the Peer would see a Security Code change")
	}
	if got.DBKey() != id.DBKey() {
		t.Error("the restored install has a different db key — its history would be unreadable")
	}

	peer := load(t, t.TempDir()).Public()
	if got.SecurityCode(peer) != id.SecurityCode(peer) {
		t.Error("the restored install computes a different Security Code")
	}
}

func TestSecurityCodeIsFortyDigits(t *testing.T) {
	a := load(t, t.TempDir()).Public()
	b := load(t, t.TempDir()).Public()

	code := identity.SecurityCodeFor(a, b)
	if len(code) != 40 {
		t.Errorf("Security Code %q is %d characters, want 40", code, len(code))
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			t.Fatalf("Security Code %q contains the non-digit %q", code, r)
		}
	}
}

// Both participants compare the same string, whichever of them hosted and
// whichever one computes it.
func TestSecurityCodeIsSymmetricAndStable(t *testing.T) {
	a := load(t, t.TempDir()).Public()
	b := load(t, t.TempDir()).Public()

	code := identity.SecurityCodeFor(a, b)
	if reversed := identity.SecurityCodeFor(b, a); reversed != code {
		t.Errorf("Security Code depends on argument order: %q vs %q", code, reversed)
	}
	if again := identity.SecurityCodeFor(a, b); again != code {
		t.Errorf("Security Code is not deterministic: %q then %q", code, again)
	}

	// A different Identity on either side is a different code, which is what
	// makes the blocking "Security Code changed" prompt possible.
	c := load(t, t.TempDir()).Public()
	if other := identity.SecurityCodeFor(a, c); other == code {
		t.Error("a different Peer Identity produced the same Security Code")
	}
	if self := identity.SecurityCodeFor(a, a); self == code {
		t.Error("a pair of one Identity produced the same Security Code as the real pair")
	}
}

// The code is read aloud down a phone line, so it has to break into blocks.
func TestSecurityCodeGroups(t *testing.T) {
	a := load(t, t.TempDir()).Public()
	b := load(t, t.TempDir()).Public()

	code := identity.SecurityCodeFor(a, b)
	groups := code.Groups()
	if len(groups) != 8 {
		t.Fatalf("Groups() returned %d groups, want 8", len(groups))
	}
	for _, g := range groups {
		if len(g) != 5 {
			t.Errorf("group %q is %d characters, want 5", g, len(g))
		}
	}
	if joined := strings.Join(groups, ""); joined != string(code) {
		t.Errorf("groups rejoin to %q, want the code %q", joined, code)
	}
}

// A participant id on the wire is the Identity public key, base64-encoded.
func TestPublicKeyRoundTrip(t *testing.T) {
	key := load(t, t.TempDir()).Public()

	parsed, err := identity.ParsePublicKey(key.String())
	if err != nil {
		t.Fatalf("ParsePublicKey(%q): %v", key, err)
	}
	if parsed != key {
		t.Errorf("round trip changed the key: %q became %q", key, parsed)
	}

	for _, bad := range []string{"", "not base64!!", "c2hvcnQ="} {
		if _, err := identity.ParsePublicKey(bad); err == nil {
			t.Errorf("ParsePublicKey(%q) accepted a key it should have refused", bad)
		}
	}
}

// Identity ends up in log lines and error messages; the private key and the
// db key must not come with it.
func TestFormattingAnIdentityKeepsSecrets(t *testing.T) {
	id := load(t, t.TempDir())

	text := fmt.Sprintf("%v %s %+v", id, id, id)
	for name, secret := range map[string][32]byte{"private key": id.Private(), "db key": id.DBKey()} {
		if strings.Contains(text, fmt.Sprintf("%v", secret)) {
			t.Errorf("formatting an Identity exposed its %s: %s", name, text)
		}
	}
	if !strings.Contains(text, id.Public().String()) {
		t.Errorf("formatting an Identity does not name it: %s", text)
	}
}

func TestDirIsUnderTheUserConfigDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("AppData", t.TempDir())
	} else {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	}
	config, err := os.UserConfigDir()
	if err != nil {
		t.Skipf("no user config directory here: %v", err)
	}

	dir, err := identity.Dir()
	if err != nil {
		t.Fatalf("Dir(): %v", err)
	}
	if want := filepath.Join(config, "dcc"); dir != want {
		t.Errorf("Dir() = %q, want %q", dir, want)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s has mode %o, want %o", path, got, want)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
