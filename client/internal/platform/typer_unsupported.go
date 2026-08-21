//go:build (!darwin && !windows) || (darwin && !cgo)

package platform

import "errors"

// Neither macOS nor Windows, or a macOS build without cgo. There is no portable
// way to type into another application, so the client reports it rather than
// pretending to work.
func newPlatform() (platformType, error) {
	return nil, errors.New("writing text at the cursor is only supported on Windows and macOS")
}

func available() (bool, string) {
	return false, "Writing text at the cursor is only supported on Windows and macOS."
}
