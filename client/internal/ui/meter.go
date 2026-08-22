package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
)

// A level meter, in the shape every audio tool uses: a row of segments that
// fill from the left, green through amber to red.
//
// A single filled bar answers "is the microphone working". This answers the
// question actually being asked, which is "am I at a good level" — and the
// answer is legible without reading a number, because the colours mean the
// same thing here as on every mixer anyone has ever used. Green is speaking
// level, amber is loud, red is clipping and the decoder will hear distortion.
//
// Segments rather than a smooth gradient: a discrete row is much easier to
// judge at a glance than a continuous fill, and it does not shimmer when the
// level sits between two values.

const (
	meterSegments = 20
	// Where the colours change, as a fraction of full scale. Peak level from a
	// microphone at comfortable speaking distance sits around a third; above
	// four fifths, 16-bit samples are close enough to the rail that the
	// waveform starts to flatten.
	meterAmberFrom = 0.60
	meterRedFrom   = 0.85
)

type levelMeter struct {
	segments []*canvas.Rectangle
	object   fyne.CanvasObject
	lit      int
}

func newLevelMeter() *levelMeter {
	meter := &levelMeter{segments: make([]*canvas.Rectangle, meterSegments)}
	row := container.NewGridWithColumns(meterSegments)
	for index := range meter.segments {
		segment := canvas.NewRectangle(meterUnlit)
		segment.CornerRadius = 1
		segment.SetMinSize(fyne.NewSize(6, 14))
		meter.segments[index] = segment
		row.Add(segment)
	}
	meter.object = row
	meter.lit = -1 // so the first Set always paints
	return meter
}

func (m *levelMeter) Object() fyne.CanvasObject { return m.object }

// Set lights the segments up to level, which is 0..1 peak amplitude.
//
// Only repaints when the number of lit segments changes. The meter is driven
// at video rate off the audio callback, and refreshing twenty rectangles on
// every sample block is a lot of work to make a picture that looks identical.
func (m *levelMeter) Set(level float64) {
	lit := int(clamp01(level)*meterSegments + 0.5)
	if lit == m.lit {
		return
	}
	m.lit = lit
	for index, segment := range m.segments {
		colour := meterUnlit
		if index < lit {
			colour = meterColourAt(float64(index) / float64(meterSegments-1))
		}
		if segment.FillColor != colour {
			segment.FillColor = colour
			segment.Refresh()
		}
	}
}

// meterColourAt is the colour of the segment at a given fraction of full
// scale — the position on the scale, not the current level, so a segment does
// not change colour as the level moves past it.
func meterColourAt(fraction float64) color.NRGBA {
	switch {
	case fraction >= meterRedFrom:
		return planRed
	case fraction >= meterAmberFrom:
		return planAmber
	default:
		return planGreen
	}
}

func clamp01(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
