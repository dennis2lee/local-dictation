//go:build darwin && cgo

package platform

/*
#cgo LDFLAGS: -framework ApplicationServices -framework CoreFoundation
#include <ApplicationServices/ApplicationServices.h>
#include <stdlib.h>

// Insert a UTF-16 string at the cursor.
//
// CGEventKeyboardSetUnicodeString attaches the string to a synthetic key event,
// which is how macOS lets you type text that has no key on the keyboard. The
// virtual keycode is 0 because no physical key is involved.
static void ld_type_utf16(const UniChar *text, int length) {
    CGEventRef down = CGEventCreateKeyboardEvent(NULL, 0, true);
    CGEventRef up   = CGEventCreateKeyboardEvent(NULL, 0, false);
    if (down == NULL || up == NULL) {
        if (down) CFRelease(down);
        if (up) CFRelease(up);
        return;
    }
    CGEventKeyboardSetUnicodeString(down, length, text);
    CGEventKeyboardSetUnicodeString(up, length, text);
    CGEventPost(kCGHIDEventTap, down);
    CGEventPost(kCGHIDEventTap, up);
    CFRelease(down);
    CFRelease(up);
}

// kVK_Delete is the Backspace key; macOS names it Delete.
static const CGKeyCode LD_BACKSPACE = 51;

static void ld_backspace(int count) {
    for (int i = 0; i < count; i++) {
        CGEventRef down = CGEventCreateKeyboardEvent(NULL, LD_BACKSPACE, true);
        CGEventRef up   = CGEventCreateKeyboardEvent(NULL, LD_BACKSPACE, false);
        if (down) { CGEventPost(kCGHIDEventTap, down); CFRelease(down); }
        if (up)   { CGEventPost(kCGHIDEventTap, up);   CFRelease(up); }
    }
}

static int ld_is_trusted(void) {
    return AXIsProcessTrusted() ? 1 : 0;
}
*/
import "C"

import (
	"errors"
	"unicode/utf16"
	"unsafe"
)

// unicodeChunk is how many UTF-16 units go into one synthetic event.
//
// CGEventKeyboardSetUnicodeString accepts more, but long strings are unreliable
// in practice: some applications only read the first part of the attached
// string. Twenty is the size that has always been safe.
const unicodeChunk = 20

type darwinTyper struct{}

func newPlatform() (platformType, error) {
	if C.ld_is_trusted() == 0 {
		return nil, errors.New(
			"Local Dictation is not trusted for Accessibility. " +
				"Open System Settings > Privacy & Security > Accessibility, " +
				"turn on Local Dictation, and restart the app")
	}
	return &keystrokeComposer{typer: &darwinTyper{}}, nil
}

func available() (bool, string) {
	if C.ld_is_trusted() == 0 {
		return false, "Accessibility permission is required. " +
			"System Settings > Privacy & Security > Accessibility."
	}
	return true, ""
}

func (d *darwinTyper) name() string { return "macOS synthetic input (CGEvent)" }

func (d *darwinTyper) typeText(text string) error {
	units := utf16.Encode([]rune(text))
	for start := 0; start < len(units); start += unicodeChunk {
		end := min(start+unicodeChunk, len(units))
		chunk := units[start:end]
		C.ld_type_utf16((*C.UniChar)(unsafe.Pointer(&chunk[0])), C.int(len(chunk)))
	}
	return nil
}

func (d *darwinTyper) backspace(count int) error {
	if count <= 0 {
		return nil
	}
	C.ld_backspace(C.int(count))
	return nil
}

func (d *darwinTyper) close() error { return nil }
