package ui

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// The dot floated above the middle of the words beside it, because HBox
// stretches its children to the row's full height and a GridWrap lays its cell
// out at the top of whatever it is given. It is the one row someone looks at
// while dictating, so a pixel matters more than it would anywhere else.
func TestTheIndicatorDotSitsLevelWithItsWords(t *testing.T) {
	application := test.NewApp()
	defer application.Quit()

	indicator := newLED("Korean: connected")
	window := test.NewWindow(indicator.Object())
	defer window.Close()
	window.Resize(fyne.NewSize(320, 80))

	driver := fyne.CurrentApp().Driver()
	dot := driver.AbsolutePositionForObject(indicator.dot).Y + indicator.dot.Size().Height/2
	words := driver.AbsolutePositionForObject(indicator.caption).Y + indicator.caption.Size().Height/2

	if difference := dot - words; difference > 1 || difference < -1 {
		t.Errorf("the dot's middle is at %.1f and the caption's at %.1f", dot, words)
	}
}

// A text field that runs under the scroll bar looks like the window was cut off
// rather than laid out, so the right margin has to clear it.
func TestFieldsAreInsetFurtherOnTheScrollBarSide(t *testing.T) {
	application := test.NewApp()
	defer application.Quit()

	field := widget.NewEntry()
	box, ok := inset(field).(*fyne.Container)
	if !ok {
		t.Fatalf("inset returned %T", inset(field))
	}
	window := test.NewWindow(box)
	defer window.Close()
	window.Resize(fyne.NewSize(420, 120))

	left := field.Position().X
	right := box.Size().Width - (field.Position().X + field.Size().Width)

	if right <= left {
		t.Errorf("margins are %.0f left and %.0f right; the right one carries the scroll bar", left, right)
	}
	if bar := (planTheme{}).Size(theme.SizeNameScrollBar); right < bar {
		t.Errorf("the right margin is %.0f, narrower than the %.0f scroll bar", right, bar)
	}
	if left < 8 {
		t.Errorf("the left margin is %.0f, which reads as touching the frame", left)
	}
}
