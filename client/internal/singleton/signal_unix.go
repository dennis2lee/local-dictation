//go:build !windows

package singleton

import (
	"os"
	"os/signal"
	"syscall"
)

// watchSignal turns SIGUSR1 into a request to show the window. A later launch
// sends it after finding the lock held.
func watchSignal(lock *Lock) {
	incoming := make(chan os.Signal, 1)
	signal.Notify(incoming, syscall.SIGUSR1)
	go func() {
		for range incoming {
			select {
			case lock.show <- struct{}{}:
			default:
			}
		}
	}()
}
