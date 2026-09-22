package main

import (
	"flag"
	"fmt"
	"math"
	"sync"
	"time"
)

// Platform files provide these. Both callbacks run on the backend's thread.
//   startCapture: cb receives 48 kHz mono int16 chunks as they arrive.
//   startPlayback: fill is asked for 48 kHz mono int16 chunks; return count.
// (see audio_linux.go / audio_windows.go)

type chunk struct {
	at time.Time
	s  []int16
}

func runAudio(args []string) {
	fs := flag.NewFlagSet("audio", flag.ExitOnError)
	dur := fs.Duration("dur", 12*time.Second, "run time")
	impulse := fs.Bool("impulse", true, "play periodic click and detect it in capture (acoustic round trip)")
	fs.Parse(args)

	q := make(chan chunk, 64)
	var mu sync.Mutex
	queueAge := &stats{}
	var pending []int16

	// impulse detector state
	var lastClick time.Time
	var clickArmed bool
	var rtts []time.Duration
	var peak float64
	var capSamples int

	stopCap, err := startCapture(opusRate, func(s []int16) {
		capSamples += len(s)
		var e float64
		for _, v := range s {
			f := math.Abs(float64(v)) / 32768
			if f > e {
				e = f
			}
		}
		if e > peak {
			peak = e
		}
		mu.Lock()
		if clickArmed && e > 0.25 && time.Since(lastClick) > 15*time.Millisecond {
			rtts = append(rtts, time.Since(lastClick))
			clickArmed = false
		}
		mu.Unlock()
		select {
		case q <- chunk{time.Now(), append([]int16(nil), s...)}:
		default:
		}
	})
	if err != nil {
		verdict("audio capture open", false, err.Error())
		return
	}
	defer stopCap()

	clickLen := opusRate * 5 / 1000
	var clickPos = clickLen
	start := time.Now()
	stopPlay, err := startPlayback(opusRate, func(out []int16) int {
		n := 0
		for n < len(out) {
			if len(pending) == 0 {
				select {
				case c := <-q:
					queueAge.add(time.Since(c.at))
					pending = c.s
				default:
					for ; n < len(out); n++ {
						out[n] = 0
					}
					break
				}
				continue
			}
			k := copy(out[n:], pending)
			pending = pending[k:]
			n += k
		}
		if *impulse {
			mu.Lock()
			if time.Since(lastClick) > 2*time.Second && time.Since(start) > time.Second {
				lastClick, clickArmed, clickPos = time.Now(), true, 0
			}
			mu.Unlock()
			for i := range out {
				if clickPos < clickLen {
					out[i] = int16(0.9 * 32767 * math.Sin(2*math.Pi*1000*float64(clickPos)/opusRate))
					clickPos++
				}
			}
		}
		return len(out)
	})
	if err != nil {
		verdict("audio playback open", false, err.Error())
		return
	}
	defer stopPlay()

	fmt.Printf("mic -> speaker loopback for %s (%s). Speak; you should hear yourself.\n", *dur, audioBackend)
	for time.Since(start) < *dur {
		time.Sleep(time.Second)
		mu.Lock()
		fmt.Printf("  t=%2.0fs captured %6.1fs peak %.2f  queue-age %s  clicks heard %d\n",
			time.Since(start).Seconds(), float64(capSamples)/opusRate, peak, queueAge, len(rtts))
		peak = 0
		mu.Unlock()
	}
	fmt.Printf("\nbuffer latency added by our queue: %s\n", queueAge)
	if len(rtts) > 0 {
		var sum time.Duration
		for _, r := range rtts {
			sum += r
		}
		avg := sum / time.Duration(len(rtts))
		fmt.Printf("acoustic round trip (speaker->mic, includes device + air): avg %.0f ms over %d clicks\n", ms(avg), len(rtts))
		verdict("audio loopback < 100 ms", avg < 100*time.Millisecond, fmt.Sprintf("%.0f ms round trip", ms(avg)))
	} else {
		fmt.Println("no clicks detected in capture (headphones? mic muted?) — judge by ear + queue-age")
		verdict("audio loopback", capSamples > 0, fmt.Sprintf("captured %.1fs, no acoustic measurement", float64(capSamples)/opusRate))
	}
}
