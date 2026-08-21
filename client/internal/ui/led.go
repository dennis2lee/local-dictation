package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/dennis2lee/local-dictation/client/internal/session"
)

// Colours for the plan's LED table: grey idle, amber working, green live, red
// needs attention. Chosen to stay legible on both the light and dark themes.
var ledColours = map[session.LED]color.Color{
	session.Gray:  color.NRGBA{R: 0x9e, G: 0x9e, B: 0x9e, A: 0xff},
	session.Amber: color.NRGBA{R: 0xf5, G: 0xa6, B: 0x23, A: 0xff},
	session.Green: color.NRGBA{R: 0x2e, G: 0xa0, B: 0x43, A: 0xff},
	session.Red:   color.NRGBA{R: 0xd7, G: 0x3a, B: 0x3a, A: 0xff},
}

// led is a coloured dot with a caption.
type led struct {
	dot     *canvas.Circle
	caption *widget.Label
	widget  fyne.CanvasObject
}

func newLED(caption string) *led {
	dot := canvas.NewCircle(ledColours[session.Gray])

	label := widget.NewLabel(caption)
	indicator := &led{dot: dot, caption: label}
	// A Circle has no minimum size of its own, so a GridWrap gives it one and
	// keeps the dot from collapsing next to the label.
	indicator.widget = container.NewHBox(
		container.NewGridWrap(fyne.NewSize(14, 14), dot),
		label,
	)
	return indicator
}

// Set updates the colour and caption. Safe to call only on the Fyne goroutine —
// callers coming from elsewhere go through fyne.Do.
func (l *led) Set(state session.LED, caption string) {
	colour, ok := ledColours[state]
	if !ok {
		colour = ledColours[session.Gray]
	}
	l.dot.FillColor = colour
	l.dot.Refresh()
	l.caption.SetText(caption)
}

func (l *led) Object() fyne.CanvasObject { return l.widget }
