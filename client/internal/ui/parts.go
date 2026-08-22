package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// The three shapes the plan's mockup is built from, so the tabs read as layout
// rather than as a pile of containers: a panel, a section heading and a chip.

// inset is the margin between a tab's content and the window frame, and it is
// deliberately wider on the right: that edge carries the scroll bar, and a text
// field running underneath it looks like the window was cut off rather than
// laid out.
func inset(content fyne.CanvasObject) fyne.CanvasObject {
	return container.New(layout.NewCustomPaddedLayout(10, 12, 14, 22), content)
}

// panel is the light rounded block the plan puts the status line in — filled,
// hairline border, generous padding.
func panel(content fyne.CanvasObject) fyne.CanvasObject {
	background := canvas.NewRectangle(planPanel)
	background.StrokeColor = planPanelBorder
	background.StrokeWidth = 1
	background.CornerRadius = 11
	return container.NewStack(background, container.NewPadded(content))
}

// sectionHeading is the small bold label above each group in Settings —
// "Servers", "Microphone", "Software update".
func sectionHeading(text string) *widget.Label {
	label := widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	label.SizeName = theme.SizeNameSubHeadingText
	return label
}

// caption is the muted explanatory line under a control.
func caption(text string) *widget.Label {
	label := widget.NewLabel(text)
	label.SizeName = theme.SizeNameCaptionText
	label.Importance = widget.LowImportance
	label.Wrapping = fyne.TextWrapWord
	return label
}

// headline is the status line itself: the largest text in the window, because
// it is the one thing someone looks at mid-sentence.
func headline(text string) *widget.Label {
	label := widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	label.SizeName = theme.SizeNameHeadingText
	return label
}

// chip is the outlined pill the plan puts the shortcut in, top right. It wraps
// a label the caller keeps, so rebinding the shortcut updates what it shows.
func chip(label *widget.Label) fyne.CanvasObject {
	label.Alignment = fyne.TextAlignCenter
	label.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}
	label.SizeName = theme.SizeNameCaptionText

	background := canvas.NewRectangle(planBackground)
	background.StrokeColor = planInputBorder
	background.StrokeWidth = 1
	background.CornerRadius = 7
	return container.NewStack(background, label)
}

// primaryButton is the filled blue action in the plan's mockup: one per group,
// the thing you are meant to press.
func primaryButton(button *widget.Button) *widget.Button {
	button.Importance = widget.HighImportance
	return button
}
