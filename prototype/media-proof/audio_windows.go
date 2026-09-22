package main

import (
	"fmt"
	"runtime"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/moutend/go-wca/pkg/wca"
)

const audioBackend = "moutend/go-wca (WASAPI shared, auto-convert PCM)"

func pcmFormat(rate int) *wca.WAVEFORMATEX {
	return &wca.WAVEFORMATEX{WFormatTag: 1, NChannels: 1, NSamplesPerSec: uint32(rate),
		NAvgBytesPerSec: uint32(rate * 2), NBlockAlign: 2, WBitsPerSample: 16}
}

func openClient(flow uint32, rate int) (*wca.IAudioClient, func(), error) {
	if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
		return nil, nil, err
	}
	var mmde *wca.IMMDeviceEnumerator
	if err := wca.CoCreateInstance(wca.CLSID_MMDeviceEnumerator, 0, wca.CLSCTX_ALL, wca.IID_IMMDeviceEnumerator, &mmde); err != nil {
		return nil, nil, err
	}
	var mmd *wca.IMMDevice
	if err := mmde.GetDefaultAudioEndpoint(flow, wca.EConsole, &mmd); err != nil {
		return nil, nil, err
	}
	var ps *wca.IPropertyStore
	if err := mmd.OpenPropertyStore(wca.STGM_READ, &ps); err == nil {
		var pv wca.PROPVARIANT
		if ps.GetValue(&wca.PKEY_Device_FriendlyName, &pv) == nil {
			fmt.Println("  device:", pv.String())
		}
		ps.Release()
	}
	var ac *wca.IAudioClient
	if err := mmd.Activate(wca.IID_IAudioClient, wca.CLSCTX_ALL, nil, &ac); err != nil {
		return nil, nil, err
	}
	flags := uint32(wca.AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM | wca.AUDCLNT_STREAMFLAGS_SRC_DEFAULT_QUALITY)
	if err := ac.Initialize(wca.AUDCLNT_SHAREMODE_SHARED, flags, 200000 /*20ms*/, 0, pcmFormat(rate), nil); err != nil {
		return nil, nil, fmt.Errorf("Initialize: %w", err)
	}
	return ac, func() { ac.Release(); mmd.Release(); mmde.Release(); ole.CoUninitialize() }, nil
}

func startCapture(rate int, cb func([]int16)) (func(), error) {
	errc := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		ac, release, err := openClient(wca.ECapture, rate)
		if err != nil {
			errc <- err
			return
		}
		defer release()
		var acc *wca.IAudioCaptureClient
		if err := ac.GetService(wca.IID_IAudioCaptureClient, &acc); err != nil {
			errc <- err
			return
		}
		defer acc.Release()
		if err := ac.Start(); err != nil {
			errc <- err
			return
		}
		errc <- nil
		var data *byte
		var frames, flags uint32
		var dp, qp uint64
		for {
			select {
			case <-done:
				ac.Stop()
				return
			default:
			}
			if err := acc.GetBuffer(&data, &frames, &flags, &dp, &qp); err != nil || frames == 0 {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			s := unsafe.Slice((*int16)(unsafe.Pointer(data)), int(frames))
			cb(s)
			acc.ReleaseBuffer(frames)
		}
	}()
	if err := <-errc; err != nil {
		return nil, err
	}
	return func() { close(done) }, nil
}

func startPlayback(rate int, fill func([]int16) int) (func(), error) {
	errc := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		ac, release, err := openClient(wca.ERender, rate)
		if err != nil {
			errc <- err
			return
		}
		defer release()
		var bufFrames uint32
		ac.GetBufferSize(&bufFrames)
		var arc *wca.IAudioRenderClient
		if err := ac.GetService(wca.IID_IAudioRenderClient, &arc); err != nil {
			errc <- err
			return
		}
		defer arc.Release()
		if err := ac.Start(); err != nil {
			errc <- err
			return
		}
		errc <- nil
		var data *byte
		var padding uint32
		for {
			select {
			case <-done:
				ac.Stop()
				return
			default:
			}
			if ac.GetCurrentPadding(&padding) != nil {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			avail := bufFrames - padding
			if avail == 0 {
				time.Sleep(2 * time.Millisecond)
				continue
			}
			if err := arc.GetBuffer(avail, &data); err != nil {
				time.Sleep(2 * time.Millisecond)
				continue
			}
			s := unsafe.Slice((*int16)(unsafe.Pointer(data)), int(avail))
			fill(s)
			arc.ReleaseBuffer(avail, 0)
		}
	}()
	if err := <-errc; err != nil {
		return nil, err
	}
	return func() { close(done) }, nil
}
