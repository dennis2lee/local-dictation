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

	// The state is the largest text in the window; the line under it is the
	// quiet one. That is the plan's hierarchy, and it is the right one — the
	// state is what someone glances at mid-sentence.
	tab.status = newLED("Dictation stopped")
	tab.status.caption.SizeName = theme.SizeNameHeadingText
	tab.status.caption.TextStyle = fyne.TextStyle{Bold: true}
	tab.detail = caption("Ready to start with the global shortcut.")

	// The handler is attached after the initial value, not with it. SetSelected
	// fires OnChanged, and firing it here would run onLanguageChanged — which
	// calls ApplySettings, which calls back into a mainTab that this line has
	// not finished building and that App.mainTab does not point at yet. That is
	// a nil dereference before the window ever appears.
	tab.language = widget.NewRadioGroup([]string{"Korean", "English"}, nil)
	tab.language.Horizontal = true
	tab.language.SetSelected(languageLabel(settings.Language))
	tab.language.OnChanged = tab.onLanguageChanged

	tab.shortcut = widget.NewLabel(settings.Hotkey.String())
	tab.shortcut.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}

	tab.problem = widget.NewLabel("")
	tab.problem.Wrapping = fyne.TextWrapWord
	tab.problem.Hide()

	if app.textAdapterErr != nil {
		tab.problem.SetText(app.textAdapterErr.Error())
		tab.problem.Show()
	}

	// The plan's Main tab is one panel: the state, what to do about it, and the
	// language, with the shortcut on a chip in the corner. No section headings —
	// there is only one thing here.
	tab.body = container.NewVBox(
		container.NewBorder(nil, nil, nil, chip(tab.shortcut), widget.NewLabel("")),
		panel(container.NewVBox(
			container.NewHBox(tab.status.Object()),
			tab.detail,
			tab.language,
		)),
		tab.problem,
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
	return container.NewVScroll(inset(m.body))
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
