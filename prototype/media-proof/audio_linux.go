package main

import (
	"github.com/jfreymuth/pulse"
)

const audioBackend = "jfreymuth/pulse"

func startCapture(rate int, cb func([]int16)) (func(), error) {
	c, err := pulse.NewClient(pulse.ClientApplicationName("dcc-mediaproof"))
	if err != nil {
		return nil, err
	}
	st, err := c.NewRecord(pulse.Int16Writer(func(s []int16) (int, error) { cb(s); return len(s), nil }),
		pulse.RecordSampleRate(rate), pulse.RecordMono, pulse.RecordLatency(0.02))
	if err != nil {
		c.Close()
		return nil, err
	}
	st.Start()
	return func() { st.Stop(); st.Close(); c.Close() }, nil
}

func startPlayback(rate int, fill func([]int16) int) (func(), error) {
	c, err := pulse.NewClient(pulse.ClientApplicationName("dcc-mediaproof"))
	if err != nil {
		return nil, err
	}
	st, err := c.NewPlayback(pulse.Int16Reader(func(s []int16) (int, error) { return fill(s), nil }),
		pulse.PlaybackSampleRate(rate), pulse.PlaybackMono, pulse.PlaybackLatency(0.02))
	if err != nil {
		c.Close()
		return nil, err
	}
	st.Start()
	return func() { st.Stop(); st.Close(); c.Close() }, nil
}
