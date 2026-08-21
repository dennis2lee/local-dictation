package update

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"
)

// DefaultRepo is where this project publishes its releases. A fork points the
// client elsewhere with update.github_repo in the settings file.
const DefaultRepo = "dennis2lee/local-dictation"

// ErrRateLimited is GitHub's unauthenticated API budget running out — sixty
// requests an hour per address, shared with everything else on the network
// that talks to the API. It is worth naming: "HTTP 403" reads like a
// permission problem, and this one clears by itself.
var ErrRateLimited = errors.New("github.com is rate-limiting update checks; try again in an hour")

// ErrNoInstaller means the newest release has nothing for this platform.
var ErrNoInstaller = errors.New("that release publishes no installer for this platform")

// GitHub reads the project's public releases.
//
// This is a weaker guarantee than the signed manifest above, and the difference
// is worth stating plainly: the trust here is HTTPS and the repository itself,
// not an offline key. What it still guarantees is that the bytes on disk are
// the bytes the release published — every release carries a SHA256SUMS, and a
// check copies the line for this platform's installer into the Artifact, so
// Download verifies against it exactly as it does for a signed manifest.
type GitHub struct {
	// Repo is "owner/name". Empty means DefaultRepo.
	Repo string
	// Current is the running version.
	Current    string
	HTTPClient *http.Client

	// api replaces https://api.github.com, and goos the platform the installer
	// is chosen for. Tests only: pinning the platform is what lets the whole
	// chain — release document, checksum, download — run on a machine this
	// project publishes no installer for.
	api  string
	goos string
}

var _ Source = GitHub{}

// Describe names where a check will go, so the window can say so before
// anyone presses the button.
func (g GitHub) Describe() string { return "github.com/" + g.repo() }

// Check reads the latest release and, when it is newer, the checksum that
// pins the download.
func (g GitHub) Check(ctx context.Context) (Result, error) {
	repo := g.repo()
	if err := CheckRepo(repo); err != nil {
		return Result{}, err
	}
	client := g.client()

	raw, err := g.get(ctx, client, g.base()+"/repos/"+repo+"/releases/latest")
	if err != nil {
		return Result{}, err
	}
	var release struct {
		Tag    string         `json:"tag_name"`
		Page   string         `json:"html_url"`
		Assets []releaseAsset `json:"assets"`
	}
	if err := json.Unmarshal(raw, &release); err != nil {
		return Result{}, fmt.Errorf("read the release from github.com: %w", err)
	}

	version := strings.TrimPrefix(strings.TrimSpace(release.Tag), "v")
	if version == "" {
		return Result{}, fmt.Errorf("github.com/%s has no released version yet", repo)
	}

	result := Result{
		Current:   g.Current,
		Available: version,
		Newer:     IsNewer(version, g.Current),
		Page:      release.Page,
		Platform:  PlatformKey(),
	}
	// Everything below is a second and third request. Nothing needs them when
	// the running build is already the newest one.
	if !result.Newer {
		return result, nil
	}

	installer, ok := installerFor(g.platform(), release.Assets)
	if !ok {
		return result, fmt.Errorf("%w: release %s carries %s",
			ErrNoInstaller, version, assetList(release.Assets))
	}
	sums, ok := assetNamed("SHA256SUMS", release.Assets)
	if !ok {
		return result, fmt.Errorf(
			"release %s publishes no SHA256SUMS, so %s cannot be verified; fetch it by hand from %s",
			version, installer.Name, release.Page)
	}

	published, err := g.get(ctx, client, sums.URL)
	if err != nil {
		return result, err
	}
	digest := expectedHash(published, installer.Name)
	if digest == "" {
		return result, fmt.Errorf("the SHA256SUMS of release %s does not list %s",
			version, installer.Name)
	}

	result.Artifact = Artifact{URL: installer.URL, SHA256: digest, Size: installer.Size}
	return result, nil
}

// releaseAsset is one published file.
type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// installerFor picks the file a given OS installs from. A release publishes
// one per platform — a .pkg for macOS, an .msi for Windows — alongside the
// server tarball and the checksums, which are not it.
func installerFor(goos string, assets []releaseAsset) (releaseAsset, bool) {
	var suffix string
	switch goos {
	case "darwin":
		suffix = ".pkg"
	case "windows":
		suffix = ".msi"
	default:
		return releaseAsset{}, false
	}
	for _, asset := range assets {
		if strings.HasSuffix(strings.ToLower(asset.Name), suffix) && asset.URL != "" {
			return asset, true
		}
	}
	return releaseAsset{}, false
}

func assetNamed(name string, assets []releaseAsset) (releaseAsset, bool) {
	for _, asset := range assets {
		if asset.Name == name && asset.URL != "" {
			return asset, true
		}
	}
	return releaseAsset{}, false
}

// assetList is only ever read in an error, where naming what the release does
// have is the difference between "try again" and "look at the release page".
func assetList(assets []releaseAsset) string {
	if len(assets) == 0 {
		return "no files"
	}
	names := make([]string, 0, len(assets))
	for _, asset := range assets {
		names = append(names, asset.Name)
	}
	return strings.Join(names, ", ")
}

// expectedHash reads one line out of a sha256sum file. Both the plain and the
// binary-mode form ("*name") are accepted, because which one a release carries
// depends on which tool wrote it.
func expectedHash(sums []byte, name string) string {
	scanner := bufio.NewScanner(bytes.NewReader(sums))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") == name {
			return fields[0]
		}
	}
	return ""
}

// CheckRepo refuses anything that is not owner/name.
//
// The value is pasted straight into a URL path, and it comes from a settings
// file, so a stray slash or scheme has to be a refusal rather than something
// that quietly sends the check to another host.
func CheckRepo(repo string) error {
	malformed := fmt.Errorf("%q is not a GitHub repository; it should read owner/name", repo)
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return malformed
	}
	for _, segment := range []string{owner, name} {
		for _, letter := range segment {
			switch {
			case letter >= 'a' && letter <= 'z',
				letter >= 'A' && letter <= 'Z',
				letter >= '0' && letter <= '9',
				letter == '-', letter == '_', letter == '.':
			default:
				return malformed
			}
		}
	}
	return nil
}

func (g GitHub) repo() string {
	if trimmed := strings.TrimSpace(g.Repo); trimmed != "" {
		return trimmed
	}
	return DefaultRepo
}

func (g GitHub) platform() string {
	if g.goos != "" {
		return g.goos
	}
	return runtime.GOOS
}

func (g GitHub) base() string {
	if g.api != "" {
		return strings.TrimSuffix(g.api, "/")
	}
	return "https://api.github.com"
}

func (g GitHub) client() *http.Client {
	if g.HTTPClient != nil {
		return g.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// get fetches a small document and turns GitHub's status codes into sentences
// someone can act on.
func (g GitHub) get(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	// Applied to the release document and to the checksum URL the document
	// points at, so a tampered response cannot walk the second fetch off TLS.
	if !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("refusing to fetch %s: update checks use https", url)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("reach github.com: %w", err)
	}
	defer response.Body.Close()

	switch {
	case response.StatusCode == http.StatusOK:
	case response.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("github.com/%s has published no releases", g.repo())
	case (response.StatusCode == http.StatusForbidden ||
		response.StatusCode == http.StatusTooManyRequests) &&
		response.Header.Get("X-RateLimit-Remaining") == "0":
		return nil, ErrRateLimited
	default:
		return nil, fmt.Errorf("github.com returned HTTP %d", response.StatusCode)
	}

	// A megabyte is far more than a release document or a checksum file needs,
	// and it bounds a hostile response.
	return io.ReadAll(io.LimitReader(response.Body, 1<<20))
}
