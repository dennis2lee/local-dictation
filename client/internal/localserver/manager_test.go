package localserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dennis2lee/local-dictation/client/internal/protocol"
)

func TestUpdateAppliesAChangedPythonPath(t *testing.T) {
	// The regression: the old code compared the settings *after* assigning
	// them, so a changed interpreter or server directory never invalidated the
	// cached resolution and the old one was used until the app restarted.
	manager := NewManager(ManagerSettings{PythonPath: "/usr/bin/python3.11"})
	manager.python = "/state/venv/bin/python"
	manager.serverDir = "/resolved/server"

	next := manager.settings
	next.PythonPath = "/opt/python3.12/bin/python3"
	if err := manager.Update(context.Background(), next); err != nil {
		t.Fatal(err)
	}

	if manager.python != "" || manager.serverDir != "" {
		t.Errorf("cached resolution survived a PythonPath change: python=%q serverDir=%q",
			manager.python, manager.serverDir)
	}
	if manager.settings.PythonPath != next.PythonPath {
		t.Errorf("settings.PythonPath = %q, want %q", manager.settings.PythonPath, next.PythonPath)
	}
}

func TestUpdateWithUnchangedSettingsKeepsTheResolvedRuntime(t *testing.T) {
	// Prepare resolves ServerDir and the interpreter once; re-saving identical
	// settings must not throw that work away (or restart a loaded model).
	settings := ManagerSettings{ModelPath: "/models/large-v3"}
	manager := NewManager(settings)
	manager.python = "/state/venv/bin/python"
	manager.serverDir = "/resolved/server"

	if err := manager.Update(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	if manager.python == "" || manager.serverDir == "" {
		t.Error("an unchanged save discarded the prepared runtime")
	}
}

func TestWaitReadyFailsFastWhenAnotherServerOwnsThePort(t *testing.T) {
	// The regression: a language mismatch from the health endpoint was
	// retried until the five-minute start timeout instead of being reported.
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "ready", "language": "en"})
	}))
	defer foreign.Close()

	port, err := strconv.Atoi(strings.TrimPrefix(foreign.URL, "http://127.0.0.1:"))
	if err != nil {
		t.Fatalf("could not read the test server port from %s", foreign.URL)
	}

	server := &Server{
		options: Options{Language: protocol.Korean, StartTimeout: time.Minute},
		port:    port,
		done:    make(chan struct{}),
	}

	start := time.Now()
	err = server.WaitReady(context.Background())
	if err == nil {
		t.Fatal("WaitReady accepted a server for the wrong language")
	}
	if !strings.Contains(err.Error(), `serves "en"`) {
		t.Errorf("err = %v, want it to name the conflicting language", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("WaitReady took %s to report a definitive conflict", elapsed)
	}
}

func TestWriteServerConfigCarriesTheDraftModel(t *testing.T) {
	dir := t.TempDir()

	path := filepath.Join(dir, "with-draft.yaml")
	options := Options{ModelPath: "/models/large-v3-turbo", DraftModelPath: "/models/base", Language: protocol.Korean}
	if err := writeServerConfig(path, options, 8765); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `draft_path: "/models/base"`) {
		t.Errorf("generated config lacks the draft model:\n%s", raw)
	}

	path = filepath.Join(dir, "without-draft.yaml")
	options.DraftModelPath = ""
	if err := writeServerConfig(path, options, 8765); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "draft_path") {
		t.Errorf("generated config mentions draft_path with no draft configured:\n%s", raw)
	}
}
