//go:build !linux && !windows

package media

// System has no driver here. A Call on such a platform still connects and
// still carries the other side's audio and video nowhere: silent and blind,
// not broken.
func System() Devices { return noDevices{} }

// noDevices refuses both ends of the boundary with ErrNoDevices.
type noDevices struct{}

// Capture implements Devices.
func (noDevices) Capture() (Source, error) { return nil, ErrNoDevices }

// Playback implements Devices.
func (noDevices) Playback() (Sink, error) { return nil, ErrNoDevices }

// Camera implements Devices.
func (noDevices) Camera() (Camera, error) { return nil, ErrNoCamera }
