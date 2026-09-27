# G.722 for wideband audio, still not Opus

ADR 0004 accepted telephone quality as the price of having a send side at all,
and the Call it bought sounds like a telephone: 8 kHz sampling is 4 kHz of
bandwidth, and every /s/ and /f/ in it lands above the ceiling. Measured
against a 16 kHz reference, the old path returned a voice-like signal 10.6 dB
above its own error and threw away 32 to 36 dB of everything above 5 kHz. That
is the whole complaint, and no amount of fixing the plumbing around it moves
the number.

Audio is therefore **G.722 — SB-ADPCM, RTP payload type 9, 16 kHz mono, 20 ms
frames, 64 kbit/s**. It doubles the audio bandwidth for *exactly* the bitrate
G.711 cost: one byte per two samples means a frame is still 160 bytes, the RTP
clock is still 8000 — a wart of RFC 3551 section 4.5.2 that every stack
reproduces, so it must not be "fixed" to 16000 — and nothing about the packet
rate, the MTU or the pacing changes. The same reference signal comes back
17.3 dB above its error, a 5 kHz tone 22.4 dB and a 6.5 kHz tone 26.1 dB where
both were simply gone before. It is two hundred lines of integer arithmetic and
six tables from the recommendation, needs no library, and pion already has a
payloader for it.

A pure-Go Opus encoder does now exist, which is the premise ADR 0004 was built
on and it is no longer true: `pion/opus` grew one on master, and it works —
32 kbit/s CBR at 48 kHz fullband, mono, 0.7% of a core to encode in real time,
19 to 25 dB on speech-like signals, no errors over thousands of frames. It is
strictly the better codec on paper: half the bitrate, five times the bandwidth,
and the device boundary would run at 48 kHz where the hardware already is, so
neither sound server would resample at all. It is not in this Call for two
reasons. It exists only on an untagged commit days old, which is a bet dcc has
taken before — `govpx` is pinned the same way — but taken *with a prototype
gating it*, per ADR 0002. And it is a perceptual codec: its quality is not
something a signal-to-noise number can sign off, and its own quality baselines
score under 14 dB on a chirp at 96 kbit/s because waveform error is not what it
optimises. G.722 is a waveform codec, so the measurements above are the answer
rather than a proxy for it. Opus is the next step, and the step needs ears.

Uncompressed L16 was the other candidate and is the one that measures perfectly
— both ends are dcc, so nothing had to agree to it. It costs 256 kbit/s at
16 kHz against G.722's 64, on a Call already carrying VP8 with no congestion
control anywhere in it, and pion has no L16 payloader, so the audio track would
have to be hand-packetized the way the video tracks already are. Four times the
bitrate for the difference between 7 kHz of bandwidth and 8 kHz is not a trade
worth making.

## Consequences

- Both peers must be rebuilt. Payload type 9 replaces 0 in the one
  `RTPCodecCapability`, and there is no fallback: a Call between an old build
  and a new one negotiates nothing and carries no audio.
- The codec is stateful, which G.711 was not. `Encode` and `Decode` are methods
  on an `Encoder` and a `Decoder` that belong to one Call's send and receive
  side, because G.722's quantiser step, predictor and filter memory depend on
  every sample before them. Frames only decode in order, which is why the
  receive side now conceals a gap rather than closing it.
- Still 64 kbit/s, still 50 packets a second, still 160 bytes a frame. The
  encoder costs around 1% of a core; the decoder less.
- The device boundary moved from 8 kHz to 16 kHz, so both sound servers
  resample half as far — still resampling, since the hardware is at 48 kHz.
  Whether the µ-law era's aliasing was audible on top of the missing band is
  not something a measurement here can separate.
- Nobody has heard this. Every number above is a measurement; a listening test
  on real hardware, on both platforms, is the part still owed.
- Still no echo cancellation and no noise suppression. The help text's advice
  to use headphones stands.
