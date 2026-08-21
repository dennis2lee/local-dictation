// Package ui is the desktop application.
//
// Every string the user sees is English, per the project plan. The split
// between tabs follows it too: Main holds what is touched every day, Settings
// holds what is touched once after installing.
//
// Threading: Fyne owns the main goroutine. Session updates, connection tests
// and local-server progress all arrive on other goroutines, so anything that
// touches a widget from one of those goes through fyne.Do.
package ui

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"

	"github.com/dennis2lee/local-dictation/client/internal/audio"
	"github.com/dennis2lee/local-dictation/client/internal/config"
	"github.com/dennis2lee/local-dictation/client/internal/dial"
	"github.com/dennis2lee/local-dictation/client/internal/hotkey"
	"github.com/dennis2lee/local-dictation/client/internal/input"
	"github.com/dennis2lee/local-dictation/client/internal/platform"
	"github.com/dennis2lee/local-dictation/client/internal/protocol"
	"github.com/dennis2lee/local-dictation/client/internal/session"
)

// AppID is the Fyne application identifier, used for preferences storage.
const AppID = "com.local-dictation.client"

// Options configures the application.
type Options struct {
	Version  string
	Settings config.Config
	StateDir string
}

// App owns the window, the tray and everything under them.
type App struct {
	options Options

	fyne   fyne.App
	window fyne.Window

	dialer     *dial.Dialer
	controller *session.Controller
	capture    *audio.Capture
	composer   *input.Composer
	hotkeys    *hotkey.Manager

	mainTab     *mainTab
	settingsTab *settingsTab
	tabs        *container.AppTabs

	mu       sync.RWMutex
	settings config.Config

	// textAdapterErr records why text cannot be written, if it cannot. The
	// Settings tab shows it, because the alternative is a user pressing the
	// shortcut and watching nothing happen.
	textAdapterErr error

	quitOnce sync.Once
}

// New wires the application together without showing anything.
func New(options Options) (*App, error) {
	application := &App{
		options:  options,
		settings: options.Settings,
		fyne:     fyneapp.NewWithID(AppID),
	}

	capture, err := audio.NewCapture()
	if err != nil {
		return nil, fmt.Errorf("start the audio backend: %w", err)
	}
	application.capture = capture

	// A missing text adapter is not fatal. The app still opens so the user can
	// read why, grant the permission and try again.
	adapter, err := platform.New()
	if err != nil {
		application.textAdapterErr = err
		adapter = disabledPlatform{reason: err}
	}
	application.composer = input.NewComposer(adapter)

	application.dialer = dial.New(options.Settings, options.StateDir)
	application.controller = session.New(session.Options{
		Dialer:          application.dialer,
		Audio:           capture,
		Composer:        application.composer,
		ClientVersion:   options.Version,
		FinalizeTimeout: 20 * time.Second,
	})

	application.hotkeys = hotkey.New(application.onShortcut)
	application.buildWindow()
	application.registerHotkey(options.Settings.Hotkey)

	go application.consumeUpdates()
	return application, nil
}

func (a *App) buildWindow() {
	a.window = a.fyne.NewWindow("Local Dictation")
	a.window.Resize(fyne.NewSize(560, 620))
	a.window.SetCloseIntercept(func() {
		// Closing the window keeps dictation available from the tray, which is
		// the point of a shortcut-driven tool.
		a.window.Hide()
	})

	a.mainTab = newMainTab(a)
	a.settingsTab = newSettingsTab(a)
	a.tabs = container.NewAppTabs(
		container.NewTabItem("Main", a.mainTab.content()),
		container.NewTabItem("Settings", a.settingsTab.content()),
	)
	a.window.SetContent(a.tabs)

	if desktopApp, ok := a.fyne.(desktop.App); ok {
		menu := fyne.NewMenu("Local Dictation",
			fyne.NewMenuItem("Show", func() { a.window.Show(); a.window.RequestFocus() }),
			fyne.NewMenuItem("Start / stop dictation", a.onShortcut),
			fyne.NewMenuItemSeparator(),
			fyne.NewMenuItem("Quit", a.Quit),
		)
		desktopApp.SetSystemTrayMenu(menu)
	}
}

// Run shows the window and blocks until the app quits.
func (a *App) Run() {
	a.window.ShowAndRun()
}

// Quit tears everything down in the order that loses the least text.
func (a *App) Quit() {
	a.quitOnce.Do(func() {
		// Abort first: it drops the volatile partial and keeps what was
		// committed, which is what someone quitting mid-sentence wants.
		a.controller.Abort("Local Dictation is closing.")
		a.hotkeys.Unregister()
		_ = a.composer.Close()
		_ = a.capture.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := a.dialer.Shutdown(ctx); err != nil {
			log.Printf("stopping the local server: %v", err)
		}
		a.fyne.Quit()
	})
}

// Settings returns the configuration in force.
func (a *App) Settings() config.Config {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.settings
}

// ApplySettings validates, persists and activates new settings.
func (a *App) ApplySettings(updated config.Config) error {
	if err := updated.Validate(); err != nil {
		return err
	}
	if err := updated.Save(); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}

	a.mu.Lock()
	previous := a.settings
	a.settings = updated
	a.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.dialer.Update(ctx, updated); err != nil {
		return fmt.Errorf("apply the server settings: %w", err)
	}

	if updated.Hotkey.String() != previous.Hotkey.String() {
		a.registerHotkey(updated.Hotkey)
	}
	a.mainTab.settingsChanged(updated)
	return nil
}

func (a *App) registerHotkey(binding config.Hotkey) {
	err := a.hotkeys.Register(hotkey.Binding{Modifiers: binding.Modifiers, Key: binding.Key})
	if err == nil {
		a.mainTab.setShortcutProblem("")
		return
	}
	message := fmt.Sprintf("The shortcut %s could not be registered. %s",
		binding.String(), hotkey.PermissionHint())
	log.Printf("hotkey: %v", err)
	a.mainTab.setShortcutProblem(message)
}

// onShortcut is the global shortcut and the tray item.
func (a *App) onShortcut() {
	settings := a.Settings()
	go func() {
		err := a.controller.Toggle(context.Background(), settings.Language, settings.Audio.DeviceID)
		if err != nil && err != session.ErrBusy {
			log.Printf("dictation: %v", err)
		}
	}()
}

// consumeUpdates moves session state onto the UI.
func (a *App) consumeUpdates() {
	for update := range a.controller.Updates() {
		update := update
		fyne.Do(func() { a.mainTab.applyUpdate(update) })
	}
}

// disabledPlatform stands in when no text adapter is available, so the rest of
// the app can run and explain the problem instead of failing to start.
type disabledPlatform struct{ reason error }

func (d disabledPlatform) Name() string               { return "unavailable" }
func (d disabledPlatform) BeginComposition() error    { return d.reason }
func (d disabledPlatform) SetMarkedText(string) error { return d.reason }
func (d disabledPlatform) CommitText(string) error    { return d.reason }
func (d disabledPlatform) EndComposition() error      { return nil }
func (d disabledPlatform) CancelComposition() error   { return nil }
func (d disabledPlatform) Close() error               { return nil }

var _ input.Platform = disabledPlatform{}
var _ = protocol.Korean
var _ = widget.NewLabel

// textAdapterAvailability reports whether text can be written at the cursor.
func (a *App) textAdapterAvailability() (bool, string) {
	if a.textAdapterErr != nil {
		return false, a.textAdapterErr.Error()
	}
	return platform.Available()
}
