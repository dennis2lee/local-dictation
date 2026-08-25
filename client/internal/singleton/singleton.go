// Package singleton keeps one copy of the app running per user.
//
// The app lives in the tray. Launching it again — from the Start menu, from a
// desktop shortcut, from double-clicking the thing already running — used to
// start a second copy, which put a second icon in the tray, registered the same
// global shortcut twice and started its own speech server on its own ports.
// None of that is visible until something behaves oddly.
//
// So the second copy does not start. It asks the first one to show itself and
// exits, which is what someone launching an app that is already running meant.
package singleton

import "errors"

// ErrAlreadyRunning is returned by Acquire when another copy holds the lock.
// The caller has already been asked to show itself by the time this is seen.
var ErrAlreadyRunning = errors.New("another copy of Local Dictation is already running")

// Lock is one process's claim on being the only copy.
type Lock struct {
	release func()
	// show receives a value each time another launch asks this copy to come to
	// the front. Nil when the platform cannot report it.
	show chan struct{}
}

// Show reports launches by later copies. Reading from it is optional: the
// second copy exits either way.
func (l *Lock) Show() <-chan struct{} {
	if l == nil {
		return nil
	}
	return l.show
}

// Release gives the claim up. Safe on a nil Lock.
func (l *Lock) Release() {
	if l == nil || l.release == nil {
		return
	}
	l.release()
	l.release = nil
}
