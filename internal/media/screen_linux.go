package media

import (
	"io"
	"log"

	"github.com/jezek/xgb"
)

// The X client library underneath kbinani/screenshot logs to standard error,
// and it logs on every connection it makes — which, since a screen grab opens
// one per frame, is ten lines a second while a share is running. dcc-cli is a
// terminal program: anything written to standard error lands in the middle of
// the conversation and stays there. So the library is silenced here, once, for
// the whole process.
//
// Nothing is lost by it. What the capture path actually needs to report — a
// display that cannot be read at all — comes back as an error from the grab and
// is put in front of the participant properly.
func init() {
	xgb.Logger = log.New(io.Discard, "", 0)
}
