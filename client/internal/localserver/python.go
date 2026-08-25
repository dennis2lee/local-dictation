// Package localserver lets the client be the server.
//
// The plan's standalone mode says the client must work with no remote server.
// It does that by starting the same Python server on 127.0.0.1 and speaking the
// same protocol to it — not by reimplementing Whisper in Go. One inference
// implementation, one streaming policy, one set of tests. The only thing that
// changes between modes is the URL.
package localserver

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// MinimumPython is the oldest interpreter the server supports.
var MinimumPython = Version{3, 11}

// Version is a major.minor Python version.
type Version struct{ Major, Minor int }

func (v Version) String() string { return fmt.Sprintf("%d.%d", v.Major, v.Minor) }

// AtLeast reports whether v is new enough.
func (v Version) AtLeast(other Version) bool {
	if v.Major != other.Major {
		return v.Major > other.Major
	}
	return v.Minor >= other.Minor
}

// Interpreter is a usable Python found on this machine.
type Interpreter struct {
	Path    string
	Version Version
}

// ErrNoPython means no interpreter new enough was found.
var ErrNoPython = errors.New("no suitable Python interpreter found")

// FindPython returns the first interpreter that is new enough.
//
// `explicit` wins if set — someone who pointed at a specific interpreter meant
// it, and silently using a different one would be baffling.
func FindPython(ctx context.Context, explicit string, candidates []string) (Interpreter, error) {
	var tried []string

	check := func(name string) (Interpreter, bool) {
		path, err := exec.LookPath(name)
		if err != nil {
			tried = append(tried, fmt.Sprintf("%s (not found)", name))
			return Interpreter{}, false
		}
		version, err := pythonVersion(ctx, path)
		if err != nil {
			tried = append(tried, fmt.Sprintf("%s (%v)", path, err))
			return Interpreter{}, false
		}
		if !version.AtLeast(MinimumPython) {
			tried = append(tried, fmt.Sprintf("%s (Python %s, need %s+)", path, version, MinimumPython))
			return Interpreter{}, false
		}
		return Interpreter{Path: path, Version: version}, true
	}

	if strings.TrimSpace(explicit) != "" {
		interpreter, ok := check(explicit)
		if !ok {
			return Interpreter{}, fmt.Errorf("%w: %s", ErrNoPython, strings.Join(tried, "; "))
		}
		return interpreter, nil
	}

	for _, name := range candidates {
		if interpreter, ok := check(name); ok {
			return interpreter, nil
		}
	}
	return Interpreter{}, fmt.Errorf("%w (tried %s); install Python %s or newer",
		ErrNoPython, strings.Join(tried, ", "), MinimumPython)
}

func pythonVersion(ctx context.Context, path string) (Version, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	output, err := exec.CommandContext(ctx, path,
		"-c", "import sys; print('%d.%d' % sys.version_info[:2])").Output()
	if err != nil {
		return Version{}, fmt.Errorf("could not run it: %w", err)
	}
	parts := strings.Split(strings.TrimSpace(string(output)), ".")
	if len(parts) != 2 {
		return Version{}, fmt.Errorf("unexpected version output %q", string(output))
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return Version{}, fmt.Errorf("unexpected major version %q", parts[0])
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return Version{}, fmt.Errorf("unexpected minor version %q", parts[1])
	}
	return Version{major, minor}, nil
}

// MissingModules reports which of the named modules this interpreter cannot
// import. An empty slice means the runtime is ready to serve.
//
// The list comes from the backend rather than being fixed here: each one wants
// a different inference package, and an environment built for one of them is
// missing the others by design, not by accident.
func MissingModules(ctx context.Context, pythonPath string, modules []string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	const script = `
import importlib.util, sys
for name in sys.argv[1:]:
    if importlib.util.find_spec(name) is None:
        print(name)
`
	args := append([]string{"-c", script}, modules...)
	command := exec.CommandContext(ctx, pythonPath, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("probe dependencies: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	var missing []string
	scanner := bufio.NewScanner(&stdout)
	for scanner.Scan() {
		if name := strings.TrimSpace(scanner.Text()); name != "" {
			missing = append(missing, name)
		}
	}
	return missing, scanner.Err()
}

// VenvPython is the interpreter inside a virtual environment.
func VenvPython(venvDir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(venvDir, "Scripts", "python.exe")
	}
	return filepath.Join(venvDir, "bin", "python")
}

// EnsureRuntime makes `venvDir` into an environment that can run the server,
// creating it and installing dependencies if needed. It returns the path of the
// interpreter to use.
//
// `wheelDir`, when it exists, is used as the only package source — that is how
// a closed network installs without an index. Otherwise pip uses whatever index
// it is configured with.
//
// Progress lines are streamed to `progress` so the Settings tab can show what
// is happening; installing ctranslate2 is not fast, and a frozen window looks
// like a hang.
func EnsureRuntime(ctx context.Context, base Interpreter, venvDir, wheelDir string, backend Backend, progress func(string)) (string, error) {
	if progress == nil {
		progress = func(string) {}
	}

	venvPython := VenvPython(venvDir)
	if _, err := os.Stat(venvPython); err != nil {
		progress(fmt.Sprintf("Creating a Python environment with %s (Python %s)…", base.Path, base.Version))
		if err := stream(ctx, progress, base.Path, "-m", "venv", venvDir); err != nil {
			return "", fmt.Errorf("create the virtual environment: %w", err)
		}
	}

	missing, err := MissingModules(ctx, venvPython, backend.Modules())
	if err != nil {
		return "", err
	}
	if len(missing) == 0 {
		progress("Python environment is ready.")
		return venvPython, nil
	}

	progress(fmt.Sprintf("Installing server dependencies (%s)…", strings.Join(missing, ", ")))
	args := []string{"-m", "pip", "install", "--disable-pip-version-check", "--no-input"}
	if info, statErr := os.Stat(wheelDir); statErr == nil && info.IsDir() {
		// Offline install from the wheels shipped alongside the app.
		progress("Using the bundled wheels; no network access needed.")
		args = append(args, "--no-index", "--find-links", wheelDir)
	}
	args = append(args, backend.PackageSpecs()...)

	if err := stream(ctx, progress, venvPython, args...); err != nil {
		return "", fmt.Errorf("install dependencies: %w", err)
	}

	if missing, err = MissingModules(ctx, venvPython, backend.Modules()); err != nil {
		return "", err
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("still missing after install: %s", strings.Join(missing, ", "))
	}
	progress("Python environment is ready.")
	return venvPython, nil
}

// stream runs a command, forwarding its interleaved stdout and stderr to
// `progress` a line at a time.
func stream(ctx context.Context, progress func(string), name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	reader, writer := io.Pipe()
	command.Stdout = writer
	command.Stderr = writer

	if err := command.Start(); err != nil {
		writer.Close()
		return err
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			if line := strings.TrimRight(scanner.Text(), "\r"); line != "" {
				progress(line)
			}
		}
	}()

	err := command.Wait()
	writer.Close()
	<-done
	return err
}
