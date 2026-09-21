# Quick Tunnel operational facts for the Rendezvous

Resolves issue #2. Scope: what the app must know to drive `cloudflared` Quick Tunnels (TryCloudflare) unattended on Windows and Linux, with a pre-installed binary and no Cloudflare account.

Sources are primary only: Cloudflare developer docs, the `cloudflared` source on GitHub (`master`, checked 2026-09-21), Go and Microsoft docs, plus empirical runs of `cloudflared 2026.9.1` on Linux (WSL2) on 2026-09-21. Empirical claims are marked **[observed]**.

Key source files (cloudflared `master`):

- `cmd/cloudflared/tunnel/quick_tunnel.go` — https://github.com/cloudflare/cloudflared/blob/master/cmd/cloudflared/tunnel/quick_tunnel.go
- `cmd/cloudflared/tunnel/cmd.go` — https://github.com/cloudflare/cloudflared/blob/master/cmd/cloudflared/tunnel/cmd.go
- `cmd/cloudflared/tunnel/signal.go` — https://github.com/cloudflare/cloudflared/blob/master/cmd/cloudflared/tunnel/signal.go
- `cmd/cloudflared/cliutil/logger.go` — https://github.com/cloudflare/cloudflared/blob/master/cmd/cloudflared/cliutil/logger.go
- `cmd/cloudflared/main.go` — https://github.com/cloudflare/cloudflared/blob/master/cmd/cloudflared/main.go
- `cmd/cloudflared/windows_service.go` — https://github.com/cloudflare/cloudflared/blob/master/cmd/cloudflared/windows_service.go
- `metrics/metrics.go`, `metrics/readiness.go` — https://github.com/cloudflare/cloudflared/blob/master/metrics/metrics.go , https://github.com/cloudflare/cloudflared/blob/master/metrics/readiness.go
- `logger/create.go` — https://github.com/cloudflare/cloudflared/blob/master/logger/create.go
- `connection/observer.go` — https://github.com/cloudflare/cloudflared/blob/master/connection/observer.go
- `supervisor/supervisor.go`, `supervisor/tunnel.go` — https://github.com/cloudflare/cloudflared/blob/master/supervisor/supervisor.go , https://github.com/cloudflare/cloudflared/blob/master/supervisor/tunnel.go
- `cmd/cloudflared/updater/update.go` — https://github.com/cloudflare/cloudflared/blob/master/cmd/cloudflared/updater/update.go

Canonical doc page: https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/do-more-with-tunnels/trycloudflare/ (the older `/cloudflare-one/connections/connect-networks/...` path still serves the same content).

---

## 1. Invocation

Documented command:

```sh
cloudflared tunnel --url http://localhost:8080
```

Source: https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/do-more-with-tunnels/trycloudflare/

How it is dispatched in code: `cloudflared tunnel` with `--url` (or `--hello-world`) set and a non-empty hidden `--quick-service` (default `https://api.trycloudflare.com`) calls `RunQuickTunnel` (`cmd.go`: `shouldRunQuickTunnel := c.IsSet("url") || c.IsSet(ingress.HelloWorldFlag)`). `--url` defaults to `http://localhost:8080`, env `TUNNEL_URL` (`cmd.go`).

`RunQuickTunnel` (`quick_tunnel.go`): POSTs to `<quick-service>/tunnel` with a 15 s HTTP client timeout (`httpTimeout = 15 * time.Second`), parses `{success, result:{id,name,hostname,account_tag,secret}, errors:[{code,message}]}`, then forces `--protocol quic` (unless the user set `--protocol`) and `--ha-connections 1`, and starts the normal tunnel server with `QuickTunnelUrl` set.

Recommended invocation for unattended use (flags verified against `cloudflared tunnel --help` on 2026.9.1 and `cmd.go`):

```sh
cloudflared tunnel --url http://127.0.0.1:<port> \
  --no-autoupdate \
  --output json \
  --metrics 127.0.0.1:<metrics-port> \
  --protocol auto \
  --grace-period 5s
```

- `--no-autoupdate`: "Disable periodic check for updates, restarting the server with the new version." (`cmd.go`; env `NO_AUTOUPDATE`). Auto-update, when enabled, checks every 24 h (`updater.DefaultCheckUpdateFreq = time.Hour * 24`) and restarts the process. It is already disabled when run from a shell, on Windows, or when installed by a package manager (`updater/update.go`: `noUpdateInShellMessage`, `noUpdateOnWindowsMessage`, `noUpdateManagedPackageMessage`). Docs: "This parameter does not apply when cloudflared runs on Windows, was installed by a package manager, or runs interactively in a terminal." https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/configure-tunnels/cloudflared-parameters/run-parameters/ . Pass it anyway: a child process spawned by a GUI app is not "interactive in a terminal".
- `--output json`: "Output format for the logs (default, json)" (`cliutil/logger.go` `FlagLogOutput`; env `TUNNEL_LOG_OUTPUT`). See section 2.
- `--metrics <ip:port>`: metrics/health HTTP server. See section 3.
- `--protocol auto`: needed to get http2 fallback; quick tunnels otherwise pin `quic`. See section 5.
- Caveat: "Quick Tunnels are currently not supported if a `config.yaml` configuration file is present in the `.cloudflared` directory." https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/do-more-with-tunnels/trycloudflare/ . The app should not rely on the user's `~/.cloudflared`; the log line `Cannot determine default configuration path. No file [config.yml config.yaml] in [...]` is the normal, healthy case **[observed]**.

## 2. Parsing the `*.trycloudflare.com` URL from output

### Where it goes

All cloudflared logs go to **stderr**, never stdout. `logger/create.go` `createConsoleLogger`: JSON mode returns `&consoleWriter{out: os.Stderr}`; default mode returns `zerolog.ConsoleWriter{Out: colorable.NewColorable(os.Stderr), NoColor: config.noColor || !term.IsTerminal(...), TimeFormat: time.RFC3339}`. Colour is auto-disabled when stderr is not a TTY, so a pipe gets plain text. **[observed]** stdout was empty in all runs.

### Exact lines (default format)

`RunQuickTunnel` logs via `cliutil.LogTable`, which wraps lines in an ASCII box and logs each row at INFO (`cliutil/logger.go` `asciiBox`/`renderBoxLine`: `"|" + spacer + line + padding + spacer + "|"` with `padding = 2`). Observed output (2026.9.1):

```
2026-09-21T19:45:14Z INF Thank you for trying Cloudflare Tunnel. Doing so, without a Cloudflare account, ... (disclaimer)
2026-09-21T19:45:14Z INF Requesting new quick Tunnel on trycloudflare.com...
2026-09-21T19:45:20Z INF +--------------------------------------------------------------------------------------------+
2026-09-21T19:45:20Z INF |  Your quick Tunnel has been created! Visit it at (it may take some time to be reachable):  |
2026-09-21T19:45:20Z INF |  https://monitored-poultry-favor-characteristic.trycloudflare.com                          |
2026-09-21T19:45:20Z INF +--------------------------------------------------------------------------------------------+
```

The literal strings in source are `"Your quick Tunnel has been created! Visit it at (it may take some time to be reachable):"` and the URL, prefixed with `https://` if the API response lacks it (`quick_tunnel.go`). The box width is dynamic (longest line + padding), so match the URL, not the box.

### Exact lines (`--output json`)

One JSON object per line, keys `level`, `message`, `time` (RFC3339 UTC), plus context fields. **[observed]**:

```
{"level":"info","message":"|  https://alt-consumption-humor-themselves.trycloudflare.com                                |","time":"2026-09-21T19:45:30Z"}
{"connIndex":0,"connection":"0131df48-...","event":0,"ip":"198.41.200.23","level":"info","location":"lhr13","message":"Registered tunnel connection","protocol":"quic","time":"2026-09-21T19:45:31Z"}
```

The URL is still inside the ASCII-box row in `message`; there is no dedicated `url` field. JSON mode helps because it removes ANSI/format ambiguity and gives structured `level`/fields for the later lines, but URL extraction is still a regex.

### Recommended regex

`https://[a-z0-9-]+\.trycloudflare\.com` applied to every stderr line (both modes). Hostnames are `<word>-<word>-<word>-<word>.trycloudflare.com` **[observed]** but treat the label as opaque `[a-z0-9-]+`.

### Important: URL printed != reachable

The URL line is emitted **before** any edge connection is attempted. **[observed]** with an unreachable edge (`--edge 127.0.0.1:1`) the URL box still printed, then `Failed to dial a quic connection` repeated forever. Readiness must be gated on one of:

1. Log line `Registered tunnel connection` (INFO, fields `connIndex`, `connection`, `event`, `ip`, `location`, `protocol`) — source `connection/observer.go` `logConnected`.
2. `GET http://<metrics>/ready` returning 200 (section 3).

Even after registration the docs say "it may take some time to be reachable" (DNS propagation of the random subdomain); the app should retry the first outbound fetch of its own URL.

## 3. `--metrics` endpoints (best structured channel)

`--metrics` "Listen address for metrics reporting. If no address is passed cloudflared will try to bind to [localhost:20241 ... localhost:20245]. If all are unavailable, a random port will be used." (`cmd.go`; docs https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/monitor-tunnels/metrics/ ). Log line: `Starting metrics server on 127.0.0.1:20999/metrics` **[observed]**. Pass an explicit `127.0.0.1:<port>` so the app knows where it is.

Handlers registered in `metrics/metrics.go` `newMetricsHandler`:

| Path | Response | Source |
|---|---|---|
| `/quicktunnel` | `{"hostname":"<name>.trycloudflare.com"}` (no scheme) | `router.HandleFunc("/quicktunnel", ... fmt.Fprintf(w, `{"hostname":"%s"}`, config.QuickTunnelHostname))` |
| `/ready` | `{"status":200,"readyConnections":1,"connectorId":"<uuid>"}` with HTTP 200 when >0 edge connections, else HTTP 503 with `"status":503,"readyConnections":0` | `metrics/readiness.go` `makeResponse` |
| `/healthcheck` | `OK\n` (process alive) | `metrics.go` |
| `/metrics` | Prometheus text | `metrics.go` |
| `/config` | current ingress config JSON | `metrics.go` |
| `/debug/pprof/cmdline` | 403 (deliberately blocked) | `metrics.go` |

**[observed]** on 2026.9.1: `/quicktunnel` → `{"hostname":"monitored-poultry-favor-characteristic.trycloudflare.com"}`; `/ready` → `{"status":200,"readyConnections":1,"connectorId":"3782d772-..."}`; `/healthcheck` → `OK`.

Timing caveat **[observed]**: the metrics server starts ~1 s *after* the URL box is logged (`Starting metrics server` at 19:45:21 vs URL at 19:45:20). Poll `/quicktunnel` with retries from process start; do not assume it is up immediately. Recommended app strategy: regex stderr for the URL (fast path) **and** poll `/ready` until 200 before advertising the URL; `/quicktunnel` is a robust fallback if log parsing fails. Only `/metrics` is in the public docs; the rest are source-level facts subject to change.

## 4. WebSocket support

- Tunnels FAQ: "Yes. Cloudflare Tunnel has full support for Websockets." https://developers.cloudflare.com/cloudflare-one/faq/cloudflare-tunnels-faq/
- Cloudflare edge: proxied WebSockets are on for all plans with no extra setup. https://developers.cloudflare.com/network/websockets/
- Only the upgrade request counts as an HTTP request; the open socket is one long-lived request. https://developers.cloudflare.com/network/websockets/ . For the 200 in-flight limit (section 6), assume each open WebSocket occupies one slot.
- Quick Tunnels explicitly **do not support Server-Sent Events (SSE)**. https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/do-more-with-tunnels/trycloudflare/ . Use WebSocket, not SSE, for the signaling channel.
- Common failure "WebSocket: Bad handshake" — causes listed: tunnel not running/connected, WebSockets disabled, SSL/TLS set to Off, Bot Fight Mode, Worker routes. https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/troubleshoot-tunnels/common-errors/ . For quick tunnels only the first applies (no zone settings to control).

## 5. Timeouts

Cloudflare-side (edge), from https://developers.cloudflare.com/fundamentals/reference/connection-limits/ :

- Proxy Read Timeout (Cloudflare to origin): 100 s, configurable only for Enterprise. Applies to non-WebSocket HTTP responses.
- Client keep-alive / HTTP/2 idle to Cloudflare: 400 s.
- WebSocket idle: "Cloudflare will close a WebSocket connection when no data is transmitted in either direction for a period of time." No number is published; Enterprise can request custom values; the doc recommends a client-side heartbeat. https://developers.cloudflare.com/network/websockets/ . **Recommendation**: app-level ping every 20-30 s on the signaling WebSocket.

cloudflared-side (origin proxying), defaults from `cloudflared tunnel --help` (2026.9.1) and https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/configure-tunnels/cloudflared-parameters/origin-parameters/ :

- `--proxy-connect-timeout` 30 s (TCP connect to origin), `--proxy-tls-timeout` 10 s, `--proxy-tcp-keepalive` 30 s, `--proxy-keepalive-connections` 100, `--proxy-keepalive-timeout` 1m30s (idle pooled connection). No per-request read deadline is imposed by cloudflared for streams.

Edge connection (cloudflared to Cloudflare): QUIC over UDP/7844, HTTP/2 over TCP/7844 fallback; firewall doc: "Ensure port 7844 is allowed for both TCP and UDP protocols (for http2 and quic)", hostnames `region1.v2.argotunnel.com`, `region2.v2.argotunnel.com`. https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/configure-tunnels/tunnel-with-firewall/ . `--protocol auto`: "will automatically configure the quic protocol. If cloudflared is unable to establish UDP connections, it will fallback to using the http2 protocol." https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/configure-tunnels/cloudflared-parameters/run-parameters/ . **Quick tunnels force `quic` unless `--protocol` is explicitly set** (`quick_tunnel.go`: `if !sc.c.IsSet(flags.Protocol) { _ = sc.c.Set(flags.Protocol, "quic") }`), so pass `--protocol auto` explicitly to get http2 fallback on UDP-blocked networks. UDP idle: "Network devices aggressively timeout UDP traffic on idle connections" — mitigation is app keepalives or `protocol: http2`. https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/troubleshoot-tunnels/common-errors/

## 6. Limits and caveats (documented)

All from https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/do-more-with-tunnels/trycloudflare/ unless noted:

- "Quick Tunnels are intended for testing and development only." "We don't guarantee any SLA or uptime of TryCloudflare."
- Hard limit of **200 in-flight (concurrent) requests** per quick tunnel; exceeding it returns HTTP **429**.
- No SSE.
- Not supported when a `config.yaml` exists in `.cloudflared`.
- Disclaimer logged at startup (source `quick_tunnel.go` `disclaimer`): "...these account-less Tunnels have no uptime guarantee, are subject to the Cloudflare Online Services Terms of Use (https://www.cloudflare.com/website-terms/), and Cloudflare reserves the right to investigate your use of Tunnels for violations of such terms."
- Random subdomain, new on every launch; cannot be chosen or reused ("generates a random subdomain on trycloudflare.com").
- Single edge connection (`ha-connections` forced to 1, `quick_tunnel.go`), so any edge blip drops the tunnel until cloudflared reconnects (it auto-reconnects; section 7).
- Provisioning rate limit: `api.trycloudflare.com/tunnel` can return **429 / Cloudflare error 1015** to an IP that creates tunnels too often; older cloudflared produced a cryptic `failed to unmarshal quick Tunnel` in that case. https://github.com/cloudflare/cloudflared/issues/972 , https://github.com/cloudflare/cloudflared/pull/1302 . Current `master` reports `quick tunnel provisioning failed with status <code>: <body or [code] message>` (`quick_tunnel.go`). No numeric rate is published. **Implication**: never restart-loop cloudflared tightly; back off exponentially on provisioning failure.
- Version support: "Cloudflare supports versions of cloudflared that are within one year of the most recent release." https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/downloads/

## 7. Startup latency and reconnect behaviour

**[observed]** (Linux, WSL2, London, 2026.9.1, three runs): process start → URL box printed: ~6 s (dominated by the `api.trycloudflare.com` POST); URL → `Registered tunnel connection`: ~1-2 s; total start → ready: **~7.7 s** (measured 7.65 s). The provisioning POST has a hard 15 s client timeout (`httpTimeout`). Budget ~10 s typical, 30 s worst case before declaring failure.

Reconnect: on edge loss, supervisor logs `Retrying connection in up to <d>` and reconnects with backoff; `--retries` (default 5) bounds retries per protocol before protocol fallback (`--max-edge-addr-retries` 8, hidden), **not** total attempts. **[observed]** with an unreachable edge and `--retries 1`, cloudflared kept dialing every ~5 s (`Failed to dial a quic connection error="failed to dial to edge with quic: timeout: no recent network activity"`) for 2+ minutes without exiting. The process only exits on its own when all connections end while shutting down (`no more connections active and exiting`, `supervisor.go`) or on `initial tunnel connection failed`. The hostname stays the same across reconnects (same tunnel credentials), so the app does not need to re-parse it.

Since 2026.5.2 cloudflared runs **connectivity pre-checks** at `tunnel run` startup (DNS for `region1/region2.v2.argotunnel.com`, UDP+TCP 7844, TCP 443 to api.cloudflare.com) and "exits early with the failure" if DNS fails or both transports fail. https://developers.cloudflare.com/changelog/post/2026-05-27-cloudflared-connectivity-prechecks/ . Flag `--no-prechecks` (`cmd.go`). **[observed]** no pre-check table appeared in the quick-tunnel runs on 2026.9.1; the source comment says pre-checks "are diagnostic only and do not gate tunnel startup" (`cmd.go` `runPrechecks`), so do not rely on them for quick tunnels.

## 8. Failure modes

| Condition | Behaviour | Evidence |
|---|---|---|
| No network / DNS down | Exits **code 1** within seconds; last stderr line (no timestamp/level, printed by urfave/cli): `failed to request quick Tunnel: Post "https://api.trycloudflare.com/tunnel": dial tcp: lookup ...: no such host` | **[observed]** with `--quick-service https://nonexistent.invalid` |
| API unreachable (refused / 443 firewalled) | Exits code 1: `failed to request quick Tunnel: Post "...": dial tcp ...: connect: connection refused`; a silent drop hits the 15 s `httpTimeout` first | **[observed]**, `quick_tunnel.go` |
| API 4xx/5xx (incl. 429 rate limit) | Exits code 1: `quick tunnel provisioning failed with status 429: ...` | `quick_tunnel.go` |
| UDP/7844 blocked, TCP allowed | With forced `quic`: repeated `Failed to dial a quic connection` + warning "...most likely your machine/network is getting its egress UDP to port 7844 (or others) blocked or dropped..." (`supervisor/tunnel.go`); with `--protocol auto` falls back to http2 | source + run-parameters doc |
| Both 7844 UDP and TCP blocked | URL printed but never `Registered tunnel connection`; `/ready` stays 503; process keeps retrying, does not exit | **[observed]** analogue with `--edge 127.0.0.1:1` |
| Origin (our local server) down | Tunnel registers fine; visitors get 502; cloudflared logs `dial tcp [::1]:<port>: connect: connection refused` | https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/troubleshoot-tunnels/common-errors/ |
| `config.yaml` present in `~/.cloudflared` | Quick tunnels "not supported" | trycloudflare doc |
| Binary too old | Documented floor "cloudflared 2020.5.1 or later"; support window is one year from latest release; older builds lack `/quicktunnel`, `--output json`, etc. | trycloudflare doc; downloads doc |
| >200 concurrent requests | Edge returns 429 to visitors | trycloudflare doc |

Exit codes: cloudflared uses urfave/cli; an error returned from the action yields exit 1 with the error text on stderr **[observed]**. `cloudflared update` uses exit code 11 to signal "updated" (`main.go`). Clean shutdown exits 0 **[observed]** for both SIGTERM and SIGINT.

## 9. Binary detection and version

- Locate via `PATH` (`exec.LookPath("cloudflared")`). Linux packages/tarball install to `/usr/local/bin/cloudflared` **[observed]**; Windows MSI/winget puts `cloudflared.exe` on PATH. Downloads: https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/downloads/ . The MSI default directory is not documented on that page; verify empirically on Windows before hard-coding a fallback path.
- `cloudflared --version` / `-v` / `-V` prints `cloudflared version 2026.9.1 (built 2026-09-11-13:35 UTC)` **[observed]**; format string `"%s (built %s%s)"` with `Version`, `BuildTime`, optional build-type suffix (`main.go` `app.Version`). Dev builds print `DEV`.
- `cloudflared version --short` / `-s` prints just `2026.9.1` **[observed]** (`main.go`: `strings.Split(c.App.Version, " ")[0]`). Prefer this for parsing.
- Version scheme is `YYYY.M.N` (e.g. `2026.9.1`, `2026.8.3`), tags at https://github.com/cloudflare/cloudflared/releases . Latest at time of writing: 2026.9.1 (2026-09-11).
- Also logged at startup: `Version 2026.9.1 (Checksum <sha256>)` and `GOOS: linux, GOVersion: go1.26.8, GoArch: amd64` **[observed]**.
- Windows: "Instances of cloudflared do not automatically update on Windows. You will need to perform manual updates." https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/downloads/

Suggested minimum: **2026.5.2** (inside the one-year support window; has `--output json`, `/quicktunnel`, `/ready`, pre-checks). Absolute documented floor for quick tunnels is 2020.5.1, but anything older than ~one year is unsupported by Cloudflare. Gate on the `version --short` value with a `YYYY.M.N` comparison.

## 10. Clean shutdown

cloudflared's shutdown path (`tunnel/signal.go` `waitForSignal`): `signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)`; on receipt logs `Initiating graceful shutdown due to signal <name> ...`, closes `graceShutdownC`; `waitToShutdown` (`cmd.go`) then waits up to `--grace-period` (default 30 s: "stop accepting new requests, wait for in-progress requests to terminate, then shutdown. Waiting for in-progress requests will timeout after this grace period, or when a second SIGTERM/SIGINT is received") and exits 0 with `Tunnel server stopped` / `Metrics server stopped` **[observed]**. Docs: https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/configure-tunnels/cloudflared-parameters/run-parameters/

**[observed]** with no in-flight requests, SIGTERM → exit in <1 s (`Retrying connection in up to 1s`, `Connection terminated`, `no more connections active and exiting`, exit 0). Same for SIGINT.

### Linux

Send `SIGTERM` (`cmd.Process.Signal(syscall.SIGTERM)`); wait up to `--grace-period` plus margin; then `SIGKILL`. Pass `--grace-period 5s` (or lower) to bound shutdown time — there is no persistent state to lose. Set `SysProcAttr.Pdeathsig = syscall.SIGTERM` so the tunnel dies if the app crashes.

### Windows

There is no SIGTERM. Go's `os/signal` on Windows: "If Notify is called for os.Interrupt, ^C or ^BREAK will cause os.Interrupt to be sent on the channel", and `CTRL_CLOSE_EVENT`/`CTRL_LOGOFF_EVENT`/`CTRL_SHUTDOWN_EVENT` are delivered as `syscall.SIGTERM`. https://pkg.go.dev/os/signal#hdr-Windows . cloudflared registers both `SIGINT` and `SIGTERM`, so a console control event triggers its graceful path, logging `Initiating graceful shutdown due to signal interrupt ...`.

To deliver one from a parent process: `GenerateConsoleCtrlEvent(CTRL_BREAK_EVENT, <pid>)`, which requires the child to have been created with `CREATE_NEW_PROCESS_GROUP` (the child's PID becomes its process-group ID) and to share the parent's console. `CTRL_C_EVENT` "cannot be limited to a specific process group" (would also hit the parent), so use `CTRL_BREAK_EVENT`. https://learn.microsoft.com/en-us/windows/console/generateconsolectrlevent . In Go: `cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}` then `windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(pid))`.

Caveats: (a) if the app is a GUI process with no console, it must `AllocConsole`/attach one (or give the child its own hidden console) for console control events to work; (b) `Process.Kill()` on Windows is `TerminateProcess` — abrupt, but acceptable as the fallback because a quick tunnel has no on-disk state and the edge simply sees the connection drop. Not running as a Windows service: `windows_service.go` only takes the SCM stop path when `svc.IsAnInteractiveSession()` is false; a child of a user app is interactive and takes the plain `app.Run` path, so console events are the only graceful trigger. Use a Job Object with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` so the tunnel dies with the app.

## 11. Recommended driver contract (summary)

1. Detect: `exec.LookPath("cloudflared")`; run `cloudflared version --short`; parse `YYYY.M.N`; require >= 2026.5.2 (tunable).
2. Spawn: `cloudflared tunnel --url http://127.0.0.1:<p> --no-autoupdate --output json --metrics 127.0.0.1:<m> --protocol auto --grace-period 5s`, stdout ignored, stderr line-scanned; Windows: `CREATE_NEW_PROCESS_GROUP` + job object; Linux: `Pdeathsig`.
3. URL: regex `https://[a-z0-9-]+\.trycloudflare\.com` on stderr, fallback `GET /quicktunnel`.
4. Ready: line `"message":"Registered tunnel connection"` or `GET /ready` → 200; then self-probe the public URL with retries (DNS propagation).
5. Health: poll `/ready` (503 = disconnected, reconnecting); `/healthcheck` = process alive.
6. Failure: exit code 1 + `failed to request quick Tunnel` / `quick tunnel provisioning failed with status 429` → back off exponentially (min 30 s) before relaunch; never tight-loop (rate limit 1015).
7. Stop: SIGTERM (Linux) / `CTRL_BREAK_EVENT` (Windows), wait grace + 2 s, then kill.
8. Signaling: WebSocket only (no SSE); app-level ping every 20-30 s; stay well under 200 concurrent requests.
