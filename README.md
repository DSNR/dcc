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
| `internal/storage` | Conversations in SQLite, encrypted at rest |
| `internal/media` | capture, encode and decode for Calls |
| `docs/adr` | the decisions that are expensive to revisit |

The two binaries — `dcc-cli` and `dcc-gui` — will sit under `cmd/`; both are
thin consumers of `internal/session`.

## Working on it

```sh
go build ./...
go vet ./...
go test ./...
```

Real `cloudflared` tunnels, real capture devices and browser interop are
verified by hand; the test suite covers the in-process seams only.
