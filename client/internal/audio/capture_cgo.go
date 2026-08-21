//go:build cgo

package audio

import (
	"fmt"
	"sync"

	"github.com/gen2brain/malgo"

	"github.com/dennis2lee/local-dictation/client/internal/protocol"
)

// Capture is a miniaudio-backed microphone.
//
// One context is kept for the process lifetime because enumerating devices
// means initialising the platform audio backend, and doing that on every
// Settings tab repaint is both slow and, on some Windows drivers, flaky.
type Capture struct {
	mu      sync.Mutex
	context *malgo.AllocatedContext
	device  *malgo.Device
	meter   *LevelMeter
	sink    func([]byte)
}

// NewCapture initialises the audio backend.
func NewCapture() (*Capture, error) {
	context, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(string) {})
	if err != nil {
		return nil, fmt.Errorf("initialise the audio backend: %w", err)
	}
	return &Capture{context: context, meter: NewLevelMeter()}, nil
}

// Meter exposes the input level, for the microphone test in Settings.
func (c *Capture) Meter() *LevelMeter { return c.meter }

// Devices lists the capture devices, with the system default first.
func (c *Capture) Devices() ([]Device, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	infos, err := c.context.Devices(malgo.Capture)
	if err != nil {
		return nil, fmt.Errorf("list capture devices: %w", err)
	}

	devices := []Device{SystemDefault()}
	for _, info := range infos {
		name := info.Name()
		if name == "" {
			continue
		}
		devices = append(devices, Device{
			ID:      info.ID.String(),
			Name:    name,
			Default: info.IsDefault != 0,
		})
	}
	return devices, nil
}

// Start opens the device and streams frames to sink until Stop.
//
// sink is called from the audio thread. It must return promptly: the session
// controller's sink only does a non-blocking WebSocket write for exactly this
// reason.
func (c *Capture) Start(deviceID string, sink func([]byte)) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.device != nil {
		return fmt.Errorf("capture is already running")
	}

	config := malgo.DefaultDeviceConfig(malgo.Capture)
	config.Capture.Format = malgo.FormatS16
	config.Capture.Channels = protocol.Channels
	config.SampleRate = protocol.SampleRate
	// miniaudio's period is expressed in frames at the device's sample rate.
	config.PeriodSizeInFrames = uint32(protocol.SampleRate * FrameMilliseconds / 1000)
	config.Alsa.NoMMap = 1

	if deviceID != "" {
		id, err := c.resolveLocked(deviceID)
		if err != nil {
			return err
		}
		config.Capture.DeviceID = id.Pointer()
	}

	c.sink = sink
	callbacks := malgo.DeviceCallbacks{
		Data: func(_, input []byte, frameCount uint32) {
			if len(input) == 0 {
				return
			}
			// Copy: miniaudio reuses this buffer as soon as we return, and the
			// frame is about to cross a goroutine boundary into the socket.
			frame := make([]byte, len(input))
			copy(frame, input)
			c.meter.Push(frame)
			if sink != nil {
				sink(frame)
			}
		},
	}

	device, err := malgo.InitDevice(c.context.Context, config, callbacks)
	if err != nil {
		c.sink = nil
		return fmt.Errorf("%w: %v", ErrNoDevice, err)
	}
	if err := device.Start(); err != nil {
		device.Uninit()
		c.sink = nil
		return fmt.Errorf("start the microphone: %w", err)
	}
	c.device = device
	return nil
}

// Stop ends capture. Safe to call when nothing is running.
func (c *Capture) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopLocked()
}

func (c *Capture) stopLocked() error {
	if c.device == nil {
		return nil
	}
	device := c.device
	c.device, c.sink = nil, nil
	if err := device.Stop(); err != nil {
		device.Uninit()
		return fmt.Errorf("stop the microphone: %w", err)
	}
	device.Uninit()
	c.meter.Reset()
	return nil
}

// Close releases the backend.
func (c *Capture) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.stopLocked()
	if c.context == nil {
		return nil
	}
	context := c.context
	c.context = nil
	_ = context.Uninit()
	context.Free()
	return nil
}

func (c *Capture) resolveLocked(deviceID string) (malgo.DeviceID, error) {
	infos, err := c.context.Devices(malgo.Capture)
	if err != nil {
		return malgo.DeviceID{}, fmt.Errorf("list capture devices: %w", err)
	}
	for _, info := range infos {
		if info.ID.String() == deviceID {
			return info.ID, nil
		}
	}
	// The saved device is gone. Say which one, so the message in the UI names
	// the headset the user unplugged rather than a hex blob.
	return malgo.DeviceID{}, fmt.Errorf("%w: the selected microphone is no longer connected", ErrNoDevice)
}
