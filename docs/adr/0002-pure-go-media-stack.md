# Pure-Go media stack with per-component cgo fallbacks

`pion/mediadevices` is the only integrated capture+encode stack for pion, but every codec it ships and most of its drivers are cgo, which would cost the plain cross-compile from Linux that the rest of dcc relies on. We take pure Go as the default build on both OSes: `go-wca` / `jfreymuth/pulse` for audio, `blackjack/webcam` for the Linux camera, `kbinani/screenshot` for screen, VP8 via `thesyncim/govpx`, Opus via `pion/opus`. Both codecs are months old with few users, so a prototype gates the build and every driver and codec sits behind an interface with a `cgo`-tagged alternate (mediadevices openh264/opus static libs, cross-built with zig). No pure-Go Windows camera library exists, so Windows send-side webcam is deferred past MVP rather than pulling cgo into the Windows build.

## Consequences

- The camera transceiver is still negotiated on Windows so the protocol stays identical; Windows just never writes frames and reports `cam:false`.
- All three transceivers (mic, camera, screen) are negotiated once at Call accept, so mute, camera off and screen share are frame-writing plus a `media` state message, never renegotiation.
- No echo cancellation in MVP; help text says to use headphones.
- Raising resolution, moving to VP9, or swapping in a cgo codec later touches only the codec package, not the WebRTC layer.
