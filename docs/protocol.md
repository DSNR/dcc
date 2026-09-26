# The dcc wire protocol, version 1

Every message dcc sends is a flat JSON object. `internal/wire` is the
canonical implementation, and the hand-maintained golden files under
`internal/wire/testdata/golden/` are the schema's source of truth — they are
written from this document, never regenerated from the code, so the two can
disagree and a test can say so.

## Envelope

```json
{"t":"text","v":1,"id":"018f4c1e-0b2a-7c3d-8e4f-5a6b7c8d9e0f","body":"hello"}
```

`t` names the message type and `v` is the protocol version; the remaining
fields depend on `t`. Field names are lowercase `snake_case`, the encoding is
UTF-8 JSON in a binary frame, and one object is one frame — one WebSocket
frame after Noise, or one SCTP message on the DataChannel.

A decoded frame may not exceed **64 KiB** on either path. `<`, `>` and `&` are
not escaped — nothing here is read by a browser, and escaping them costs six
bytes apiece. The 16 KiB text cap is measured on the body's UTF-8 bytes while
the frame cap is measured on the encoded frame, so a body that escapes heavily
can still be refused by the sender before it goes anywhere.

## Versioning

`v` is a single protocol-wide integer, currently `1`, present on every frame
and in both Noise handshake payloads. It is bumped only for an incompatible
change, and then together with the Noise prologue (`dcc-noise-1`), so two
incompatible builds never complete a handshake. Adding a message type or an
optional field is not an incompatible change and does not bump `v`.

## Paths

The two transports carry disjoint sets of types.

| Path | Transport | Types |
| --- | --- | --- |
| `PathSignal` | the Noise-encrypted WebSocket at the Rendezvous | `offer`, `answer`, `ice`, `rejected` |
| `PathData` | the WebRTC DataChannel | `text`, `ack`, `bye`, `call`, `accept`, `reject`, `hangup`, `media` |

A third endpoint at the Rendezvous, `/v1/relay`, carries no frames of its
own: it is the fallback relay. When no direct path works, the Peer's ICE
agent reaches the Host over ICE-TCP — each relay WebSocket is one TCP
connection's worth of RFC 4571-framed packets, spliced into the Host's TCP
mux. What crosses it is the same DTLS ciphertext a direct path would carry,
under certificates the Noise handshake authenticated, so the relay endpoint
needs no authentication of its own: a connection that cannot produce a STUN
binding with the Session's ICE credentials is dropped, and the Rendezvous
never holds keys to read what it forwards.

## What a receiver does with a frame it can't accept

Three outcomes, which `wire.Decode` reports as a `Disposition` on the error:

- **ignore** — an unknown `t`, or a known `t` on the wrong path. A newer peer
  may know types this build doesn't. Log at debug and carry on. Unknown
  *fields* are likewise ignored, silently: that is what lets the protocol grow
  without a version bump.
- **drop** — the frame is malformed: not an object, or a required field
  missing or of the wrong type, or over a cap. Discard it and leave the
  Session up. One bad message is not a bad connection.
- **close** — the connection cannot continue. This is the case for a
  malformed `offer` or `answer`, for any frame whose `v` is not `1`, for an
  oversized frame on the WebSocket, and for any problem at all with a Noise
  handshake payload.

Duplicate keys in a frame are not special-cased; Go's `encoding/json` takes
the last.

## Identifiers

A participant's id is their Identity X25519 public key, base64-encoded; there
is no separate id. The `session_id` and the sender are transport context, so
neither travels on individual frames. `conversation_id` is local to a machine
and never goes on the wire. Message and Call ids are UUIDv7, in the canonical
hyphenated form, so an id carries its own creation time.

Multi-party support later adds optional `from` and `to` fields, which by the
rule above needs no version bump.

## Noise handshake payloads

These ride inside Noise messages 2 and 3, where position says which is which,
so they carry no `t`.

**Host, message 2** — mints the Session and binds the Host's DTLS
certificate:

```json
{"v":1,"session_id":"<uuidv7>","name":"<display name>","dtls":"<sha256 hex, lowercase, no colons>"}
```

**Peer, message 3** — `session_id` present is a claim to be resuming that
Session, which the Host checks against the one it minted; absent is a fresh
join:

```json
{"v":1,"name":"<display name>","dtls":"<sha256 hex, lowercase, no colons>","session_id":"<uuidv7>"}
```

A Display Name is 1–64 characters, valid UTF-8, trimmed of surrounding
whitespace, and free of control characters. Binding each side's DTLS
fingerprint into the authenticated handshake is what makes the WebRTC
connection underneath it — and therefore the relayed path — trustworthy.

## Signaling frames

```json
{"t":"offer","v":1,"sdp":"<sdp>"}
{"t":"answer","v":1,"sdp":"<sdp>"}
{"t":"ice","v":1,"candidate":"<candidate>","mid":"<mid>","mline":0}
{"t":"rejected","v":1,"reason":"locked"}
```

`offer` and `answer` are reused to renegotiate when a Call starts. An `ice`
frame with an empty `candidate` is end-of-candidates, not a malformed
candidate; a non-empty candidate must carry a `mid`. `rejected` tells a Peer
the Invite is already locked to another Identity.

## DataChannel frames

```json
{"t":"text","v":1,"id":"<uuidv7>","body":"<utf-8, at most 16 KiB>"}
{"t":"ack","v":1,"id":"<uuidv7>"}
{"t":"bye","v":1}
{"t":"call","v":1,"call_id":"<uuidv7>"}
{"t":"accept","v":1,"call_id":"<uuidv7>"}
{"t":"reject","v":1,"call_id":"<uuidv7>","reason":"busy"|"declined"|"timeout"}
{"t":"hangup","v":1,"call_id":"<uuidv7>"}
{"t":"media","v":1,"mic":true,"cam":false,"screen":false}
```

A `text` carries no timestamp: its UUIDv7 already holds the send time in
milliseconds, and the receiver records arrival itself. Delivery is the
application-level `ack`, not the DataChannel's own reliability.

`call_id` is what lets an `accept` or `reject` that arrives after a Call has
already timed out be discarded. `media` is the sender's stream state and is
meaningful only while a Call is Active, which is why it needs no `call_id` —
but all three of its fields are required, since a missing one would otherwise
read as "off".

## Call media

A Call's audio and video do not travel as frames at all: they are RTP over
the same DTLS-protected PeerConnection the DataChannel uses, so they are
encrypted by SRTP under keys the Noise handshake already authenticated.

Accepting a Call renegotiates once, with `offer` and `answer` on the
Signaling path, and brings up all three transceivers together — microphone,
camera, screen. Muting, turning a camera off and starting a screen share are
then a matter of writing frames or not, plus the `media` frame that says so;
none of them renegotiates. Both sides make their transceivers when they
accept, but only one side offers, and an offer that arrives before the other
side has got there makes its transceivers for it — the two accepts travel on
different transports, so neither can assume it was first.

Audio is **G.711 µ-law — PCMU, RTP payload type 0, 8 kHz mono, 20 ms
frames**; ADR 0004 records why it is not Opus. A received RTP packet with an
empty payload is padding, and is skipped rather than decoded: it carries no
audio, and playing it would be a frame of silence nobody sent.

Video is **VP8 — RTP payload type 96, 90 kHz clock**, on two tracks named
`cam` and `screen` within the `dcc` stream, which is how each side tells the
other's camera from the other's screen share without renegotiating. Each frame
is packetized per **RFC 7741** at an MTU of 1200 bytes, every payload carrying
a 15-bit PictureID and the last one of a frame carrying the marker bit. A
receiver reassembles by marker bit and drops a frame whole when the sequence
numbers show a fragment missing — half a VP8 frame is not a picture.

A receiver that cannot decode — it joined after the last keyframe, or lost the
one it needed — sends an **RTCP PLI**, at most once a second; the sender
answers by forcing its encoder's next frame to be a keyframe. That is the only
way back from a broken video stream, so the video codec is offered with
`nack` and `nack pli` feedback rather than leaving either to be assumed: two
dcc installs would manage without the SDP saying so, a browser would not.

A Call does not survive a reconnect. The media path is rebuilt from nothing,
and reviving a Call silently across a gap is worse than ringing again. An
unanswered Call rings for **60 seconds**; both sides count it, so a lost
`reject` still ends the Call at both ends.
