# Gio for the GUI and the CLI video window

Gio is the only Go toolkit that is pure Go on Windows, so `dcc-gui` cross-compiles from Linux with `CGO_ENABLED=0`, and its per-frame `paint.ImageOp` over RGBA is the cheapest render path for two 30 fps video surfaces. Fyne has richer widgets but needs cgo on both OSes; Wails cannot render video frames without a WebView bridge. Gio is 0.x, so `go.mod` pins an exact version (v0.10.2) and bumps happen only in a dedicated issue. Linux builds keep Gio's default X11 + Wayland backends and therefore need gcc plus display dev headers, a cost every toolkit shares.

`dcc-cli` links Gio directly and opens its video window in-process, on its own goroutine beside Bubble Tea, rather than spawning `dcc-gui` or a third binary and streaming frames over IPC. The window opens when a Call becomes Active and closes on hangup, showing remote video or screen large with the local preview as a thumbnail overlay; `dcc-gui`'s call view reuses the same layout.

## Consequences

- No IPC frame path exists; both binaries consume the same RGBA frame events from the core media package.
- `dcc-cli` grows by roughly 6 MB and its Linux build needs the same display headers as `dcc-gui`.
- Emoji in MVP is native OS input rendered through a bundled monochrome Noto Emoji fallback font; no picker widget until post-MVP.
- If Gio's widget set proves too thin for the chat UI, the fallback is Fyne with `canvas.Image` + `ImageScaleFastest`, not Wails.
