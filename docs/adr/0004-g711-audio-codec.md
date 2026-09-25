# G.711 µ-law for MVP audio, not Opus

ADR 0002 picked Opus via `pion/opus` for audio. Building the Call showed that
`pion/opus` is a **decoder only** — its one exported constructor is
`NewDecoder`, and the only encoder in the module is an internal, partial CELT
one. No pure-Go Opus encoder exists anywhere else either, so "Opus in the
default build" would have meant a send side that cannot encode: a Call that
connects, negotiates and carries nothing.

MVP audio is therefore **G.711 µ-law — PCMU, RTP payload type 0, 8 kHz mono,
20 ms frames**. It is sixty lines of table-free arithmetic in
`internal/media/codec.go`, needs no library at all, is a codec every WebRTC
stack already speaks, and keeps the plain cross-compile from Linux that
ADR 0002 exists to protect. Both ends of a Session are dcc, so nothing but
dcc has to agree to it.

## Consequences

- Audio is telephone quality — 64 kbit/s, 4 kHz of bandwidth. Audible, and
  worse than Opus would be. For two people talking, that is the whole cost.
- No echo cancellation still, and now no noise suppression either; the help
  text's advice to use headphones matters more, not less.
- The codec is the only thing in `internal/media` that knows the format:
  `Encode` and `Decode` either side of the device boundary, and one
  `RTPCodecCapability` in `internal/transport`. Swapping in Opus — when a
  pure-Go encoder exists, or behind the `cgo`-tagged alternate ADR 0002
  already anticipates — touches those and nothing else.
- The device boundary runs at 8 kHz mono, so both drivers ask their sound
  server to resample: PulseAudio does it server-side, WASAPI through
  `AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM`. A higher-rate codec later moves that
  one constant.
