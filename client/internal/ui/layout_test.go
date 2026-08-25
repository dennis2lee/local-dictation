package ui

import (
	"context"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/dennis2lee/local-dictation/client/internal/audio"
	"github.com/dennis2lee/local-dictation/client/internal/config"
	"github.com/dennis2lee/local-dictation/client/internal/models"
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

// -- the settings groups fit the window ------------------------------------

// The window is sized to the tallest group of settings, so a group that does
// not fit is what makes the window grow.
//
// Settings used to be one column roughly 1335px long in a 620px window: every
// setting sat behind a scroll past every other setting, and the height was a
// compromise between two bad options. Split into tabbed groups the window is
// 480, and this is the check that keeps it there — add a field to a group that
// is already full and this fails with the number, rather than the window
// quietly needing to be taller again.
//
// A group is allowed to overflow. It scrolls, so nothing becomes unreachable.
// What is not allowed is overflowing without anyone noticing.
func TestEverySettingsGroupFitsTheWindow(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeLocal, config.ModeRemote} {
		settings := testSettings()
		settings.Mode = mode

		app := halfBuiltApp(settings)
		test.ApplyTheme(t, planTheme{})

		tab := newTestSettingsTab(app, settings)
		outer := container.NewAppTabs(
			container.NewTabItem("Main", widget.NewLabel("")),
			container.NewTabItem("Settings", tab.content()),
		)
		// Nothing is laid out until it is on screen, and Main is selected by
		// default: measured from there, every group reports a negative size.
		outer.SelectIndex(1)

		window := test.NewWindow(outer)
		defer window.Close()
		window.Resize(fyne.NewSize(windowWidth, windowHeight))

		for index, item := range tab.groups.Items {
			// Only the selected group is laid out, so each has to be visited.
			tab.groups.SelectIndex(index)
			scroll, ok := item.Content.(*container.Scroll)
			if !ok {
				t.Fatalf("%s: group content is %T, not a scroll", item.Text, item.Content)
			}
			if item.Text == "Models" {
				// The one group that is a list rather than a form. It grows
				// with the catalogue, so holding it to the window would mean
				// never adding a model. What it owes instead is that the
				// models the chosen backend needs are above the fold — see
				// TestTheModelsYouNeedAreVisibleWithoutScrolling.
				continue
			}
			wanted := scroll.Content.MinSize().Height
			if over := wanted - scroll.Size().Height; over > 0 {
				t.Errorf("%s mode: the %q group wants %.0fpx and has %.0f, so %.0f is behind a scroll",
					mode, item.Text, wanted, scroll.Size().Height, over)
			}
		}
	}
}

// Save applies every group at once, so it has to be reachable from all of
// them: inside one, it would scroll away with that group and read as saving
// only what is above it.
func TestSaveIsReachableFromEverySettingsGroup(t *testing.T) {
	settings := testSettings()
	app := halfBuiltApp(settings)
	test.ApplyTheme(t, planTheme{})

	tab := newTestSettingsTab(app, settings)
	window := test.NewWindow(tab.content())
	defer window.Close()
	window.Resize(fyne.NewSize(windowWidth, windowHeight))

	driver := fyne.CurrentApp().Driver()
	for index, item := range tab.groups.Items {
		tab.groups.SelectIndex(index)
		top := driver.AbsolutePositionForObject(tab.saveButton).Y
		if top <= 0 || top+tab.saveButton.Size().Height > windowHeight {
			t.Errorf("on the %q group, Save sits at y=%.0f in a %dpx window",
				item.Text, top, windowHeight)
		}
	}
}

// A settings tab with no audio backend behind it.
func newTestSettingsTab(app *App, settings config.Config) *settingsTab {
	tab := &settingsTab{app: app, modifierChecks: map[string]*widget.Check{}}
	// Never the real one: it downloads gigabytes.
	tab.installModel = func(context.Context, models.Model, string, func(models.Progress)) error {
		return nil
	}
	tab.listDevices = func() ([]audio.Device, error) {
		return []audio.Device{{ID: "1", Name: "MacBook Air Microphone"}}, nil
	}
	tab.message = widget.NewLabel("")
	tab.message.Wrapping = fyne.TextWrapWord
	tab.message.Hide()
	// The real one, not a copy: a second list here is a list that drifts.
	tab.groups = tab.buildGroups(settings)
	tab.actions = tab.buildActions()
	return tab
}

// Every settings group has to be one click away.
//
// Adding the Models tab took the row to seven, which stopped fitting across a
// 560px window: Fyne folded the overflow into a "…" menu and Updates — the tab
// that installs the fix for whatever brought someone here — was behind it. The
// tab bar is the one piece of navigation in the app, so it is worth holding to
// the window rather than letting the window grow to it.
func TestEverySettingsGroupIsOneClickAway(t *testing.T) {
	settings := testSettings()
	app := halfBuiltApp(settings)
	test.ApplyTheme(t, planTheme{})

	tab := newTestSettingsTab(app, settings)
	outer := container.NewAppTabs(
		container.NewTabItem("Main", widget.NewLabel("")),
		container.NewTabItem("Settings", tab.content()),
	)
	outer.SelectIndex(1)
	window := test.NewWindow(outer)
	defer window.Close()
	window.Resize(fyne.NewSize(windowWidth, windowHeight))

	// Fyne signals overflow by rendering an icon-only button beside the tabs
	// that opens the rest as a menu. Asking the AppTabs for its MinSize does
	// not show it: the renderer drops tabs until the bar fits, so the answer
	// is always "it fits".
	if overflowing(tab.groups) {
		var names []string
		for _, item := range tab.groups.Items {
			names = append(names, item.Text)
		}
		t.Errorf("the %d group tabs (%s) do not fit across a %dpx window, so some are behind a \"…\" menu",
			len(tab.groups.Items), strings.Join(names, ", "), windowWidth)
	}
}

// overflowing reports whether the tab bar folded any tab into its menu.
func overflowing(tabs *container.AppTabs) bool {
	labelled := map[string]bool{}
	for _, item := range tabs.Items {
		labelled[item.Text] = true
	}
	// A tab button is an unexported type, so it is found by the canvas text it
	// draws its name with. A tab folded into the overflow menu is not laid out
	// in the bar at all, so its name is simply absent.
	found := map[string]bool{}
	walk(test.WidgetRenderer(tabs).Objects(), func(object fyne.CanvasObject) {
		if text, ok := object.(*canvas.Text); ok && labelled[text.Text] {
			found[text.Text] = true
		}
	})
	return len(found) < len(labelled)
}

func walk(objects []fyne.CanvasObject, visit func(fyne.CanvasObject)) {
	for _, object := range objects {
		visit(object)
		switch typed := object.(type) {
		case *fyne.Container:
			walk(typed.Objects, visit)
		case fyne.Widget:
			walk(test.WidgetRenderer(typed).Objects(), visit)
		}
	}
}
