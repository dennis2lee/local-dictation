package audio

import (
	"encoding/binary"
	"sync/atomic"
)

// Gain multiplies captured audio before anything else sees it.
//
// Some microphones — laptop arrays with aggressive noise suppression in front
// of them, most USB headsets at their default mixer position — arrive quiet
// enough that Whisper hears a whisper. The decoder has no automatic gain, so
// quiet in is quiet out, and the transcript pays for it.
//
// This is a plain multiplier, not a compressor or an AGC. That is the point:
// it is predictable, the meter shows exactly what it did, and there is nothing
// in it that can pump or breathe in the middle of a sentence. What it cannot
// do is rescue a signal that was already clipping, which is why the meter has
// a red end and this has a ceiling.
const (
	// MinGain halves. Below that, a signal is better fixed at the mixer.
	MinGain = 0.5
	// MaxGain is +18 dB or so. Past this, hiss comes up with the voice and
	// what arrives is a louder bad recording.
	MaxGain = 8.0
)

// gainScale holds the multiplier as fixed point in an atomic, because it is
// read from the audio callback on every frame and written from the UI. A
// mutex there would be a lock on the audio thread; a float64 is not atomic on
// every architecture Go supports, so it travels as an integer.
const gainScale = 1024

type gain struct{ fixed atomic.Int64 }

func newGain() *gain {
	g := &gain{}
	g.set(1)
	return g
}

func (g *gain) set(multiplier float64) {
	g.fixed.Store(int64(ClampGain(multiplier) * gainScale))
}

func (g *gain) get() float64 { return float64(g.fixed.Load()) / gainScale }

// apply scales one frame of signed 16-bit little-endian PCM in place.
//
// Runs on the audio callback: no allocation, no locks, one pass. Samples that
// would leave the 16-bit range are pinned to it rather than wrapping, since a
// wrap turns a loud vowel into a burst of noise that Whisper transcribes as
// something.
func (g *gain) apply(pcm []byte) {
	fixed := g.fixed.Load()
	if fixed == gainScale { // unity: the common case, and free
		return
	}
	for index := 0; index+1 < len(pcm); index += 2 {
		sample := int64(int16(binary.LittleEndian.Uint16(pcm[index:]))) * fixed / gainScale
		switch {
		case sample > 32767:
			sample = 32767
		case sample < -32768:
			sample = -32768
		}
		binary.LittleEndian.PutUint16(pcm[index:], uint16(int16(sample)))
	}
}

// ClampGain holds a multiplier inside the range that is worth offering.
func ClampGain(multiplier float64) float64 {
	switch {
	case multiplier < MinGain:
		return MinGain
	case multiplier > MaxGain:
		return MaxGain
	default:
		return multiplier
	}
}
