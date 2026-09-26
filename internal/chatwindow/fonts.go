package chatwindow

import (
	_ "embed"
	"fmt"
	"sync"

	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/font/opentype"
)

// Emoji, without a picker. Typing one is the operating system's job — the
// compose key, the Windows emoji panel, or a paste — and dcc's job is only to
// have a glyph for whatever arrives. The Go fonts have none, so a bundled
// Noto Emoji goes on the end of the collection as a fallback: the shaper
// tries the text faces first and falls through to this for anything they do
// not cover. It is the monochrome Noto Emoji, not the colour one, because it
// is a tenth of the size and renders identically through Gio's glyph path on
// Linux and Windows alike.

//go:embed fonts/NotoEmoji-Variable.ttf
var notoEmoji []byte

// collection is the faces the window shapes text with: the Go fonts, then
// emoji. It is built once — parsing two megabytes of font per window would be
// felt — and panics if the bundled font is not a font, which is a broken
// build rather than anything a participant can cause.
func collection() []font.FontFace {
	emojiOnce.Do(func() {
		faces, err := opentype.ParseCollection(notoEmoji)
		if err != nil {
			panic(fmt.Errorf("the bundled emoji font will not parse: %w", err))
		}
		// Appending to gofont's own slice would reuse its backing store, so
		// the text faces are copied out first.
		text := gofont.Collection()
		fonts = make([]font.FontFace, 0, len(text)+len(faces))
		fonts = append(fonts, text...)
		fonts = append(fonts, faces...)
	})
	return fonts
}

// emojiOnce guards the one parse of the bundled font.
var (
	emojiOnce sync.Once
	fonts     []font.FontFace
)
