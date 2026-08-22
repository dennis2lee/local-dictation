package ui

import "testing"

// The colours are the point: green, amber, red mean the same thing here as on
// every mixer, so the level is legible without reading a number.
func TestTheScaleGoesGreenAmberRed(t *testing.T) {
	for _, want := range []struct {
		fraction float64
		colour   string
	}{
		{0.0, "green"}, {0.4, "green"}, {0.59, "green"},
		{0.60, "amber"}, {0.8, "amber"}, {0.84, "amber"},
		{0.85, "red"}, {1.0, "red"},
	} {
		got := meterColourAt(want.fraction)
		if name := colourName(got); name != want.colour {
			t.Errorf("at %.2f of full scale the segment is %s, want %s",
				want.fraction, name, want.colour)
		}
	}
}

// A segment's colour comes from where it sits on the scale, not from the
// current level. Otherwise the whole row changes hue as someone speaks, which
// is exactly the thing a meter is not supposed to do.
func TestSegmentColoursDoNotDependOnTheLevel(t *testing.T) {
	meter := newLevelMeter()

	meter.Set(1.0)
	loud := make([]string, len(meter.segments))
	for index, segment := range meter.segments {
		loud[index] = colourName(segment.FillColor)
	}

	meter.Set(0.2)
	for index, segment := range meter.segments {
		if colourName(segment.FillColor) == "unlit" {
			continue // beyond the level, correctly dark
		}
		if got := colourName(segment.FillColor); got != loud[index] {
			t.Errorf("segment %d is %s when quiet and %s when loud", index, got, loud[index])
		}
	}
}

func TestTheMeterFillsFromTheLeft(t *testing.T) {
	meter := newLevelMeter()

	meter.Set(0.5)

	lit := 0
	for _, segment := range meter.segments {
		if colourName(segment.FillColor) != "unlit" {
			lit++
		}
	}
	if lit != meterSegments/2 {
		t.Errorf("%d of %d segments lit at half scale", lit, meterSegments)
	}

	meter.Set(0)
	for index, segment := range meter.segments {
		if colourName(segment.FillColor) != "unlit" {
			t.Errorf("segment %d still lit at silence", index)
		}
	}
}

// Driven off the audio callback, so a level that has not moved a whole segment
// must not repaint twenty rectangles.
func TestAnUnchangedLevelDoesNotRepaint(t *testing.T) {
	meter := newLevelMeter()
	meter.Set(0.5)
	before := meter.lit

	meter.Set(0.502) // same segment count
	if meter.lit != before {
		t.Errorf("lit went from %d to %d for a level that did not move a segment",
			before, meter.lit)
	}
}

func TestLevelsOutsideTheScaleAreClamped(t *testing.T) {
	meter := newLevelMeter()
	meter.Set(-1)
	if meter.lit != 0 {
		t.Errorf("a negative level lit %d segments", meter.lit)
	}
	meter.Set(9)
	if meter.lit != meterSegments {
		t.Errorf("an over-scale level lit %d of %d", meter.lit, meterSegments)
	}
}

func colourName(c interface{}) string {
	switch c {
	case planGreen:
		return "green"
	case planAmber:
		return "amber"
	case planRed:
		return "red"
	case meterUnlit:
		return "unlit"
	}
	return "?"
}
