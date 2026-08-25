package singleton

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// Acquire claims the single-instance lock with a named mutex.
//
// A kernel object rather than a lock file, because Windows releases it when
// the process dies however it dies. A file left behind by a crash would make
// the app refuse to start until someone deleted it, which is a worse failure
// than the one being prevented.
//
// The names are prefixed Local\, which scopes them to the logon session: two
// people signed in to the same machine each get their own copy, which is
// right — they have their own settings, their own models and their own tray.
func Acquire(id string) (*Lock, error) {
	mutexName, err := windows.UTF16PtrFromString(`Local\` + id)
	if err != nil {
		return nil, err
	}
	eventName, err := windows.UTF16PtrFromString(`Local\` + id + `.show`)
	if err != nil {
		return nil, err
	}

	// Created before the mutex is tested: if this copy turns out to be the
	// second one, the event it needs to signal is the one the first copy is
	// already waiting on, and CreateEvent returns a handle to that same object.
	event, err := windows.CreateEvent(nil, 0 /* auto-reset */, 0 /* unsignalled */, eventName)
	if err != nil && err != windows.ERROR_ALREADY_EXISTS {
		return nil, fmt.Errorf("create the show event: %w", err)
	}

	mutex, err := windows.CreateMutex(nil, false, mutexName)
	if err != nil && err != windows.ERROR_ALREADY_EXISTS {
		windows.CloseHandle(event)
		return nil, fmt.Errorf("create the instance mutex: %w", err)
	}
	if err == windows.ERROR_ALREADY_EXISTS {
		// Someone else has it. Ask them to come forward, then go away. The
		// event is set even if nobody is listening: it is auto-reset, so an
		// older copy that does not watch for it leaves nothing behind.
		_ = windows.SetEvent(event)
		windows.CloseHandle(event)
		windows.CloseHandle(mutex)
		return nil, ErrAlreadyRunning
	}

	lock := &Lock{show: make(chan struct{}, 1)}
	stop := make(chan struct{})
	lock.release = func() {
		close(stop)
		windows.CloseHandle(event)
		windows.CloseHandle(mutex)
	}

	go func() {
		for {
			// A five-second wait rather than an infinite one so this goroutine
			// notices Release and stops, instead of holding the handle open
			// against a process that is trying to exit.
			state, err := windows.WaitForSingleObject(event, 5000)
			select {
			case <-stop:
				return
			default:
			}
			if err != nil {
				return
			}
			if state == uint32(windows.WAIT_OBJECT_0) {
				select {
				case lock.show <- struct{}{}:
				default: // one pending request is as good as several
				}
			}
		}
	}()

	return lock, nil
}
