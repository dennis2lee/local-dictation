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

// FocusWatcher reports which window has focus, so a session can notice the
// user clicking into a different application mid-sentence. A nil watcher
// disables the check.
type FocusWatcher interface {
	Current() (string, error)
}

// Dialer opens a session socket. The controller never builds URLs itself; the
// endpoint provider does, because in local mode it also has to start a server.
type Dialer interface {
	Dial(ctx context.Context, language protocol.Language, progress func(string)) (Session, error)
}

// Session is one live server connection. (The transport also offers a flush
// that keeps the connection open; the controller has no use for it — a stop
// both flushes and ends the session.)
type Session interface {
	SendStart(ctx context.Context, start protocol.Start) error
	SendAudio(ctx context.Context, pcm []byte) error
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
	// Focus, when set, is polled during a session. Dictation types into
	// whatever has focus, so a session that outlives the window it started in
	// would keep writing into somewhere the user did not intend.
	Focus FocusWatcher
	// FocusPollInterval is how often that check runs. Short enough to catch a
	// click before a whole sentence lands in the wrong place, long enough not
	// to matter.
	FocusPollInterval time.Duration
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
	// finished closes once every inbound event has been applied and the
	// connection is over. Stop waits on it: `stop` makes the server flush,
	// deliver the final transcript and close, so the connection ending is the
	// one signal that arrives whether or not the utterance had anything to say.
	finished chan struct{}

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
	if options.FocusPollInterval == 0 {
		options.FocusPollInterval = 400 * time.Millisecond
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
	finished := c.finished
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

	go c.consume(sessionCtx, session, language, finished)

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

	c.watchFocus(sessionCtx)
	return nil
}

// watchFocus aborts the session if the user clicks into another window.
//
// The alternative is worse than stopping: the rest of the sentence follows the
// cursor into whatever they clicked — a chat box, a search field, or a password
// box. Committed text stays where it was written; only the provisional tail is
// removed.
func (c *Controller) watchFocus(ctx context.Context) {
	if c.options.Focus == nil {
		return
	}
	origin, err := c.options.Focus.Current()
	if err != nil || origin == "" {
		// Focus cannot be observed here — no permission, or an unsupported
		// platform. Skip the check rather than aborting on every poll.
		return
	}

	go func() {
		ticker := time.NewTicker(c.options.FocusPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if c.State() != Listening {
				return
			}
			current, err := c.options.Focus.Current()
			if err != nil || current == "" || current == origin {
				continue
			}
			c.Abort("Dictation stopped: you moved to another window. " +
				"Text already confirmed was kept.")
			return
		}
	}()
}

// Stop finalizes: stop capturing, ask the server to stop, and wait until every
// event — the final transcript included — has been applied at the cursor.
func (c *Controller) Stop(ctx context.Context) error {
	c.mu.Lock()
	if c.state != Listening {
		c.mu.Unlock()
		return ErrBusy
	}
	c.state = Finalizing
	session, cancel, finished, language := c.session, c.cancel, c.finished, c.language
	c.mu.Unlock()

	c.publish(Update{State: Finalizing, Detail: "Finishing the sentence…", Language: language})

	// Order matters: stop the microphone first so no audio arrives after the
	// stop, which the server would ignore anyway.
	if err := c.options.Audio.Stop(); err != nil {
		c.publish(Update{State: Finalizing, Detail: fmt.Sprintf("microphone: %v", err), Language: language})
	}

	stopCtx, stopCancel := context.WithTimeout(ctx, c.options.FinalizeTimeout)
	defer stopCancel()

	// `stop` makes the server flush, deliver the final transcript and close
	// the connection. Waiting for the connection to end — rather than for a
	// final event that an empty or failed utterance never produces — is what
	// keeps a no-speech stop instant instead of burning the finalize timeout.
	if err := session.SendStop(stopCtx); err != nil {
		session.CloseNow() // unblocks consume, so the wait below cannot hang
		<-finished
		cancel()
		c.finish(session, language, fmt.Errorf("could not stop the session: %w", err))
		return err
	}

	select {
	case <-finished:
	case <-stopCtx.Done():
		c.publish(Update{
			State:    Finalizing,
			Detail:   "The server did not finish in time; keeping what was confirmed.",
			Language: language,
		})
		session.CloseNow()
	}

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
func (c *Controller) consume(ctx context.Context, session Session, language protocol.Language, finished chan struct{}) {
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
