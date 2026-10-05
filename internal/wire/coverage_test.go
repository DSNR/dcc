package wire

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestEveryFrameTypeHasAGolden keeps the golden files and the protocol in
// step: a type added to the package without a golden, or a golden left behind
// by a type that was removed, fails here rather than going unnoticed.
func TestEveryFrameTypeHasAGolden(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "golden", "*.json"))
	if err != nil {
		t.Fatalf("listing goldens: %v", err)
	}
	covered := map[Type]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		var env struct {
			T Type `json:"t"`
		}
		if err := json.Unmarshal(b, &env); err != nil {
			t.Fatalf("%s is not valid JSON: %v", f, err)
		}
		// The handshake payloads carry no type; they have their own tests.
		if env.T == "" {
			continue
		}
		if _, known := frameSpecs[env.T]; !known {
			t.Errorf("%s is a golden for %q, which is not a frame type", f, env.T)
		}
		covered[env.T] = f
	}
	for typ := range frameSpecs {
		if _, ok := covered[typ]; !ok {
			t.Errorf("frame type %q has no golden file under testdata/golden", typ)
		}
	}
}
