# Media capture and encode stack in Go (Windows + Linux, pion/webrtc)

Resolves GitHub issue #4. Researched 2026-09-21 against primary sources (repo source, go.mod, build tags, godoc). Context: dcc is pure-Go-preferred, cross-compiles Windows from Linux.

## TL;DR

- `pion/webrtc` itself is pure Go (no cgo in its module graph); transport is never the cgo problem. [go.mod](https://github.com/pion/webrtc/blob/master/go.mod)
- `pion/mediadevices` is the only integrated capture+encode stack, but **every codec package it ships is cgo**, its Windows camera driver is cgo (DirectShow C++), its Linux camera driver has a cgo header include, its microphone driver is cgo (malgo), and its Linux screen driver is cgo (Xlib/XShm). Only its non-Linux screen driver (via `kbinani/screenshot`) is cgo-free. [README](https://github.com/pion/mediadevices/blob/master/README.md)
- Pure-Go capture is possible on both platforms for **screen** (kbinani/screenshot) and **mic** (Windows: moutend/go-wca; Linux: jfreymuth/pulse), and for **Linux camera** (blackjack/webcam, V4L2). **Windows camera has no pure-Go library**; it is the single hard gap.
- Pure-Go encoders now exist: **Opus** (`pion/opus` encoder, landed Aug-Sep 2026, untagged) and **VP8/VP9** (`thesyncim/govpx`, byte-parity with libvpx, May 2026, 2 stars, requires Go 1.26). Both very new; nothing mature.
- Pure-Go decode: `golang.org/x/image/vp8` is keyframe-only (explicitly errors on inter frames), useless for video. `govpx` has full VP8/VP9 decoders; `pion/opus` decoder is complete (SILK+CELT+hybrid).
- Cross-compiling cgo to Windows from Linux works with `zig cc -target x86_64-windows-gnu` (Go >= 1.18 / CL 291630) or mingw-w64 gcc; realistic for plain C deps (libopus/openh264 static libs are bundled for windows-x64 in mediadevices), harder for the DirectShow C++ camera driver.

## Matrix: platform x source x codec x cgo

### Capture sources

| Platform | Source | Library | cgo? | Notes / evidence |
|---|---|---|---|---|
| Linux | Camera | `pion/mediadevices/pkg/driver/camera` | **yes** (header-only) | `camera_linux.go` has `// #include <linux/videodev2.h>` + `import "C"` for V4L2 pixfmt constants. [src](https://github.com/pion/mediadevices/blob/master/pkg/driver/camera/camera_linux.go) |
| Linux | Camera | `blackjack/webcam` (what mediadevices wraps) | **no** | Pure Go V4L2 via ioctl; go.mod depends only on `golang.org/x/sys`; no `import "C"`. [repo](https://github.com/blackjack/webcam) |
| Windows | Camera | `pion/mediadevices/pkg/driver/camera` | **yes** (C++) | `camera_windows.go`: `#cgo LDFLAGS: -lstrmiids -lole32 -loleaut32 -lquartz`, `#include <dshow.h>`, with `camera_windows.cpp/.hpp`. [src](https://github.com/pion/mediadevices/blob/master/pkg/driver/camera/camera_windows.go) |
| Windows | Camera | (pure Go) | n/a | **None found.** Would require hand-rolling Media Foundation/DirectShow COM via `go-ole`/`syscall` (no maintained library located). |
| Linux | Microphone | `pion/mediadevices/pkg/driver/microphone` -> `gen2brain/malgo` | **yes** | malgo = miniaudio bindings; `#cgo linux,!android LDFLAGS: -ldl -lpthread -lm`. README: "Requires cgo but does not require linking to anything on the Windows/macOS and it links only -ldl on Linux/BSDs." [malgo](https://github.com/gen2brain/malgo) / [miniaudio.go](https://github.com/gen2brain/malgo/blob/master/miniaudio.go) |
| Windows | Microphone | same (malgo -> WASAPI/DirectSound/WinMM) | **yes** | As above. mediadevices offers `-tags nomicrophone` to drop malgo for "cross-compilation where CGO dependencies like malgo are unavailable". [microphone.go build tag](https://github.com/pion/mediadevices/blob/master/pkg/driver/microphone/microphone.go) |
| Windows | Microphone | `moutend/go-wca` | **no** | "Pure golang bindings for Windows Core Audio API. The cgo is not required." Capture examples exist (`CaptureSharedEventDriven`, `CaptureSharedTimerDriven`). Depends on `go-ole`. 132 stars, last push 2024-07. [repo](https://github.com/moutend/go-wca) |
| Linux | Microphone | `jfreymuth/pulse` | **no** | "PulseAudio client implementation in pure Go ... without any CGO"; has `demo/record`. Works on PipeWire via pulse compat. 104 stars, push 2026-08. [repo](https://github.com/jfreymuth/pulse) |
| Linux | Microphone | `yobert/alsa` | **no** | Pure Go ALSA ioctl; author warns limited hardware compat, x86_64-centric. Fallback only. [repo](https://github.com/yobert/alsa) |
| Linux | Screen | `pion/mediadevices/pkg/driver/screen` (linux files) | **yes** | `x11capture_linux.go`: `#cgo pkg-config: x11 xext`, Xlib + XShm. X11 only, no Wayland. [src](https://github.com/pion/mediadevices/blob/master/pkg/driver/screen/x11capture_linux.go) |
| Linux | Screen | `kbinani/screenshot` | **no** | X11 via pure-Go `jezek/xgb` + `gen2brain/shm`; Wayland via xdg-desktop-portal over `godbus/dbus` (`nix_wayland.go`). README: "cgo free except for GOOS=darwin". [repo](https://github.com/kbinani/screenshot) / [nix_xwindow.go](https://github.com/kbinani/screenshot/blob/main/nix_xwindow.go) / [nix_wayland.go](https://github.com/kbinani/screenshot/blob/main/nix_wayland.go) |
| Windows | Screen | `pion/mediadevices/pkg/driver/screen` (`screen.go`, `+build !linux`) -> `kbinani/screenshot` | **no** | GDI BitBlt via `lxn/win` + `syscall.LoadLibrary("user32.dll")`. [screen.go](https://github.com/pion/mediadevices/blob/master/pkg/driver/screen/screen.go) / [windows.go](https://github.com/kbinani/screenshot/blob/main/windows.go) |

Note: kbinani/screenshot is single-frame `CaptureRect`; mediadevices polls it on a ticker. Fine for screen-share at modest fps; no DXGI desktop duplication (would need cgo or DIY COM).

### Encoders

| Codec | Library | cgo? | Windows-from-Linux | Notes / evidence |
|---|---|---|---|---|
| VP8/VP9 | `mediadevices/pkg/codec/vpx` | **yes** | needs a Windows libvpx build + pkg-config | `#cgo pkg-config: vpx`. Also ships a cgo decoder (`vpx_decoder.go`). [src](https://github.com/pion/mediadevices/blob/master/pkg/codec/vpx/vpx.go) |
| H.264 | `mediadevices/pkg/codec/x264` | **yes** | needs Windows libx264 | `#cgo pkg-config: x264`. GPL. [src](https://github.com/pion/mediadevices/blob/master/pkg/codec/x264/x264.go) |
| H.264 | `mediadevices/pkg/codec/openh264` | **yes** | **easiest cgo path**: static lib bundled | `openh264_static.go` (`+build !dynamic`) links `lib/libopenh264-windows-x64.a -lssp`; also linux-x64/arm64/armv7, darwin. No system lib needed. [src](https://github.com/pion/mediadevices/blob/master/pkg/codec/openh264/openh264_static.go) / [lib/](https://github.com/pion/mediadevices/tree/master/pkg/codec/openh264/lib) |
| AV1 | `mediadevices/pkg/codec/svtav1` | **yes** | needs libsvtav1 | README. |
| H.264 (HW) | `mediadevices/pkg/codec/vaapi`, `mmal` | **yes** | Linux-only / RPi-only | README. |
| Opus | `mediadevices/pkg/codec/opus` | **yes** | static lib bundled (`libopus-windows-x64.a`) | `opus_static.go` (`+build !dynamic`); `-tags dynamic` switches to pkg-config. [src](https://github.com/pion/mediadevices/blob/master/pkg/codec/opus/opus_static.go) / [lib/](https://github.com/pion/mediadevices/tree/master/pkg/codec/opus/lib) |
| Opus | `hraban/opus` | **yes** | README: "needs libs on Windows"; cross-compilation listed as non-feature | [repo](https://github.com/hraban/opus) |
| Opus | `pion/opus` | **no** | plain `go build` | "Pure Go implementation of the Opus Codec". Decoder (SILK+CELT+hybrid, PLC) is the mature part; `encoder.go` first landed 2026-08-31 (18 commits since), **after** the only tag v0.1.0 (2026-06-08). Encoder emits CELT-only fullband 20 ms (`Encode`) or SILK-only NB/MB/WB 20/40/60 ms (`EncodeSILK`); no hybrid mode. Roadmap issue #9 still lists SILK/CELT encoder unchecked. 560 stars, active daily. [repo](https://github.com/pion/opus) / [encoder.go](https://github.com/pion/opus/blob/master/encoder.go) / [issue 9](https://github.com/pion/opus/issues/9) / [releases](https://github.com/pion/opus/releases) |
| VP8/VP9 | `thesyncim/govpx` | **no** | plain `go build`; `-tags purego` drops asm | "production-grade VP8 and VP9 Profile 0 encoder + decoder written entirely in Go, validated byte-for-byte against libvpx ... no cgo". Realtime CBR, frame dropping, temporal SVC, RFC 7741/9628 RTP packetizers, SDP helpers. Claims 720p30 VP8 encode 7.3 ms/frame vs libvpx 5.4 ms (M4 Max). **Requires Go 1.26.** Created 2026-05-06, 2 stars, single author, BSD-3. [repo](https://github.com/thesyncim/govpx) / [go.mod](https://github.com/thesyncim/govpx/blob/main/go.mod) |
| VP8 | `opd-ai/vp8` | **no** | plain `go build` | Pure-Go VP8 encoder, I+P frames, integer-pel ME only, simple loop filter only. Created 2026-03, 2 stars, MIT. Toy-tier vs govpx. [repo](https://github.com/opd-ai/vp8) |
| VP8 (still) | `KarpelesLab/gowebp`, `skrashevich/go-webp`, etc. | no | n/a | WebP keyframe-only VP8; not usable for video streams. |
| H.264 | (pure Go) | n/a | n/a | **None found.** |

### Decoders / render side

| Codec | Library | cgo? | Notes |
|---|---|---|---|
| VP8 | `golang.org/x/image/vp8` | no | **Keyframe-only.** `decode.go`: `if !d.frameHeader.KeyFrame { return errors.New("vp8: Golden / AltRef frames are not implemented") }`; written for WebP stills. Not a video decoder. [src](https://github.com/golang/image/blob/master/vp8/decode.go) |
| VP8/VP9 | `thesyncim/govpx` | no | Full decoders, error concealment, threading; VP9 passes official conformance corpus per README. [repo](https://github.com/thesyncim/govpx) |
| VP8/VP9 | `mediadevices/pkg/codec/vpx` (`vpx_decoder.go`) | yes | libvpx. |
| Opus | `pion/opus` | no | Complete decoder incl. hybrid + PLC; "Match ordinary Opus decoding to libopus exactly" (2026-09-21). [commits](https://github.com/pion/opus/commits/master) |
| Opus | `hraban/opus`, mediadevices opus | yes | libopus. |

Audio playback (`ebitengine/oto` etc.) is out of scope for this issue.

## Windows cross-compile from Linux with cgo

- Go disables cgo when cross-compiling unless `CGO_ENABLED=1` **and** `CC`/`CXX` (or `CC_FOR_windows_amd64`) point at a C cross-compiler. [cmd/cgo doc](https://pkg.go.dev/cmd/cgo) / [Go wiki](https://go.dev/wiki/WindowsCrossCompiling)
- Option A, mingw-w64: `apt install gcc-mingw-w64-x86-64`, then `CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC=x86_64-w64-mingw32-gcc CXX=x86_64-w64-mingw32-g++ go build`. Any C library (libvpx, libx264) must itself be cross-built or fetched as a mingw `.a`; pkg-config must be pointed at it (`PKG_CONFIG_PATH`, per mediadevices README).
- Option B, zig: `CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC="zig cc -target x86_64-windows-gnu" CXX="zig c++ -target x86_64-windows-gnu" go build`. Zig bundles mingw-w64 headers/libc so no separate toolchain. The early rt0_go segfault (golang/go#43886 / ziglang/zig#7874) was fixed by CL 291630 (Go 1.18); goreleaser's reference config builds windows/amd64+arm64 this way. [goreleaser example](https://github.com/goreleaser/goreleaser-example-zig-cgo/blob/main/.goreleaser.yaml) / [go#43886](https://github.com/golang/go/issues/43886) / [zig#7874](https://github.com/ziglang/zig/issues/7874)
- Realism per component:
  - `openh264` + `opus` mediadevices packages: **realistic**; prebuilt `*-windows-x64.a` in-tree, no pkg-config. Only x64 (no windows/arm64 libs).
  - `malgo`: realistic; self-contained C (miniaudio.h), no link deps on Windows.
  - `camera_windows.cpp` (DirectShow): C++ + `dshow.h`, `strmiids`, `quartz`. mingw-w64 ships these headers/import libs; zig bundles mingw headers but Windows SDK coverage has had gaps (ziglang/zig#23115 re WinRT). **Untested; medium risk.**
  - `vpx` / `x264`: **painful**; need cross-built libvpx/libx264 + pkg-config `.pc` files for the mingw triple. Avoid.
- Cost even when it works: cgo binaries lose the "plain `go build` on any host" property, need a CI toolchain image, and Windows CI/dev builds need MSYS2/mingw locally.

## Recommendation

1. **Transport/codec plumbing: pion/webrtc + pion/rtp packetizers, pure Go.** Unchanged.
2. **Audio: go pure Go now.** Opus via `pion/opus` (decoder proven; encoder brand-new: pin a commit, test interop vs libopus/browser, keep `mediadevices/pkg/codec/opus` static-lib as cgo fallback behind a build tag). Capture: `jfreymuth/pulse` (Linux) and `moutend/go-wca` (Windows); `malgo` cgo fallback if hardware coverage bites.
3. **Screen share: pure Go now** via `kbinani/screenshot` directly (both OSes, Wayland via portal) + a ticker; skip mediadevices' cgo X11 driver.
4. **Camera: Linux pure Go via `blackjack/webcam`; Windows is the gap.** Options: (a) defer webcam on Windows to post-MVP, (b) accept cgo for the Windows build only (mediadevices DirectShow driver, cross-built with zig/mingw), (c) write a small pure-Go Media Foundation `IMFSourceReader` binding via `go-ole` (COM vtable calls; nontrivial but bounded).
5. **Video codec: VP8 via `thesyncim/govpx` (pure Go)** for both encode and decode; it is the only pure-Go option that is realtime-capable and libvpx-parity-tested, but it is 4 months old with ~no users and needs Go 1.26. Prototype and benchmark on a Windows laptop before committing. If it fails, fallback is `mediadevices/pkg/codec/openh264` (bundled static lib, cgo, cross-compilable) with H.264, the least-painful cgo codec. `x/image/vp8` is not a substitute for decode.
6. Structure the code so capture drivers and codecs sit behind interfaces with `//go:build` tags (`purego` vs `cgo`), so the cgo fallback can be swapped in per-platform without touching the WebRTC layer.

## Sources

- https://github.com/pion/mediadevices (README, go.mod, `pkg/driver/{camera,microphone,screen}`, `pkg/codec/{vpx,x264,openh264,opus}`)
- https://github.com/pion/webrtc/blob/master/go.mod
- https://github.com/kbinani/screenshot
- https://github.com/gen2brain/malgo
- https://github.com/blackjack/webcam
- https://github.com/moutend/go-wca
- https://github.com/jfreymuth/pulse
- https://github.com/yobert/alsa
- https://github.com/pion/opus (README, encoder.go, errors.go, issue #9, releases)
- https://github.com/hraban/opus
- https://github.com/thesyncim/govpx
- https://github.com/opd-ai/vp8
- https://github.com/golang/image/blob/master/vp8/decode.go
- https://pkg.go.dev/cmd/cgo
- https://go.dev/wiki/WindowsCrossCompiling
- https://github.com/goreleaser/goreleaser-example-zig-cgo
- https://github.com/golang/go/issues/43886
- https://github.com/ziglang/zig/issues/7874
- https://github.com/ziglang/zig/issues/23115
