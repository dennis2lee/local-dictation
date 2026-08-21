package platform

// Focus tracking.
//
// Dictation types into whatever window has focus. If the user clicks into a
// different application mid-sentence, the rest of the sentence follows the
// cursor there — into a chat box, a search field, or worse, a password field.
// The plan calls this out as a medium risk with a specific mitigation: track
// the window the session started against, and cancel the provisional text when
// it changes.
//
// The identifier is opaque and carries no window title, so nothing here can
// leak what the user was doing.

// FocusWatcher reports which window has focus.
type FocusWatcher interface {
	// Current returns an opaque identifier for the focused window. Two calls
	// return the same value exactly when focus has not moved.
	Current() (string, error)
	// Close releases any OS resources.
	Close() error
}

// NewFocusWatcher returns a watcher for this platform, or nil when focus cannot
// be observed. A nil watcher means the session simply does not check — which is
// the honest behaviour, rather than pretending focus never changes.
func NewFocusWatcher() FocusWatcher { return newFocusWatcher() }
