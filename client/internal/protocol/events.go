// Package protocol is the Go half of the wire contract in protocol/v1.
//
// The JSON Schemas in that directory are authoritative. Anything changed here
// has to be changed there too, and protocol_test.go validates these structs
// against those files so the two cannot drift apart silently.
package protocol

import (
	"encoding/json"
	"fmt"
)

// Version is the only protocol version this client speaks. It does not
// negotiate downward: an operator seeing an explicit protocol_unsupported error
// is better off than a user wondering why dictation got worse.
const Version = 1

// Audio format constants. The server rejects anything else.
const (
	SampleRate     = 16000
	Channels       = 1
	BytesPerSample = 2
	Encoding       = "pcm_s16le"
)

// Language is the dictation language, which also selects the port.
type Language string

const (
	Korean  Language = "ko"
	English Language = "en"
)

func (l Language) Valid() bool { return l == Korean || l == English }

func (l Language) String() string {
	switch l {
	case Korean:
		return "Korean"
	case English:
		return "English"
	default:
		return string(l)
	}
}

// ErrorCode enumerates every code the server may send.
type ErrorCode string

const (
	ErrProtocolUnsupported ErrorCode = "protocol_unsupported"
	ErrLanguageMismatch    ErrorCode = "language_mismatch"
	ErrServerBusy          ErrorCode = "server_busy"
	ErrAudioFormatInvalid  ErrorCode = "audio_format_invalid"
	ErrAudioBeforeStart    ErrorCode = "audio_before_start"
	ErrUtteranceTooLong    ErrorCode = "utterance_too_long"
	ErrInferenceFailed     ErrorCode = "inference_failed"
	ErrSessionTimeout      ErrorCode = "session_timeout"
	ErrMalformedMessage    ErrorCode = "malformed_message"
	ErrInternalError       ErrorCode = "internal_error"
)

// UserMessage turns a wire code into something worth putting in the UI. The
// server's message field is precise but written for an operator reading logs.
func (c ErrorCode) UserMessage() string {
	switch c {
	case ErrProtocolUnsupported:
		return "The server speaks a different protocol version. Update Local Dictation."
	case ErrLanguageMismatch:
		return "That port serves the other language. Check the ports in Settings."
	case ErrServerBusy:
		return "The server is busy. Try again in a moment."
	case ErrAudioFormatInvalid, ErrAudioBeforeStart, ErrMalformedMessage:
		return "The server rejected this session. Please report this."
	case ErrUtteranceTooLong:
		return "That was a long stretch without a pause; the sentence was completed for you."
	case ErrInferenceFailed:
		return "The server could not transcribe part of that. Committed text was kept."
	case ErrSessionTimeout:
		return "The session timed out waiting for audio."
	case ErrInternalError:
		return "The server hit an internal error."
	default:
		return "The server reported an error."
	}
}

// AudioFormat is sent in Start so the server can reject a mismatch up front
// rather than transcribing noise.
type AudioFormat struct {
	Encoding   string `json:"encoding"`
	SampleRate int    `json:"sample_rate"`
	Channels   int    `json:"channels"`
}

// DefaultAudioFormat is the only format protocol v1 defines.
func DefaultAudioFormat() AudioFormat {
	return AudioFormat{Encoding: Encoding, SampleRate: SampleRate, Channels: Channels}
}

// Start opens a session. It must be the first message on the socket.
type Start struct {
	Type            string      `json:"type"`
	ProtocolVersion int         `json:"protocol_version"`
	SessionID       string      `json:"session_id"`
	ClientVersion   string      `json:"client_version"`
	Language        Language    `json:"language"`
	Audio           AudioFormat `json:"audio"`
}

func NewStart(sessionID, clientVersion string, language Language) Start {
	return Start{
		Type:            "start",
		ProtocolVersion: Version,
		SessionID:       sessionID,
		ClientVersion:   clientVersion,
		Language:        language,
		Audio:           DefaultAudioFormat(),
	}
}

// Flush asks the server to finalize whatever audio it still holds.
type Flush struct {
	Type            string `json:"type"`
	ProtocolVersion int    `json:"protocol_version"`
}

func NewFlush() Flush { return Flush{Type: "flush", ProtocolVersion: Version} }

// Stop ends the session.
type Stop struct {
	Type            string `json:"type"`
	ProtocolVersion int    `json:"protocol_version"`
}

func NewStop() Stop { return Stop{Type: "stop", ProtocolVersion: Version} }

// Ready acknowledges Start.
type Ready struct {
	Type            string   `json:"type"`
	ProtocolVersion int      `json:"protocol_version"`
	SessionID       string   `json:"session_id"`
	Language        Language `json:"language"`
	Model           string   `json:"model"`
	ServerVersion   string   `json:"server_version"`
}

// Transcript is one rendering of the current utterance.
//
// Stable+Partial reconstructs the hypothesis exactly: Stable carries the
// separator that precedes Partial, so the client must not insert a space of its
// own between them.
type Transcript struct {
	Type            string `json:"type"`
	ProtocolVersion int    `json:"protocol_version"`
	UtteranceID     string `json:"utterance_id"`
	Revision        int64  `json:"revision"`
	Stable          string `json:"stable"`
	Partial         string `json:"partial"`
	Final           bool   `json:"final"`
}

// Text is the full current hypothesis.
func (t Transcript) Text() string { return t.Stable + t.Partial }

// Error is a server-side failure. Fatal errors are followed by socket closure.
type Error struct {
	Type            string    `json:"type"`
	ProtocolVersion int       `json:"protocol_version"`
	Code            ErrorCode `json:"code"`
	Message         string    `json:"message"`
	Fatal           bool      `json:"fatal"`
}

func (e Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

// Closed is the server's acknowledgement that the session is over.
type Closed struct {
	Type            string `json:"type"`
	ProtocolVersion int    `json:"protocol_version"`
	Reason          string `json:"reason"`
}

// ServerEvent is one decoded inbound message. Exactly one field is non-nil.
type ServerEvent struct {
	Ready      *Ready
	Transcript *Transcript
	Error      *Error
	Closed     *Closed
}

type envelope struct {
	Type            string `json:"type"`
	ProtocolVersion int    `json:"protocol_version"`
}

// ErrUnknownEvent means the server sent a type this version does not know.
// It is not fatal: a forward-compatible server may add events, and ignoring one
// is better than dropping a session mid-sentence.
var ErrUnknownEvent = fmt.Errorf("unknown server event")

// DecodeServerEvent parses one inbound text frame.
func DecodeServerEvent(raw []byte) (ServerEvent, error) {
	var head envelope
	if err := json.Unmarshal(raw, &head); err != nil {
		return ServerEvent{}, fmt.Errorf("decode envelope: %w", err)
	}
	if head.ProtocolVersion != 0 && head.ProtocolVersion != Version {
		return ServerEvent{}, fmt.Errorf(
			"server sent protocol version %d, this client speaks %d",
			head.ProtocolVersion, Version)
	}

	switch head.Type {
	case "ready":
		var event Ready
		if err := json.Unmarshal(raw, &event); err != nil {
			return ServerEvent{}, fmt.Errorf("decode ready: %w", err)
		}
		return ServerEvent{Ready: &event}, nil
	case "transcript":
		var event Transcript
		if err := json.Unmarshal(raw, &event); err != nil {
			return ServerEvent{}, fmt.Errorf("decode transcript: %w", err)
		}
		return ServerEvent{Transcript: &event}, nil
	case "error":
		var event Error
		if err := json.Unmarshal(raw, &event); err != nil {
			return ServerEvent{}, fmt.Errorf("decode error: %w", err)
		}
		return ServerEvent{Error: &event}, nil
	case "closed":
		var event Closed
		if err := json.Unmarshal(raw, &event); err != nil {
			return ServerEvent{}, fmt.Errorf("decode closed: %w", err)
		}
		return ServerEvent{Closed: &event}, nil
	default:
		return ServerEvent{}, fmt.Errorf("%w: %q", ErrUnknownEvent, head.Type)
	}
}

// FrameBytes is the byte length of an audio frame covering ms milliseconds.
func FrameBytes(ms int) int {
	return SampleRate * BytesPerSample * Channels * ms / 1000
}

// FrameDurationSeconds is the inverse of FrameBytes.
func FrameDurationSeconds(n int) float64 {
	return float64(n) / float64(SampleRate*BytesPerSample*Channels)
}
