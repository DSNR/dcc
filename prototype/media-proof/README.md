# PROTOTYPE — pure-Go media proof (dcc issue #16)

Throwaway harness. Answers: does the pure-Go media stack chosen in #12 / ADR 0002
hold up on real hardware? Not production code; nothing here is meant to be reused
except the numbers and the lessons in the issue's resolution comment.

## Build

```sh
cd prototype/media-proof
go build -tags novulkan,nox11 -o mediaproof .            # Linux (Wayland; drop nox11 if libxkbcommon-x11-dev + libx11-xcb-dev are installed)
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o mediaproof.exe .   # Windows, cross-compiled, no cgo
```

## Run (each prints PASS/FAIL against the ticket's thresholds)

| command | what it measures | needs a human? |
|---|---|---|
| `mediaproof vp8` | govpx 640x480@30 encode ms/frame (3 configs), decode, I420→RGBA | no |
| `mediaproof opus` | pion/opus CELT 20 ms round trip: SNR, lag, ms/packet | no |
| `mediaproof screen [-dur 10s]` | kbinani/screenshot display 0 at 10 fps + downscale to 1280 wide, % of one core | no |
| `mediaproof audio [-dur 12s]` | mic→speaker loopback (pulse / WASAPI). Prints our queue latency; plays a click every 2 s and times it in the mic (acoustic round trip) | yes: listen, speak |
| `mediaproof gio [-dur 15s]` | decode VP8 in a goroutine, paint RGBA in a Gio window at 30 fps; counts presented/skipped | yes: watch the window |
| `mediaproof browser [-packetizer govpx\|pion] [-screen] [-play]` | pion/webrtc → Chrome: govpx VP8 + pion/opus out, Chrome mic (libopus) in, decoded by pion/opus. Open http://127.0.0.1:8080 | yes: look/listen; or headless Chrome with fake devices |

Headless Chrome for the browser test (no human needed):

```sh
google-chrome --headless=new --use-fake-device-for-media-stream --use-fake-ui-for-media-stream \
  --autoplay-policy=no-user-gesture-required --user-data-dir=/tmp/chrome-proof http://127.0.0.1:8080
```

Results and verdict live on the GitHub issue.
