package ui

import (
	"image/color"
	"math"
	"testing"
)

// relativeLuminance is WCAG 2.1's definition, on straight sRGB.
func relativeLuminance(c color.NRGBA) float64 {
	channel := func(v uint8) float64 {
		f := float64(v) / 255
		if f <= 0.04045 {
			return f / 12.92
		}
		return math.Pow((f+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(c.R) + 0.7152*channel(c.G) + 0.0722*channel(c.B)
}

func contrast(a, b color.NRGBA) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// Two values in this palette were unreadable for eight releases and nothing
// said so: the border around every text field measured 1.80:1 against the
// window, where WCAG asks a control boundary for 3:1, and disabled text
// measured 2.48:1. Both are the kind of thing that looks deliberate — a quiet
// border, a greyed-out label — right up until someone cannot find the edge of
// a field.
func TestEveryColourThatCarriesMeaningIsReadable(t *testing.T) {
	cases := []struct {
		what       string
		fore, back color.NRGBA
		need       float64
	}{
		// Text, against whichever surface it sits on.
		{"body text on the window", planForeground, planBackground, 4.5},
		{"body text on a panel", planForeground, planPanel, 4.5},
		{"captions on the window", planMuted, planBackground, 4.5},
		{"captions on a panel", planMuted, planPanel, 4.5},
		{"disabled text", planDisabledText, planBackground, 4.5},
		{"the accent as text", planAccent, planBackground, 4.5},
		{"a label on the Save button", planAccentText, planAccent, 4.5},

		// Text that carries a state: the danger and success labels in Settings.
		{"an error message", planRed, planBackground, 4.5},
		{"a success label", planGreen, planBackground, 4.5},

		// Controls and indicators, which WCAG holds to 3:1 rather than 4.5.
		{"a text field's border", planInputBorder, planBackground, 3.0},
		{"the listening indicator", planGreen, planPanel, 3.0},
		{"the connecting indicator", planAmber, planPanel, 3.0},
		{"the failed indicator", planRed, planPanel, 3.0},
	}

	for _, c := range cases {
		if got := contrast(c.fore, c.back); got < c.need {
			t.Errorf("%s: %.2f:1, needs %.1f:1 (%02X%02X%02X on %02X%02X%02X)",
				c.what, got, c.need,
				c.fore.R, c.fore.G, c.fore.B, c.back.R, c.back.G, c.back.B)
		}
	}
}

// The idle dot is deliberately quiet and deliberately exempt: "stopped" is
// also stated in words beside it, and an indicator that shouts about nothing
// happening is worse than one that does not. Asserted rather than left
// unmentioned so the exemption is a decision and not an oversight.
func TestTheIdleIndicatorIsQuietOnPurpose(t *testing.T) {
	got := contrast(planGray, planPanel)
	if got >= 3.0 {
		t.Errorf("the idle dot now measures %.2f:1; if that was deliberate, move it "+
			"into the table above and delete this test", got)
	}
	if got < 1.8 {
		t.Errorf("the idle dot has faded to %.2f:1, which is no longer visible at all", got)
	}
}

// Saturation is the organising rule of this palette: the three states that
// mean something — connecting, listening, failed — are the most chromatic
// things in the window, which is what lets them be found without being loud.
// An accent that creeps back toward neon undoes it.
//
// Measured as CIELAB chroma rather than HSV saturation. HSV calls a near-black
// panel "49% saturated" because it divides by a tiny maximum channel, which
// would make this test assert nonsense about a colour nobody perceives as
// colourful.
func TestNothingCompetesWithTheStateColours(t *testing.T) {
	states := map[string]color.NRGBA{
		"connecting": planAmber,
		"listening":  planGreen,
		"failed":     planRed,
	}
	// planGray is not here on purpose: idle is the absence of a state, and it
	// belongs with the chrome it has to sit quietly among.
	chrome := map[string]color.NRGBA{
		"the accent":     planAccent,
		"captions":       planMuted,
		"field borders":  planInputBorder,
		"the panel":      planPanel,
		"the window":     planBackground,
		"body text":      planForeground,
		"the idle light": planGray,
	}

	quietest, quietestName := math.Inf(1), ""
	for name, c := range states {
		if got := chroma(c); got < quietest {
			quietest, quietestName = got, name
		}
	}
	for name, c := range chrome {
		if got := chroma(c); got >= quietest {
			t.Errorf("%s has chroma %.1f, at or above the %s indicator's %.1f — "+
				"it now competes with the one thing on screen that carries an answer",
				name, got, quietestName, quietest)
		}
	}
}

// chroma is CIELAB C*: how colourful a colour is, independent of how light it
// is. Distance from the neutral axis in Lab, via linear sRGB and D65 XYZ.
func chroma(c color.NRGBA) float64 {
	linear := func(v uint8) float64 {
		f := float64(v) / 255
		if f <= 0.04045 {
			return f / 12.92
		}
		return math.Pow((f+0.055)/1.055, 2.4)
	}
	r, g, b := linear(c.R), linear(c.G), linear(c.B)

	x := (0.4124*r + 0.3576*g + 0.1805*b) / 0.95047
	y := 0.2126*r + 0.7152*g + 0.0722*b
	z := (0.0193*r + 0.1192*g + 0.9505*b) / 1.08883

	f := func(t float64) float64 {
		if t > 0.008856 {
			return math.Cbrt(t)
		}
		return 7.787*t + 16.0/116.0
	}
	return math.Hypot(500*(f(x)-f(y)), 200*(f(y)-f(z)))
}
