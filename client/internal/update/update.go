// Package update looks for a newer build.
//
// There are two places it can look, and which one is in play is a deployment
// decision rather than a preference:
//
//   - an internal distribution server, when a manifest URL is configured. The
//     manifest is signed with an ed25519 key, and an unsigned or badly signed
//     one is not an "unknown" state to warn about, it is a refusal. Ed25519
//     rather than a certificate chain: the key is small enough to paste into a
//     settings file, verification is a dozen lines with no parsing surface, and
//     revoking it means shipping a new client — the honest model for an
//     internally distributed tool.
//   - the project's public GitHub releases otherwise, in github.go. Nobody
//     running this from the published installers has an internal server, and
//     the alternative to reading github.com is a client that can never tell
//     anyone an update exists.
//
// What both share is the last rule, and it is the one that matters most: the
// download is verified against a published hash before anything is executed,
// and this package never executes it. Installing stays the user's own act.
package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ErrNotConfigured means no manifest URL is set, so checking is a no-op.
var ErrNotConfigured = errors.New("no update server is configured")

// ErrUntrusted means the manifest failed signature verification.
var ErrUntrusted = errors.New("the update manifest is not correctly signed")

// Manifest is what the distribution server publishes.
//
// The signature covers the canonical JSON of everything except itself, so a
// tampered version, URL or hash all invalidate it.
type Manifest struct {
	Version   string              `json:"version"`
	Notes     string              `json:"notes,omitempty"`
	Published string              `json:"published,omitempty"`
	Artifacts map[string]Artifact `json:"artifacts"`
	// Signature is base64 ed25519 over the manifest with this field removed.
	Signature string `json:"signature"`
}

// Artifact is one platform's installer.
type Artifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Result is what a check found.
type Result struct {
	Current   string
	Available string
	// Newer is true only when Available is strictly greater than Current.
	Newer    bool
	Notes    string
	Artifact Artifact
	// Platform is the artifact key that was looked up, e.g. "windows/amd64".
	Platform string
	// Page is where a person can read about the release themselves. Only the
	// GitHub source has one.
	Page string
}

// A Source is somewhere a newer build can be found.
type Source interface {
	Check(context.Context) (Result, error)
	// Describe names the source in one phrase, for the window to show before
	// a check runs and in whatever it says afterwards.
	Describe() string
}

// SourceFor picks between them: the internal server when one is configured,
// the project's public releases otherwise. A managed deployment sets a
// manifest URL and never touches github.com; everyone else gets the releases
// they installed from in the first place.
func SourceFor(manifestURL, publicKey, repo, current string) Source {
	if strings.TrimSpace(manifestURL) != "" {
		return Checker{ManifestURL: manifestURL, PublicKey: publicKey, Current: current}
	}
	return GitHub{Repo: repo, Current: current}
}

// Checker talks to one distribution server.
type Checker struct {
	ManifestURL string
	PublicKey   string
	Current     string
	HTTPClient  *http.Client
}

var _ Source = Checker{}

// Describe names the source. The URL itself is the only honest description:
// "the internal update server" would hide which one when a settings file is
// wrong, which is exactly when someone is reading this.
func (c Checker) Describe() string {
	if trimmed := strings.TrimSpace(c.ManifestURL); trimmed != "" {
		return trimmed
	}
	return "no update server"
}

// PlatformKey is the artifact key for the running build.
func PlatformKey() string { return runtime.GOOS + "/" + runtime.GOARCH }

// Check fetches and verifies the manifest.
func (c Checker) Check(ctx context.Context) (Result, error) {
	if strings.TrimSpace(c.ManifestURL) == "" {
		return Result{}, ErrNotConfigured
	}
	if !strings.HasPrefix(c.ManifestURL, "https://") {
		return Result{}, errors.New("the update manifest URL must use https")
	}

	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.ManifestURL, nil)
	if err != nil {
		return Result{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return Result{}, fmt.Errorf("reach the update server: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("update server returned HTTP %d", response.StatusCode)
	}

	// 1 MiB is far more than a manifest needs and bounds a hostile response.
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return Result{}, fmt.Errorf("read the manifest: %w", err)
	}

	manifest, err := Verify(raw, c.PublicKey)
	if err != nil {
		return Result{}, err
	}

	platform := PlatformKey()
	artifact, ok := manifest.Artifacts[platform]
	if !ok {
		return Result{}, fmt.Errorf("the manifest has no build for %s", platform)
	}

	return Result{
		Current:   c.Current,
		Available: manifest.Version,
		Newer:     IsNewer(manifest.Version, c.Current),
		Notes:     manifest.Notes,
		Artifact:  artifact,
		Platform:  platform,
	}, nil
}

// Verify parses a manifest and checks its signature.
func Verify(raw []byte, publicKeyBase64 string) (Manifest, error) {
	if strings.TrimSpace(publicKeyBase64) == "" {
		return Manifest{}, fmt.Errorf("%w: no public key is configured", ErrUntrusted)
	}
	publicKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(publicKeyBase64))
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return Manifest{}, fmt.Errorf("%w: the configured public key is not a 32-byte ed25519 key", ErrUntrusted)
	}

	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse the manifest: %w", err)
	}
	if manifest.Signature == "" {
		return Manifest{}, fmt.Errorf("%w: it carries no signature", ErrUntrusted)
	}
	signature, err := base64.StdEncoding.DecodeString(manifest.Signature)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: the signature is not valid base64", ErrUntrusted)
	}

	signed, err := SigningPayload(manifest)
	if err != nil {
		return Manifest{}, err
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), signed, signature) {
		return Manifest{}, ErrUntrusted
	}
	return manifest, nil
}

// SigningPayload is the exact bytes a signature covers: the manifest with the
// signature field cleared, serialised by encoding/json.
//
// Go's encoder sorts map keys and writes struct fields in declaration order, so
// this is reproducible. The signing tool in build/ uses the same function,
// which is what keeps signer and verifier honest about "canonical".
func SigningPayload(manifest Manifest) ([]byte, error) {
	manifest.Signature = ""
	payload, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("encode the manifest for signing: %w", err)
	}
	return payload, nil
}

// Download fetches an artifact into dir and verifies its hash.
//
// It never executes anything: installing is the user's explicit action, and on
// both platforms it means handing the file to the OS installer.
func Download(ctx context.Context, artifact Artifact, dir string, client *http.Client) (string, error) {
	if !strings.HasPrefix(artifact.URL, "https://") {
		return "", errors.New("the artifact URL must use https")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Minute}
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.URL, nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("download the update: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the update server returned HTTP %d", response.StatusCode)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	target := filepath.Join(dir, filepath.Base(artifact.URL))
	file, err := os.CreateTemp(dir, ".update-*")
	if err != nil {
		return "", err
	}
	temporary := file.Name()
	defer os.Remove(temporary)

	digest := sha256.New()
	limit := artifact.Size
	if limit <= 0 {
		limit = 1 << 31 // 2 GiB ceiling when the manifest does not say
	}
	written, err := io.Copy(io.MultiWriter(file, digest), io.LimitReader(response.Body, limit))
	file.Close()
	if err != nil {
		return "", fmt.Errorf("write the update: %w", err)
	}
	if artifact.Size > 0 && written != artifact.Size {
		return "", fmt.Errorf("the download is %s, the manifest says %s",
			strconv.FormatInt(written, 10), strconv.FormatInt(artifact.Size, 10))
	}

	actual := hex.EncodeToString(digest.Sum(nil))
	if !strings.EqualFold(actual, artifact.SHA256) {
		// Nothing is kept: a file that fails its hash is either corrupt or
		// substituted, and both mean it must not reach an installer.
		return "", fmt.Errorf("the downloaded update does not match the signed hash "+
			"(expected %s, got %s)", artifact.SHA256, actual)
	}

	if err := os.Rename(temporary, target); err != nil {
		return "", fmt.Errorf("install the download: %w", err)
	}
	return target, nil
}

// IsNewer compares dotted numeric versions, ignoring any pre-release suffix.
//
// Deliberately narrow: build/release.sh only ever emits MAJOR.MINOR.PATCH, and
// a full semver parser would be surface area for no benefit.
func IsNewer(candidate, current string) bool {
	return compareVersions(candidate, current) > 0
}

func compareVersions(a, b string) int {
	partsA, partsB := versionParts(a), versionParts(b)
	for index := range max(len(partsA), len(partsB)) {
		var valueA, valueB int
		if index < len(partsA) {
			valueA = partsA[index]
		}
		if index < len(partsB) {
			valueB = partsB[index]
		}
		if valueA != valueB {
			if valueA > valueB {
				return 1
			}
			return -1
		}
	}
	return 0
}

func versionParts(version string) []int {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if index := strings.IndexAny(version, "-+"); index >= 0 {
		version = version[:index]
	}
	fields := strings.Split(version, ".")
	parts := make([]int, 0, len(fields))
	for _, field := range fields {
		value, err := strconv.Atoi(field)
		if err != nil {
			break
		}
		parts = append(parts, value)
	}
	return parts
}
