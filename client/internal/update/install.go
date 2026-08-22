package update

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Installing is deliberately the OS installer's job, not this application's.
//
// Both platforms put the app somewhere only an administrator can write, so
// replacing it needs an authenticated install either way. Handing the file to
// the platform installer means the password prompt is the system's own, shown
// by a process the user can verify — this application never sees a credential,
// and there is nothing here that could be talked into running an arbitrary
// file. What it launches is the artifact that was just checked against the
// hash published with the release, and nothing else.
//
// After the installer starts, the running application has to go: on both
// platforms the bundle it is executing from is what gets replaced. Restarting
// afterwards is a separate small process that outlives us, waits for the
// installer to finish, and opens the app again.

// ErrUnsupportedInstaller means this platform has no scripted install path.
var ErrUnsupportedInstaller = errors.New("no installer is available for this platform")

// InstallCommand is the command that installs a downloaded artifact.
//
// Split out and pure so the argument construction can be tested on any machine
// — the shape of these commands is the part worth getting wrong quietly.
func InstallCommand(goos, path string) (string, []string, error) {
	switch goos {
	case "darwin":
		// `open` hands the package to Installer.app, which asks for
		// authorisation itself and shows the user what it is about to do.
		// Deliberately not `sudo installer`: that would mean this process
		// collecting an administrator password, which it has no business
		// doing.
		if !strings.EqualFold(filepath.Ext(path), ".pkg") {
			return "", nil, fmt.Errorf("expected a .pkg, got %s", filepath.Base(path))
		}
		return "/usr/bin/open", []string{path}, nil
	case "windows":
		if !strings.EqualFold(filepath.Ext(path), ".msi") {
			return "", nil, fmt.Errorf("expected an .msi, got %s", filepath.Base(path))
		}
		// msiexec raises the UAC prompt itself. /passive shows progress and no
		// questions: everything it would ask is already decided by the package.
		return "msiexec.exe", []string{"/i", path, "/passive", "/norestart"}, nil
	default:
		return "", nil, fmt.Errorf("%w: %s", ErrUnsupportedInstaller, goos)
	}
}

// RestartCommand is a detached process that waits for the installer to finish
// and then opens the application again.
//
// It has to be a separate process for the obvious reason: the one that would
// otherwise do the waiting is the one being replaced. The wait is on the
// installer's own process rather than a fixed sleep, because how long a user
// takes over an authorisation dialog is not something to guess at.
func RestartCommand(goos, appPath string) (string, []string, error) {
	switch goos {
	case "darwin":
		return "/bin/sh", []string{"-c", fmt.Sprintf(
			// Wait for Installer.app to appear and then go away. The first
			// loop bounds how long we wait for it to show up at all, so a user
			// who dismisses the package without installing does not leave a
			// process waiting forever.
			`for i in $(seq 1 60); do pgrep -x Installer >/dev/null && break; sleep 1; done
			 while pgrep -x Installer >/dev/null; do sleep 1; done
			 sleep 2
			 open -a %q`, appPath)}, nil
	case "windows":
		return "cmd.exe", []string{"/c", fmt.Sprintf(
			`for /l %%%%i in (1,1,600) do @(tasklist /fi "imagename eq msiexec.exe" | find /i "msiexec.exe" >nul || (start "" %q & exit)) & timeout /t 1 >nul`,
			appPath)}, nil
	default:
		return "", nil, fmt.Errorf("%w: %s", ErrUnsupportedInstaller, goos)
	}
}

// Install launches the platform installer for a downloaded artifact, and a
// detached watcher that reopens the application once it finishes.
//
// It returns as soon as both are started. The caller is expected to quit
// immediately afterwards: the bundle it is running from is about to be
// replaced underneath it.
func Install(path, appPath string) error {
	name, args, err := InstallCommand(runtime.GOOS, path)
	if err != nil {
		return err
	}
	if appPath != "" {
		if watcher, watchArgs, err := RestartCommand(runtime.GOOS, appPath); err == nil {
			// Best effort by design. Failing to arrange the restart is not a
			// reason to refuse the update — the user can open the app again.
			_ = exec.Command(watcher, watchArgs...).Start()
		}
	}
	return exec.Command(name, args...).Start()
}
