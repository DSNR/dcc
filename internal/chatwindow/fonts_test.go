package chatwindow

import "testing"

// TestEmojiFallbackCoversWhatTheGoFontsDoNot: the bundled Noto Emoji is the
// only reason an emoji someone typed is a picture rather than a hollow box,
// so the build is checked for it — that the font parses, that it is last in
// the collection where the shaper falls through to it, and that it covers
// emoji the text faces do not.
func TestEmojiFallbackCoversWhatTheGoFontsDoNot(t *testing.T) {
	faces := collection()
	if len(faces) < 2 {
		t.Fatalf("the collection has %d faces", len(faces))
	}
	emoji := faces[len(faces)-1]
	text := faces[0]

	for _, r := range []rune{'👋', '🙂', '🎉'} {
		if _, covered := emoji.Face.Face().NominalGlyph(r); !covered {
			t.Errorf("the emoji font has no glyph for %q", r)
		}
		if _, covered := text.Face.Face().NominalGlyph(r); covered {
			t.Logf("the text face already covers %q; the fallback is belt and braces", r)
		}
	}
	// The text faces must still come first, or every letter would be shaped
	// by a font that has none.
	if _, covered := text.Face.Face().NominalGlyph('a'); !covered {
		t.Error("the first face in the collection cannot shape plain text")
	}
}
