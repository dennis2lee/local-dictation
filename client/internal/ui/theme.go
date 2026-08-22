package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// The palette.
//
// It began as the project plan's light mockup and is now a dark one: deep
// indigo, cyan, and indicator colours bright enough to read at a glance
// against it. That is a deliberate departure from the plan, chosen from three
// candidates rendered through Fyne rather than drawn in something Fyne cannot
// reproduce.
//
// One look, whichever variant the desktop asks for. A light theme is not a
// recolouring of these values — the accent and every indicator would need
// choosing again against a white ground — and half-doing it would look worse
// than not doing it.
var (
	planBackground   = color.NRGBA{0x0D, 0x0E, 0x1A, 0xFF} // the window
	planPanel        = color.NRGBA{0x15, 0x17, 0x2B, 0xFF} // the status block
	planForeground   = color.NRGBA{0xE6, 0xE9, 0xFF, 0xFF} // headings and body
	planMuted        = color.NRGBA{0x7B, 0x82, 0xB8, 0xFF} // captions, inactive tabs
	planPanelBorder  = color.NRGBA{0x2A, 0x2F, 0x55, 0xFF} // around the status panel
	planSeparator    = color.NRGBA{0x22, 0x26, 0x44, 0xFF} // the hairlines
	planInputBorder  = color.NRGBA{0x35, 0x3B, 0x66, 0xFF}
	planAccent       = color.NRGBA{0x00, 0xE5, 0xFF, 0xFF} // primary button, active tab
	planAccentText   = color.NRGBA{0x06, 0x08, 0x14, 0xFF} // on top of the accent
	planDisabledText = color.NRGBA{0x4A, 0x50, 0x7A, 0xFF}

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
	planGray  = color.NRGBA{0x50, 0x57, 0x85, 0xFF} // not checked / stopped
	planAmber = color.NRGBA{0xFF, 0xC1, 0x07, 0xFF} // checking / connecting / finalizing
	planGreen = color.NRGBA{0x2B, 0xF5, 0x83, 0xFF} // connected / listening
	planRed   = color.NRGBA{0xFF, 0x2D, 0x8A, 0xFF} // failed / error

	// The unlit segment of the level meter: present enough to show the scale,
	// quiet enough that an idle microphone does not draw the eye.
	meterUnlit = color.NRGBA{0x23, 0x27, 0x45, 0xFF}
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
	case theme.ColorNameDisabled, theme.ColorNameDisabledButton:
		return planDisabledText
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
