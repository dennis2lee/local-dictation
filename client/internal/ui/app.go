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

	"github.com/dennis2lee/local-dictation/client/internal/audio"
	"github.com/dennis2lee/local-dictation/client/internal/config"
	"github.com/dennis2lee/local-dictation/client/internal/dial"
	"github.com/dennis2lee/local-dictation/client/internal/hotkey"
	"github.com/dennis2lee/local-dictation/client/internal/input"
	"github.com/dennis2lee/local-dictation/client/internal/platform"
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
	hotkeys    registrar

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
		Focus:           focusWatcher(),
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
	// Before any widget is built, so nothing is measured against the default
	// theme's type sizes and then re-laid out.
	a.fyne.Settings().SetTheme(planTheme{})

	// Set on the app before the window exists: Fyne hands the app icon to the
	// window, the tray and the taskbar, and a window created first would keep
	// the toolkit's default in its title bar.
	if icon := appIcon(); icon != nil {
		a.fyne.SetIcon(icon)
	}

	a.window = a.fyne.NewWindow(windowTitle(a.options.Version))
	if icon := appIcon(); icon != nil {
		a.window.SetIcon(icon)
	}
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
		// Same main-queue rule as registration, and Quit is called from the
		// window's close handler — which is the main goroutine.
		unregistered := make(chan struct{})
		go func() { defer close(unregistered); a.hotkeys.Unregister() }()
		select {
		case <-unregistered:
		case <-time.After(2 * time.Second):
			log.Print("hotkey: unregister did not finish; quitting anyway")
		}
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

// registrar is the part of hotkey.Manager the App uses. It is an interface so
// the registration path can be exercised without a keyboard — and that path
// needs exercising, because getting it wrong is not a failed shortcut but a
// dead process.
type registrar interface {
	Register(hotkey.Binding) error
	Unregister()
}

// registerHotkey hands the work to another goroutine and returns.
//
// On macOS both Register and Unregister reach dispatch_sync onto the main
// queue. Calling that from the goroutine locked to the main thread is a queue
// waiting on itself, which libdispatch detects and traps: SIGTRAP inside cgo,
// no window, no message. Before Run() there is nothing draining that queue
// either, so even without the trap it would wait forever.
//
// Off the main goroutine it enqueues and completes once the event loop is up.
// The result reaches the window through setShortcutProblem, which routes itself
// back onto the Fyne goroutine.
func (a *App) registerHotkey(binding config.Hotkey) {
	go a.registerHotkeyNow(binding)
}

func (a *App) registerHotkeyNow(binding config.Hotkey) {
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

// focusWatcher adapts the platform watcher to the session's interface, mapping
// "not supported here" onto a nil watcher rather than a broken one.
func focusWatcher() session.FocusWatcher {
	watcher := platform.NewFocusWatcher()
	if watcher == nil {
		return nil
	}
	return watcher
}

// textAdapterAvailability reports whether text can be written at the cursor.
func (a *App) textAdapterAvailability() (bool, string) {
	if a.textAdapterErr != nil {
		return false, a.textAdapterErr.Error()
	}
	return platform.Available()
}

// windowTitle carries the version, so a screenshot or a support conversation
// says which build it is without anyone opening a menu.
func windowTitle(version string) string {
	if version == "" {
		return "Local Dictation"
	}
	return "Local Dictation " + version
}
