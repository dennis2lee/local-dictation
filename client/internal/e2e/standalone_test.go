// Package e2e exercises standalone mode the way a user does: the client starts
// its own Python server, streams real audio at real time, and the text lands in
// a document.
//
// It needs a real model, so it is opt-in:
//
//	LOCAL_DICTATION_TEST_MODEL=/path/to/large-v3-turbo \
//	LOCAL_DICTATION_TEST_AUDIO=/path/to/speech.wav \
//	LOCAL_DICTATION_TEST_EXPECT="today at 3" \
//	go test ./internal/e2e/ -run Standalone -v -timeout 20m
package e2e

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dennis2lee/local-dictation/client/internal/config"
	"github.com/dennis2lee/local-dictation/client/internal/dial"
	"github.com/dennis2lee/local-dictation/client/internal/input"
	"github.com/dennis2lee/local-dictation/client/internal/localserver"
	"github.com/dennis2lee/local-dictation/client/internal/protocol"
	"github.com/dennis2lee/local-dictation/client/internal/session"
)

// wavAudio is a capture device that replays a WAV file at real time, so the
// server sees the same frame cadence a microphone would produce.
type wavAudio struct {
	pcm    []byte
	stop   chan struct{}
	done   chan struct{}
	frame  int
	period time.Duration
}

func newWavAudio(t *testing.T, path string) *wavAudio {
	t.Helper()
	pcm := readWAV(t, path)
	return &wavAudio{
		pcm:    pcm,
		frame:  protocol.FrameBytes(20),
		period: 20 * time.Millisecond,
	}
}

func (w *wavAudio) Start(_ string, sink func([]byte)) error {
	w.stop, w.done = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(w.period)
		defer ticker.Stop()
		for offset := 0; offset < len(w.pcm); offset += w.frame {
			select {
			case <-w.stop:
				return
			case <-ticker.C:
			}
			end := min(offset+w.frame, len(w.pcm))
			sink(w.pcm[offset:end])
		}
		// A second of silence, so the server's VAD closes the utterance the way
		// it would when someone stops talking.
		silence := make([]byte, w.frame)
		for range 50 {
			select {
			case <-w.stop:
				return
			case <-ticker.C:
			}
			sink(silence)
		}
	}()
	return nil
}

func (w *wavAudio) Stop() error {
	if w.stop == nil {
		return nil
	}
	close(w.stop)
	<-w.done
	w.stop = nil
	return nil
}

func (w *wavAudio) Duration() time.Duration {
	return time.Duration(protocol.FrameDurationSeconds(len(w.pcm)) * float64(time.Second))
}

func readWAV(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(raw) < 44 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		t.Fatalf("%s is not a WAV file", path)
	}
	// Walk the chunks rather than assuming a 44-byte header: `say` writes a
	// LIST chunk that a fixed offset would read as audio.
	for offset := 12; offset+8 <= len(raw); {
		id := string(raw[offset : offset+4])
		size := int(binary.LittleEndian.Uint32(raw[offset+4 : offset+8]))
		body := offset + 8
		if id == "fmt " && body+16 <= len(raw) {
			channels := binary.LittleEndian.Uint16(raw[body+2 : body+4])
			rate := binary.LittleEndian.Uint32(raw[body+4 : body+8])
			bits := binary.LittleEndian.Uint16(raw[body+14 : body+16])
			if channels != protocol.Channels || rate != protocol.SampleRate || bits != 16 {
				t.Fatalf("%s is %d ch / %d Hz / %d bit, need 1 / 16000 / 16", path, channels, rate, bits)
			}
		}
		if id == "data" {
			end := min(body+size, len(raw))
			return raw[body:end]
		}
		offset = body + size + size%2
	}
	t.Fatalf("%s has no data chunk", path)
	return nil
}

func TestStandaloneModeTranscribesRealAudio(t *testing.T) {
	modelPath := os.Getenv("LOCAL_DICTATION_TEST_MODEL")
	audioPath := os.Getenv("LOCAL_DICTATION_TEST_AUDIO")
	if modelPath == "" || audioPath == "" {
		t.Skip("set LOCAL_DICTATION_TEST_MODEL and LOCAL_DICTATION_TEST_AUDIO to run this")
	}

	stateDir := t.TempDir()
	settings := config.Default()
	settings.Mode = config.ModeLocal
	settings.Language = protocol.Language(envOr("LOCAL_DICTATION_TEST_LANGUAGE", "en"))
	settings.Local.ModelPath = modelPath
	settings.Local.DraftModelPath = os.Getenv("LOCAL_DICTATION_TEST_DRAFT")
	settings.Local.VadModelPath = os.Getenv("LOCAL_DICTATION_TEST_VAD")
	settings.Local.PythonPath = os.Getenv("LOCAL_DICTATION_TEST_PYTHON")
	settings.Local.ServerDir = os.Getenv("LOCAL_DICTATION_TEST_SERVER_DIR")

	if serverDir, err := localserver.ResolveServerDir(settings.Local.ServerDir); err == nil {
		settings.Local.ServerDir = serverDir
	} else {
		t.Fatalf("locate the server: %v", err)
	}

	dialer := dial.New(settings, stateDir)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = dialer.Shutdown(ctx)
	})

	document := input.NewFakePlatform()
	audio := newWavAudio(t, audioPath)

	controller := session.New(session.Options{
		Dialer:          dialer,
		Audio:           audio,
		Composer:        input.NewComposer(document),
		ClientVersion:   "test",
		FinalizeTimeout: 60 * time.Second,
	})

	go func() {
		for update := range controller.Updates() {
			t.Logf("[%s] %s", update.State, update.Detail)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	started := time.Now()
	if err := controller.Start(ctx, settings.Language, ""); err != nil {
		t.Fatalf("start: %v\nserver log: %s", err, tailServerLog(stateDir, settings.Language))
	}
	t.Logf("session live after %s (includes the venv bootstrap and model load)", time.Since(started).Round(time.Second))

	// Let the file play through, plus the trailing silence.
	time.Sleep(audio.Duration() + 2*time.Second)

	if err := controller.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}

	got := strings.TrimSpace(document.Committed())
	t.Logf("committed text: %q", got)
	if got == "" {
		t.Fatalf("nothing was written at the cursor\nserver log: %s",
			tailServerLog(stateDir, settings.Language))
	}
	if document.Marked() != "" {
		t.Errorf("provisional text was left behind: %q", document.Marked())
	}
	if document.Composing() {
		t.Error("the composition was never closed")
	}

	if expected := os.Getenv("LOCAL_DICTATION_TEST_EXPECT"); expected != "" {
		if !strings.Contains(strings.ToLower(got), strings.ToLower(expected)) {
			t.Errorf("transcript %q does not contain %q", got, expected)
		}
	}
}

func tailServerLog(stateDir string, language protocol.Language) string {
	raw, err := os.ReadFile(filepath.Join(stateDir, "server-"+string(language)+".log"))
	if err != nil {
		return "(no log)"
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) > 25 {
		lines = lines[len(lines)-25:]
	}
	return "\n" + strings.Join(lines, "\n")
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
