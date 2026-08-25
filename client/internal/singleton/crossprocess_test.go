package singleton

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The bug is two processes, not two calls. Within one process the platform
// primitives can behave differently — flock is per file description, so a
// same-process test can pass on a system where the real thing would not — and
// on Windows the mutex is a kernel object this test process never exercises at
// all. So the second copy is a second copy.
func TestASecondProcessIsRefused(t *testing.T) {
	dir := t.TempDir()
	Dir(dir)

	held, err := Acquire("test.crossprocess")
	if err != nil {
		t.Fatalf("this process could not take the lock: %v", err)
	}
	defer held.Release()

	out, err := runChild(t, dir, "test.crossprocess")
	if err == nil {
		t.Fatalf("a second process started; it printed %q", out)
	}
	if !strings.Contains(out, "already running") {
		t.Errorf("the second process failed for the wrong reason: %q", out)
	}

	// And it asked this one to come forward on its way out.
	select {
	case <-held.Show():
	case <-time.After(5 * time.Second):
		t.Error("the second process exited without asking this one to show itself")
	}
}

// The other half: once the first copy is gone, the next launch must work.
// A lock that outlives the process would leave the app unable to start.
func TestAnotherProcessCanTakeTheLockAfterwards(t *testing.T) {
	dir := t.TempDir()
	Dir(dir)

	held, err := Acquire("test.afterwards")
	if err != nil {
		t.Fatal(err)
	}
	held.Release()

	if out, err := runChild(t, dir, "test.afterwards"); err != nil {
		t.Errorf("a later process was refused: %v (%q)", err, out)
	}
}

// runChild re-runs this test binary as a separate process, which lands in the
// TestMain branch below and does nothing but try to take the lock.
func runChild(t *testing.T, dir, id string) (string, error) {
	t.Helper()
	command := exec.Command(os.Args[0])
	command.Env = append(os.Environ(),
		"LD_SINGLETON_CHILD=1",
		"LD_SINGLETON_DIR="+dir,
		"LD_SINGLETON_ID="+id,
	)
	raw, err := command.CombinedOutput()
	return strings.TrimSpace(string(raw)), err
}

func TestMain(m *testing.M) {
	if os.Getenv("LD_SINGLETON_CHILD") == "" {
		os.Exit(m.Run())
	}

	Dir(os.Getenv("LD_SINGLETON_DIR"))
	lock, err := Acquire(os.Getenv("LD_SINGLETON_ID"))
	if errors.Is(err, ErrAlreadyRunning) {
		os.Stdout.WriteString("already running\n")
		os.Exit(3)
	}
	if err != nil {
		os.Stdout.WriteString("error: " + err.Error() + "\n")
		os.Exit(4)
	}
	lock.Release()
	os.Stdout.WriteString("acquired\n")
	os.Exit(0)
}
