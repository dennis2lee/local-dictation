//go:build darwin

package hotkey

import (
	"fmt"
	"strings"

	"golang.design/x/hotkey"
)

// AvailableModifiers is what the Settings tab offers on this platform.
func AvailableModifiers() []string { return []string{"Ctrl", "Shift", "Option", "Cmd"} }

func parseModifier(name string) (hotkey.Modifier, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "ctrl", "control":
		return hotkey.ModCtrl, nil
	case "shift":
		return hotkey.ModShift, nil
	case "alt", "option", "opt":
		return hotkey.ModOption, nil
	case "cmd", "command", "super", "win":
		return hotkey.ModCmd, nil
	default:
		return 0, fmt.Errorf("unknown modifier %q", name)
	}
}

// isPermissionError recognises the Accessibility refusal.
//
// macOS delivers global hotkeys through a CGEventTap, which the system refuses
// to create until the app is trusted for Accessibility / Input Monitoring. The
// refusal is not a crash and not a prompt — the tap simply does not get made —
// so this has to become a visible instruction in Settings rather than a silent
// dead shortcut.
func isPermissionError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "accessibility") ||
		strings.Contains(message, "input monitoring") ||
		strings.Contains(message, "permission")
}

// PermissionHint tells the user exactly where to go.
func PermissionHint() string {
	return "Open System Settings > Privacy & Security > Accessibility and turn on " +
		"Local Dictation, then restart the app."
}
