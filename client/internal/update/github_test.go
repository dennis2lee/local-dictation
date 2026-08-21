package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

// releaseServer stands in for github.com: the release document, the files it
// points at, and the SHA256SUMS over them. It is TLS because the client
// refuses plain HTTP, and that refusal is worth exercising rather than
// working around.
type releaseServer struct {
	*httptest.Server
	requests atomic.Int64
}

// sumsFor names the files SHA256SUMS gets a line for. nil publishes no
// SHA256SUMS at all — both are releases this client has to cope with.
func newReleaseServer(t *testing.T, tag string, files map[string][]byte, sumsFor []string) *releaseServer {
	t.Helper()
	server := &releaseServer{}

	if sumsFor != nil {
		sums := ""
		for _, name := range sumsFor {
			digest := sha256.Sum256(files[name])
			sums += fmt.Sprintf("%s  %s\n", hex.EncodeToString(digest[:]), name)
		}
		files["SHA256SUMS"] = []byte(sums)
	}

	server.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.requests.Add(1)
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			assets := []map[string]any{}
			for name, body := range files {
				assets = append(assets, map[string]any{
					"name":                 name,
					"browser_download_url": server.URL + "/download/" + name,
					"size":                 len(body),
				})
			}
			json.NewEncoder(w).Encode(map[string]any{
				"tag_name": tag,
				"html_url": server.URL + "/releases/tag/" + tag,
				"assets":   assets,
			})
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/download/")
		body, ok := files[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server
}

func (r *releaseServer) source(current, goos string) GitHub {
	return GitHub{
		Repo:       "dennis2lee/local-dictation",
		Current:    current,
		HTTPClient: r.Client(),
		api:        r.URL,
		goos:       goos,
	}
}

// The whole chain in one test, because the parts being individually right is
// not the claim worth making: a check has to produce an Artifact that Download
// then accepts, or the button does nothing.
func TestAGitHubReleaseCheckProducesADownloadThatVerifies(t *testing.T) {
	installer := []byte("the macOS package bytes")
	server := newReleaseServer(t, "v0.2.0", map[string][]byte{
		"LocalDictation-0.2.0.pkg":            installer,
		"LocalDictation-0.2.0-x64.msi":        []byte("the Windows package bytes"),
		"local-dictation-server-0.2.0.tar.gz": []byte("the server tarball"),
	}, []string{"LocalDictation-0.2.0.pkg", "LocalDictation-0.2.0-x64.msi", "local-dictation-server-0.2.0.tar.gz"})

	result, err := server.source("0.1.10", "darwin").Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !result.Newer || result.Available != "0.2.0" {
		t.Fatalf("result = %+v", result)
	}
	if !strings.HasSuffix(result.Artifact.URL, "LocalDictation-0.2.0.pkg") {
		t.Errorf("artifact URL = %q, want the .pkg", result.Artifact.URL)
	}
	if result.Artifact.Size != int64(len(installer)) {
		t.Errorf("artifact size = %d, want %d", result.Artifact.Size, len(installer))
	}
	if result.Page == "" {
		t.Error("no release page to send anyone to")
	}

	// The hash the check copied out of SHA256SUMS is the one Download checks
	// against. If Check took it from the wrong line, this fails.
	path, err := Download(context.Background(), result.Artifact, t.TempDir(), server.Client())
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(installer) {
		t.Fatalf("downloaded %q, err = %v", got, err)
	}
}

// A check that finds nothing new should cost one request, not three. Someone
// pressing the button on a current build should not be fetching checksums for
// a release they already run.
func TestAnUpToDateCheckAsksGitHubOnce(t *testing.T) {
	server := newReleaseServer(t, "v0.1.10", map[string][]byte{
		"LocalDictation-0.1.10.pkg": []byte("x"),
	}, []string{"LocalDictation-0.1.10.pkg"})

	result, err := server.source("0.1.10", "darwin").Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result.Newer {
		t.Errorf("0.1.10 reported as newer than itself")
	}
	if got := server.requests.Load(); got != 1 {
		t.Errorf("made %d requests, want 1", got)
	}
}

// Sixty requests an hour, per address, shared with whatever else on the
// network talks to the API. "HTTP 403" would read as a permission problem.
func TestARateLimitedCheckSaysThatIsWhatHappened(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	source := GitHub{Current: "0.1.0", HTTPClient: server.Client(), api: server.URL}
	_, err := source.Check(context.Background())
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
}

func TestARepoWithNoReleasesIsExplained(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Not Found", http.StatusNotFound)
	}))
	defer server.Close()

	source := GitHub{Current: "0.1.0", HTTPClient: server.Client(), api: server.URL}
	_, err := source.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no releases") {
		t.Fatalf("err = %v, want a sentence about there being no releases", err)
	}
}

// Without SHA256SUMS there is nothing to verify the download against, and an
// unverifiable installer must not be offered as a button. The release page is
// still named, because fetching it by hand remains the user's call.
func TestAReleaseWithoutChecksumsIsNotOffered(t *testing.T) {
	server := newReleaseServer(t, "v0.2.0", map[string][]byte{
		"LocalDictation-0.2.0.pkg": []byte("unverifiable"),
	}, nil)

	result, err := server.source("0.1.0", "darwin").Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "SHA256SUMS") {
		t.Fatalf("err = %v, want a complaint about the missing checksums", err)
	}
	if result.Artifact.URL != "" {
		t.Errorf("an unverifiable artifact was handed out: %+v", result.Artifact)
	}
}

// A release whose checksum file leaves the installer out is the same problem:
// there is a file to download and no published hash to hold it to.
func TestAnInstallerMissingFromTheChecksumsIsNotOffered(t *testing.T) {
	server := newReleaseServer(t, "v0.2.0", map[string][]byte{
		"LocalDictation-0.2.0.pkg":            []byte("unlisted"),
		"local-dictation-server-0.2.0.tar.gz": []byte("the only listed file"),
	}, []string{"local-dictation-server-0.2.0.tar.gz"})

	result, err := server.source("0.1.0", "darwin").Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "LocalDictation-0.2.0.pkg") {
		t.Fatalf("err = %v, want the unlisted installer named", err)
	}
	if result.Artifact.URL != "" {
		t.Errorf("an unverifiable artifact was handed out: %+v", result.Artifact)
	}
}

func TestTheInstallerIsPickedByPlatform(t *testing.T) {
	assets := []releaseAsset{
		{Name: "SHA256SUMS", URL: "https://x/sums"},
		{Name: "local-dictation-server-0.2.0.tar.gz", URL: "https://x/tar"},
		{Name: "LocalDictation-0.2.0.pkg", URL: "https://x/pkg"},
		{Name: "LocalDictation-0.2.0-x64.msi", URL: "https://x/msi"},
	}
	for _, want := range []struct {
		goos, name string
		found      bool
	}{
		{"darwin", "LocalDictation-0.2.0.pkg", true},
		{"windows", "LocalDictation-0.2.0-x64.msi", true},
		{"linux", "", false}, // nothing is published for it, and pretending otherwise is worse
	} {
		got, ok := installerFor(want.goos, assets)
		if ok != want.found || got.Name != want.name {
			t.Errorf("installerFor(%q) = %q, %v; want %q, %v",
				want.goos, got.Name, ok, want.name, want.found)
		}
	}
}

func TestChecksumLinesAreReadInEitherForm(t *testing.T) {
	sums := []byte("" +
		"aaaa  LocalDictation-0.2.0.pkg\n" +
		"bbbb *LocalDictation-0.2.0-x64.msi\n" +
		"\n" +
		"# a comment nobody writes but which must not become a hash\n")
	for _, want := range []struct{ name, digest string }{
		{"LocalDictation-0.2.0.pkg", "aaaa"},
		{"LocalDictation-0.2.0-x64.msi", "bbbb"},
		{"LocalDictation-0.3.0.pkg", ""},
	} {
		if got := expectedHash(sums, want.name); got != want.digest {
			t.Errorf("expectedHash(%q) = %q, want %q", want.name, got, want.digest)
		}
	}
}

// The repo is pasted into a URL path and it comes from a settings file.
func TestOnlyAnOwnerNameRepoIsAccepted(t *testing.T) {
	for _, good := range []string{"dennis2lee/local-dictation", "a/b", "Some-Org/repo.name_2"} {
		if err := CheckRepo(good); err != nil {
			t.Errorf("CheckRepo(%q) = %v, want accepted", good, err)
		}
	}
	for _, bad := range []string{
		"", "owner", "owner/", "/name", "owner/name/extra",
		"https://evil.example/x", "owner/../../other", "owner/name?x=1", "owner name/x",
	} {
		if err := CheckRepo(bad); err == nil {
			t.Errorf("CheckRepo(%q) was accepted", bad)
		}
	}
}

func TestSourceForPrefersAConfiguredInternalServer(t *testing.T) {
	internal := SourceFor("https://dist.internal/manifest.json", "key", "", "0.1.0")
	if _, ok := internal.(Checker); !ok {
		t.Fatalf("a configured manifest URL routed to %T", internal)
	}
	if got := internal.Describe(); got != "https://dist.internal/manifest.json" {
		t.Errorf("Describe() = %q", got)
	}

	public := SourceFor("", "", "", "0.1.0")
	if _, ok := public.(GitHub); !ok {
		t.Fatalf("no manifest URL routed to %T, want the GitHub source", public)
	}
	if got := public.Describe(); got != "github.com/"+DefaultRepo {
		t.Errorf("Describe() = %q, want the default repo", got)
	}
	if got := SourceFor("", "", "someone/fork", "0.1.0").Describe(); got != "github.com/someone/fork" {
		t.Errorf("a configured repo is ignored: Describe() = %q", got)
	}
}
