// Registering a shortcut is not a small thing to get wrong. On macOS it reaches
// dispatch_sync onto the main queue, so doing it from the goroutine locked to
// the main thread traps inside cgo — no panic to catch, no window, no message.
// These pin the two properties that keep that from happening.
package ui

import (
	"errors"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"github.com/dennis2lee/local-dictation/client/internal/config"
	"github.com/dennis2lee/local-dictation/client/internal/hotkey"
)

// fakeRegistrar records what it was asked to do, and can be held open to prove
// the caller is not waiting on it.
type fakeRegistrar struct {
	mu           sync.Mutex
	registered   []hotkey.Binding
	unregistered int
	err          error
	release      chan struct{} // when non-nil, Register blocks until it closes
}

func (f *fakeRegistrar) Register(binding hotkey.Binding) error {
	if f.release != nil {
		<-f.release
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registered = append(f.registered, binding)
	return f.err
}

func (f *fakeRegistrar) Unregister() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unregistered++
}

func (f *fakeRegistrar) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.registered)
}

func appWithRegistrar(t *testing.T, fake *fakeRegistrar) *App {
	t.Helper()
	settings := testSettings()
	app := &App{settings: settings, fyne: test.NewApp(), hotkeys: fake}
	app.mainTab = newMainTab(app)
	return app
}

func binding() config.Hotkey {
	return config.Hotkey{Modifiers: []string{"Ctrl", "Shift"}, Key: "M"}
}

// The property that matters: registerHotkey must hand back control rather than
// run the registration where it was called from.
func TestRegisteringAShortcutDoesNotBlockTheCaller(t *testing.T) {
	fake := &fakeRegistrar{release: make(chan struct{})}
	app := appWithRegistrar(t, fake)

	done := make(chan struct{})
	go func() { defer close(done); app.registerHotkey(binding()) }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("registerHotkey blocked on the registration; on macOS that is a deadlock the system traps")
	}

	if got := fake.calls(); got != 0 {
		t.Fatalf("the registration ran before the caller was released (%d call(s))", got)
	}
	close(fake.release)

	waitFor(t, func() bool { return fake.calls() == 1 }, "the registration never happened")
}

func TestASuccessfulRegistrationClearsTheShortcutProblem(t *testing.T) {
	fake := &fakeRegistrar{}
	app := appWithRegistrar(t, fake)
	app.mainTab.problem.SetText("something earlier went wrong")
	app.mainTab.problem.Show()

	app.registerHotkeyNow(binding())

	if !app.mainTab.problem.Hidden {
		t.Errorf("the problem label is still showing %q", app.mainTab.problem.Text)
	}
	if got := fake.registered; len(got) != 1 || got[0].Key != "M" {
		t.Errorf("registered %+v, want one binding for M", got)
	}
}

func TestAFailedRegistrationSaysSoInTheWindow(t *testing.T) {
	fake := &fakeRegistrar{err: errors.New("another application already uses it")}
	app := appWithRegistrar(t, fake)

	app.registerHotkeyNow(binding())

	if app.mainTab.problem.Hidden {
		t.Fatal("the shortcut could not be registered and nothing said so")
	}
	if text := app.mainTab.problem.Text; text == "" {
		t.Error("the problem label is empty")
	}
}

func TestRebindingRegistersTheNewShortcut(t *testing.T) {
	fake := &fakeRegistrar{}
	app := appWithRegistrar(t, fake)

	app.registerHotkeyNow(binding())
	app.registerHotkeyNow(config.Hotkey{Modifiers: []string{"Ctrl"}, Key: "J"})

	if len(fake.registered) != 2 {
		t.Fatalf("expected two registrations, got %d", len(fake.registered))
	}
	if fake.registered[1].Key != "J" {
		t.Errorf("second registration was for %q, want J", fake.registered[1].Key)
	}
}

func waitFor(t *testing.T, done func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(message)
}
