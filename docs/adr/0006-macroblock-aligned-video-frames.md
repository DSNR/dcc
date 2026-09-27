# Macroblock-aligned video frames, because govpx reads past a partial one

Sharing a 1920x1080 screen killed dcc a few seconds in, every time, with an
index out of range inside govpx — the pure-Go VP8 encoder ADR 0002 picked. The
read is in `checkDotArtifactCandidate`, govpx's port of libvpx's
`check_dot_artifact_candidate`
(`vp8_encoder_segmentation.go:788`, `:813`, `:834`, `:846`): for a macroblock
whose LAST-reference corner is sharp while the source is flat, it compares the
four 16x16 corners of the source and the reference. It takes the corners by
slicing the plane at the macroblock's origin and indexing row 15 of it. A
1080-row frame is 67.5 macroblocks tall, so its bottom macroblock row has eight
rows of pixels, not sixteen, and row 15 is 28800 bytes into a slice with 15360
left. libvpx gets away with the same read because its source buffers always
carry a 32-pixel border; govpx hands the encoder the caller's planes
untouched, and ours — like anyone's — are exactly `width*height`. Every other
source read in govpx clamps to the visible frame (`clampSourceCoord` in
`internal/vp8/encoder/intra_error.go`); this one does not.

A reproducer driving govpx directly at the screen's operating point pins the
trigger down: the frame size is the whole story. It needs an inter frame, a
macroblock that has coded still against LAST for more than thirty frames — a
static corner of a desktop, about three seconds of sharing at ten frames a
second — and a width or height that is not a multiple of sixteen. It happens at
`CpuUsed` 0, -4 and -6 alike and with one thread as readily as four, so neither
the speed preset nor the thread count is a way out; 1920x1072 and 1360x768 run
clean where 1920x1080 and 1366x768 crash. The camera never hit it only because
640x480 is a whole number of macroblocks both ways.

**So dcc encodes whole macroblocks and nothing else.** `screenSize` rounds a
display down to a multiple of sixteen rather than of two, and since the capture
scales the whole display into whatever size that returns, a 1080-row display is
shared entire, squashed by eight rows' worth — not cropped. `newEncoder` rounds
the same way for whatever a device hands over and encodes the top-left corner
of anything larger, which is what protects the camera from a driver that offers
640x360. There is no newer govpx to bump to: the version in `go.mod` is the
latest the module proxy has, and a local fork of a codec this size to fix four
lines is a maintenance burden out of all proportion to an eight-row crop.

Encoding also no longer panics the process on its own: `encode` recovers, turns
the panic into the same error a dead capture device produces, and abandons the
encoder rather than reusing one whose reference buffers were left half-written.
Being honest about what that buys — it only catches a panic raised on the
calling goroutine. govpx's row workers have no recover of their own, and at
1080p the crash lands on one of those, where nothing dcc can write will reach
it. The frame geometry is the fix; the recover is the net under it.

## Consequences

- A shared screen is at most fifteen rows and columns shorter than the cap
  suggests: a 1080-row display goes out at 1920x1072. Nobody watching a
  shared screen can tell.
- A camera frame that is not macroblock-aligned is cropped, not scaled, so a
  640x360 camera sends 640x352. Scaling it would mean a resampler in the
  camera path for the sake of four rows.
- The four-thread, `CpuUsed` -6 operating point stands, and is still load
  bearing: at 1920x1072 a screen frame encodes in 31 ms on average and 40 ms
  at the 95th percentile against a 100 ms budget, where one thread averages
  72 ms and misses the deadline outright at the 95th.
- A screen share whose encoder does die now stops and says so — the stream
  releases the display and whoever tells the other side what this side is
  sending is told — instead of taking the Call and the process with it.
- Worth reporting upstream: `dotArtifactCornerCandidateY` and
  `dotArtifactCornerCandidateUV` should clamp to `src.Height`/`src.Width` the
  way `MacroblockMeanLumaSSE` does, and govpx's row workers should recover and
  hand the panic back through `EncodeInto` rather than killing the process.
