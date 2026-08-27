package localserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/dennis2lee/local-dictation/client/internal/protocol"
)

// Manager owns the local servers for the lifetime of the app.
//
// Servers are started lazily, per language. Each instance holds the model in
// memory, so starting both up front would double the footprint for a user who
// only ever dictates in one language — and starting the second one takes as
// long as the first did, which the user is not waiting on.
type Manager struct {
	mu       sync.Mutex
	settings ManagerSettings
	// python and serverDir are derived by Prepare from settings.PythonPath and
	// settings.ServerDir. They live apart from the settings so that comparing
	// old and new settings in Update compares what the user configured, not
	// what resolution produced.
	python    string
	serverDir string
	servers   map[protocol.Language]*Server
}

// ManagerSettings is the subset of client configuration local mode needs.
type ManagerSettings struct {
	PythonPath string
	ServerDir  string
	ModelPath  string
	// DraftModelPath is the optional small model that produces partial text;
	// see docs/latency.md. Empty runs a single model.
	DraftModelPath string
	VadModelPath   string
	StateDir       string
	CPUThreads     int
	MinSpeechMs    int
	// Backend is the hardware the server decodes on. Empty means CPU, which
	// is what every settings file written before this field existed says.
	Backend Backend
	// Fixed ports, or 0 to choose free ones.
	KoreanPort  int
	EnglishPort int
}

func NewManager(settings ManagerSettings) *Manager {
	return &Manager{settings: settings, servers: make(map[protocol.Language]*Server)}
}

// Prepare resolves the interpreter and installs dependencies if needed. It is
// safe to call repeatedly; after the first success it is nearly free.
func (m *Manager) Prepare(ctx context.Context, progress func(string)) error {
	m.mu.Lock()
	settings := m.settings
	alreadyPrepared := m.python != ""
	m.mu.Unlock()

	if alreadyPrepared {
		return nil
	}

	serverDir, err := ResolveServerDir(settings.ServerDir)
	if err != nil {
		return err
	}

	base, err := FindPython(ctx, settings.PythonPath, defaultCandidates())
	if err != nil {
		return err
	}

	// One environment per backend, never shared: see Backend.VenvName.
	backend := settings.Backend.Normalise()
	venvDir := filepath.Join(settings.StateDir, backend.VenvName())
	wheelDir := filepath.Join(filepath.Dir(serverDir), backend.WheelDirName())
	python, err := EnsureRuntime(ctx, base, venvDir, wheelDir, backend, progress)
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.python = python
	m.serverDir = serverDir
	m.mu.Unlock()
	return nil
}

// Ensure returns a ready server for a language, starting it if necessary.
func (m *Manager) Ensure(ctx context.Context, language protocol.Language, progress func(string)) (*Server, error) {
	if err := m.Prepare(ctx, progress); err != nil {
		return nil, err
	}

	m.mu.Lock()
	existing, running := m.servers[language]
	if running {
		if exited, _ := existing.Exited(); exited {
			delete(m.servers, language)
			running = false
		}
	}
	settings, python, serverDir := m.settings, m.python, m.serverDir
	m.mu.Unlock()

	if running {
		return existing, nil
	}

	if progress != nil {
		progress(fmt.Sprintf("Starting the %s server…", language))
	}

	port := settings.KoreanPort
	if language == protocol.English {
		port = settings.EnglishPort
	}

	server, err := Start(ctx, Options{
		PythonPath:     python,
		ServerDir:      serverDir,
		ModelPath:      settings.ModelPath,
		DraftModelPath: settings.DraftModelPath,
		VadModelPath:   settings.VadModelPath,
		Language:       language,
		Port:           port,
		CPUThreads:     settings.CPUThreads,
		MinSpeechMs:    settings.MinSpeechMs,
		StateDir:       settings.StateDir,
		Backend:        settings.Backend,
	})
	if err != nil {
		return nil, err
	}

	if progress != nil {
		progress("Loading the model — this takes a while the first time…")
	}
	if err := server.WaitReady(ctx); err != nil {
		_ = server.Stop(context.Background())
		return nil, err
	}
	if progress != nil {
		progress(fmt.Sprintf("%s server ready on port %d.", language, server.Port()))
	}

	m.mu.Lock()
	m.servers[language] = server
	m.mu.Unlock()
	return server, nil
}

// Running reports the server for a language, if one is up.
func (m *Manager) Running(language protocol.Language) (*Server, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	server, ok := m.servers[language]
	if ok {
		if exited, _ := server.Exited(); exited {
			return nil, false
		}
	}
	return server, ok
}

// StopAll shuts every local server down. Called on quit.
func (m *Manager) StopAll(ctx context.Context) error {
	m.mu.Lock()
	servers := make([]*Server, 0, len(m.servers))
	for _, server := range m.servers {
		servers = append(servers, server)
	}
	m.servers = make(map[protocol.Language]*Server)
	m.mu.Unlock()

	var firstErr error
	for _, server := range servers {
		if err := server.Stop(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Update swaps in new settings, stopping any server whose configuration
// changed. A model path change has to restart the process — the weights are
// loaded once at startup.
func (m *Manager) Update(ctx context.Context, settings ManagerSettings) error {
	m.mu.Lock()
	// Every field feeds either the generated config or the process
	// environment, so any difference means the running servers are stale.
	restartNeeded := m.settings != settings
	// The cached interpreter and resolved directory are derived from these
	// two; comparing *before* the assignment is what makes a change actually
	// take effect rather than being compared against itself.
	// Backend belongs here too: it decides which environment Prepare builds,
	// so a cached interpreter from the previous backend is the wrong one.
	if m.settings.PythonPath != settings.PythonPath ||
		m.settings.ServerDir != settings.ServerDir ||
		m.settings.Backend.Normalise() != settings.Backend.Normalise() {
		m.python = ""
		m.serverDir = ""
	}
	m.settings = settings
	m.mu.Unlock()

	if restartNeeded {
		return m.StopAll(ctx)
	}
	return nil
}

// ResolveServerDir finds the directory holding the `app` package.
//
// The installers place the server next to the executable; a developer runs from
// a checkout. Both are looked for, in that order, so neither needs a config
// entry to work.
func ResolveServerDir(configured string) (string, error) {
	var candidates []string
	if configured != "" {
		candidates = append(candidates, configured)
	}

	if executable, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(executable); err == nil {
			executable = resolved
		}
		dir := filepath.Dir(executable)
		candidates = append(candidates,
			filepath.Join(dir, "server"),                    // Windows: alongside the .exe
			filepath.Join(dir, "..", "Resources", "server"), // macOS: inside the .app bundle
			filepath.Join(dir, "..", "server"),
			filepath.Join(dir, "..", "..", "server"), // a Go build in client/bin
		)
	}
	// Walk up from the working directory so `go run ./cmd/...` finds the
	// checkout's server no matter which subdirectory it was started from.
	if working, err := os.Getwd(); err == nil {
		for depth := range 5 {
			candidates = append(candidates,
				filepath.Join(working, strings.Repeat(".."+string(filepath.Separator), depth), "server"))
		}
	}

	var tried []string
	for _, candidate := range candidates {
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(absolute, "app", "main.py")); err == nil {
			return absolute, nil
		}
		tried = append(tried, absolute)
	}
	return "", fmt.Errorf("could not find the server (looked in %v); set it in Settings", tried)
}

func defaultCandidates() []string {
	if runtime.GOOS == "windows" {
		return []string{"python3.12.exe", "python3.11.exe", "python.exe", "py.exe"}
	}
	return []string{"python3.13", "python3.12", "python3.11", "python3"}
}
