//go:build !windows

package singleton

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// Acquire claims the single-instance lock with an advisory file lock.
//
// flock rather than the presence of a file: the kernel drops it when the
// process dies, so a crash cannot leave the app unable to start. The file
// carries the running process's id so a second launch has someone to signal.
//
// `id` is used as the file name under the directory given to Dir.
func Acquire(id string) (*Lock, error) {
	path := filepath.Join(lockDir, id+".lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		defer file.Close()
		if pid := readPID(path); pid > 0 {
			// Best effort: the running copy shows itself if it is listening.
			// A copy too old to know about the signal ignores it, and this
			// one exits either way.
			_ = syscall.Kill(pid, syscall.SIGUSR1)
		}
		return nil, ErrAlreadyRunning
	}

	if err := file.Truncate(0); err == nil {
		_, _ = file.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0)
		_ = file.Sync()
	}

	lock := &Lock{show: make(chan struct{}, 1)}
	lock.release = func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		_ = os.Remove(path)
	}
	watchSignal(lock)
	return lock, nil
}

func readPID(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil {
		return 0
	}
	return pid
}

// lockDir is where the lock file goes. Set by Dir before Acquire.
var lockDir = os.TempDir()

// Dir points the lock at a directory the app already owns, so the file sits
// beside the settings rather than in a shared temporary directory where
// another user's copy could own the same name.
func Dir(dir string) {
	if dir != "" {
		lockDir = dir
	}
}
