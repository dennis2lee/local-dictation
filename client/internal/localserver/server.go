package localserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dennis2lee/local-dictation/client/internal/protocol"
)

// Options describes one language server to run on this machine.
type Options struct {
	PythonPath string
	ServerDir  string // the directory containing the `app` package
	ModelPath  string
	// DraftModelPath, when set, is a second, small model that produces the
	// partial text while ModelPath produces the committed text.
	DraftModelPath string
	VadModelPath   string
	Language       protocol.Language
	// Port 0 asks the OS for a free one, which avoids colliding with whatever
	// else the user happens to be running.
	Port       int
	CPUThreads int
	// Backend is the hardware to decode on. Empty means CPU.
	Backend Backend
	// StateDir holds the generated config and the server log.
	StateDir string
	// StartTimeout bounds how long we wait for readiness. Loading large-v3 from
	// a cold page cache is slow, so this is generous by design.
	StartTimeout time.Duration
}

func (o Options) withDefaults() Options {
	if o.StartTimeout == 0 {
		o.StartTimeout = 5 * time.Minute
	}
	if o.Language == "" {
		o.Language = protocol.Korean
	}
	return o
}

// Server is one supervised Python process.
type Server struct {
	options Options
	command *exec.Cmd
	port    int
	logPath string

	mu       sync.Mutex
	recent   []string // tail of the log, for the UI
	exitErr  error
	exited   bool
	stopping bool
	done     chan struct{}
}

const recentLogLines = 40

// Start launches the server. It returns as soon as the process is running;
// call WaitReady to find out when it can actually transcribe.
func Start(ctx context.Context, options Options) (*Server, error) {
	options = options.withDefaults()

	if options.ServerDir == "" {
		return nil, fmt.Errorf("server directory is not set")
	}
	if _, err := os.Stat(filepath.Join(options.ServerDir, "app", "main.py")); err != nil {
		return nil, fmt.Errorf("no server found in %s: %w", options.ServerDir, err)
	}
	if options.ModelPath == "" {
		return nil, fmt.Errorf("no model directory configured; see docs/model-setup.md")
	}
	if _, err := os.Stat(options.ModelPath); err != nil {
		return nil, fmt.Errorf("model directory %s is not readable: %w", options.ModelPath, err)
	}
	if options.DraftModelPath != "" {
		// A typo here must fail loudly: silently running without the draft
		// would look identical, just with several times the latency.
		if _, err := os.Stat(options.DraftModelPath); err != nil {
			return nil, fmt.Errorf("draft model directory %s is not readable: %w", options.DraftModelPath, err)
		}
	}

	port := options.Port
	if port == 0 {
		free, err := FreePort()
		if err != nil {
			return nil, fmt.Errorf("find a free port: %w", err)
		}
		port = free
	}

	if err := os.MkdirAll(options.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", options.StateDir, err)
	}

	configPath := filepath.Join(options.StateDir, fmt.Sprintf("server-%s.yaml", options.Language))
	if err := writeServerConfig(configPath, options, port); err != nil {
		return nil, err
	}

	logPath := filepath.Join(options.StateDir, fmt.Sprintf("server-%s.log", options.Language))
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", logPath, err)
	}

	server := &Server{
		options: options,
		port:    port,
		logPath: logPath,
		done:    make(chan struct{}),
	}

	command := exec.Command(options.PythonPath, "-m", "app.main",
		"--config", configPath, "--backend", options.Backend.Engine())
	command.Dir = options.ServerDir
	command.Env = append(os.Environ(), "PYTHONUNBUFFERED=1")
	command.Stdout = &tee{file: logFile, server: server}
	command.Stderr = command.Stdout
	configureProcess(command)

	if err := command.Start(); err != nil {
		logFile.Close()
		return nil, fmt.Errorf("start the server: %w", err)
	}
	server.command = command

	go func() {
		err := command.Wait()
		logFile.Close()
		server.mu.Lock()
		server.exited = true
		if !server.stopping {
			server.exitErr = err
		}
		server.mu.Unlock()
		close(server.done)
	}()

	return server, nil
}

// Port is the loopback port this instance listens on.
func (s *Server) Port() int { return s.port }

// Language is what this instance transcribes.
func (s *Server) Language() protocol.Language { return s.options.Language }

// LogPath is the file the process writes to.
func (s *Server) LogPath() string { return s.logPath }

// RecentLog returns the tail of the server's output, for showing an operator
// why a start failed without making them open a file.
func (s *Server) RecentLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.recent...)
}

// Exited reports whether the process is gone, and why.
func (s *Server) Exited() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exited, s.exitErr
}

// WaitReady polls /health/ready until the model is loaded and warmed up.
//
// It watches for process death too: a server that dies while loading would
// otherwise leave the caller waiting out the whole StartTimeout for a process
// that is never coming back.
func (s *Server) WaitReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.options.StartTimeout)
	defer cancel()

	url := fmt.Sprintf("http://127.0.0.1:%d/health/ready", s.port)
	client := &http.Client{Timeout: 3 * time.Second}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		ready, err := s.probe(ctx, client, url)
		if err == nil && ready {
			return nil
		}
		if errors.Is(err, errWrongServer) {
			// Polling cannot fix a port that something else owns; five more
			// minutes of it would just hide the message that explains the fix.
			return fmt.Errorf("the %s server could not start: %w "+
				"(set a different port, or 0 for a free one, in Settings)",
				s.options.Language, err)
		}

		select {
		case <-s.done:
			_, exitErr := s.Exited()
			return fmt.Errorf("the %s server exited before it was ready: %w\n%s",
				s.options.Language, exitErr, strings.Join(s.RecentLog(), "\n"))
		case <-ctx.Done():
			return fmt.Errorf("the %s server did not become ready within %s\n%s",
				s.options.Language, s.options.StartTimeout, strings.Join(s.RecentLog(), "\n"))
		case <-ticker.C:
		}
	}
}

func (s *Server) probe(ctx context.Context, client *http.Client, url string) (bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	response, err := client.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false, nil
	}
	var body struct {
		Status   string `json:"status"`
		Language string `json:"language"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return false, err
	}
	if body.Language != string(s.options.Language) {
		// Something else is already listening on this port. Better to say so
		// than to dictate Korean into an English server.
		return false, fmt.Errorf("%w: port %d serves %q, not %q",
			errWrongServer, s.port, body.Language, s.options.Language)
	}
	return body.Status == "ready", nil
}

// errWrongServer means the port answered like a server for something else —
// a condition that waiting longer can never repair.
var errWrongServer = errors.New("another server owns the port")

// Stop shuts the process down, escalating to a kill if it will not go.
func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	if s.exited {
		s.mu.Unlock()
		return nil
	}
	s.stopping = true
	s.mu.Unlock()

	if err := terminate(s.command); err != nil {
		return fmt.Errorf("signal the server: %w", err)
	}

	select {
	case <-s.done:
		return nil
	case <-time.After(15 * time.Second):
	case <-ctx.Done():
	}

	if s.command.Process != nil {
		_ = s.command.Process.Kill()
	}
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		return fmt.Errorf("the %s server did not exit", s.options.Language)
	}
	return nil
}

// FreePort asks the OS for an unused loopback port.
//
// There is a race between closing this listener and the server binding it. In
// practice the window is microseconds on a machine where nothing else is
// hunting for ports, and the alternative — a fixed port — collides far more
// often in the field.
func FreePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

// tee writes the child's output to the log file and keeps the tail in memory.
type tee struct {
	file   *os.File
	server *Server
}

func (t *tee) Write(p []byte) (int, error) {
	n, err := t.file.Write(p)
	t.server.mu.Lock()
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line == "" {
			continue
		}
		t.server.recent = append(t.server.recent, line)
	}
	if extra := len(t.server.recent) - recentLogLines; extra > 0 {
		t.server.recent = append([]string(nil), t.server.recent[extra:]...)
	}
	t.server.mu.Unlock()
	return n, err
}

// writeServerConfig generates the YAML the server reads.
//
// It is regenerated on every start rather than kept as user-editable state:
// the port may be chosen at runtime, and a stale file pointing at a port
// nothing is listening on is a confusing failure.
func writeServerConfig(path string, options Options, port int) error {
	// Beside the model counts as configured: that is where both installers put
	// the detector, and treating a blank setting as "energy detector please"
	// left it on disk and unused. See ResolveDetector for what that costs.
	vad := "energy"
	vadPath := ResolveDetector(options.VadModelPath, options.ModelPath)
	if vadPath != "" {
		vad = "silero"
	}

	var builder strings.Builder
	builder.WriteString("# Generated by Local Dictation. Edits are overwritten on every start.\n")
	fmt.Fprintf(&builder, "server:\n  host: \"127.0.0.1\"\n  port: %d\n  instance_name: %q\n",
		port, "local-dictation-"+string(options.Language))
	fmt.Fprintf(&builder, "model:\n  path: %q\n  device: %q\n  compute_type: \"int8\"\n  language: %q\n",
		options.ModelPath, options.Backend.Device(), string(options.Language))
	if options.DraftModelPath != "" {
		fmt.Fprintf(&builder, "  draft_path: %q\n", options.DraftModelPath)
	}
	fmt.Fprintf(&builder, "  beam_size: 1\n  cpu_threads: %d\n  num_workers: 1\n", options.CPUThreads)
	fmt.Fprintf(&builder, "streaming:\n  chunk_ms: 600\n  silence_ms: 600\n  vad: %q\n", vad)
	if vadPath != "" {
		fmt.Fprintf(&builder, "  silero_model_path: %q\n", vadPath)
	} else {
		builder.WriteString("  silero_model_path: null\n")
	}
	// One user, one session. A second concurrent session on a laptop would only
	// make both of them miss the latency budget.
	builder.WriteString("limits:\n  max_sessions: 1\n")
	// Loopback only: no TLS to terminate, nothing on the wire to protect.
	builder.WriteString("security:\n  require_client_certificate: false\n")
	builder.WriteString("logging:\n  level: \"INFO\"\n  json: true\n  store_audio: false\n  store_transcript: false\n")

	if err := os.WriteFile(path, []byte(builder.String()), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
