package ui

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/dennis2lee/local-dictation/client/internal/config"
	"github.com/dennis2lee/local-dictation/client/internal/protocol"
	"github.com/dennis2lee/local-dictation/client/internal/session"
)

// mainTab is the everyday surface: what state dictation is in, which language,
// and what key starts it.
type mainTab struct {
	app *App

	status   *led
	detail   *widget.Label
	language *widget.RadioGroup
	shortcut *widget.Label
	problem  *widget.Label
	body     fyne.CanvasObject
}

func newMainTab(app *App) *mainTab {
	settings := app.Settings()
	tab := &mainTab{app: app}

	tab.status = newLED("Dictation stopped")
	tab.detail = widget.NewLabel("Ready to start with the global shortcut.")
	tab.detail.Wrapping = fyne.TextWrapWord

	tab.language = widget.NewRadioGroup([]string{"Korean", "English"}, tab.onLanguageChanged)
	tab.language.Horizontal = true
	tab.language.SetSelected(languageLabel(settings.Language))

	tab.shortcut = widget.NewLabel(settings.Hotkey.String())
	tab.shortcut.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}

	tab.problem = widget.NewLabel("")
	tab.problem.Wrapping = fyne.TextWrapWord
	tab.problem.Hide()

	if app.textAdapterErr != nil {
		tab.problem.SetText(app.textAdapterErr.Error())
		tab.problem.Show()
	}

	tab.body = container.NewVBox(
		widget.NewLabelWithStyle("Dictation", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		tab.status.Object(),
		tab.detail,
		widget.NewSeparator(),

		widget.NewLabelWithStyle("Language", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		tab.language,
		widget.NewLabel("The language decides which server the session connects to."),
		widget.NewSeparator(),

		widget.NewLabelWithStyle("Shortcut", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		container.NewHBox(widget.NewLabel("Press"), tab.shortcut, widget.NewLabel("to start, press it again to stop.")),
		tab.problem,
		widget.NewSeparator(),

		container.NewHBox(
			widget.NewButtonWithIcon("Start / stop", theme.MediaRecordIcon(), app.onShortcut),
			widget.NewButtonWithIcon("Settings", theme.SettingsIcon(), func() {
				app.tabs.SelectIndex(1)
			}),
		),
	)
	return tab
}

func (m *mainTab) content() fyne.CanvasObject {
	return container.NewPadded(container.NewVScroll(m.body))
}

// onLanguageChanged is rejected mid-session: the plan locks the language while
// listening, because the audio is already on its way to one specific server.
func (m *mainTab) onLanguageChanged(label string) {
	state := m.app.controller.State()
	settings := m.app.Settings()

	if !state.AcceptsSettingsChanges() {
		m.language.SetSelected(languageLabel(settings.Language))
		m.detail.SetText("The language cannot change while dictation is running.")
		return
	}

	settings.Language = languageFromLabel(label)
	if err := m.app.ApplySettings(settings); err != nil {
		m.detail.SetText(err.Error())
	}
}

// applyUpdate renders one session state change. Called on the Fyne goroutine.
func (m *mainTab) applyUpdate(update session.Update) {
	m.status.Set(update.State.LED(), statusCaption(update.State))
	if update.Detail != "" {
		m.detail.SetText(update.Detail)
	}
	if update.State.AcceptsSettingsChanges() {
		m.language.Enable()
	} else {
		m.language.Disable()
	}
	m.app.settingsTab.setEditable(update.State.AcceptsSettingsChanges())
}

func (m *mainTab) settingsChanged(settings config.Config) {
	fyne.Do(func() {
		m.shortcut.SetText(settings.Hotkey.String())
		m.language.SetSelected(languageLabel(settings.Language))
	})
}

func (m *mainTab) setShortcutProblem(message string) {
	set := func() {
		if message == "" {
			m.problem.Hide()
			return
		}
		m.problem.SetText(message)
		m.problem.Show()
	}
	// registerHotkey runs both during construction (already on the Fyne
	// goroutine) and from Save, so route it either way.
	fyne.Do(set)
}

func statusCaption(state session.State) string {
	switch state {
	case session.Idle:
		return "Dictation stopped"
	case session.Connecting:
		return "Connecting"
	case session.Listening:
		return "Listening"
	case session.Finalizing:
		return "Finishing"
	case session.Error:
		return "Needs attention"
	default:
		return state.String()
	}
}

func languageLabel(language protocol.Language) string {
	if language == protocol.English {
		return "English"
	}
	return "Korean"
}

func languageFromLabel(label string) protocol.Language {
	if label == "English" {
		return protocol.English
	}
	return protocol.Korean
}

var _ = fmt.Sprintf
