//go:build windows

package platform

import (
	"errors"
	"strconv"
	"syscall"
)

var getForegroundWindow = user32.NewProc("GetForegroundWindow")

type windowsFocus struct{}

func newFocusWatcher() FocusWatcher { return windowsFocus{} }

// Current identifies the foreground window by its handle.
//
// The handle rather than the owning process: switching between two windows of
// the same application still moves the cursor, and a sentence continuing into
// a different document of the same editor is just as wrong as one continuing
// into a different app.
func (windowsFocus) Current() (string, error) {
	handle, _, err := getForegroundWindow.Call()
	if handle == 0 {
		if err != nil && !errors.Is(err, syscall.Errno(0)) {
			return "", err
		}
		return "", errors.New("no window has focus")
	}
	return "hwnd:" + strconv.FormatUint(uint64(handle), 16), nil
}

func (windowsFocus) Close() error { return nil }
