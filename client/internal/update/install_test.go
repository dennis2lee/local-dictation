package update

import (
	"errors"
	"strings"
	"testing"
)

// The shape of these commands is the part that would go wrong quietly: an
// installer that is never launched leaves the user with a downloaded file and
// a message, which is what this replaced.
func TestTheInstallerForEachPlatform(t *testing.T) {
	name, args, err := InstallCommand("darwin", "/tmp/LocalDictation-0.2.0.pkg")
	if err != nil {
		t.Fatalf("darwin: %v", err)
	}
	if name != "/usr/bin/open" || len(args) != 1 || args[0] != "/tmp/LocalDictation-0.2.0.pkg" {
		t.Errorf("darwin runs %s %v", name, args)
	}

	name, args, err = InstallCommand("windows", `C:\Users\x\Downloads\LocalDictation-0.2.0-x64.msi`)
	if err != nil {
		t.Fatalf("windows: %v", err)
	}
	if name != "msiexec.exe" {
		t.Errorf("windows runs %s", name)
	}
	if strings.Join(args, " ") != `/i C:\Users\x\Downloads\LocalDictation-0.2.0-x64.msi /passive /norestart` {
		t.Errorf("windows args = %v", args)
	}
}

// Only the artifact this project publishes for the running platform. A file
// that is not one is a bug somewhere upstream, and running it anyway is how an
// updater becomes a way to execute arbitrary things.
func TestOnlyThePlatformsOwnInstallerIsLaunched(t *testing.T) {
	for _, refused := range []struct{ goos, path string }{
		{"darwin", "/tmp/LocalDictation-0.2.0-x64.msi"},
		{"darwin", "/tmp/something.sh"},
		{"windows", "/tmp/LocalDictation-0.2.0.pkg"},
		{"windows", `C:\tmp\payload.exe`},
	} {
		if _, _, err := InstallCommand(refused.goos, refused.path); err == nil {
			t.Errorf("%s accepted %s", refused.goos, refused.path)
		}
	}
}

func TestAPlatformWithNoInstallerSaysSo(t *testing.T) {
	_, _, err := InstallCommand("linux", "/tmp/whatever.deb")
	if !errors.Is(err, ErrUnsupportedInstaller) {
		t.Fatalf("err = %v, want ErrUnsupportedInstaller", err)
	}
	if _, _, err := RestartCommand("linux", "/opt/app"); !errors.Is(err, ErrUnsupportedInstaller) {
		t.Fatalf("restart err = %v", err)
	}
}

// The restart waits for the installer rather than sleeping a guessed number of
// seconds: how long someone takes over an authorisation dialog is not
// something to guess at.
func TestTheRestartWaitsForTheInstallerToFinish(t *testing.T) {
	_, args, err := RestartCommand("darwin", "/Applications/Local Dictation.app")
	if err != nil {
		t.Fatal(err)
	}
	script := strings.Join(args, " ")
	if !strings.Contains(script, "pgrep -x Installer") {
		t.Errorf("darwin restart does not wait for the installer: %s", script)
	}
	if !strings.Contains(script, `open -a "/Applications/Local Dictation.app"`) {
		t.Errorf("darwin restart does not reopen the app: %s", script)
	}

	_, args, err = RestartCommand("windows", `C:\Program Files\Local Dictation\local-dictation.exe`)
	if err != nil {
		t.Fatal(err)
	}
	script = strings.Join(args, " ")
	if !strings.Contains(script, "msiexec.exe") {
		t.Errorf("windows restart does not wait for the installer: %s", script)
	}
}
