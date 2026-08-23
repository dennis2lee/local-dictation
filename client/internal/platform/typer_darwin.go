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

// Asks, rather than only checking.
//
// AXIsProcessTrusted answers the question silently, and an app that only ever
// asks silently never appears in System Settings > Privacy & Security >
// Accessibility at all — there is nothing for the user to switch on, and the
// instructions we print name a row that is not there. Passing
// kAXTrustedCheckOptionPrompt is what puts it in the list and shows the system
// dialog offering to open the pane.
//
// Only called when a silent check has already said no — see newPlatform. The
// prompting call is documented not to show anything to an app that is already
// trusted, but "documented not to" is a poor thing to rest a dialog on when
// the dialog appears at launch, so the decision is made here where it can be
// read.
static int ld_request_trust(void) {
    CFStringRef keys[] = { kAXTrustedCheckOptionPrompt };
    CFTypeRef values[] = { kCFBooleanTrue };
    CFDictionaryRef options = CFDictionaryCreate(
        kCFAllocatorDefault, (const void **)keys, (const void **)values, 1,
        &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
    if (options == NULL) {
        return AXIsProcessTrusted() ? 1 : 0;
    }
    Boolean trusted = AXIsProcessTrustedWithOptions(options);
    CFRelease(options);
    return trusted ? 1 : 0;
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
	// Already trusted is the common case, and it must be silent. Asking is for
	// the first launch only: the difference between asking and checking is
	// whether the app is in the Accessibility list at all — macOS adds it when
	// it asks, and until then the instructions below name a row the user
	// cannot find.
	if C.ld_is_trusted() == 1 {
		return &keystrokeComposer{typer: &darwinTyper{}}, nil
	}
	if C.ld_request_trust() == 0 {
		return nil, errors.New(
			"Local Dictation is not trusted for Accessibility, so it cannot type. " +
				"macOS should have offered to open System Settings > Privacy & " +
				"Security > Accessibility — turn Local Dictation on there, then " +
				"restart the app. If the row is missing, add it with + and pick " +
				"Local Dictation from Applications")
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
