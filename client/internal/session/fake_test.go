package session

import (
	"context"
	"errors"
	"sync"

	"github.com/dennis2lee/local-dictation/client/internal/protocol"
)

// fakeSession is a server on a channel. Tests drive it by pushing events.
type fakeSession struct {
	mu     sync.Mutex
	events chan protocol.ServerEvent
	audio  int
	closed bool

	startErr error
	stopErr  error
	err      error

	stopped   chan struct{}
	stopOnce  sync.Once
	closeOnce sync.Once
}

func newFakeSession() *fakeSession {
	return &fakeSession{
		events:  make(chan protocol.ServerEvent, 64),
		stopped: make(chan struct{}),
	}
}

func (f *fakeSession) SendStart(context.Context, protocol.Start) error { return f.startErr }

func (f *fakeSession) SendAudio(_ context.Context, pcm []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audio += len(pcm)
	return nil
}

func (f *fakeSession) SendStop(context.Context) error {
	f.stopOnce.Do(func() { close(f.stopped) })
	return f.stopErr
}

func (f *fakeSession) Events() <-chan protocol.ServerEvent { return f.events }

func (f *fakeSession) Err() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

func (f *fakeSession) Close() error {
	f.closeOnce.Do(func() {
		f.mu.Lock()
		f.closed = true
		f.mu.Unlock()
		close(f.events)
	})
	return nil
}

func (f *fakeSession) CloseNow() { _ = f.Close() }

func (f *fakeSession) AudioBytes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.audio
}

func (f *fakeSession) push(event protocol.ServerEvent) { f.events <- event }

func (f *fakeSession) pushReady() {
	f.push(protocol.ServerEvent{Ready: &protocol.Ready{
		Type: "ready", ProtocolVersion: 1, SessionID: "s-1", Language: protocol.Korean, Model: "fake",
	}})
}

func (f *fakeSession) pushTranscript(revision int64, stable, partial string, final bool) {
	f.push(protocol.ServerEvent{Transcript: &protocol.Transcript{
		Type: "transcript", ProtocolVersion: 1, UtteranceID: "u-1",
		Revision: revision, Stable: stable, Partial: partial, Final: final,
	}})
}

// pushClosed mimics the server ending the session after a stop: the `closed`
// event arrives and then the connection is gone.
func (f *fakeSession) pushClosed() {
	f.push(protocol.ServerEvent{Closed: &protocol.Closed{
		Type: "closed", ProtocolVersion: 1, Reason: "client_stop",
	}})
}

func (f *fakeSession) pushError(code protocol.ErrorCode, fatal bool) {
	f.push(protocol.ServerEvent{Error: &protocol.Error{
		Type: "error", ProtocolVersion: 1, Code: code, Message: string(code), Fatal: fatal,
	}})
}

// fakeDialer hands out prepared sessions.
type fakeDialer struct {
	mu       sync.Mutex
	sessions []*fakeSession
	err      error
	dialed   int
	progress []string
	language protocol.Language
}

func (d *fakeDialer) Dial(_ context.Context, language protocol.Language, progress func(string)) (Session, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dialed++
	d.language = language
	if progress != nil {
		progress("dialing")
		d.progress = append(d.progress, "dialing")
	}
	if d.err != nil {
		return nil, d.err
	}
	if len(d.sessions) == 0 {
		return nil, errors.New("no session prepared")
	}
	session := d.sessions[0]
	d.sessions = d.sessions[1:]
	return session, nil
}

// fakeAudio records what the controller asked of the microphone.
type fakeAudio struct {
	mu       sync.Mutex
	started  int
	stopped  int
	deviceID string
	sink     func([]byte)
	startErr error
	stopErr  error
}

func (a *fakeAudio) Start(deviceID string, sink func([]byte)) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.startErr != nil {
		return a.startErr
	}
	a.started++
	a.deviceID = deviceID
	a.sink = sink
	return nil
}

func (a *fakeAudio) Stop() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopped++
	a.sink = nil
	return a.stopErr
}

func (a *fakeAudio) emit(frame []byte) {
	a.mu.Lock()
	sink := a.sink
	a.mu.Unlock()
	if sink != nil {
		sink(frame)
	}
}

func (a *fakeAudio) Started() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.started
}

func (a *fakeAudio) Stopped() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stopped
}

func (a *fakeAudio) Capturing() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sink != nil
}
