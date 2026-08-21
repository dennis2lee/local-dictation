// Package session drives one dictation session from shortcut to final text.
//
// It is the only place that knows about all four moving parts — the server, the
// microphone, the cursor and the UI — and it keeps them in step through one
// state machine. Everything it touches is an interface, so the whole flow is
// testable without a sound card, a server or a window.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dennis2lee/local-dictation/client/internal/input"
	"github.com/dennis2lee/local-dictation/client/internal/protocol"
)

// ErrBusy means the shortcut fired while a session was already starting or
// stopping. Deliberately not an error the UI shows: the plan says a repeated
// shortcut during CONNECTING is ignored.
var ErrBusy = errors.New("a session is already in progress")

// AudioSource captures microphone audio as 16 kHz mono PCM frames.
type AudioSource interface {
	// Start begins capture, calling sink for each frame. sink must not block.
	Start(deviceID string, sink func([]byte)) error
	// Stop ends capture and waits for the last frame to be delivered.
	Stop() error
}

// Dialer opens a session socket. The controller never builds URLs itself; the
// endpoint provider does, because in local mode it also has to start a server.
type Dialer interface {
	Dial(ctx context.Context, language protocol.Language, progress func(string)) (Session, error)
}

// Session is one live server connection.
type Session interface {
	SendStart(ctx context.Context, start protocol.Start) error
	SendAudio(ctx context.Context, pcm []byte) error
	SendFlush(ctx context.Context) error
	SendStop(ctx context.Context) error
	Events() <-chan protocol.ServerEvent
	Err() error
	Close() error
	CloseNow()
}

// Update is a state change worth showing the user.
type Update struct {
	State State
	// Detail is a human-readable line for the status area. May be empty.
	Detail string
	// Err is set when the update reports a failure.
	Err error
	// Language is what the session was using.
	Language protocol.Language
}

// Options configures a controller.
type Options struct {
	Dialer   Dialer
	Audio    AudioSource
	Composer *input.Composer
	// ClientVersion is reported to the server for support purposes.
	ClientVersion string
	// FinalizeTimeout bounds the wait for the final transcript after flush. On
	// expiry the composer commits whatever it has rather than leaving the user
	// with dangling marked text.
	FinalizeTimeout time.Duration
	// Now is injectable for tests.
	Now func() time.Time
}

// Controller owns the session lifecycle.
type Controller struct {
	options Options

	mu       sync.Mutex
	state    State
	language protocol.Language
	deviceID string
	session  Session
	cancel   context.CancelFunc
	finished chan struct{}
	// finals carries one token per final transcript. Stop waits on it rather
	// than on the socket closing: after a flush the server sends the final
	// transcript and keeps the session open, so waiting for the connection to
	// end would burn the whole finalize timeout every single time.
	finals chan struct{}

	updates chan Update
}

// New builds a controller. It does not start anything.
func New(options Options) *Controller {
	if options.FinalizeTimeout == 0 {
		options.FinalizeTimeout = 10 * time.Second
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Controller{
		options: options,
		state:   Idle,
		updates: make(chan Update, 32),
	}
}

// Updates yields state changes for the UI.
func (c *Controller) Updates() <-chan Update { return c.updates }

// State reports the current state.
func (c *Controller) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Toggle is what the global shortcut calls: start if idle, stop if listening.
//
// During Connecting and Finalizing it does nothing, which is what makes a
// double-tap of the shortcut harmless instead of leaving a half-open session.
func (c *Controller) Toggle(ctx context.Context, language protocol.Language, deviceID string) error {
	switch c.State() {
	case Idle, Error:
		return c.Start(ctx, language, deviceID)
	case Listening:
		return c.Stop(ctx)
	default:
		return ErrBusy
	}
}

// Start opens a session and begins dictating.
func (c *Controller) Start(ctx context.Context, language protocol.Language, deviceID string) error {
	c.mu.Lock()
	if c.state != Idle && c.state != Error {
		c.mu.Unlock()
		return ErrBusy
	}
	c.state = Connecting
	c.language = language
	c.deviceID = deviceID
	sessionCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	c.cancel = cancel
	c.finished = make(chan struct{})
	c.finals = make(chan struct{}, 1)
	finished, finals := c.finished, c.finals
	c.mu.Unlock()

	c.publish(Update{State: Connecting, Detail: "Connecting…", Language: language})
	c.options.Composer.Reset()

	session, err := c.options.Dialer.Dial(sessionCtx, language, func(line string) {
		c.publish(Update{State: Connecting, Detail: line, Language: language})
	})
	if err != nil {
		cancel()
		close(finished)
		c.fail(fmt.Errorf("could not reach the %s server: %w", language, err))
		return err
	}

	start := protocol.NewStart(newSessionID(), c.options.ClientVersion, language)
	if err := session.SendStart(sessionCtx, start); err != nil {
		session.CloseNow()
		cancel()
		close(finished)
		c.fail(fmt.Errorf("could not start the session: %w", err))
		return err
	}

	// Wait for `ready` before opening the microphone. Capturing audio the
	// server would refuse only teaches the user that the shortcut is unreliable.
	if err := awaitReady(sessionCtx, session, 30*time.Second); err != nil {
		session.CloseNow()
		cancel()
		close(finished)
		c.fail(err)
		return err
	}

	c.mu.Lock()
	c.session = session
	c.mu.Unlock()

	go c.consume(sessionCtx, session, language, finished, finals)

	if err := c.options.Audio.Start(deviceID, func(frame []byte) {
		// Best effort: a dropped frame costs a syllable, a blocked callback
		// costs the whole capture stream.
		_ = session.SendAudio(sessionCtx, frame)
	}); err != nil {
		session.CloseNow()
		cancel()
		c.fail(fmt.Errorf("could not open the microphone: %w", err))
		return err
	}

	c.mu.Lock()
	c.state = Listening
	c.mu.Unlock()
	c.publish(Update{State: Listening, Detail: "Listening", Language: language})
	return nil
}

// Stop finalizes: stop capturing, flush, wait for the final transcript, close.
func (c *Controller) Stop(ctx context.Context) error {
	c.mu.Lock()
	if c.state != Listening {
		c.mu.Unlock()
		return ErrBusy
	}
	c.state = Finalizing
	session, cancel, finished, finals, language := c.session, c.cancel, c.finished, c.finals, c.language
	c.mu.Unlock()

	// Discard finals from utterances the user already saw complete; only one
	// that arrives after the flush below ends this wait.
	select {
	case <-finals:
	default:
	}

	c.publish(Update{State: Finalizing, Detail: "Finishing the sentence…", Language: language})

	// Order matters: stop the microphone first so no audio arrives after the
	// flush, which the server would ignore anyway.
	if err := c.options.Audio.Stop(); err != nil {
		c.publish(Update{State: Finalizing, Detail: fmt.Sprintf("microphone: %v", err), Language: language})
	}

	flushCtx, flushCancel := context.WithTimeout(ctx, c.options.FinalizeTimeout)
	defer flushCancel()

	if err := session.SendFlush(flushCtx); err != nil {
		cancel()
		<-finished
		c.finish(session, language, fmt.Errorf("could not flush: %w", err))
		return err
	}

	select {
	case <-finals:
	case <-finished: // the socket ended first
	case <-flushCtx.Done():
		c.publish(Update{
			State:    Finalizing,
			Detail:   "The server did not finish in time; keeping what was confirmed.",
			Language: language,
		})
	}

	_ = session.SendStop(context.WithoutCancel(ctx))
	cancel()
	c.finish(session, language, nil)
	return nil
}

// Abort tears the session down without waiting, keeping committed text and
// dropping the partial. Used on quit and when a device disappears.
func (c *Controller) Abort(reason string) {
	c.mu.Lock()
	session, cancel, language := c.session, c.cancel, c.language
	if c.state == Idle {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	_ = c.options.Audio.Stop()
	if cancel != nil {
		cancel()
	}
	if session != nil {
		session.CloseNow()
	}
	_ = c.options.Composer.DropPartial()

	c.mu.Lock()
	c.state = Idle
	c.session = nil
	c.mu.Unlock()
	c.publish(Update{State: Idle, Detail: reason, Language: language})
}

// consume applies inbound events until the session ends.
func (c *Controller) consume(ctx context.Context, session Session, language protocol.Language, finished, finals chan struct{}) {
	defer close(finished)

	for event := range session.Events() {
		switch {
		case event.Transcript != nil:
			if err := c.options.Composer.Apply(*event.Transcript); err != nil {
				c.publish(Update{
					State:    c.State(),
					Detail:   "The text could not be written at the cursor.",
					Err:      err,
					Language: language,
				})
			}
			if event.Transcript.Final {
				// One token per finalized utterance, non-blocking so a session
				// with many pauses does not stall on a Stop that never came.
				select {
				case finals <- struct{}{}:
				default:
				}
			}

		case event.Error != nil:
			c.handleServerError(*event.Error, language)
			if event.Error.Fatal {
				return
			}

		case event.Closed != nil:
			return
		}
	}

	if err := session.Err(); err != nil && ctx.Err() == nil {
		_ = c.options.Composer.DropPartial()
		c.publish(Update{
			State:    Error,
			Detail:   "The connection to the server dropped. Text confirmed so far was kept.",
			Err:      err,
			Language: language,
		})
	}
}

func (c *Controller) handleServerError(serverError protocol.Error, language protocol.Language) {
	if serverError.Fatal {
		_ = c.options.Composer.DropPartial()
	}
	c.publish(Update{
		State:    c.State(),
		Detail:   serverError.Code.UserMessage(),
		Err:      serverError,
		Language: language,
	})
}

// finish returns to Idle, sealing whatever composition is still open.
func (c *Controller) finish(session Session, language protocol.Language, cause error) {
	if err := c.options.Composer.Finish(); err != nil {
		c.publish(Update{State: Finalizing, Detail: "Could not finish the text.", Err: err, Language: language})
	}
	if session != nil {
		_ = session.Close()
	}

	c.mu.Lock()
	c.session = nil
	c.state = Idle
	if cause != nil {
		c.state = Error
	}
	state := c.state
	c.mu.Unlock()

	detail := "Ready"
	if cause != nil {
		detail = cause.Error()
	}
	c.publish(Update{State: state, Detail: detail, Err: cause, Language: language})
}

// fail moves to Error, dropping the partial and keeping committed text.
func (c *Controller) fail(err error) {
	_ = c.options.Composer.DropPartial()
	c.mu.Lock()
	c.state = Error
	c.session = nil
	language := c.language
	c.mu.Unlock()
	c.publish(Update{State: Error, Detail: err.Error(), Err: err, Language: language})
}

func (c *Controller) publish(update Update) {
	select {
	case c.updates <- update:
	default:
		// The UI is not draining. Dropping a status line is preferable to
		// stalling the session that produces it.
	}
}

func awaitReady(ctx context.Context, session Session, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case event, ok := <-session.Events():
			if !ok {
				if err := session.Err(); err != nil {
					return fmt.Errorf("the server closed the connection: %w", err)
				}
				return errors.New("the server closed the connection before it was ready")
			}
			switch {
			case event.Ready != nil:
				return nil
			case event.Error != nil:
				return fmt.Errorf("%s", event.Error.Code.UserMessage())
			}
		case <-timer.C:
			return errors.New("the server did not acknowledge the session")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func newSessionID() string {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		// A predictable id is only a support inconvenience, never a
		// correctness problem, so this never fails a session.
		return fmt.Sprintf("s-fallback-%d", time.Now().UnixNano())
	}
	return "s-" + hex.EncodeToString(buffer)
}
