package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// The palette.
//
// It began as the project plan's light mockup and is now a dark one: indigo
// ground, a soft-blue accent, and four indicator colours. That is a deliberate
// departure from the plan, chosen from candidates rendered through Fyne rather
// than drawn in something Fyne cannot reproduce.
//
// The organising rule is that saturation is reserved. Nothing in the window is
// at full chroma except the four states, so the state is the only colour on
// screen and does not have to shout to be found. The accent used to be neon
// cyan at 12.5:1 — on the active tab, the radio, the focus ring and the Save
// button at once — and everything competed with the one dot that carried an
// answer.
//
// Two values were also simply unreadable and are not any more: the border
// around every text field measured 1.80:1 against the window, where a control
// boundary wants 3:1, and disabled text measured 2.48:1, which is not greyed
// out so much as gone. They are 3.06:1 and 4.56:1 now.
//
// What this palette does not fix is that four states cannot be told apart by
// hue alone: under deuteranopia the connecting and listening colours converge,
// and the failed one lands near the idle one. Searching only the hue bands
// that still mean stopped, connecting, live and failed, the best separation
// any four colours reach is 1.43:1 — so no palette fixes it. A mark inside the
// dot would; that is a separate change from this one.
//
// One look, whichever variant the desktop asks for. A light theme is not a
// recolouring of these values — the accent and every indicator would need
// choosing again against a white ground — and half-doing it would look worse
// than not doing it.
var (
	planBackground   = color.NRGBA{0x10, 0x12, 0x23, 0xFF} // the window
	planPanel        = color.NRGBA{0x19, 0x1C, 0x31, 0xFF} // the status block
	planForeground   = color.NRGBA{0xE4, 0xE7, 0xF5, 0xFF} // headings and body
	planMuted        = color.NRGBA{0x91, 0x99, 0xC2, 0xFF} // captions, inactive tabs
	planPanelBorder  = color.NRGBA{0x33, 0x3A, 0x63, 0xFF} // around the status panel
	planSeparator    = color.NRGBA{0x28, 0x2D, 0x4B, 0xFF} // the hairlines
	planInputBorder  = color.NRGBA{0x55, 0x5F, 0x96, 0xFF}
	planAccent       = color.NRGBA{0x7D, 0xD3, 0xFC, 0xFF} // primary button, active tab
	planAccentText   = color.NRGBA{0x08, 0x13, 0x1C, 0xFF} // on top of the accent
	planDisabledText = color.NRGBA{0x73, 0x7C, 0xA6, 0xFF}
	// The fill of a disabled button, which is a different job from the colour
	// of disabled text and has to stay quiet. Raising both together turned the
	// greyed-out Save button into a pale block louder than the live one.
	planDisabledFill = color.NRGBA{0x26, 0x2B, 0x45, 0xFF}

	// Hover and pressed are overlays rather than colours, and their alpha is
	// the whole point. Fyne blends these over whatever a control already is, so
	// an opaque value replaces it outright — which is what turned the Save
	// button pale the moment a pointer crossed it, as if it had been disabled.
	//
	// White rather than black, because they now sit on a dark ground: the way
	// to show a surface reacting is to lift it, not to sink it further.
	planHover   = color.NRGBA{0xFF, 0xFF, 0xFF, 0x14}
	planPressed = color.NRGBA{0xFF, 0xFF, 0xFF, 0x3D}

	// The LED colours, and the meaning the plan attaches to each.
	//
	// Green is deliberately not the accent, and not near it. The accent is
	// cyan, it is all over the window, and a "connected" light the same colour
	// reads as decoration rather than as an answer — the one thing this dot
	// exists to be.
	planGray  = color.NRGBA{0x58, 0x61, 0x8A, 0xFF} // not checked / stopped
	planAmber = color.NRGBA{0xD3, 0xA0, 0x4A, 0xFF} // checking / connecting / finalizing
	planGreen = color.NRGBA{0x6F, 0xD9, 0xA2, 0xFF} // connected / listening
	planRed   = color.NRGBA{0xF2, 0x74, 0x8C, 0xFF} // failed / error

	// The unlit segment of the level meter: present enough to show the scale,
	// quiet enough that an idle microphone does not draw the eye.
	meterUnlit = color.NRGBA{0x25, 0x2A, 0x44, 0xFF}
)

// planTheme dresses the standard widgets in the plan's palette. Fyne draws the
// controls; what is chosen here is colour, radius, spacing and type size.
type planTheme struct{}

var _ fyne.Theme = planTheme{}

func (planTheme) Color(name fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	switch name {
	case theme.ColorNameBackground:
		return planBackground
	case theme.ColorNameForeground, theme.ColorNameHeaderBackground:
		if name == theme.ColorNameHeaderBackground {
			return planBackground
		}
		return planForeground
	case theme.ColorNameForegroundOnPrimary:
		return planAccentText
	case theme.ColorNamePrimary, theme.ColorNameFocus, theme.ColorNameSelection:
		return planAccent
	case theme.ColorNameButton, theme.ColorNameInputBackground:
		return planBackground
	case theme.ColorNameOverlayBackground, theme.ColorNameMenuBackground:
		return planBackground
	case theme.ColorNameInputBorder:
		return planInputBorder
	case theme.ColorNameSeparator:
		return planSeparator
	case theme.ColorNamePlaceHolder, theme.ColorNameScrollBar:
		return planMuted
	case theme.ColorNameDisabled:
		return planDisabledText
	case theme.ColorNameDisabledButton:
		return planDisabledFill
	case theme.ColorNameHover:
		return planHover
	case theme.ColorNamePressed:
		return planPressed
	case theme.ColorNameSuccess:
		return planGreen
	case theme.ColorNameWarning:
		return planAmber
	case theme.ColorNameError:
		return planRed
	case theme.ColorNameShadow:
		return color.NRGBA{0x00, 0x00, 0x00, 0x60}
	default:
		return theme.DefaultTheme().Color(name, theme.VariantDark)
	}
}

func (planTheme) Font(style fyne.TextStyle) fyne.Resource {
	return theme.DefaultTheme().Font(style)
}

func (planTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(name)
}

func (planTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNameText:
		return 13
	case theme.SizeNameHeadingText:
		return 17 // "Dictation stopped"
	case theme.SizeNameSubHeadingText:
		return 12 // "Servers", "Microphone"
	case theme.SizeNameCaptionText:
		return 11 // field labels
	case theme.SizeNameInputRadius, theme.SizeNameSelectionRadius:
		return 7
	case theme.SizeNameInputBorder:
		return 1
	case theme.SizeNameSeparatorThickness:
		return 1
	case theme.SizeNamePadding:
		return 5
	case theme.SizeNameInnerPadding:
		return 9
	case theme.SizeNameScrollBar:
		return 10
	default:
		return theme.DefaultTheme().Size(name)
	}
}
