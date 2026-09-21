# Fallback relay over the Rendezvous WebSocket (issue #3)

Date: 2026-09-21. Part of #1.

## Question

ICE uses public STUN only, no TURN. When ICE fails, what can be relayed over the
Host's Rendezvous WebSocket (exposed by a cloudflared Quick Tunnel, HTTP/WS only)?

## Constraints established from primary sources

- Quick Tunnels are started with `cloudflared tunnel --url http://localhost:8080`,
  have a hard limit of 200 in-flight requests (429 beyond that), do not support SSE,
  and carry no SLA ("intended for testing and development only").
  https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/do-more-with-tunnels/trycloudflare/
- Cloudflare proxies WebSockets on all plans with no extra config; only the upgrade
  request counts as an HTTP request; idle connections are closed after inactivity, so
  a heartbeat is recommended.
  https://developers.cloudflare.com/network/websockets/
- Proxy idle timeout 900 s, proxy read timeout 125 s (524), write timeout 30 s.
  https://developers.cloudflare.com/fundamentals/reference/connection-limits/
- Arbitrary (non-HTTP) TCP through a tunnel requires `cloudflared` on the *client*
  side too ("cloudflared will need to be installed on each user device that will
  connect"; `cloudflared access tcp --hostname tcp.site.com --url localhost:9210`)
  and an Access-fronted named tunnel. A plain TCP client cannot reach a tunnelled
  TCP origin. https://developers.cloudflare.com/cloudflare-one/applications/non-http/cloudflared-authentication/arbitrary-tcp/
  and https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/use-cases/ssh/
- Consequence: the only transport the Client can open to the Host through the Quick
  Tunnel is an HTTP request or a WebSocket. No UDP, no raw TCP.

## pion facts that matter

- `SettingEngine.SetNet(net transport.Net)` replaces pion's network stack for ICE.
  https://github.com/pion/webrtc/blob/master/settingengine.go
- `SettingEngine.SetICETCPMux(ice.TCPMux)` "enables ICE-TCP when set to a non-nil
  value" (also enable `NetworkTypeTCP4/6`).
  https://github.com/pion/webrtc/blob/master/settingengine.go
- `webrtc.NewICETCPMux(logger, listener net.Listener, readBufferSize int) ice.TCPMux`
  "enables use of passive ICE TCP candidates". The listener is any `net.Listener`.
  https://github.com/pion/webrtc/blob/master/icemux.go
- `ice.TCPMuxDefault` routes each accepted conn by the ufrag in the first STUN
  Binding request (`GetConnByUfrag`), and frames packets with a 2-byte big-endian
  length prefix (RFC 4571) via `readStreamingPacket`/`writeStreamingPacket`.
  https://github.com/pion/ice/blob/master/tcp_mux.go
- Active side: `addRemotePassiveTCPCandidate` calls `newActiveTCPConn`, which dials
  with a plain `net.Dialer` (`dialer.DialContext(ctx, "tcp", remote)`), **not** via
  `transport.Net`. There is no loopback filter on remote candidates; local
  interfaces are enumerated with `includeLoopback`.
  https://github.com/pion/ice/blob/master/active_tcp.go ,
  https://github.com/pion/ice/blob/master/agent.go
- `SetICEProxyDialer` is used only for TURN/TURNS over TCP (`gatherCandidatesRelay`,
  wrapped with `turn.NewSTUNConn`). https://github.com/pion/ice/blob/master/gather.go
- `ice.UDPMux` is `GetConn(ufrag, addr) (net.PacketConn, error)`,
  `RemoveConnByUfrag`, `Close`. https://pkg.go.dev/github.com/pion/ice/v4#UDPMux
- pion/turn v4: server takes `ListenerConfigs` (`net.Listener` + RelayAddressGenerator);
  client takes `Conn net.PacketConn`; `NewSTUNConn(net.Conn) net.PacketConn` packetises
  a stream; RFC 6062 TCP allocations implemented.
  https://pkg.go.dev/github.com/pion/turn/v4 , https://github.com/pion/turn/blob/master/README.md ,
  https://github.com/pion/turn/blob/master/examples/turn-server/tcp/main.go
- Layers are usable standalone: `sctp.Config{NetConn net.Conn}` /
  `ClientWithOptions`; `dtls.Client(conn net.PacketConn, rAddr, cfg)`;
  `datachannel.Client(stream *sctp.Stream, cfg)`.
  https://pkg.go.dev/github.com/pion/sctp , https://pkg.go.dev/github.com/pion/dtls/v3 ,
  https://pkg.go.dev/github.com/pion/datachannel
- `coder/websocket.NetConn(ctx, c, msgType) net.Conn` "is for tunneling arbitrary
  protocols over WebSockets" (each Write = one message).
  https://pkg.go.dev/github.com/coder/websocket#NetConn

## Standards facts

- ICE-TCP: TCP candidates are lower priority and "only use the TCP-based ones if the
  UDP ones fail"; TCP media uses RFC 4571 framing "even if the application layer
  protocol is not RTP"; frames may be media, DTLS or STUN.
  https://www.rfc-editor.org/rfc/rfc6544.html
- RFC 4571: 16-bit big-endian LENGTH then packet. https://www.rfc-editor.org/rfc/rfc4571.html
- WebRTC transports MUST support ICE-TCP and TURN over TCP and TLS; RFC 4571 framing
  MUST be used on TCP. https://www.rfc-editor.org/rfc/rfc8835.html
- Data channels: SCTP over DTLS (RFC 8261) over ICE/UDP; "DTLS protects the complete
  SCTP packet". https://www.rfc-editor.org/rfc/rfc8831.html
- Security arch: all media MUST be SRTP; DTLS-SRTP with fingerprints in signaling;
  "the signaling server can potentially mount a man-in-the-middle attack unless
  implementations have some mechanism for independently verifying keys".
  https://www.rfc-editor.org/rfc/rfc8827.html
- Opus: 6-510 kbit/s; fullband speech sweet spot 28-40 kbit/s.
  https://www.rfc-editor.org/rfc/rfc6716.html

## Options

### (a) Run the real PeerConnection over the WS: ICE-TCP with a WS-backed conn

Mechanism: keep the full pion stack (ICE -> DTLS -> SCTP/SRTP). Host runs an
ICE-TCP passive candidate whose `net.Listener` is a WS adapter: each fallback WS
connection is wrapped with `websocket.NetConn` and returned from `Accept()`; the
mux then does normal STUN-ufrag routing and RFC 4571 framing on that stream.
Client side: pion dials passive TCP candidates with a raw `net.Dialer`, so the
client runs a loopback TCP listener (127.0.0.1:N) that bridges each accepted conn
to a WS to the Host, and injects a synthetic remote passive TCP candidate
`127.0.0.1:N` via `AddICECandidate`. Enable `NetworkTypeTCP4` and
`SetIncludeLoopbackCandidate(true)` so a loopback local interface exists to dial from
(pion enumerates local IPs with `includeLoopback`; no remote-loopback filter).

Alternatives inside (a):
- `SetNet(transport.Net)`: does not help; active TCP bypasses `transport.Net`.
- Custom `ice.UDPMux` over WS: workable on the Host side only; the Client still
  needs a host candidate it can send to, which again ends up as a loopback bridge.
  The TCP-mux route uses pion's already-specified stream framing, so prefer it.
- `SetICEProxyDialer`: TURN-only, irrelevant without a TURN URL.

Feasibility: yes, with no pion patches. Everything used is exported API.

Complexity: moderate. ~300-500 lines Go: WS listener adapter (Host), loopback
bridge + candidate injection (Client), WS keepalive, fallback trigger on
`ICEConnectionStateFailed`. Needs a prototype to confirm the loopback dial path
and that pion accepts a remote candidate it did not learn via SDP (it does via
`AddICECandidate`).

Latency/bandwidth: path is Client -> CF edge -> tunnel -> Host, over TCP+TLS+WS.
Head-of-line blocking (RFC 6544 rationale for preferring UDP) applies; SRTP
retransmits/NACK still work but jitter rises. Audio at Opus 28-40 kbit/s is
realistic (comparable to TURN-over-TCP which RFC 8835 mandates browsers support).
Video works but expect degraded quality; pion's congestion control cannot see TCP
loss so bitrate adaptation is weak. Read timeout 125 s / idle 900 s are far above
any WS ping interval; 200 in-flight request cap is one request per WS.

E2EE: holds. Cloudflare and the tunnel see STUN + DTLS ciphertext only; keys never
leave the peers. Trust in signaling is unchanged (fingerprints already traverse the
same WS today). Note that the Quick Tunnel terminates TLS at Cloudflare, so the
existing out-of-band fingerprint/SAS check is what stops a MITM (RFC 8827).

### (b) pion/turn hosted by the Host behind the tunnel (TURN over TCP/TLS)

Mechanism: Host runs `turn.NewServer` with a TCP `ListenerConfig`; Client uses
`turn:host:port?transport=tcp` in `ICEServers`.

Feasibility through a Quick Tunnel: no. The Quick Tunnel only carries HTTP/WS;
raw TCP (which TURN-TCP is) needs `cloudflared access tcp` on the Client plus a
named tunnel with Access. pion's TURN client dials TCP with `net.Dial` /
`proxyDialer` and speaks STUN framing, not WebSocket. One could wrap the TURN
listener in a WS adapter on the Host and give pion a `proxy.Dialer` that returns a
WS-backed conn on the Client (`SetICEProxyDialer` is honoured for TURN-TCP). That is
feasible, but it just reinvents (a) with an extra TURN allocation hop and relay
address bookkeeping (the relay leg is loopback anyway since the TURN server is the
peer).

Complexity: higher than (a) (TURN server + auth + WS adapter + proxy dialer).
Latency/bandwidth: same path as (a) plus TURN encapsulation overhead.
E2EE: holds (TURN never sees inside DTLS).
Verdict: pointless indirection for a two-party call where the relay is one of the
parties.

### (c) Separate framed WS protocol carrying text/control only

Mechanism: app-level frames over the existing signaling WS; media not offered.

Feasibility: trivial. Complexity: low, but it is a second protocol path with its
own message types, ordering, and reconnection logic, and its own E2EE layer.

E2EE: does NOT hold unless a separate key exchange is added (the WS is TLS to
Cloudflare, plaintext at Cloudflare's edge, then tunnel-encrypted to the Host).
You would need to reuse the DTLS identity or add a second AEAD channel keyed from
the same fingerprints. That is a new crypto surface to get right, and it
duplicates what data channels already give for free.

Media: none.

## Comparison

| | (a) ICE-TCP over WS | (b) TURN behind tunnel | (c) text-only WS |
|---|---|---|---|
| Works through Quick Tunnel | yes (WS) | no (raw TCP) unless wrapped, which becomes (a) | yes |
| pion changes | none, exported API | none, but extra server | n/a |
| Code size | ~300-500 LoC | ~600+ LoC | ~200 LoC + new crypto |
| Audio | realistic | realistic | none |
| Video | degraded but works | degraded | none |
| E2EE | preserved (DTLS) | preserved | needs new layer |
| Single code path for chat | yes (same DataChannel) | yes | no |

## Recommendation

Choose (a): ICE-TCP passive candidate on the Host backed by a WS listener, plus a
Client-side loopback TCP-to-WS bridge and injected `127.0.0.1` passive candidate.

Rationale: it is the only option that keeps one code path (DataChannel for chat,
SRTP for media) and keeps E2EE without adding crypto; it uses only exported pion API
and standard RFC 4571 framing; and it is the same shape browsers use for
TURN-over-TCP fallback, so the audio/video realism is known. Treat video over the
fallback as best-effort.

Open items to prototype: (1) confirm pion dials a remote `127.0.0.1` passive
candidate when `SetIncludeLoopbackCandidate(true)`; (2) WS ping interval under
Cloudflare idle timeout; (3) trigger policy (start fallback on
`ICEConnectionStateFailed`, or gather the TCP candidate up-front at low priority so
ICE selects it automatically, per RFC 6544 priority ordering).
