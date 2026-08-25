package singleton

import (
	"errors"
	"testing"
	"time"
)

func TestASecondCopyIsRefused(t *testing.T) {
	// The regression: launching the app again added a second tray icon, a
	// second registration of the same global shortcut, and a second speech
	// server on its own ports — none of which announces itself.
	Dir(t.TempDir())

	first, err := Acquire("test.singleton.refused")
	if err != nil {
		t.Fatalf("the first copy could not take the lock: %v", err)
	}
	defer first.Release()

	second, err := Acquire("test.singleton.refused")
	if !errors.Is(err, ErrAlreadyRunning) {
		second.Release()
		t.Fatalf("a second copy started; err = %v", err)
	}
	if second != nil {
		t.Error("a refused copy still got a lock back")
	}
}

func TestTheLockIsReleasedWhenTheAppQuits(t *testing.T) {
	// Quitting and relaunching has to work. A lock that outlived the process
	// would be worse than the problem: the app would refuse to start until
	// someone found and deleted something.
	Dir(t.TempDir())

	first, err := Acquire("test.singleton.released")
	if err != nil {
		t.Fatal(err)
	}
	first.Release()

	second, err := Acquire("test.singleton.released")
	if err != nil {
		t.Fatalf("the lock survived Release: %v", err)
	}
	second.Release()
}

func TestTwoDifferentAppsDoNotBlockEachOther(t *testing.T) {
	Dir(t.TempDir())

	one, err := Acquire("test.singleton.a")
	if err != nil {
		t.Fatal(err)
	}
	defer one.Release()

	two, err := Acquire("test.singleton.b")
	if err != nil {
		t.Fatalf("a different id was refused: %v", err)
	}
	two.Release()
}

func TestTheRunningCopyIsAskedToShowItself(t *testing.T) {
	// Without this the second launch appears to do nothing, which reads as the
	// app being broken rather than as it already being open.
	Dir(t.TempDir())

	first, err := Acquire("test.singleton.show")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()

	if _, err := Acquire("test.singleton.show"); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("expected the second copy to be refused, got %v", err)
	}

	select {
	case <-first.Show():
	case <-time.After(3 * time.Second):
		t.Error("the running copy was never asked to come forward")
	}
}

func TestAReleasedLockIsSafeToReleaseAgain(t *testing.T) {
	Dir(t.TempDir())
	lock, err := Acquire("test.singleton.twice")
	if err != nil {
		t.Fatal(err)
	}
	lock.Release()
	lock.Release() // must not panic
	var absent *Lock
	absent.Release()
	if absent.Show() != nil {
		t.Error("a nil lock reported a show channel")
	}
}
