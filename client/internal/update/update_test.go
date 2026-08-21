package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func sign(t *testing.T, manifest Manifest, key ed25519.PrivateKey) []byte {
	t.Helper()
	payload, err := SigningPayload(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, payload))
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func keypair(t *testing.T) (string, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(public), private
}

func sampleManifest() Manifest {
	return Manifest{
		Version: "0.2.0",
		Notes:   "Faster partials.",
		Artifacts: map[string]Artifact{
			PlatformKey(): {URL: "https://dist.internal/ld-0.2.0.pkg", SHA256: "abc", Size: 10},
		},
	}
}

func TestAValidManifestVerifies(t *testing.T) {
	public, private := keypair(t)
	manifest, err := Verify(sign(t, sampleManifest(), private), public)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if manifest.Version != "0.2.0" {
		t.Errorf("version = %q", manifest.Version)
	}
}

func TestATamperedManifestIsRefused(t *testing.T) {
	public, private := keypair(t)
	raw := sign(t, sampleManifest(), private)

	// Change the download URL, leaving the signature alone — the attack the
	// signature exists to stop.
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	decoded["artifacts"].(map[string]any)[PlatformKey()].(map[string]any)["url"] = "https://evil.example/x.pkg"
	tampered, _ := json.Marshal(decoded)

	if _, err := Verify(tampered, public); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("err = %v, want ErrUntrusted", err)
	}
}

func TestAManifestSignedByTheWrongKeyIsRefused(t *testing.T) {
	public, _ := keypair(t)
	_, otherPrivate := keypair(t)
	if _, err := Verify(sign(t, sampleManifest(), otherPrivate), public); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("err = %v, want ErrUntrusted", err)
	}
}

func TestAnUnsignedManifestIsRefused(t *testing.T) {
	public, _ := keypair(t)
	raw, _ := json.Marshal(sampleManifest())
	if _, err := Verify(raw, public); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("err = %v, want ErrUntrusted", err)
	}
}

func TestNoPublicKeyMeansNoInstall(t *testing.T) {
	_, private := keypair(t)
	if _, err := Verify(sign(t, sampleManifest(), private), ""); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("err = %v, want ErrUntrusted", err)
	}
}

func TestCheckReportsANewerVersion(t *testing.T) {
	public, private := keypair(t)
	raw := sign(t, sampleManifest(), private)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(raw)
	}))
	defer server.Close()

	checker := Checker{
		ManifestURL: server.URL,
		PublicKey:   public,
		Current:     "0.1.0",
		HTTPClient:  server.Client(),
	}
	result, err := checker.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Newer || result.Available != "0.2.0" {
		t.Errorf("result = %+v", result)
	}
}

func TestCheckRefusesPlainHTTP(t *testing.T) {
	checker := Checker{ManifestURL: "http://dist.internal/manifest.json", PublicKey: "x"}
	if _, err := checker.Check(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "https") {
		t.Fatalf("err = %v, want an https complaint", err)
	}
}

func TestCheckWithoutAManifestURLIsANoOp(t *testing.T) {
	if _, err := (Checker{}).Check(context.Background()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
}

func TestADownloadThatFailsItsHashIsDiscarded(t *testing.T) {
	payload := []byte("this is not the installer you are looking for")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(payload)
	}))
	defer server.Close()

	dir := t.TempDir()
	_, err := Download(context.Background(), Artifact{
		URL:    server.URL + "/ld.pkg",
		SHA256: hex.EncodeToString(sha256.New().Sum(nil)), // hash of nothing
		Size:   int64(len(payload)),
	}, dir, server.Client())
	if err == nil || !strings.Contains(err.Error(), "signed hash") {
		t.Fatalf("err = %v, want a hash mismatch", err)
	}

	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".update-") {
			t.Errorf("a failed download was kept: %s", entry.Name())
		}
	}
}

func TestAGoodDownloadIsKept(t *testing.T) {
	payload := []byte("installer bytes")
	digest := sha256.Sum256(payload)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(payload)
	}))
	defer server.Close()

	path, err := Download(context.Background(), Artifact{
		URL:    server.URL + "/ld.pkg",
		SHA256: hex.EncodeToString(digest[:]),
		Size:   int64(len(payload)),
	}, t.TempDir(), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("file = %q, err = %v", got, err)
	}
}

func TestVersionComparison(t *testing.T) {
	cases := []struct {
		candidate, current string
		newer              bool
	}{
		{"0.2.0", "0.1.0", true},
		{"0.1.1", "0.1.0", true},
		{"1.0.0", "0.9.9", true},
		{"0.1.0", "0.1.0", false},
		{"0.1.0", "0.2.0", false},
		{"v0.2.0", "0.1.0", true},
		{"0.2.0-rc1", "0.1.0", true},
		{"0.1.0", "0.1.0-rc1", false},
		{"0.10.0", "0.9.0", true}, // not a string comparison
	}
	for _, c := range cases {
		if got := IsNewer(c.candidate, c.current); got != c.newer {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", c.candidate, c.current, got, c.newer)
		}
	}
}
