package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	"github.com/dennis2lee/local-dictation/client/internal/session"
)

// The plan's LED table: grey idle, amber working, green live, red needs
// attention. The values live in theme.go with the rest of the palette, so the
// dot and everything around it come from one place.
var ledColours = map[session.LED]color.Color{
	session.Gray:  planGray,
	session.Amber: planAmber,
	session.Green: planGreen,
	session.Red:   planRed,
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
	//
	// Centred, because HBox stretches its children to the row's full height and
	// a GridWrap lays its cell out at the top of whatever it is given. The dot
	// then floats above the middle of the words beside it — a small thing that
	// reads as sloppy on the one row someone looks at while dictating.
	//
	// The gap between the two is theme padding plus the label's own inner
	// padding, which together put the dot most of a character away from the
	// word it belongs to. An HBox with no padding of its own, and the label
	// pulled back over its inner padding, sits them together as one thing.
	indicator.widget = container.New(layout.NewCustomPaddedHBoxLayout(0),
		container.NewCenter(container.NewGridWrap(fyne.NewSize(11, 11), dot)),
		container.New(layout.NewCustomPaddedLayout(0, 0, -4, 0), label),
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
