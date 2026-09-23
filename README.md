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
| `internal/media` | capture, encode and decode for Calls |
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

Two clients on one machine need no Cloudflare account and no `cloudflared` at
all: `-local` publishes the Rendezvous on loopback instead.

## Working on it

```sh
go build ./...
go vet ./...
go test ./...
```

Real `cloudflared` tunnels, real capture devices and browser interop are
verified by hand; the test suite covers the in-process seams only.
