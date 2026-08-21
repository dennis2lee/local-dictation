package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dennis2lee/local-dictation/client/internal/input"
	"github.com/dennis2lee/local-dictation/client/internal/protocol"
)

type harness struct {
	controller *Controller
	dialer     *fakeDialer
	audio      *fakeAudio
	platform   *input.FakePlatform
	session    *fakeSession
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	session := newFakeSession()
	dialer := &fakeDialer{sessions: []*fakeSession{session}}
	audio := &fakeAudio{}
	platform := input.NewFakePlatform()

	controller := New(Options{
		Dialer:          dialer,
		Audio:           audio,
		Composer:        input.NewComposer(platform),
		ClientVersion:   "0.1.0-test",
		FinalizeTimeout: 2 * time.Second,
	})
	return &harness{controller: controller, dialer: dialer, audio: audio, platform: platform, session: session}
}

// start brings the harness to Listening, with `ready` already delivered.
func (h *harness) start(t *testing.T) {
	t.Helper()
	h.session.pushReady()
	if err := h.controller.Start(context.Background(), protocol.Korean, "mic-1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := h.controller.State(); got != Listening {
		t.Fatalf("state = %v, want Listening", got)
	}
}

func waitFor(t *testing.T, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", message)
}

func TestStatesFollowTheDocumentedSequence(t *testing.T) {
	h := newHarness(t)
	if got := h.controller.State(); got != Idle {
		t.Fatalf("initial state = %v, want Idle", got)
	}
	h.start(t)

	if h.audio.Started() != 1 {
		t.Errorf("microphone started %d times, want 1", h.audio.Started())
	}
	if h.audio.deviceID != "mic-1" {
		t.Errorf("device = %q, want mic-1", h.audio.deviceID)
	}

	go func() {
		<-h.session.flushed
		h.session.pushTranscript(1, "오늘 오후 세 시에", "", true)
	}()

	if err := h.controller.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := h.controller.State(); got != Idle {
		t.Errorf("state after Stop = %v, want Idle", got)
	}
	if h.audio.Stopped() != 1 {
		t.Errorf("microphone stopped %d times, want 1", h.audio.Stopped())
	}
	if got := h.platform.Committed(); got != "오늘 오후 세 시에" {
		t.Errorf("committed = %q", got)
	}
}

func TestTheMicrophoneStopsBeforeTheFlush(t *testing.T) {
	h := newHarness(t)
	h.start(t)

	capturingAtFlush := make(chan bool, 1)
	go func() {
		<-h.session.flushed
		capturingAtFlush <- h.audio.Capturing()
		h.session.pushTranscript(1, "done", "", true)
	}()

	if err := h.controller.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if <-capturingAtFlush {
		t.Error("still capturing when flush was sent; audio after flush is discarded by the server")
	}
}

func TestAudioFramesReachTheServer(t *testing.T) {
	h := newHarness(t)
	h.start(t)

	frame := make([]byte, protocol.FrameBytes(20))
	for range 10 {
		h.audio.emit(frame)
	}
	waitFor(t, func() bool { return h.session.AudioBytes() == 10*len(frame) }, "audio frames")

	go func() {
		<-h.session.flushed
		h.session.pushTranscript(1, "x", "", true)
	}()
	_ = h.controller.Stop(context.Background())
}

func TestTranscriptsLandAtTheCursorWhileListening(t *testing.T) {
	h := newHarness(t)
	h.start(t)

	h.session.pushTranscript(1, "", "오늘", false)
	waitFor(t, func() bool { return h.platform.Document() == "오늘" }, "first partial")

	h.session.pushTranscript(2, "오늘 ", "오후", false)
	waitFor(t, func() bool { return h.platform.Document() == "오늘 오후" }, "second partial")

	go func() {
		<-h.session.flushed
		h.session.pushTranscript(3, "오늘 오후", "", true)
	}()
	if err := h.controller.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := h.platform.Committed(); got != "오늘 오후" {
		t.Errorf("committed = %q, want %q", got, "오늘 오후")
	}
}

func TestRepeatedShortcutDuringConnectingIsIgnored(t *testing.T) {
	h := newHarness(t)
	// Two sessions queued, so a second dial would succeed if it happened.
	h.dialer.sessions = append(h.dialer.sessions, newFakeSession())
	h.start(t)

	if err := h.controller.Start(context.Background(), protocol.Korean, "mic-1"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Start returned %v, want ErrBusy", err)
	}
	if h.dialer.dialed != 1 {
		t.Errorf("dialed %d times, want 1", h.dialer.dialed)
	}
}

func TestToggleStartsThenStops(t *testing.T) {
	h := newHarness(t)
	h.session.pushReady()

	if err := h.controller.Toggle(context.Background(), protocol.Korean, ""); err != nil {
		t.Fatal(err)
	}
	if got := h.controller.State(); got != Listening {
		t.Fatalf("state = %v, want Listening", got)
	}

	go func() {
		<-h.session.flushed
		h.session.pushTranscript(1, "hi", "", true)
	}()
	if err := h.controller.Toggle(context.Background(), protocol.Korean, ""); err != nil {
		t.Fatal(err)
	}
	if got := h.controller.State(); got != Idle {
		t.Errorf("state = %v, want Idle", got)
	}
}

func TestADialFailureLeavesTheDocumentAlone(t *testing.T) {
	h := newHarness(t)
	h.dialer.err = errors.New("connection refused")

	err := h.controller.Start(context.Background(), protocol.Korean, "")
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := h.controller.State(); got != Error {
		t.Errorf("state = %v, want Error", got)
	}
	if h.audio.Started() != 0 {
		t.Error("microphone was opened despite the dial failing")
	}
	if got := h.platform.Document(); got != "" {
		t.Errorf("document = %q, want empty", got)
	}
}

func TestAFatalServerErrorDropsThePartialAndKeepsTheRest(t *testing.T) {
	h := newHarness(t)
	h.start(t)

	h.session.pushTranscript(1, "confirmed ", "guess", false)
	waitFor(t, func() bool { return h.platform.Document() == "confirmed guess" }, "partial text")

	h.session.pushError(protocol.ErrServerBusy, true)
	waitFor(t, func() bool { return h.platform.Document() == "confirmed " }, "partial to be dropped")

	if got := h.platform.Committed(); got != "confirmed " {
		t.Errorf("committed = %q, want the confirmed prefix", got)
	}
}

func TestANonFatalErrorKeepsTheSessionAlive(t *testing.T) {
	h := newHarness(t)
	h.start(t)

	h.session.pushError(protocol.ErrInferenceFailed, false)
	waitFor(t, func() bool {
		for {
			select {
			case update := <-h.controller.Updates():
				if update.Err != nil && strings.Contains(update.Detail, "could not transcribe") {
					return true
				}
			default:
				return false
			}
		}
	}, "the non-fatal error to be reported")

	if got := h.controller.State(); got != Listening {
		t.Errorf("state = %v, want the session to survive", got)
	}
}

func TestStopSurvivesAServerThatNeverFinalizes(t *testing.T) {
	h := newHarness(t)
	h.controller.options.FinalizeTimeout = 150 * time.Millisecond
	h.start(t)

	h.session.pushTranscript(1, "committed ", "hanging", false)
	waitFor(t, func() bool { return h.platform.Document() == "committed hanging" }, "text")

	start := time.Now()
	if err := h.controller.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Stop took %s; it should give up at the finalize timeout", elapsed)
	}
	if got := h.controller.State(); got != Idle {
		t.Errorf("state = %v, want Idle", got)
	}
	// Finish() commits the marked text rather than leaving it dangling.
	if got := h.platform.Committed(); got != "committed hanging" {
		t.Errorf("committed = %q", got)
	}
}

func TestAbortKeepsCommittedTextAndDropsTheGuess(t *testing.T) {
	h := newHarness(t)
	h.start(t)

	h.session.pushTranscript(1, "kept ", "lost", false)
	waitFor(t, func() bool { return h.platform.Document() == "kept lost" }, "text")

	h.controller.Abort("microphone disconnected")
	if got := h.controller.State(); got != Idle {
		t.Errorf("state = %v, want Idle", got)
	}
	if got := h.platform.Document(); got != "kept " {
		t.Errorf("document = %q, want %q", got, "kept ")
	}
}

func TestAMicrophoneFailureTearsTheSessionDown(t *testing.T) {
	h := newHarness(t)
	h.audio.startErr = errors.New("device in use")
	h.session.pushReady()

	err := h.controller.Start(context.Background(), protocol.Korean, "mic-1")
	if err == nil || !strings.Contains(err.Error(), "device in use") {
		t.Fatalf("err = %v, want the device error", err)
	}
	if got := h.controller.State(); got != Error {
		t.Errorf("state = %v, want Error", got)
	}
}

func TestTheLanguageChoiceSelectsTheServer(t *testing.T) {
	h := newHarness(t)
	h.session.pushReady()
	if err := h.controller.Start(context.Background(), protocol.English, ""); err != nil {
		t.Fatal(err)
	}
	if h.dialer.language != protocol.English {
		t.Errorf("dialed the %q server, want en", h.dialer.language)
	}
	h.controller.Abort("done")
}

func TestStatesMapToTheDocumentedLEDColours(t *testing.T) {
	cases := map[State]LED{
		Idle:       Gray,
		Connecting: Amber,
		Listening:  Green,
		Finalizing: Amber,
		Error:      Red,
	}
	for state, want := range cases {
		if got := state.LED(); got != want {
			t.Errorf("%v LED = %v, want %v", state, got, want)
		}
	}
	if Listening.AcceptsSettingsChanges() {
		t.Error("settings must be locked while listening")
	}
	if !Idle.AcceptsSettingsChanges() {
		t.Error("settings must be editable when idle")
	}
}
