// Package audio captures microphone input as the PCM the protocol requires.
//
// The capture device is asked for 16 kHz mono signed 16-bit samples directly,
// so nothing here resamples or converts. If a device cannot do that the OS
// audio layer converts on our behalf, which is both faster and more correct
// than anything this package would do by hand.
package audio

import (
	"errors"
	"math"
	"sync"

	"github.com/dennis2lee/local-dictation/client/internal/protocol"
)

// ErrNoDevice means the configured microphone is gone — unplugged, or claimed
// by another application.
var ErrNoDevice = errors.New("microphone not available")

// Device is one capture device offered to the user.
type Device struct {
	// ID is the platform identifier stored in settings. Empty means "system
	// default", which is deliberately a real, selectable entry: a user who
	// switches headsets wants the default to follow them.
	ID   string
	Name string
	// Default marks the device the OS would pick.
	Default bool
}

// SystemDefault is the entry representing "whatever the OS is using".
func SystemDefault() Device {
	return Device{ID: "", Name: "Default system microphone", Default: true}
}

// LevelMeter accumulates input level for the Settings tab's microphone test.
//
// It is written from the capture callback and read from the UI thread, so every
// field is behind the mutex. The callback runs on an audio thread: it must not
// allocate, block or take a slow lock, which is why this does nothing but a few
// arithmetic operations.
type LevelMeter struct {
	mu    sync.Mutex
	peak  float64
	rms   float64
	decay float64
}

// NewLevelMeter returns a meter whose displayed level falls back smoothly, so a
// bar driven by it does not flicker between frames.
func NewLevelMeter() *LevelMeter { return &LevelMeter{decay: 0.85} }

// Push feeds one frame of PCM.
func (m *LevelMeter) Push(pcm []byte) {
	peak, rms := analyse(pcm)
	m.mu.Lock()
	if peak > m.peak {
		m.peak = peak
	} else {
		m.peak *= m.decay
	}
	m.rms = m.rms*m.decay + rms*(1-m.decay)
	m.mu.Unlock()
}

// Level reports peak and smoothed RMS, both 0..1.
func (m *LevelMeter) Level() (peak, rms float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.peak, m.rms
}

// Reset clears the meter between tests.
func (m *LevelMeter) Reset() {
	m.mu.Lock()
	m.peak, m.rms = 0, 0
	m.mu.Unlock()
}

func analyse(pcm []byte) (peak, rms float64) {
	count := len(pcm) / 2
	if count == 0 {
		return 0, 0
	}
	var total float64
	for i := 0; i+1 < len(pcm); i += 2 {
		sample := float64(int16(uint16(pcm[i])|uint16(pcm[i+1])<<8)) / 32768.0
		magnitude := math.Abs(sample)
		if magnitude > peak {
			peak = magnitude
		}
		total += sample * sample
	}
	return peak, math.Sqrt(total / float64(count))
}

// FrameSize is how much audio one callback delivers. 20 ms keeps latency low
// without paying WebSocket framing overhead on every syllable.
const FrameMilliseconds = 20

// FrameBytes is FrameMilliseconds expressed in bytes of PCM.
func FrameBytes() int { return protocol.FrameBytes(FrameMilliseconds) }
