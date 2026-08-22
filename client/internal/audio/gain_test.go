package audio

import (
	"encoding/binary"
	"math"
	"testing"
)

func pcm(samples ...int16) []byte {
	out := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(out[index*2:], uint16(sample))
	}
	return out
}

func samples(raw []byte) []int16 {
	out := make([]int16, len(raw)/2)
	for index := range out {
		out[index] = int16(binary.LittleEndian.Uint16(raw[index*2:]))
	}
	return out
}

func TestGainScalesTheSignal(t *testing.T) {
	g := newGain()
	g.set(2)

	frame := pcm(100, -100, 1000, -1000, 0)
	g.apply(frame)

	want := []int16{200, -200, 2000, -2000, 0}
	for index, got := range samples(frame) {
		if got != want[index] {
			t.Errorf("sample %d = %d, want %d", index, got, want[index])
		}
	}
}

// A wrap turns a loud vowel into a burst of noise, and Whisper transcribes
// noise as words. Pinning to the rail is merely distorted; wrapping is
// gibberish.
func TestLoudSamplesArePinnedNotWrapped(t *testing.T) {
	g := newGain()
	g.set(8)

	frame := pcm(30000, -30000, 5000)
	g.apply(frame)

	got := samples(frame)
	if got[0] != 32767 {
		t.Errorf("a loud positive sample became %d, want 32767", got[0])
	}
	if got[1] != -32768 {
		t.Errorf("a loud negative sample became %d, want -32768", got[1])
	}
	if got[2] != 32767 {
		t.Errorf("5000 x 8 = 40000 became %d, want it pinned", got[2])
	}
}

// Unity has to be free: it runs on the audio callback for every frame of every
// session, and the overwhelming majority of installs never touch the slider.
func TestUnityGainLeavesTheFrameUntouched(t *testing.T) {
	g := newGain()

	original := pcm(1, -1, 12345, -12345, 32767, -32768)
	frame := append([]byte(nil), original...)
	g.apply(frame)

	for index := range frame {
		if frame[index] != original[index] {
			t.Fatalf("unity gain changed byte %d", index)
		}
	}
}

func TestGainIsHeldInsideAUsefulRange(t *testing.T) {
	for _, want := range []struct{ given, expected float64 }{
		{0, MinGain}, {0.1, MinGain}, {MinGain, MinGain},
		{1, 1}, {3.5, 3.5},
		{MaxGain, MaxGain}, {40, MaxGain}, {math.Inf(1), MaxGain},
	} {
		if got := ClampGain(want.given); got != want.expected {
			t.Errorf("ClampGain(%v) = %v, want %v", want.given, got, want.expected)
		}
	}
}

// An odd trailing byte is not a whole sample. Reading past it would be a
// panic on the audio thread, which takes the process with it.
func TestARaggedFrameIsNotReadPastTheEnd(t *testing.T) {
	g := newGain()
	g.set(2)

	frame := []byte{0x10, 0x27, 0x05} // one sample and a stray byte
	g.apply(frame)

	if frame[2] != 0x05 {
		t.Errorf("the odd byte was modified")
	}
	if got := int16(binary.LittleEndian.Uint16(frame)); got != 20000 {
		t.Errorf("whole sample = %d, want 20000", got)
	}
}

// Set from the UI, read from the audio callback, on every frame.
func TestGainCanBeChangedWhileCapturing(t *testing.T) {
	g := newGain()
	if got := g.get(); got != 1 {
		t.Errorf("a new gain is %v, want unity", got)
	}
	g.set(4)
	if got := g.get(); got != 4 {
		t.Errorf("gain = %v after setting 4", got)
	}
}
