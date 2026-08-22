package ui

import (
	"testing"

	"github.com/dennis2lee/local-dictation/client/internal/audio"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
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

// "Input level" was drawn straight across the gain slider.
//
// caption() wraps, so it reports a minimum width of roughly one character and
// expects a container to hand it a width. Border does not: it gives the left
// slot exactly the minimum and the rest to the middle, so 70px of text was
// allocated 16px and drew over a slider that began at 24px.
func TestARowLabelDoesNotDrawOverTheControlBesideIt(t *testing.T) {
	application := test.NewApp()
	defer application.Quit()

	label := inlineCaption("Input level")
	slider := widget.NewSlider(0, 1)
	row := container.NewBorder(nil, nil, label, widget.NewLabel("+0 dB"), slider)

	window := test.NewWindow(row)
	defer window.Close()
	window.Resize(fyne.NewSize(420, 90))

	// The width the words actually occupy, which is the thing that overlaps.
	// Asking the label itself is no good: a wrapping one answers with the 16px
	// it is willing to fold down to, which is the very number that causes this.
	reference := widget.NewLabel("Input level")
	reference.SizeName = label.SizeName
	drawn := reference.MinSize().Width

	driver := fyne.CurrentApp().Driver()
	textEnds := driver.AbsolutePositionForObject(label).X + drawn
	sliderStarts := driver.AbsolutePositionForObject(slider).X

	if textEnds > sliderStarts {
		t.Errorf("the label's text runs to %.1f but the slider starts at %.1f, so %.1f pixels of it are underneath the control",
			textEnds, sliderStarts, textEnds-sliderStarts)
	}
	if label.Size().Width < drawn {
		t.Errorf("the label was allocated %.1f for text that draws %.1f wide", label.Size().Width, drawn)
	}
}

// The slider is dragged, so it must not change size while a pointer is holding
// it: the reading beside it grows from "-6 dB" to "+18 dB" and the slider was
// giving up that width, sliding the thumb out from under the cursor.
func TestTheGainSliderKeepsItsWidthAsTheReadingChanges(t *testing.T) {
	application := test.NewApp()
	defer application.Quit()

	reading := widget.NewLabel("")
	slider := widget.NewSlider(0, 1)
	row := container.NewBorder(nil, nil, inlineCaption("Input level"),
		fixedWidth(reading, widestGainLabel()), slider)

	window := test.NewWindow(row)
	defer window.Close()
	window.Resize(fyne.NewSize(420, 90))

	reading.SetText(gainText(audio.MinGain))
	row.Refresh()
	quiet := slider.Size().Width

	reading.SetText(gainText(audio.MaxGain))
	row.Refresh()
	loud := slider.Size().Width

	if quiet != loud {
		t.Errorf("the slider is %.1f wide at %s and %.1f at %s", quiet, gainText(audio.MinGain), loud, gainText(audio.MaxGain))
	}
}
