package session

// State is the client's dictation state machine, exactly as the plan defines it.
type State int

const (
	// Idle waits for the shortcut. Settings can be changed here and only here.
	Idle State = iota
	// Connecting prepares the microphone and the language server. A second
	// shortcut press is ignored rather than queued.
	Connecting
	// Listening streams audio and writes at the cursor. Language and microphone
	// are locked.
	Listening
	// Finalizing has stopped capturing and is waiting for the final transcript.
	Finalizing
	// Error means something recoverable went wrong: partial text was dropped,
	// committed text was kept.
	Error
)

func (s State) String() string {
	switch s {
	case Idle:
		return "Stopped"
	case Connecting:
		return "Connecting"
	case Listening:
		return "Listening"
	case Finalizing:
		return "Finalizing"
	case Error:
		return "Error"
	default:
		return "Unknown"
	}
}

// LED is the indicator colour the UI shows for a state, following the plan's
// table: grey idle, amber working, green live, red needs attention.
type LED string

const (
	Gray  LED = "gray"
	Amber LED = "amber"
	Green LED = "green"
	Red   LED = "red"
)

// LED maps a state to its indicator colour.
func (s State) LED() LED {
	switch s {
	case Listening:
		return Green
	case Connecting, Finalizing:
		return Amber
	case Error:
		return Red
	default:
		return Gray
	}
}

// AcceptsSettingsChanges reports whether the UI should let settings be edited.
func (s State) AcceptsSettingsChanges() bool { return s == Idle || s == Error }
