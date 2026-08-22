//go:build !cgo

package audio

import "errors"

// Capture without cgo cannot reach any audio backend. This build exists so the
// pure-Go packages can be vetted and tested in a container without miniaudio's
// toolchain; the shipped binaries are always built with cgo enabled.
type Capture struct {
	meter *LevelMeter
	gain  *gain
}

var errNoCGO = errors.New("this build has no audio support (built without cgo)")

func NewCapture() (*Capture, error) {
	return &Capture{meter: NewLevelMeter(), gain: newGain()}, nil
}

func (c *Capture) Meter() *LevelMeter               { return c.meter }
func (c *Capture) SetGain(multiplier float64)       { c.gain.set(multiplier) }
func (c *Capture) Gain() float64                    { return c.gain.get() }
func (c *Capture) Devices() ([]Device, error)       { return []Device{SystemDefault()}, errNoCGO }
func (c *Capture) Start(string, func([]byte)) error { return errNoCGO }
func (c *Capture) Stop() error                      { return nil }
func (c *Capture) Close() error                     { return nil }
