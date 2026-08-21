//go:build !windows

package localserver

import (
	"os/exec"
	"syscall"
)

// configureProcess puts the child in its own process group so a Ctrl+C in a
// terminal-launched client does not race the client's own shutdown path.
func configureProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminate asks the server to shut down gracefully. uvicorn handles SIGTERM by
// finishing in-flight work first, which is how a final transcript still reaches
// the user when the app is quit mid-sentence.
func terminate(command *exec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	// Negative PID signals the whole group, catching any helper uvicorn spawned.
	if err := syscall.Kill(-command.Process.Pid, syscall.SIGTERM); err == nil {
		return nil
	}
	return command.Process.Signal(syscall.SIGTERM)
}
