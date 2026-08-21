// Package platform writes text at the cursor of whatever application has focus.
//
// Two mechanisms exist for provisional text on a desktop OS:
//
//  1. A real input method — Windows TSF, macOS InputMethodKit. Provisional text
//     is *marked*: underlined, owned by the IME, replaced atomically, and
//     discarded if the process dies. This is the mechanism the plan names, and
//     it is the better one.
//  2. Synthetic keystrokes. Provisional text is ordinary typed text, and
//     replacing it means sending backspaces.
//
// This package implements (2), and input.Platform is shaped so that (1) can be
// dropped in behind it without touching the composer or the session controller.
//
// The reason is an installation constraint, not a preference. A TSF text
// service is a COM DLL that has to be registered with the system and picked in
// the language bar; an InputMethodKit input method is a separate .app in
// /Library/Input Methods that the user selects from the input menu. Both turn
// "run the installer" into "run the installer, then configure your keyboard",
// and neither can be exercised without that step. Synthetic input works
// everywhere the moment the app has Accessibility permission, which the global
// shortcut already needs.
//
// What is given up, and is worth knowing before choosing:
//
//   - Partial text is not underlined. It looks like text the user typed, until
//     it is replaced.
//   - Replacing partial text sends backspaces, so an application with
//     aggressive autocorrect or an editor with auto-indent can interfere.
//   - Password fields must never receive dictation. See Guard.
package platform

import (
	"github.com/dennis2lee/local-dictation/client/internal/input"
)

// New returns the best text adapter available on this machine.
func New() (input.Platform, error) { return newPlatform() }

// Available reports whether text can be written at all, and why not if it
// cannot. The Settings tab shows this rather than letting the first dictation
// session be the moment the user finds out.
func Available() (bool, string) { return available() }

// platformType is what every build's newPlatform returns. Declared here so the
// per-OS files do not each need to import the input package.
type platformType = input.Platform
