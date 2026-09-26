# dcc
DSNR Chat Client

End-to-end encrypted, serverless, peer-to-peer chat and calling between two
people. Cloudflare is used only to find each other, never to hold content.

## Layout

| Path | What lives there |
| --- | --- |
| `internal/wire` | the protocol's message schema, canonical — see [docs/protocol.md](docs/protocol.md) |
| `internal/session` | the Session state machine: commands in, events out |
| `internal/identity` | the persistent local keypair and the Security Code |
| `internal/rendezvous` | the cloudflared Quick Tunnel and the Invite |
| `internal/signaling` | the Noise handshake and the SDP/ICE exchange |
| `internal/transport` | the WebRTC PeerConnection and the DataChannel |
| `internal/storage` | Conversations in SQLite, encrypted at rest |
| `internal/media` | capture, encode and decode for Calls — microphone, speaker, camera |
| `internal/window` | the video window, in Gio, opened in-process by whichever UI wants one |
| `internal/cli` | the terminal interface: a Bubble Tea Model over `session` |
| `cmd/dcc-cli` | the terminal binary — flags, Identity, and nothing else |
| `docs/adr` | the decisions that are expensive to revisit |

Both binaries — `dcc-cli` and `dcc-gui`, the latter still to come — are thin
consumers of `internal/session`, which is where every decision about a Session
is made.

## Using dcc-cli

```sh
go run ./cmd/dcc-cli -name "your name"
```

`/invite` opens a Rendezvous and prints an Invite to hand over; `/connect
<invite>` joins someone else's. Both sides then compare the Security Code and
answer `yes` or `no` before anything either of them types is shown. `/help`
lists the rest. While no Session is connected the commands work without their
slash.

`/call` rings the other person; inside a Call, `/camera on` sends this side's
webcam and `/camera off` releases the device again. A Call's video appears in a
window of its own, opened in this same process when the Call goes Active and
closed when it ends, so the terminal stays a terminal. Windows receives video
but does not send it — there is no pure-Go Windows camera library, and ADR 0002
records why dcc will not pull in cgo for one.

Two clients on one machine need no Cloudflare account and no `cloudflared` at
all: `-local` publishes the Rendezvous on loopback instead.

## Working on it

```sh
go build -tags novulkan ./...
go vet -tags novulkan ./...
go test -tags novulkan ./...
```

The video window is the one part that is not pure Go: Gio needs a windowing
system, which on Linux means cgo and headers at build time — hence the tag,
and these packages.

```sh
sudo apt install libxkbcommon-x11-dev libx11-xcb-dev libwayland-dev libegl1-mesa-dev libxcursor-dev
```

Add `nox11` (`-tags nox11,novulkan`) to build the Wayland backend only, which
is what a machine without the X11 headers can do. Everything except
`internal/window` and `cmd/dcc-cli` builds and tests with no tags at all —
the window sits behind a seam in `internal/cli`. The Windows build needs none
of this either: Gio is cgo-free there, so `GOOS=windows go build ./...`
cross-compiles from Linux with nothing installed.

Real `cloudflared` tunnels, real capture devices — microphone and webcam — and
browser interop are verified by hand; the test suite covers the in-process
seams only. The terminal tests watch the video window open and close without
one appearing on screen. To see a real one:

```sh
DCC_WINDOW=1 go test -tags novulkan ./internal/window/
```
