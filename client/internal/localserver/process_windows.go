//go:build windows

package localserver

import (
	"os/exec"
	"syscall"
)

// configureProcess hides the console window the Python interpreter would
// otherwise flash up, and puts the child in its own group.
func configureProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

// terminate stops the server.
//
// Windows has no SIGTERM, and delivering CTRL_BREAK to another process group
// from a GUI process is unreliable. Killing is acceptable here because the
// client always flushes and waits for the final transcript before it stops the
// server, so there is no in-flight work to lose.
func terminate(command *exec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	return command.Process.Kill()
}
