//go:build darwin && cgo

package platform

/*
#cgo LDFLAGS: -framework ApplicationServices -framework CoreFoundation
#include <ApplicationServices/ApplicationServices.h>

// Process id of the window that is frontmost on screen.
//
// CGWindowListCopyWindowInfo returns windows in front-to-back order, so the
// first one at layer 0 is what the user is looking at. Layer 0 excludes the
// menu bar, the Dock and other system furniture, which would otherwise be
// reported as a focus change every time the pointer crossed them.
static int ld_frontmost_pid(void) {
    CFArrayRef windows = CGWindowListCopyWindowInfo(
        kCGWindowListOptionOnScreenOnly | kCGWindowListExcludeDesktopElements,
        kCGNullWindowID);
    if (windows == NULL) {
        return -1;
    }

    int pid = -1;
    CFIndex count = CFArrayGetCount(windows);
    for (CFIndex i = 0; i < count; i++) {
        CFDictionaryRef window = (CFDictionaryRef)CFArrayGetValueAtIndex(windows, i);
        if (window == NULL) continue;

        CFNumberRef layerRef = (CFNumberRef)CFDictionaryGetValue(window, kCGWindowLayer);
        int layer = 0;
        if (layerRef == NULL || !CFNumberGetValue(layerRef, kCFNumberIntType, &layer)) continue;
        if (layer != 0) continue;

        CFNumberRef pidRef = (CFNumberRef)CFDictionaryGetValue(window, kCGWindowOwnerPID);
        if (pidRef != NULL && CFNumberGetValue(pidRef, kCFNumberIntType, &pid)) {
            break;
        }
    }

    CFRelease(windows);
    return pid;
}
*/
import "C"

import (
	"errors"
	"strconv"
)

type darwinFocus struct{}

func newFocusWatcher() FocusWatcher { return darwinFocus{} }

func (darwinFocus) Current() (string, error) {
	pid := int(C.ld_frontmost_pid())
	if pid <= 0 {
		// Screen Recording permission gates the window list on some macOS
		// versions. Without it there is nothing to compare, so the session
		// skips the check rather than aborting on every poll.
		return "", errors.New("the frontmost window could not be determined")
	}
	return "pid:" + strconv.Itoa(pid), nil
}

func (darwinFocus) Close() error { return nil }
