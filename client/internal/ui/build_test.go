// The window is built once, at startup, and until now nothing exercised that.
// A widget constructor that fires its own change handler mid-build reaches an
// App whose fields are still nil, and the app dies before it draws anything —
// which is what these cover.
package ui

import (
	"testing"

	"fyne.io/fyne/v2/test"

	"github.com/dennis2lee/local-dictation/client/internal/config"
	"github.com/dennis2lee/local-dictation/client/internal/protocol"
)

// A bare App: no dialer, no controller, no window. Anything a tab constructor
// calls back into will dereference nil, which is the point — construction must
// not call back into the App at all.
func halfBuiltApp(settings config.Config) *App {
	return &App{settings: settings, fyne: test.NewApp()}
}

func testSettings() config.Config {
	settings := config.Default()
	settings.Mode = config.ModeLocal
	settings.Language = protocol.Korean
	settings.Local.ModelPath = "/models/large-v3-turbo"
	return settings
}

func TestTheMainTabBuildsWithoutCallingBackIntoTheApp(t *testing.T) {
	app := halfBuiltApp(testSettings())

	tab := newMainTab(app)

	if tab.language.Selected != "Korean" {
		t.Errorf("language radio shows %q, want Korean", tab.language.Selected)
	}
	if tab.language.OnChanged == nil {
		t.Error("the language handler was never attached, so the radio is inert")
	}
	if tab.shortcut == nil || tab.status == nil || tab.detail == nil {
		t.Error("the tab finished construction with widgets missing")
	}
}

// buildServerSection, not newSettingsTab: the rest of the tab reaches for the
// audio backend, and the mode radio is what this is about.
func TestTheSettingsTabBuildsWithoutCallingBackIntoTheApp(t *testing.T) {
	settings := testSettings()
	tab := &settingsTab{app: halfBuiltApp(settings)}

	tab.buildServerSection(settings)

	if tab.mode.Selected != "This computer" {
		t.Errorf("mode radio shows %q, want \"This computer\"", tab.mode.Selected)
	}
	if tab.mode.OnChanged == nil {
		t.Error("the mode handler was never attached, so the radio is inert")
	}
	// The initial mode still has to decide whether the remote half is on
	// screen, even though the handler no longer runs during construction.
	if !tab.remoteBox.Hidden {
		t.Error("local mode was selected but the remote settings are showing")
	}
}

// The local server's settings have their own tab now, so the mode no longer
// hides them: hiding them would empty that tab, and someone may well want to
// point the built-in server at a model before switching to it.
func TestTheLocalServerSettingsStayVisibleInRemoteMode(t *testing.T) {
	settings := testSettings()
	settings.Mode = config.ModeRemote

	tab := &settingsTab{app: halfBuiltApp(settings)}
	tab.buildServerSection(settings)
	tab.buildLocalServerSection(settings)

	if tab.localBox.Hidden {
		t.Error("the local server's tab would be blank in remote mode")
	}
	tab.mode.SetSelected("This computer")
	if tab.localBox.Hidden {
		t.Error("switching to local mode hid the local server's settings")
	}
}

func TestTheSettingsTabShowsTheRemoteHalfInRemoteMode(t *testing.T) {
	settings := testSettings()
	settings.Mode = config.ModeRemote
	settings.Remote.Host = "dictation.internal"

	tab := &settingsTab{app: halfBuiltApp(settings)}
	tab.buildServerSection(settings)

	if tab.remoteBox.Hidden {
		t.Error("remote mode was selected but the remote settings are hidden")
	}
}

// The certificate fields are for a managed deployment. Someone connecting a
// laptop to a Mac on the same network has nothing to put in them, and three
// blank required-looking fields are how a working setup looks broken.
func TestCertificateFieldsAreHiddenUntilTLSIsOn(t *testing.T) {
	settings := testSettings()
	settings.Mode = config.ModeRemote
	settings.Remote.Host = "192.168.1.20"
	settings.Remote.TLS.Enabled = false

	tab := &settingsTab{app: halfBuiltApp(settings)}
	tab.buildServerSection(settings)

	if !tab.tlsBox.Hidden {
		t.Error("TLS is off but the certificate fields are showing")
	}
	if tab.useTLS.Checked {
		t.Error("the TLS switch is on when the settings say it is off")
	}

	tab.useTLS.SetChecked(true)
	if tab.tlsBox.Hidden {
		t.Error("TLS was switched on and the certificate fields stayed hidden")
	}

	tab.useTLS.SetChecked(false)
	if !tab.tlsBox.Hidden {
		t.Error("TLS was switched off and the certificate fields stayed visible")
	}
}

func TestCertificateFieldsShowWhenTLSIsAlreadyOn(t *testing.T) {
	settings := testSettings()
	settings.Mode = config.ModeRemote
	settings.Remote.Host = "dictation.internal"
	settings.Remote.TLS.Enabled = true

	tab := &settingsTab{app: halfBuiltApp(settings)}
	tab.buildServerSection(settings)

	if tab.tlsBox.Hidden {
		t.Error("TLS is on and the certificate fields are hidden")
	}
}

// Nothing about a plain LAN setup should fail validation.
func TestARemoteServerWithoutTLSIsValid(t *testing.T) {
	settings := testSettings()
	settings.Mode = config.ModeRemote
	settings.Remote.Host = "192.168.1.20"
	settings.Remote.KoreanPort, settings.Remote.EnglishPort = 8765, 8766
	settings.Remote.TLS.Enabled = false

	if err := settings.Validate(); err != nil {
		t.Fatalf("a remote server without certificates should be valid: %v", err)
	}
}
