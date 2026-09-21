# GUI toolkit for live video rendering (Fyne vs Gio vs Wails)

Resolves issue #5 (part of #1). Researched 2026-09-21 against primary sources
(official docs, upstream repos, upstream issue trackers). One empirical data
point (Gio Windows cross-build) was produced locally and is labelled as such.

## Question

Simple chat window plus two live video surfaces at 30 fps on Windows and
Linux. Compare: frame-render path, achievable fps, cgo requirements,
cross-compile from Linux, binary size, maturity. Also: suitability as a
minimal standalone video window launched by the CLI. Recommend one.

## TL;DR

| | Fyne v2.8.1 | Gio v0.10.2 | Wails v2.14 / v3-beta.24 |
|---|---|---|---|
| Frame path | `canvas.Image{Image: *image.RGBA}` + `Refresh()`; texture freed + re-uploaded per refresh; CPU-side scale unless `ImageScaleFastest` | `paint.NewImageOp(*image.RGBA)` per frame + `w.Invalidate()`; `*image.RGBA` is zero-copy fast path; one GPU upload per op | Go -> WebView bridge (base64/JSON or eval events) is the bottleneck; realistic path is local HTTP/WebSocket/WebRTC into `<video>`/`<canvas>` |
| cgo Windows | **Yes** (glfw + go-gl; MSYS2 MinGW) | **No** (x/sys/windows) | No (v3 doc) but needs WebView2 runtime at run time |
| cgo Linux | Yes (GL, X11/Wayland dev libs) | Yes (X11/Wayland/EGL/Vulkan dev libs) | Yes (GTK4 + WebKitGTK 6.0, or GTK3 + WebKit2GTK 4.1 via `-tags gtk3`) |
| Linux -> Windows cross | `CGO_ENABLED=1` + mingw-w64, or fyne-cross (Docker) | `GOOS=windows CGO_ENABLED=0 go build` (verified locally) | v3: "Native Go" for Windows target; v2: works with `wails build -platform windows/amd64` |
| Binary (Windows) | ~8 MB stripped (16 MB unstripped) per upstream issue | **6.6 MB stripped / 9.4 MB unstripped** (measured) | Go binary + external WebView2 runtime + frontend assets |
| Maturity | Stable v2 line, releases 2026-07/08 | Stable-ish v0.x, v0.10.2 2026-08, two active maintainers | v2 stable (2026-08); v3 Beta, beta.22-24 shipped within one week of 2026-09-20 |
| Standalone video window | OK but heavy runtime (widget tree, GLFW, cgo) | **Best fit**: immediate-mode, one goroutine, tiny API surface | Poor: whole WebView process per window, IPC for frames |

**Recommendation: Gio.** It is the only one of the three that is pure Go on
Windows (the harder-to-cross-compile target for this project), its image
path is the cheapest of the three for a stream of `*image.RGBA` frames, and
it is the smallest and simplest thing to launch as a separate video window
from a CLI. Linux still needs cgo + X11/Wayland dev headers at build time
for all three toolkits, so that is not a differentiator.

---

## 1. Fyne

### Frame-render path

- `canvas.Image` "may be a vector or a bitmap representation, it will fill the
  area"; `Refresh` "causes this image to be redrawn with its configured state".
  Set `img.Image = frame` (an `*image.RGBA`) then `img.Refresh()`.
  https://github.com/fyne-io/fyne/blob/master/canvas/image.go
- `canvas.Raster` alternative: `NewRasterFromImage` "Rasters returned will map
  pixel for pixel to the screen"; `NewRaster(generator)` calls the generator
  with the pixel width/height on each texture build.
  https://github.com/fyne-io/fyne/blob/master/canvas/raster.go
- GL painter: on Refresh every dirty object goes through `painter.Free` ->
  `freeTexture` ("Free is also called for every object on each Refresh (see
  Canvas.FreeDirtyTextures)"), so the texture is deleted and re-created via
  `newGlImageTexture` -> `paint.PaintImage` -> `imgToTexture` on the next
  paint. i.e. one full texture upload per refreshed frame (expected for video).
  https://github.com/fyne-io/fyne/blob/master/internal/painter/gl/painter.go
  https://github.com/fyne-io/fyne/blob/master/internal/painter/gl/texture.go
- CPU cost trap: `PaintImage` -> `scaleImage` does a CPU CatmullRom or
  NearestNeighbor scale into a fresh `image.NewNRGBA` unless
  `ScaleMode == ImageScaleFastest` (or `ImageScalePixels`), which "will
  scale the image using hardware GPU if available". Use `ImageScaleFastest`.
  https://github.com/fyne-io/fyne/blob/master/internal/painter/image.go
  https://github.com/fyne-io/fyne/blob/master/canvas/image.go

### Realistic fps / CPU

- Upstream discussion #5373 (2025): 2840x2840 @ 45 fps from VLC frames was
  ~360% CPU on an i7-12700H; setting `img.ScaleMode = canvas.ImageScaleFastest`
  made it "totally smooth" at ~260% CPU; dropping the redundant `img.Refresh()`
  saved another core. Heavy, but that is 8 MP @ 45 fps; two 640x480/720p
  streams at 30 fps is ~1-3% of that pixel rate.
  https://github.com/fyne-io/fyne/discussions/5373
- Issue #2209 (closed): ~60 fps raster updates; the mistake was calling
  `SetContent()` per frame (100% of a core) instead of refreshing the raster.
  https://github.com/fyne-io/fyne/issues/2209
- Issue #1190: webcam frames at 5-10 fps via `canvas.Refresh()`; intermittent
  "image never appears" on some Windows machines, closed as question.
  https://github.com/fyne-io/fyne/issues/1190

### cgo

- Docs: "you will need Go version 1.22 or later, a C compiler and your
  system's development tools". Linux: `apt-get install golang gcc
  libgl1-mesa-dev xorg-dev libwayland-dev libxkbcommon-dev`. Windows: MSYS2
  MinGW 64-bit toolchain. https://docs.fyne.io/started/quick/
- Root cause: depends on `github.com/go-gl/glfw/v3.4/glfw` and
  `github.com/go-gl/gl` (both cgo). https://github.com/fyne-io/fyne/blob/master/go.mod
- "Remove CGo on Windows" issue #911 is still **open** (opened 2020, last
  activity 2022; maintainer notes macOS also needs cgo for notifications/menus).
  https://github.com/fyne-io/fyne/issues/911

### Cross-compile Linux -> Windows

- Docs: "To cross-compile a Fyne application you will also have to set
  CGO_ENABLED=1 which tells go to enable the C compiler", using
  `x86_64-w64-mingw64-gcc`; or use `fyne-cross windows -arch=amd64` (Docker,
  bundles MinGW). https://docs.fyne.io/started/cross-compiling/
- fyne-cross latest release v1.6.3, 2026-07-31 (GitHub releases API).
  https://github.com/fyne-io/fyne-cross/releases

### Binary size

- Issue #2393: hello.exe 16 MB; with `-ldflags "-w -s -H=windowsgui"` ~8 MB;
  maintainer: "Basic app is around 8MB due to the go runtime and some
  embedded resources", and `fyne package` applies these flags.
  https://github.com/fyne-io/fyne/issues/2393

### Maturity

- v2.8 released 2026-07-13 (GPU shapes, Wayland auto-selected on Linux, Go
  1.22 min). https://fyne.io/blog/2026/07/13/fyne-v2.8-released/
- Latest tag v2.8.1, 2026-08-26 (GitHub releases API).
  https://github.com/fyne-io/fyne/releases

### As a standalone CLI-launched video window

Works (one `app.New()`, `w.SetContent(img)`, `w.ShowAndRun()`), but pulls in
the full widget/theme runtime, GLFW and cgo on every platform, and the
per-refresh free/re-upload plus CPU scaling default are easy to get wrong.

## 2. Gio

### Frame-render path

- `paint.NewImageOp(src)`: "NewImageOp assumes the backing image is
  immutable, and may cache a copy of its contents in a GPU-friendly way.
  Create new ImageOps to ensure that changes to an image is reflected in the
  display of it." https://pkg.go.dev/gioui.org/op/paint#NewImageOp
- Source type switch: `*image.Uniform` -> colour op; `*image.RGBA` -> used
  directly with a unique handle; anything else -> "Copy the image into a GPU
  friendly format" (`draw.Draw` into a new RGBA). So decode/convert into an
  `*image.RGBA` and there is no extra CPU copy.
  https://github.com/gioui/gio/blob/main/op/paint/paint.go
- GPU side: textures cached by `(image handle, filter)`; upload happens once
  per handle (`if tex.tex != nil { return tex.tex }`), released when the
  cache entry ages out. One upload per new frame, nothing per redraw.
  https://github.com/gioui/gio/blob/main/gpu/gpu.go
- Per frame: build new `paint.NewImageOp(frame)`, `PaintOp{}`, call
  `w.Invalidate()` from the decode goroutine; render in the `app.FrameEvent`
  handler. Architecture doc: "image.NRGBA and image.Uniform images are
  efficient and treated specially" (doc lags source, which fast-paths RGBA).
  https://gioui.org/doc/architecture/drawing

### Realistic fps / CPU

- Upstream ticket "Multimedia support" (#125): an ffmpeg PoC player renders
  frames converted to RGBA via libswscale through ImageOp; "The CPU usage is
  a little high but it does look working"; asks for native YCbCr ImageOp.
  A patch "Adding native YUV support for paint.ImageOp" was posted on the
  patches list; current `paint.go` has no YCbCr case, so it is not merged.
  (sourcehut returned HTTP 502 during this research; summaries from search
  index and the GitHub mirror of paint.go.)
  https://todo.sr.ht/~eliasnaur/gio/125
  https://lists.sr.ht/~eliasnaur/gio-patches/patches/11389
- Practical consequence: cost per frame = one RGBA texture upload
  (640x480x4 = 1.2 MB; 2 streams @ 30 fps = ~74 MB/s, trivial for any GPU
  path) plus whatever the decoder spends producing RGBA. No CPU scaling; the
  GPU samples the texture at the display size.

### cgo

- Windows: "no special compiler is needed, as we don't use CGo for Windows
  support." https://gioui.org/doc/install
- Confirmed in source: `app/os_windows.go` imports `golang.org/x/sys/windows`,
  no `import "C"`. https://github.com/gioui/gio/blob/main/app/os_windows.go
- Linux: requires cgo. Docs: `apt install gcc pkg-config libwayland-dev
  libx11-dev libx11-xcb-dev libxkbcommon-x11-dev libgles2-mesa-dev
  libegl1-mesa-dev libffi-dev libxcursor-dev libvulkan-dev`; build tags
  `nox11` / `nowayland` drop one backend. https://gioui.org/doc/install/linux
- Source: `app/os_x11.go` has `#cgo linux pkg-config: x11 xkbcommon
  xkbcommon-x11 x11-xcb xcursor xfixes` and `import "C"`; `app/os_wayland.go`
  has `#cgo linux pkg-config: wayland-client wayland-cursor`.
  https://github.com/gioui/gio/blob/main/app/os_x11.go
  https://github.com/gioui/gio/blob/main/app/os_wayland.go

### Cross-compile Linux -> Windows

- Docs: "Cross-compilation is most easily achieved from Linux".
  https://gioui.org/doc/install ; Windows page: "The default windows setup
  does not require extra dependencies", use `-H windowsgui` to hide console.
  https://gioui.org/doc/install/windows
- **Measured locally (2026-09-21, go1.27.1 linux/amd64, gioui.org v0.10.2,
  no mingw/zig installed):** a minimal window drawing an `*image.RGBA` via
  `paint.NewImageOp` built with `GOOS=windows GOARCH=amd64 CGO_ENABLED=0
  go build -ldflags="-s -w -H windowsgui"` -> **6,571,520 bytes**;
  unstripped -> 9,417,728 bytes. The same program failed to build natively
  on this WSL box for lack of `vulkan/vulkan.h` and `x11-xcb.pc`, confirming
  the Linux dev-header requirement.

### Binary size

6.6 MB stripped Windows exe (measured above). No external runtime.

### Maturity

- v0.10.0 (May 2026) and v0.10.2 (Aug 2026) releases; maintained by Elias
  Naur and Chris Waldon with sponsors; Windows IME/window-placement fixes in
  the latest release. https://gioui.org/news/2026-08 ,
  https://gioui.org/news/2026-05 , tags v0.10.2/v0.10.1 on
  https://github.com/gioui/gio
- Still 0.x: v0.10.0 notes "no breaking API signature changes" but changed
  lifecycle events on some platforms, so minor-version bumps can need code
  changes. Widget set (gioui.org/widget, gio-x) is thinner than Fyne's;
  chat UI (list, editor, buttons) is covered but more hand-assembly.

### As a standalone CLI-launched video window

Best fit of the three: one goroutine, `app.Window` + `FrameEvent` loop,
~40 lines, pure Go on Windows, single static exe, no widget runtime needed.

## 3. Wails

### Frame-render path

- No native surface; the UI is a WebView (WebView2 on Windows, WebKitGTK on
  Linux). Raw frames must cross the Go -> JS bridge.
- Bound-method path base64-encodes binary; issue #2890 reports ~300 ms
  image generation + ~800 ms transport, proposes WebSocket/WebRTC or
  `<canvas>` byte arrays instead. https://github.com/wailsapp/wails/issues/2890
- v3 event path used `eval` per event and "in cases with large data transfers
  or high-frequency events, this often leads to out-of-memory (OOM) errors";
  fixed for beta.3 by switching to a pull model (issue #4587 / PR #5930).
  https://github.com/wailsapp/wails/issues/4587
- Realistic design therefore: run a local HTTP/WebSocket server (MJPEG or raw
  frames -> `<canvas>`), or terminate WebRTC in the page and hand frames to a
  `<video>` element. Either duplicates work or moves the media path out of Go.
- Linux WebKitGTK video quirks documented upstream (no `ended` event, needs
  `gst-plugins-good`, signal-handler clash with Go panics).
  https://github.com/wailsapp/wails/blob/master/website/docs/guides/linux.mdx

### cgo / platform deps

- v2 install: Linux needs "standard gcc build tools plus libgtk3 and
  libwebkit" (`libwebkit2gtk-4.1-dev` + `-tags webkit2_41` on newer distros);
  Windows needs WebView2 runtime + NPM.
  https://github.com/wailsapp/wails/blob/master/website/docs/gettingstarted/installation.mdx
- v3 install: Go 1.25+; "Wails v3 requires WebKitGTK 6.0 by default"
  (GTK4, Ubuntu 24.04+/Debian 13+); GTK3 + WebKit2GTK 4.1 via `-tags gtk3`
  "supported through the v3.0.x line and will be removed in v3.1"; WebKit2GTK
  4.0 distros (Ubuntu 20.04, Debian 11, RHEL 8) "are not supported".
  https://github.com/wailsapp/wails/blob/releases/v3-beta/docs/src/content/docs/quick-start/installation.mdx
- v3 cross-platform doc: "Windows is the simplest cross-compilation target
  because it doesn't require CGO by default"; "Linux builds require CGO for
  WebView integration"; matrix row Linux host -> Windows = "Native Go",
  -> macOS = Docker (Zig image).
  https://github.com/wailsapp/wails/blob/releases/v3-beta/docs/src/content/docs/guides/build/cross-platform.mdx
- v2 Linux -> Windows: `wails build -platform windows/amd64` works from
  Ubuntu with Wails v2.11 per a third-party write-up (secondary source);
  earlier regression tracked in #1921.
  https://chriswheeler.dev/posts/cross-compilation-with-wails/
  https://github.com/wailsapp/wails/issues/1921

### Binary size

Go binary plus embedded frontend assets; WebView2 runtime is external on
Windows (bundled with Win10/11, otherwise a download), WebKitGTK external on
Linux. Upstream does not publish a hello-world size; not measured here.

### Maturity

- v2.14.0 stable 2026-08-10; v3 "Current Status: Beta" with beta.22/23/24
  released 2026-09-14/16/20 (GitHub releases API); v3 GA tracker #5844 listed
  38 open gates / 27 blocking as of Aug 2026.
  https://github.com/wailsapp/wails/blob/releases/v3-beta/docs/src/content/docs/status.mdx
  https://github.com/wailsapp/wails/issues/5844
  https://github.com/wailsapp/wails/releases

### As a standalone CLI-launched video window

Poor: each window is a WebView process with a JS frontend, requires WebView2
/ WebKitGTK at run time, and frames still have to be tunnelled to the page.

## Recommendation

Use **Gio** for both the chat window and the CLI's detached video window.

- Only toolkit that is cgo-free on Windows, so `GOOS=windows CGO_ENABLED=0
  go build` from Linux just works (verified, 6.6 MB exe).
- Cheapest frame path: `*image.RGBA` -> `paint.NewImageOp` -> one texture
  upload; no CPU rescale; GPU samples at display size. Two 30 fps SD/HD
  streams are far below the pixel rates upstream users already push.
- Smallest thing to spawn from the CLI: no widget runtime, no WebView, no
  external runtime DLLs.

Costs to accept:
- Linux builds need gcc + X11/Wayland/EGL/Vulkan dev headers (same class of
  requirement as Fyne; Wails is worse). Use `nox11`/`nowayland` tags if one
  backend is enough.
- Immediate-mode API and thinner widget set; chat UI needs more assembly
  than Fyne's `widget.List`/`Entry`.
- 0.x versioning; pin the module version.
- Frames must be RGBA; if the decoder yields YCbCr, convert on CPU
  (`draw.Draw` into RGBA) or use a YUV->RGB shader later; upstream YUV
  ImageOp is unmerged.

Fallback if Gio's widget set proves too thin for the chat UI: Fyne with
`canvas.Image` + `ImageScaleFastest`, accepting cgo everywhere and
fyne-cross/mingw for Windows builds. Wails is not recommended for the video
surfaces.
