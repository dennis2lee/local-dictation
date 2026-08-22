package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// The palette started as the project plan's UI mockup, read off the stylesheet
// in docs/local-dictation-project-plan.html rather than approximated, and has
// since been pulled toward something quieter: darker text, greyer chrome, a
// deeper accent, and indicator colours that state a fact rather than decorate.
// Bright and friendly is the wrong register for a tool that sits open beside
// the thing someone is actually working on.
//
// The plan specifies one look, so this returns it whichever variant the desktop
// asks for. A dark theme is not a recolouring of these values — the LED colours
// and the accent would each need choosing again against a dark ground — and
// half-doing it would look worse than not doing it.
var (
	planBackground   = color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF} // the window card
	planPanel        = color.NRGBA{0xF6, 0xF8, 0xFA, 0xFF} // the status block
	planForeground   = color.NRGBA{0x1F, 0x23, 0x28, 0xFF} // headings and body
	planMuted        = color.NRGBA{0x5B, 0x65, 0x70, 0xFF} // captions, inactive tabs
	planPanelBorder  = color.NRGBA{0xDC, 0xE1, 0xE6, 0xFF} // around the status panel
	planSeparator    = color.NRGBA{0xE4, 0xE8, 0xEC, 0xFF} // the hairlines
	planInputBorder  = color.NRGBA{0xC7, 0xCF, 0xD7, 0xFF}
	planAccent       = color.NRGBA{0x1C, 0x71, 0xD8, 0xFF} // primary button, active tab
	planAccentText   = color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
	planDisabledText = color.NRGBA{0x9A, 0xA3, 0xAD, 0xFF}

	// Hover and pressed are overlays rather than colours, and their alpha is
	// the whole point. Fyne blends these over whatever a control already is, so
	// an opaque value replaces it outright — which is what turned the blue Save
	// button pale the moment a pointer crossed it, as if it had been disabled.
	//
	// Hover is now barely there: enough to tint a menu row, not enough to
	// change a filled button. Pressing is the event worth showing, and the tap
	// animation fades this one out from the centre of the button.
	planHover   = color.NRGBA{0x0F, 0x17, 0x21, 0x0D}
	planPressed = color.NRGBA{0x0F, 0x17, 0x21, 0x3D}

	// The LED colours, and the meaning the plan attaches to each.
	planGray  = color.NRGBA{0x8C, 0x95, 0x9F, 0xFF} // not checked / stopped
	planAmber = color.NRGBA{0xBF, 0x87, 0x00, 0xFF} // checking / connecting / finalizing
	planGreen = color.NRGBA{0x1A, 0x7F, 0x37, 0xFF} // connected / listening
	planRed   = color.NRGBA{0xCF, 0x22, 0x2E, 0xFF} // failed / error
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
