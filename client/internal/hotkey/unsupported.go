//go:build !darwin && !windows

// Package hotkey registers the global activation shortcut.
//
// This build has no implementation. The client supports Windows and macOS; the
// stub exists so the rest of the code, and its tests, still build on a Linux CI
// machine.
package hotkey

import (
	"errors"
	"strings"
)

// ErrPermissionRequired is never returned on this platform.
var ErrPermissionRequired = errors.New("the operating system needs permission before the shortcut can work")

// ErrUnsupported is what every registration returns here.
var ErrUnsupported = errors.New("global shortcuts are not supported on this platform")

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

// Manager satisfies the same interface as the real one and does nothing.
type Manager struct{ binding Binding }

func New(func()) *Manager { return &Manager{} }

func (m *Manager) Binding() Binding       { return m.binding }
func (m *Manager) Register(Binding) error { return ErrUnsupported }
func (m *Manager) Unregister()            {}
func AvailableModifiers() []string        { return []string{"Ctrl", "Shift", "Alt", "Super"} }
func AvailableKeys() []string             { return []string{"M"} }
func PermissionHint() string              { return "" }
