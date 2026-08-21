package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// The palette and metrics are the project plan's UI mockup, read off the
// stylesheet in docs/local-dictation-project-plan.html rather than
// approximated: a white card, hairline rules between sections, a slate-blue
// accent, and a light panel behind the status line.
//
// The plan specifies one look, so this returns it whichever variant the desktop
// asks for. A dark theme is not a recolouring of these values — the LED colours
// and the accent would each need choosing again against a dark ground — and
// half-doing it would look worse than not doing it.
var (
	planBackground   = color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF} // the window card
	planPanel        = color.NRGBA{0xF7, 0xF9, 0xFB, 0xFF} // the status block
	planForeground   = color.NRGBA{0x18, 0x20, 0x2A, 0xFF} // headings and body
	planMuted        = color.NRGBA{0x70, 0x7B, 0x87, 0xFF} // captions, inactive tabs
	planPanelBorder  = color.NRGBA{0xE0, 0xE6, 0xEB, 0xFF} // around the status panel
	planSeparator    = color.NRGBA{0xE5, 0xE9, 0xEE, 0xFF} // the hairlines
	planInputBorder  = color.NRGBA{0xCC, 0xD4, 0xDC, 0xFF}
	planAccent       = color.NRGBA{0x39, 0x78, 0xD7, 0xFF} // primary button, active tab
	planAccentText   = color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
	planHover        = color.NRGBA{0xEE, 0xF3, 0xF9, 0xFF}
	planDisabledText = color.NRGBA{0xA6, 0xAF, 0xB9, 0xFF}

	// The LED colours, and the meaning the plan attaches to each.
	planGray  = color.NRGBA{0x8D, 0x97, 0xA1, 0xFF} // not checked / stopped
	planAmber = color.NRGBA{0xE0, 0x9B, 0x2B, 0xFF} // checking / connecting / finalizing
	planGreen = color.NRGBA{0x20, 0xA6, 0x67, 0xFF} // connected / listening
	planRed   = color.NRGBA{0xD1, 0x44, 0x3C, 0xFF} // failed / error
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
	case theme.ColorNameHover, theme.ColorNamePressed:
		return planHover
	case theme.ColorNameSuccess:
		return planGreen
	case theme.ColorNameWarning:
		return planAmber
	case theme.ColorNameError:
		return planRed
	case theme.ColorNameShadow:
		return color.NRGBA{0x18, 0x20, 0x2A, 0x1A}
	default:
		return theme.DefaultTheme().Color(name, theme.VariantLight)
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
