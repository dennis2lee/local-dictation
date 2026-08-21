// Package hotkey registers the global activation shortcut.
//
// "Global" is the whole point: the plan's shortcut has to work while the user
// is in Outlook, in a browser, in anything. That is an OS-level registration,
// and every OS gates it differently — which is why the failure path here is as
// carefully written as the success path. A shortcut that silently does nothing
// is the worst possible outcome for a dictation tool.
//
// Windows and macOS are the supported client platforms; everything else gets
// the stub in unsupported.go so the rest of the client still builds and tests.

//go:build darwin || windows

package hotkey

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.design/x/hotkey"
)

// ErrPermissionRequired means the OS refused the registration until the user
// grants a permission. On macOS that is Accessibility / Input Monitoring.
var ErrPermissionRequired = errors.New("the operating system needs permission before the shortcut can work")

// Binding is a parsed chord.
type Binding struct {
	Modifiers []string
	Key       string
}

func (b Binding) String() string {
	if b.Key == "" {
		return ""
	}
	return strings.Join(append(append([]string{}, b.Modifiers...), b.Key), " + ")
}

// Manager owns one registration at a time.
type Manager struct {
	mu       sync.Mutex
	current  *hotkey.Hotkey
	stop     chan struct{}
	binding  Binding
	onToggle func()
}

// New builds a manager. `onToggle` is called on each key-down of the chord, from
// a background goroutine.
func New(onToggle func()) *Manager {
	return &Manager{onToggle: onToggle}
}

// Binding reports what is currently registered.
func (m *Manager) Binding() Binding {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.binding
}

// Register replaces any existing registration with this one.
func (m *Manager) Register(binding Binding) error {
	modifiers, key, err := translate(binding)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.unregisterLocked()

	registration := hotkey.New(modifiers, key)
	if err := registration.Register(); err != nil {
		if isPermissionError(err) {
			return fmt.Errorf("%w: %v", ErrPermissionRequired, err)
		}
		// The usual cause is that something else already owns the chord.
		return fmt.Errorf("register %s: %w (another application may already use it)", binding, err)
	}

	stop := make(chan struct{})
	m.current, m.stop, m.binding = registration, stop, binding

	go func() {
		keydown := registration.Keydown()
		for {
			select {
			case <-stop:
				return
			case _, ok := <-keydown:
				if !ok {
					return
				}
				if m.onToggle != nil {
					m.onToggle()
				}
			}
		}
	}()
	return nil
}

// Unregister releases the shortcut.
func (m *Manager) Unregister() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unregisterLocked()
}

func (m *Manager) unregisterLocked() {
	if m.stop != nil {
		close(m.stop)
		m.stop = nil
	}
	if m.current != nil {
		_ = m.current.Unregister()
		m.current = nil
	}
	m.binding = Binding{}
}

// ParseKey maps a configured key name onto the platform key code.
func ParseKey(name string) (hotkey.Key, error) {
	key, ok := keyNames[strings.ToUpper(strings.TrimSpace(name))]
	if !ok {
		return 0, fmt.Errorf("unknown shortcut key %q", name)
	}
	return key, nil
}

func translate(binding Binding) ([]hotkey.Modifier, hotkey.Key, error) {
	key, err := ParseKey(binding.Key)
	if err != nil {
		return nil, 0, err
	}
	if len(binding.Modifiers) == 0 {
		// A bare key would fire while the user is typing into any application.
		return nil, 0, errors.New("a shortcut needs at least one modifier")
	}

	modifiers := make([]hotkey.Modifier, 0, len(binding.Modifiers))
	for _, name := range binding.Modifiers {
		modifier, err := parseModifier(name)
		if err != nil {
			return nil, 0, err
		}
		modifiers = append(modifiers, modifier)
	}
	return modifiers, key, nil
}

var keyNames = map[string]hotkey.Key{
	"A": hotkey.KeyA, "B": hotkey.KeyB, "C": hotkey.KeyC, "D": hotkey.KeyD,
	"E": hotkey.KeyE, "F": hotkey.KeyF, "G": hotkey.KeyG, "H": hotkey.KeyH,
	"I": hotkey.KeyI, "J": hotkey.KeyJ, "K": hotkey.KeyK, "L": hotkey.KeyL,
	"M": hotkey.KeyM, "N": hotkey.KeyN, "O": hotkey.KeyO, "P": hotkey.KeyP,
	"Q": hotkey.KeyQ, "R": hotkey.KeyR, "S": hotkey.KeyS, "T": hotkey.KeyT,
	"U": hotkey.KeyU, "V": hotkey.KeyV, "W": hotkey.KeyW, "X": hotkey.KeyX,
	"Y": hotkey.KeyY, "Z": hotkey.KeyZ,
	"0": hotkey.Key0, "1": hotkey.Key1, "2": hotkey.Key2, "3": hotkey.Key3,
	"4": hotkey.Key4, "5": hotkey.Key5, "6": hotkey.Key6, "7": hotkey.Key7,
	"8": hotkey.Key8, "9": hotkey.Key9,
	"SPACE": hotkey.KeySpace,
}

// AvailableKeys lists what the Settings tab may offer.
func AvailableKeys() []string {
	keys := make([]string, 0, len(keyNames))
	for name := range keyNames {
		keys = append(keys, name)
	}
	return keys
}
