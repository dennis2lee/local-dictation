// Package input turns transcript events into text at the user's cursor.
//
// The hard part is not typing characters, it is that Whisper revises itself.
// The server has already absorbed most of that: it guarantees a monotonically
// growing committed prefix and a volatile tail. This package maps those two
// onto the one mechanism both operating systems give us for provisional text —
// the IME composition (marked text on macOS, a TSF composition on Windows).
//
// Committed text is real text in the document. Marked text is not: it is
// underlined, it can be replaced wholesale, and if the process dies the
// application discards it. That is exactly the behaviour partial results want.
//
// Except that neither platform adapter here is a real IME. Both are synthetic
// keystrokes, where marked text is a fiction maintained by typing characters
// and backspacing them out again — so a revised tail costs a burst of
// keystrokes on every decode pass, and someone speaking quickly outruns it.
// So the tail is not typed at all unless input.live_preview asks for it: the
// default is to wait until a word has settled and type it once.
package input

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/dennis2lee/local-dictation/client/internal/protocol"
)

// utteranceSeparator goes after a finished utterance so the next one does not
// run into it.
//
// The server splits on silence, so one press of the shortcut usually produces
// several utterances, and without this they arrive as
// "회의를 시작합니다.다음 주에" — correct text that nobody wants to read. A
// space rather than a newline: dictation goes into the middle of a sentence at
// least as often as it starts a paragraph, and a stray line break is much
// harder to undo than a stray space.
const utteranceSeparator = " "

// ErrStablePrefixChanged means the server retracted text it had committed,
// which protocol v1 forbids. The composer refuses to guess: committed text is
// already in the user's document and cannot be taken back.
var ErrStablePrefixChanged = errors.New("server retracted committed text")

// Platform is the OS-specific half: Windows TSF, macOS InputMethodKit, or the
// synthetic-keystroke fallback.
type Platform interface {
	// Name identifies the adapter in logs and in Settings.
	Name() string
	// BeginComposition starts a composition at the current cursor.
	BeginComposition() error
	// SetMarkedText replaces the composing region. Empty clears it.
	SetMarkedText(text string) error
	// CommitText inserts text as real document content, ahead of any marked
	// text, which is then re-applied by the next SetMarkedText call.
	CommitText(text string) error
	// EndComposition finalizes, leaving any marked text committed.
	EndComposition() error
	// CancelComposition discards marked text without committing it.
	CancelComposition() error
	// Close releases OS resources.
	Close() error
}

// Composer is not safe for concurrent use; the session controller owns it and
// applies events from a single goroutine.
type Composer struct {
	mu       sync.Mutex
	platform Platform

	utteranceID  string
	committed    string
	lastRevision int64
	composing    bool
	// livePreview types the volatile tail as it arrives. Off, the tail is
	// never typed at all, which is the difference between appending words and
	// rewriting a sentence on every decode pass. See config.Input.
	livePreview bool
	// wrote records whether this utterance has put anything in the document.
	// A separator only makes sense after text: a press of the shortcut that
	// caught no speech should leave the document exactly as it was.
	wrote bool
	// Utterance whose composition was torn down mid-flight. Further events for
	// it must be ignored: its committed characters are already in the document,
	// and re-applying its stable prefix would duplicate them.
	abandoned string

	// Counters surfaced in Settings for troubleshooting an app that mishandles
	// compositions. Never any text.
	StaleDiscarded  int
	UtterancesShown int
}

func NewComposer(platform Platform) *Composer {
	return &Composer{platform: platform}
}

// SetLivePreview turns the volatile tail on or off. Called from settings, and
// only while nothing is being dictated.
func (c *Composer) SetLivePreview(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.livePreview = on
}

// PlatformName reports which adapter is in use.
func (c *Composer) PlatformName() string {
	if c.platform == nil {
		return "none"
	}
	return c.platform.Name()
}

// Apply renders one transcript event at the cursor.
//
// Ordering matters: commit the newly stable text first, then re-mark the
// partial. Doing it the other way round leaves the old marked text sitting
// between the caret and the text being committed.
func (c *Composer) Apply(event protocol.Transcript) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Out-of-order or replayed events. Dropping them is what makes the stream
	// safe to reconnect: a resumed session can repeat revisions it already sent.
	if event.Revision <= c.lastRevision {
		c.StaleDiscarded++
		return nil
	}

	if event.UtteranceID != "" && event.UtteranceID == c.abandoned {
		c.StaleDiscarded++
		c.lastRevision = event.Revision
		return nil
	}

	if event.UtteranceID != c.utteranceID {
		// A new utterance starts a fresh committed prefix. The previous one was
		// already sealed by its final event.
		if err := c.endCompositionLocked(); err != nil {
			return err
		}
		c.utteranceID = event.UtteranceID
		c.committed = ""
		c.wrote = false
		c.UtterancesShown++
	}

	if !strings.HasPrefix(event.Stable, c.committed) {
		// Protocol violation. Seal what the user already has rather than
		// duplicating or retracting it, and let the caller decide what to say.
		committedRunes := len([]rune(c.committed))
		// Cancel, not end: the marked text is the tail of a hypothesis the
		// server has just contradicted, so it must not become real text. This
		// is the plan's ERROR rule — drop partial, keep stable.
		_ = c.cancelCompositionLocked()
		c.abandoned = event.UtteranceID
		c.committed = ""
		c.utteranceID = ""
		c.lastRevision = event.Revision
		return fmt.Errorf("%w: %d committed characters are not a prefix of the new stable text",
			ErrStablePrefixChanged, committedRunes)
	}

	if !c.composing {
		if err := c.platform.BeginComposition(); err != nil {
			return fmt.Errorf("begin composition: %w", err)
		}
		c.composing = true
	}

	if delta := strings.TrimPrefix(event.Stable, c.committed); delta != "" {
		if err := c.platform.CommitText(delta); err != nil {
			return fmt.Errorf("commit text: %w", err)
		}
		c.committed = event.Stable
		c.wrote = true
	}

	// Empty rather than skipped when the preview is off: passing "" is what
	// clears any tail left over from before it was turned off, and on a
	// composer that has never marked anything it does nothing at all.
	marked := ""
	if c.livePreview {
		marked = event.Partial
	}
	if err := c.platform.SetMarkedText(marked); err != nil {
		return fmt.Errorf("set marked text: %w", err)
	}

	c.lastRevision = event.Revision

	if event.Final {
		// The server sends final with an empty partial, so there is no marked
		// text left to worry about; ending the composition just releases it.
		if err := c.separateLocked(); err != nil {
			return err
		}
		if err := c.endCompositionLocked(); err != nil {
			return err
		}
		c.utteranceID = ""
		c.committed = ""
	}
	return nil
}

// separateLocked types the gap between this utterance and whatever comes next.
//
// Only after something was written, and only when the text does not already
// end in whitespace — Whisper occasionally ends a segment with a space of its
// own, and two would be as visible as none.
func (c *Composer) separateLocked() error {
	if !c.wrote || utteranceSeparator == "" {
		return nil
	}
	c.wrote = false
	if trailing := strings.TrimRight(c.committed, " \t\n"); trailing != c.committed {
		return nil
	}
	if err := c.platform.CommitText(utteranceSeparator); err != nil {
		return fmt.Errorf("commit the utterance separator: %w", err)
	}
	return nil
}

// DropPartial clears the composing region while keeping every committed
// character. This is the recovery path for a network drop, a server error or a
// focus change: the user keeps what was confirmed and loses only the guess.
func (c *Composer) DropPartial() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.composing {
		return nil
	}
	if err := c.cancelCompositionLocked(); err != nil {
		return err
	}
	c.abandoned = c.utteranceID
	c.utteranceID = ""
	c.committed = ""
	return nil
}

// Finish ends any open composition, leaving marked text committed. Used when
// the session ends normally but the server never sent a final event.
func (c *Composer) Finish() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Marked text becomes real when the composition ends, so the separator has
	// to go after that, not before it.
	err := c.endCompositionLocked()
	if err == nil {
		err = c.separateLocked()
	}
	c.abandoned = c.utteranceID
	c.utteranceID = ""
	c.committed = ""
	return err
}

// Reset clears client-side state without touching the document. Called when a
// new session starts, so a stale revision counter cannot swallow real events.
func (c *Composer) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.utteranceID = ""
	c.committed = ""
	c.abandoned = ""
	c.lastRevision = 0
	c.composing = false
	c.wrote = false
}

// Close releases the platform adapter.
func (c *Composer) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.composing {
		_ = c.platform.CancelComposition()
		c.composing = false
	}
	return c.platform.Close()
}

func (c *Composer) cancelCompositionLocked() error {
	if !c.composing {
		return nil
	}
	c.composing = false
	if err := c.platform.CancelComposition(); err != nil {
		return fmt.Errorf("cancel composition: %w", err)
	}
	return nil
}

func (c *Composer) endCompositionLocked() error {
	if !c.composing {
		return nil
	}
	c.composing = false
	if err := c.platform.EndComposition(); err != nil {
		return fmt.Errorf("end composition: %w", err)
	}
	return nil
}
