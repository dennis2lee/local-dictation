//go:build windows

package hotkey

import (
	"fmt"
	"strings"

	"golang.design/x/hotkey"
)

// AvailableModifiers is what the Settings tab offers on this platform.
func AvailableModifiers() []string { return []string{"Ctrl", "Shift", "Alt", "Win"} }

func parseModifier(name string) (hotkey.Modifier, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "ctrl", "control":
		return hotkey.ModCtrl, nil
	case "shift":
		return hotkey.ModShift, nil
	case "alt", "option", "opt":
		return hotkey.ModAlt, nil
	case "win", "super", "cmd", "command":
		return hotkey.ModWin, nil
	default:
		return 0, fmt.Errorf("unknown modifier %q", name)
	}
}

// isPermissionError is always false here: RegisterHotKey needs no permission,
// it only fails when another process already holds the combination.
func isPermissionError(error) bool { return false }

// PermissionHint has nothing to say on Windows.
func PermissionHint() string {
	return "If the shortcut does not respond, another application may already " +
		"be using it. Choose a different key in Settings."
}
