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
| `internal/words` | the lines both clients say about a Session, written once |
| `internal/client` | what both binaries do before their interface starts: flags, Identity, Store |
| `internal/cli` | the terminal interface: a Bubble Tea Model over `session` |
| `internal/gui` | the desktop client's state: what the window shows, and every action, without Gio |
| `internal/chatwindow` | the chat window itself, in Gio, painting a `gui.Screen` |
| `internal/interop` | tests only: a window and a terminal on two ends of one real Session |
| `cmd/dcc-cli` | the terminal binary — `client`, the terminal, and the video window |
| `cmd/dcc-gui` | the desktop binary — `client`, and the window |
| `docs/adr` | the decisions that are expensive to revisit |

Both binaries — `dcc-cli` and `dcc-gui` — are thin consumers of
`internal/session`, which is where every decision about a Session is made.

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

## Using dcc-gui

```sh
go run ./cmd/dcc-gui -name "your name"
```

The same Sessions as `dcc-cli`, with buttons instead of commands: **Host a
Session** opens a Rendezvous and puts the Invite across the top with a button
that copies it, and pasting an Invite into the box and pressing **Connect**
joins someone else's. Both sides then compare the Security Code the window
puts in front of them and answer before anything either of them types is
shown. Enter sends, shift+enter starts a line. **History** opens the panel of
Conversations stored on this device, each of which can be read back with
nobody connected and deleted from this device alone.

Emoji are typed the way the operating system types them — the compose key, the
Windows emoji panel, a paste — and rendered through a bundled monochrome Noto
Emoji, which is the fallback the Go fonts do not carry. There is no picker
yet. The font is in `internal/chatwindow/fonts`, under the SIL Open Font
License kept beside it.

A window and a terminal hold the same Conversation: `dcc-gui` and `dcc-cli`
drive the same `internal/session`, so either can host and either can join.

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
`internal/window`, `internal/chatwindow` and the two `cmd` binaries builds and
tests with no tags at all — the video window sits behind a seam in
`internal/cli`, and the chat window behind `internal/gui`, whose Model and
Screen are the whole of what the desktop client knows. The Windows build needs
none of this either: Gio is cgo-free there, so `GOOS=windows go build ./...`
cross-compiles from Linux with nothing installed.

Real `cloudflared` tunnels, real capture devices — microphone and webcam — and
browser interop are verified by hand; the test suite covers the in-process
seams only. The terminal tests watch the video window open and close without
one appearing on screen. To see a real one:

```sh
DCC_WINDOW=1 go test -tags novulkan ./internal/window/
```

The chat window is covered the other way round: `internal/gui` is tested
against fakes, `internal/chatwindow` lays every state of the window out into
an `op.Ops` without opening one, and `internal/interop` puts a window and a
terminal on two ends of a real loopback Session and makes them talk.
