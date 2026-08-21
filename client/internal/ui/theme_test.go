package ui

import (
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"

	"github.com/dennis2lee/local-dictation/client/internal/session"
)

func rgba(t *testing.T, c color.Color) (uint8, uint8, uint8, uint8) {
	t.Helper()
	n, ok := color.NRGBAModel.Convert(c).(color.NRGBA)
	if !ok {
		t.Fatalf("colour %v is not convertible", c)
	}
	return n.R, n.G, n.B, n.A
}

func assertColor(t *testing.T, name string, got color.Color, want color.NRGBA) {
	t.Helper()
	r, g, b, a := rgba(t, got)
	if r != want.R || g != want.G || b != want.B || a != want.A {
		t.Errorf("%s is #%02X%02X%02X (alpha %d), want #%02X%02X%02X (alpha %d)",
			name, r, g, b, a, want.R, want.G, want.B, want.A)
	}
}

// The plan's mockup is the reference. These are the values read off it, so a
// change to the palette has to be a deliberate one.
func TestTheThemeUsesThePlansPalette(t *testing.T) {
	applied := planTheme{}
	for _, want := range []struct {
		name  fyne.ThemeColorName
		label string
		value color.NRGBA
	}{
		{theme.ColorNameBackground, "background", planBackground},
		{theme.ColorNameForeground, "foreground", planForeground},
		{theme.ColorNamePrimary, "primary", planAccent},
		{theme.ColorNameForegroundOnPrimary, "text on primary", planAccentText},
		{theme.ColorNameInputBorder, "input border", planInputBorder},
		{theme.ColorNameSeparator, "separator", planSeparator},
		{theme.ColorNameSuccess, "success", planGreen},
		{theme.ColorNameWarning, "warning", planAmber},
		{theme.ColorNameError, "error", planRed},
	} {
		assertColor(t, want.label, applied.Color(want.name, theme.VariantLight), want.value)
	}
}

// The plan specifies one look. Returning the desktop's dark palette for half
// the colours and the plan's for the rest is the one outcome worth ruling out.
func TestTheThemeIsTheSameInEitherVariant(t *testing.T) {
	applied := planTheme{}
	for _, name := range []fyne.ThemeColorName{
		theme.ColorNameBackground, theme.ColorNameForeground,
		theme.ColorNamePrimary, theme.ColorNameInputBorder,
		theme.ColorNameSeparator, theme.ColorNameButton,
	} {
		light := applied.Color(name, theme.VariantLight)
		dark := applied.Color(name, theme.VariantDark)
		lr, lg, lb, la := rgba(t, light)
		dr, dg, db, da := rgba(t, dark)
		if lr != dr || lg != dg || lb != db || la != da {
			t.Errorf("%s differs between variants: light %v, dark %v", name, light, dark)
		}
	}
}

func TestTheThemeUsesThePlansTypeScale(t *testing.T) {
	applied := planTheme{}
	for _, want := range []struct {
		name  fyne.ThemeSizeName
		label string
		value float32
	}{
		{theme.SizeNameHeadingText, "status headline", 17},
		{theme.SizeNameSubHeadingText, "section heading", 12},
		{theme.SizeNameCaptionText, "field label", 11},
		{theme.SizeNameInputRadius, "input corner radius", 7},
		{theme.SizeNameSeparatorThickness, "hairline", 1},
	} {
		if got := applied.Size(want.name); got != want.value {
			t.Errorf("%s is %v, want %v", want.label, got, want.value)
		}
	}
}

// Gray, amber, green, red — and the meanings the plan attaches to them. The
// dot beside a status line is the only thing on screen that says at a glance
// whether anything is wrong.
func TestEveryLEDStateHasThePlansColour(t *testing.T) {
	for _, want := range []struct {
		state session.LED
		label string
		value color.NRGBA
	}{
		{session.Gray, "not checked / stopped", planGray},
		{session.Amber, "checking / connecting / finalizing", planAmber},
		{session.Green, "connected / listening", planGreen},
		{session.Red, "failed / error", planRed},
	} {
		got, ok := ledColours[want.state]
		if !ok {
			t.Errorf("no colour for %q (%s)", want.state, want.label)
			continue
		}
		assertColor(t, string(want.state), got, want.value)
	}
}

// An unknown state must not render an invisible dot.
func TestAnUnknownLEDStateFallsBackToGray(t *testing.T) {
	indicator := newLED("nothing yet")
	indicator.Set(session.LED("chartreuse"), "unknown")
	assertColor(t, "fallback dot", indicator.dot.FillColor, planGray)
}

func TestTheWindowTitleCarriesTheVersion(t *testing.T) {
	if got := windowTitle("0.1.10"); got != "Local Dictation 0.1.10" {
		t.Errorf("title is %q", got)
	}
	if got := windowTitle(""); got != "Local Dictation" {
		t.Errorf("title without a version is %q, want no trailing space", got)
	}
}
