// Package config is the client's on-disk settings.
//
// Two deployment shapes share one file. In "remote" mode the client talks to
// the two language servers someone else runs. In "local" mode it starts a
// server on this machine and talks to it over the loopback interface — same
// protocol, same code paths, no network.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/dennis2lee/local-dictation/client/internal/protocol"
)

// CurrentVersion is the schema version of the settings file. It is bumped when
// a field changes meaning, so an old file can be migrated rather than silently
// misread.
const CurrentVersion = 1

// Mode selects where the servers run.
type Mode string

const (
	// ModeLocal starts and supervises the Python server on this machine.
	ModeLocal Mode = "local"
	// ModeRemote connects to servers someone else operates.
	ModeRemote Mode = "remote"
)

func (m Mode) Valid() bool { return m == ModeLocal || m == ModeRemote }

// TLS describes how to reach a remote server. On loopback in local mode none of
// it applies: there is no network segment to protect.
type TLS struct {
	Enabled bool `json:"enabled"`
	// CACertificate verifies the server. Empty means the system trust store,
	// which on a closed network usually will not have the internal CA.
	CACertificate string `json:"ca_certificate"`
	// ClientCertificate and ClientKey enable mTLS.
	ClientCertificate string `json:"client_certificate"`
	ClientKey         string `json:"client_key"`
	// InsecureSkipVerify exists for a first-day bring-up against a
	// self-signed certificate. It is surfaced in the UI as a warning, not a
	// checkbox someone can set and forget.
	InsecureSkipVerify bool `json:"insecure_skip_verify"`
}

// Remote is the connection to externally operated servers.
type Remote struct {
	Host        string `json:"host"`
	KoreanPort  int    `json:"korean_port"`
	EnglishPort int    `json:"english_port"`
	TLS         TLS    `json:"tls"`
}

// Local describes the server this client starts for itself.
type Local struct {
	// PythonPath is the interpreter used to run the server. Empty means "find
	// one on PATH at startup".
	PythonPath string `json:"python_path"`
	// ServerDir contains the `app` package. The installers put it next to the
	// executable; empty means "look in the standard install locations".
	ServerDir string `json:"server_dir"`
	// ModelPath is the CTranslate2 model directory — see docs/model-setup.md.
	ModelPath string `json:"model_path"`
	// DraftModelPath is an optional second, small model (e.g. `base`) that
	// produces the partial text while ModelPath produces the committed text.
	// It is what brings the first partial from ~3.7 s down to under one
	// second — see docs/latency.md. Empty runs a single model.
	DraftModelPath string `json:"draft_model_path"`
	// VadModelPath is silero_vad.onnx. Empty falls back to the energy detector.
	VadModelPath string `json:"vad_model_path"`
	// Ports on 127.0.0.1. 0 means "pick a free one at startup", which avoids
	// colliding with anything else on the machine.
	KoreanPort  int `json:"korean_port"`
	EnglishPort int `json:"english_port"`
	// StartBothLanguages starts the unused language server too. Off by default:
	// each instance holds the model in memory, and most people dictate in one
	// language per session.
	StartBothLanguages bool `json:"start_both_languages"`
	// MaxSessions and CPUThreads are passed through to the generated config.
	CPUThreads int `json:"cpu_threads"`
}

// Audio selects the capture device.
type Audio struct {
	// DeviceID is the platform device identifier. Empty means the system
	// default, which is also what the UI shows first.
	DeviceID string `json:"device_id"`
	// DeviceName is kept only so the UI can say which device went missing.
	DeviceName string `json:"device_name"`
}

// Hotkey is the global activation chord.
type Hotkey struct {
	Modifiers []string `json:"modifiers"`
	Key       string   `json:"key"`
}

// String renders the chord the way the UI displays it.
func (h Hotkey) String() string {
	if h.Key == "" {
		return ""
	}
	return strings.Join(append(append([]string{}, h.Modifiers...), h.Key), " + ")
}

// Update says where a newer build is looked for.
type Update struct {
	// ManifestURL is an internal HTTPS URL. Empty means the check falls back
	// to the project's public GitHub releases, which is what an install from
	// the published packages wants.
	ManifestURL string `json:"manifest_url"`
	// PublicKey is the base64 ed25519 key that signs the manifest. Without it
	// the client refuses to install anything.
	PublicKey string `json:"public_key"`
	// GitHubRepo overrides which repository the public check reads, for a
	// fork that publishes its own releases. Empty means this project's.
	GitHubRepo string `json:"github_repo"`
	// CheckOnStart is off by default: a dictation tool should not make a
	// network call the user did not ask for.
	CheckOnStart bool `json:"check_on_start"`
}

// Config is the whole settings file.
type Config struct {
	Version  int               `json:"version"`
	Mode     Mode              `json:"mode"`
	Language protocol.Language `json:"language"`
	Remote   Remote            `json:"remote"`
	Local    Local             `json:"local"`
	Audio    Audio             `json:"audio"`
	Hotkey   Hotkey            `json:"hotkey"`
	Update   Update            `json:"update"`
}

// Default is what a fresh install runs with: local mode, Korean, Ctrl+Shift+M,
// update checks pointed at this project's public releases, nothing stored
// anywhere but this file.
func Default() Config {
	return Config{
		Version:  CurrentVersion,
		Mode:     ModeLocal,
		Language: protocol.Korean,
		Remote: Remote{
			Host:        "",
			KoreanPort:  8765,
			EnglishPort: 8766,
			TLS:         TLS{Enabled: true},
		},
		Local: Local{
			ModelPath:   DefaultModelPath(),
			KoreanPort:  0,
			EnglishPort: 0,
		},
		Hotkey: Hotkey{Modifiers: []string{"Ctrl", "Shift"}, Key: "M"},
	}
}

// DefaultModelPath is where fetch-model.sh / fetch-model.ps1 install by default.
func DefaultModelPath() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, appDirName, "models", "large-v3-turbo")
	}
	return ""
}

const appDirName = "LocalDictation"

// Dir is the per-user directory holding settings, logs and generated server
// configs.
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate the user config directory: %w", err)
	}
	return filepath.Join(base, appDirName), nil
}

// Path is the settings file itself.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "settings.json"), nil
}

// Load reads the settings file, falling back to defaults when it does not exist
// yet. A file that exists but cannot be parsed is an error: silently reverting
// someone's configured server address to a default would be worse.
func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Default(), err
	}
	return LoadFrom(path)
}

// LoadFrom reads a specific file. Exposed for tests and for --config.
func LoadFrom(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Default(), fmt.Errorf("read %s: %w", path, err)
	}

	// Start from defaults so a file written by an older version still gets
	// sensible values for fields it does not mention.
	config := Default()
	if err := json.Unmarshal(raw, &config); err != nil {
		return Default(), fmt.Errorf("parse %s: %w", path, err)
	}
	config.migrate()
	return config, nil
}

func (c *Config) migrate() {
	if c.Version == 0 {
		c.Version = CurrentVersion
	}
	if c.Mode == "" {
		c.Mode = ModeLocal
	}
	if c.Language == "" {
		c.Language = protocol.Korean
	}
	if c.Hotkey.Key == "" {
		c.Hotkey = Default().Hotkey
	}
}

// Save writes the settings atomically: a crash mid-write must not leave a
// truncated file that the next launch refuses to parse.
func (c Config) Save() error {
	path, err := Path()
	if err != nil {
		return err
	}
	return c.SaveTo(path)
}

// SaveTo writes to a specific file.
func (c Config) SaveTo(path string) error {
	c.Version = CurrentVersion
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}

	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	raw = append(raw, '\n')

	temporary, err := os.CreateTemp(filepath.Dir(path), ".settings-*.json")
	if err != nil {
		return fmt.Errorf("create a temporary file: %w", err)
	}
	name := temporary.Name()
	defer os.Remove(name)

	if _, err := temporary.Write(raw); err != nil {
		temporary.Close()
		return fmt.Errorf("write settings: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("flush settings: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close settings: %w", err)
	}
	// 0600: the file can name certificate paths and an internal hostname.
	if err := os.Chmod(name, 0o600); err != nil {
		return fmt.Errorf("set permissions: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("install settings: %w", err)
	}
	return nil
}

// Validate reports every problem at once, so the Settings tab can show them
// together instead of one per save.
func (c Config) Validate() error {
	var problems []string

	if !c.Mode.Valid() {
		problems = append(problems, fmt.Sprintf("mode must be local or remote, got %q", c.Mode))
	}
	if !c.Language.Valid() {
		problems = append(problems, fmt.Sprintf("language must be ko or en, got %q", c.Language))
	}
	if c.Hotkey.Key == "" {
		problems = append(problems, "a shortcut key is required")
	}

	switch c.Mode {
	case ModeRemote:
		if strings.TrimSpace(c.Remote.Host) == "" {
			problems = append(problems, "server address is required in remote mode")
		}
		problems = append(problems, portProblems("Korean port", c.Remote.KoreanPort, true)...)
		problems = append(problems, portProblems("English port", c.Remote.EnglishPort, true)...)
		if c.Remote.KoreanPort == c.Remote.EnglishPort && c.Remote.KoreanPort != 0 {
			problems = append(problems, "Korean and English ports must differ")
		}
		if c.Remote.TLS.Enabled {
			if (c.Remote.TLS.ClientCertificate == "") != (c.Remote.TLS.ClientKey == "") {
				problems = append(problems, "a client certificate needs its key, and vice versa")
			}
		}
	case ModeLocal:
		if strings.TrimSpace(c.Local.ModelPath) == "" {
			problems = append(problems, "model directory is required in local mode — see docs/model-setup.md")
		}
		problems = append(problems, portProblems("Korean port", c.Local.KoreanPort, false)...)
		problems = append(problems, portProblems("English port", c.Local.EnglishPort, false)...)
		if c.Local.KoreanPort != 0 && c.Local.KoreanPort == c.Local.EnglishPort {
			problems = append(problems, "Korean and English ports must differ")
		}
		if c.Local.CPUThreads < 0 {
			problems = append(problems, "CPU threads cannot be negative")
		}
	}

	if c.Update.ManifestURL != "" {
		if !strings.HasPrefix(c.Update.ManifestURL, "https://") {
			problems = append(problems, "the update manifest URL must use https")
		}
		if c.Update.PublicKey == "" {
			problems = append(problems, "an update manifest URL needs the signing public key")
		}
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("invalid settings:\n  - %s", strings.Join(problems, "\n  - "))
}

func portProblems(label string, port int, required bool) []string {
	if port == 0 {
		if required {
			return []string{label + " is required"}
		}
		return nil // 0 means "choose a free port"
	}
	if port < 1 || port > 65535 {
		return []string{fmt.Sprintf("%s must be between 1 and 65535, got %d", label, port)}
	}
	return nil
}

// PortFor returns the port serving a language in the current mode.
func (c Config) PortFor(language protocol.Language) int {
	if c.Mode == ModeLocal {
		if language == protocol.English {
			return c.Local.EnglishPort
		}
		return c.Local.KoreanPort
	}
	if language == protocol.English {
		return c.Remote.EnglishPort
	}
	return c.Remote.KoreanPort
}

// EndpointFor builds the WebSocket URL for a language.
//
// Local mode is always ws:// on 127.0.0.1: the traffic never reaches a network
// interface, and terminating TLS against a certificate the client would have to
// generate for itself buys nothing.
func (c Config) EndpointFor(language protocol.Language, port int) string {
	if c.Mode == ModeLocal {
		return fmt.Sprintf("ws://127.0.0.1:%d/v1/dictation", port)
	}
	scheme := "ws"
	if c.Remote.TLS.Enabled {
		scheme = "wss"
	}
	return fmt.Sprintf("%s://%s:%d/v1/dictation", scheme, c.Remote.Host, port)
}

// HealthURLFor is the readiness endpoint matching EndpointFor.
func (c Config) HealthURLFor(language protocol.Language, port int) string {
	if c.Mode == ModeLocal {
		return fmt.Sprintf("http://127.0.0.1:%d/health/ready", port)
	}
	scheme := "http"
	if c.Remote.TLS.Enabled {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s:%d/health/ready", scheme, c.Remote.Host, port)
}

// DefaultPythonCandidates lists the interpreters to try when Local.PythonPath
// is empty, most specific first.
func DefaultPythonCandidates() []string {
	if runtime.GOOS == "windows" {
		return []string{"python3.12.exe", "python3.11.exe", "python.exe", "py.exe"}
	}
	return []string{"python3.13", "python3.12", "python3.11", "python3"}
}
