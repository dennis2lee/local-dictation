//go:build (!darwin && !windows) || (darwin && !cgo)

package platform

// No way to observe focus on this build, so sessions do not check for it.
func newFocusWatcher() FocusWatcher { return nil }
