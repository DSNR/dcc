package media

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/moutend/go-wca/pkg/wca"
)

// The shape of the WASAPI streams dcc opens. Shared mode, so a Call never
// takes the sound card away from anything else, and WASAPI's own converter,
// so the pipeline can ask for 8 kHz mono whatever the device's mix format
// actually is.
const (
	// wasapiFlags asks the audio engine to resample and downmix for us,
	// which is what makes SampleRate a promise this driver can keep.
	wasapiFlags = wca.AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM | wca.AUDCLNT_STREAMFLAGS_SRC_DEFAULT_QUALITY
	// wasapiBuffer is the endpoint buffer, in REFERENCE_TIME's 100 ns
	// units: 100 ms, enough that a scheduling hiccup is not a dropout.
	wasapiBuffer = 100 * 10_000
	// sFalse and rpcChangedMode are CoInitializeEx saying the thread was
	// already in an apartment — the one it asked for, or another one. Either
	// way there is an apartment, which is all this driver needs.
	sFalse         = 0x00000001
	rpcChangedMode = 0x80010106
)

// System on Windows is WASAPI through go-wca — pure Go over COM, so the
// Windows build cross-compiles from Linux like everything else.
func System() Devices { return wasapiDevices{} }

// wasapiDevices opens the default communications endpoints.
type wasapiDevices struct{}

// Capture opens the default microphone.
func (wasapiDevices) Capture() (Source, error) {
	s, err := openStream(wca.ECapture)
	if err != nil {
		return nil, fmt.Errorf("media: opening the microphone: %w", err)
	}
	return s, nil
}

// Playback opens the default speaker.
func (wasapiDevices) Playback() (Sink, error) {
	s, err := openStream(wca.ERender)
	if err != nil {
		return nil, fmt.Errorf("media: opening the speaker: %w", err)
	}
	return s, nil
}

// openStream starts one endpoint's goroutine and waits for it to report
// whether the device opened. Capture and render differ only in which
// endpoint they ask for and which way the audio then flows.
func openStream(dataFlow uint32) (*wasapiStream, error) {
	s := &wasapiStream{
		buf:   newRing(SampleRate * bufferSeconds),
		ready: make(chan error, 1),
		done:  make(chan struct{}),
	}
	go s.run(dataFlow)
	if err := <-s.ready; err != nil {
		return nil, err
	}
	return s, nil
}

// wasapiStream is one endpoint, capture or render. Every COM call on it
// happens on the one goroutine run owns, locked to its OS thread, because
// that is the apartment the interfaces were created in; the ring is the only
// thing the pipeline and that goroutine share.
type wasapiStream struct {
	buf   *ring
	ready chan error
	done  chan struct{}
	once  sync.Once
}

// Read implements Source.
func (s *wasapiStream) Read(pcm []int16) error {
	if !s.buf.read(pcm) {
		return errors.New("media: the microphone is closed")
	}
	return nil
}

// Write implements Sink.
func (s *wasapiStream) Write(pcm []int16) error {
	s.buf.write(pcm)
	return nil
}

// Close implements both, stopping the COM goroutine and releasing the
// endpoint. Closing twice is fine.
func (s *wasapiStream) Close() error {
	s.once.Do(func() {
		close(s.done)
		s.buf.close()
	})
	return nil
}

// run owns the endpoint for its whole life: it reports on ready whether the
// device opened, then pumps until Close. Everything COM touches stays here.
func (s *wasapiStream) run(dataFlow uint32) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// A COINIT_APARTMENTTHREADED apartment, and an "already initialised"
	// answer is not a failure — some other part of the process got here
	// first on this thread.
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		var oleErr *ole.OleError
		if !errors.As(err, &oleErr) || (oleErr.Code() != sFalse && oleErr.Code() != rpcChangedMode) {
			s.ready <- err
			return
		}
	}
	defer ole.CoUninitialize()

	client, service, err := openEndpoint(dataFlow)
	if err != nil {
		s.ready <- err
		return
	}
	defer client.Release()
	defer service.Release()
	defer func() { _ = client.Stop() }()

	if err := client.Start(); err != nil {
		s.ready <- err
		return
	}
	s.ready <- nil

	if dataFlow == wca.ECapture {
		s.pumpCapture((*wca.IAudioCaptureClient)(unsafe.Pointer(service)))
		return
	}
	s.pumpRender(client, (*wca.IAudioRenderClient)(unsafe.Pointer(service)))
}

// openEndpoint activates the default endpoint for dataFlow and initialises
// it in dcc's format, returning the audio client and the capture or render
// service hanging off it.
func openEndpoint(dataFlow uint32) (*wca.IAudioClient, *ole.IUnknown, error) {
	var enumerator *wca.IMMDeviceEnumerator
	if err := wca.CoCreateInstance(wca.CLSID_MMDeviceEnumerator, 0, wca.CLSCTX_ALL, wca.IID_IMMDeviceEnumerator, &enumerator); err != nil {
		return nil, nil, err
	}
	defer enumerator.Release()

	var device *wca.IMMDevice
	// ECommunications is the endpoint Windows reserves for voice — the
	// headset, not the speakers someone is playing music through.
	if err := enumerator.GetDefaultAudioEndpoint(dataFlow, wca.ECommunications, &device); err != nil {
		return nil, nil, err
	}
	defer device.Release()

	var client *wca.IAudioClient
	if err := device.Activate(wca.IID_IAudioClient, wca.CLSCTX_ALL, nil, &client); err != nil {
		return nil, nil, err
	}

	format := &wca.WAVEFORMATEX{
		WFormatTag:      wca.WAVE_FORMAT_PCM,
		NChannels:       1,
		NSamplesPerSec:  SampleRate,
		WBitsPerSample:  16,
		NBlockAlign:     2,
		NAvgBytesPerSec: SampleRate * 2,
	}
	if err := client.Initialize(wca.AUDCLNT_SHAREMODE_SHARED, wasapiFlags, wasapiBuffer, 0, format, nil); err != nil {
		client.Release()
		return nil, nil, err
	}

	iid := wca.IID_IAudioCaptureClient
	if dataFlow == wca.ERender {
		iid = wca.IID_IAudioRenderClient
	}
	var service *ole.IUnknown
	if err := client.GetService(iid, &service); err != nil {
		client.Release()
		return nil, nil, err
	}
	return client, service, nil
}

// pumpCapture moves what the microphone has recorded into the ring. It polls
// rather than waiting on an event handle: at half a frame the poll costs
// nothing measurable and there is one less OS handle to get wrong.
func (s *wasapiStream) pumpCapture(capture *wca.IAudioCaptureClient) {
	for {
		select {
		case <-s.done:
			return
		default:
		}

		var frames uint32
		if err := capture.GetNextPacketSize(&frames); err != nil {
			return
		}
		if frames == 0 {
			if s.idle() {
				return
			}
			continue
		}
		var data *byte
		var flags uint32
		if err := capture.GetBuffer(&data, &frames, &flags, nil, nil); err != nil {
			return
		}
		pcm := make([]int16, frames)
		// A silent packet carries no buffer worth reading; WASAPI says so
		// rather than writing the zeros out.
		if flags&wca.AUDCLNT_BUFFERFLAGS_SILENT == 0 && data != nil {
			copy(pcm, unsafe.Slice((*int16)(unsafe.Pointer(data)), frames))
		}
		s.buf.write(pcm)
		if err := capture.ReleaseBuffer(frames); err != nil {
			return
		}
	}
}

// pumpRender moves the ring into whatever room the speaker has, padding with
// silence when the Call has nothing to say.
func (s *wasapiStream) pumpRender(client *wca.IAudioClient, render *wca.IAudioRenderClient) {
	var size uint32
	if err := client.GetBufferSize(&size); err != nil {
		return
	}
	for {
		select {
		case <-s.done:
			return
		default:
		}

		var padding uint32
		if err := client.GetCurrentPadding(&padding); err != nil {
			return
		}
		frames := size - padding
		if frames == 0 {
			if s.idle() {
				return
			}
			continue
		}
		var data *byte
		if err := render.GetBuffer(frames, &data); err != nil {
			return
		}
		if data != nil {
			s.buf.fill(unsafe.Slice((*int16)(unsafe.Pointer(data)), frames))
		}
		if err := render.ReleaseBuffer(frames, 0); err != nil {
			return
		}
	}
}

// idle waits out half a frame, reporting true if Close came first.
func (s *wasapiStream) idle() bool {
	select {
	case <-s.done:
		return true
	case <-time.After(FrameDuration / 2):
		return false
	}
}
