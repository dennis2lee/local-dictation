//go:build windows

package platform

import (
	"fmt"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

// Windows text injection through SendInput with KEYEVENTF_UNICODE.
//
// Each UTF-16 code unit is sent as its own synthetic key event with no virtual
// key code, which is how you type characters that have no key. Surrogate pairs
// work because both units are sent in order and the receiving application
// reassembles them.

var (
	user32      = syscall.NewLazyDLL("user32.dll")
	sendInput   = user32.NewProc("SendInput")
	getLastErr  = syscall.GetLastError
	inputSizeOf = int32(unsafe.Sizeof(keyboardInput{}))
)

const (
	inputKeyboard     = 1
	keyEventfKeyUp    = 0x0002
	keyEventfUnicode  = 0x0004
	vkBack            = 0x08
	keyEventfScanCode = 0x0008
)

// keyboardInput mirrors INPUT with a KEYBDINPUT payload. The union in the Win32
// header is the size of the largest member (MOUSEINPUT), so the struct is
// padded to match on both 32- and 64-bit.
type keyboardInput struct {
	inputType uint32
	_         uint32 // padding to align the union on 64-bit
	wVk       uint16
	wScan     uint16
	dwFlags   uint32
	time      uint32
	extraInfo uintptr
	_         [8]byte // union padding
}

type windowsTyper struct{}

func newPlatform() (platformType, error) {
	return &keystrokeComposer{typer: &windowsTyper{}}, nil
}

// available is always true on Windows: SendInput needs no permission. It is
// blocked from reaching a window running at a higher integrity level, which is
// why an elevated application will not receive dictation.
func available() (bool, string) { return true, "" }

func (w *windowsTyper) name() string { return "Windows synthetic input (SendInput)" }

func (w *windowsTyper) typeText(text string) error {
	units := utf16.Encode([]rune(text))
	if len(units) == 0 {
		return nil
	}

	events := make([]keyboardInput, 0, len(units)*2)
	for _, unit := range units {
		events = append(events,
			keyboardInput{inputType: inputKeyboard, wScan: unit, dwFlags: keyEventfUnicode},
			keyboardInput{inputType: inputKeyboard, wScan: unit, dwFlags: keyEventfUnicode | keyEventfKeyUp},
		)
	}
	return send(events)
}

func (w *windowsTyper) backspace(count int) error {
	if count <= 0 {
		return nil
	}
	events := make([]keyboardInput, 0, count*2)
	for range count {
		events = append(events,
			keyboardInput{inputType: inputKeyboard, wVk: vkBack},
			keyboardInput{inputType: inputKeyboard, wVk: vkBack, dwFlags: keyEventfKeyUp},
		)
	}
	return send(events)
}

func (w *windowsTyper) close() error { return nil }

func send(events []keyboardInput) error {
	if len(events) == 0 {
		return nil
	}
	sent, _, err := sendInput.Call(
		uintptr(len(events)),
		uintptr(unsafe.Pointer(&events[0])),
		uintptr(inputSizeOf),
	)
	if int(sent) != len(events) {
		// The usual cause is UIPI: a window running elevated will not accept
		// input from a process that is not.
		return fmt.Errorf("SendInput delivered %d of %d events: %w "+
			"(the focused window may be running as administrator)", sent, len(events), err)
	}
	return nil
}
