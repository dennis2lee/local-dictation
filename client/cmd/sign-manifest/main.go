// Command sign-manifest generates the signing key and signs update manifests.
//
// The client refuses any manifest whose ed25519 signature does not verify, so
// this is the only way to publish an update it will accept.
//
//	sign-manifest keygen -out release-key
//	sign-manifest sign -key release-key.private -manifest manifest.json
//	sign-manifest verify -public <base64> -manifest manifest.json
//
// It shares update.SigningPayload with the client, which is what keeps signer
// and verifier honest about what "the canonical bytes" means. A separate
// re-implementation here would be a second definition, and the two would drift.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dennis2lee/local-dictation/client/internal/update"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen(os.Args[2:])
	case "sign":
		err = sign(os.Args[2:])
	case "verify":
		err = verify(os.Args[2:])
	case "hash":
		err = hash(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "sign-manifest: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `sign-manifest — publish updates the client will accept

  keygen -out <prefix>            write <prefix>.private and <prefix>.public
  sign   -key <file> -manifest <file>    sign in place
  verify -public <base64> -manifest <file>
  hash   <file>...                sha256 and size, for filling in artifacts

The public key goes into each client's settings (update.public_key). The
private key never leaves whatever holds your release secrets.
`)
}

func keygen(args []string) error {
	flags := flag.NewFlagSet("keygen", flag.ExitOnError)
	prefix := flags.String("out", "release-key", "output prefix")
	if err := flags.Parse(args); err != nil {
		return err
	}

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate a key: %w", err)
	}

	privatePath := *prefix + ".private"
	publicPath := *prefix + ".public"

	// 0600 before anything is written to it: a private key must never exist,
	// even briefly, with permissions that let anyone else read it.
	file, err := os.OpenFile(privatePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create %s (it may already exist): %w", privatePath, err)
	}
	if _, err := io.WriteString(file, base64.StdEncoding.EncodeToString(private)+"\n"); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}

	encoded := base64.StdEncoding.EncodeToString(public)
	if err := os.WriteFile(publicPath, []byte(encoded+"\n"), 0o644); err != nil {
		return err
	}

	fmt.Printf("private key: %s (keep it secret)\n", privatePath)
	fmt.Printf("public key:  %s\n\n", publicPath)
	fmt.Printf("Put this in each client's settings.json:\n\n")
	fmt.Printf("  \"update\": {\n")
	fmt.Printf("    \"manifest_url\": \"https://dist.internal/local-dictation/manifest.json\",\n")
	fmt.Printf("    \"public_key\": \"%s\"\n", encoded)
	fmt.Printf("  }\n")
	return nil
}

func sign(args []string) error {
	flags := flag.NewFlagSet("sign", flag.ExitOnError)
	keyPath := flags.String("key", "", "private key file from keygen")
	manifestPath := flags.String("manifest", "", "manifest to sign, updated in place")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *keyPath == "" || *manifestPath == "" {
		return fmt.Errorf("both -key and -manifest are required")
	}

	raw, err := os.ReadFile(*keyPath)
	if err != nil {
		return fmt.Errorf("read the private key: %w", err)
	}
	privateKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(privateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("%s is not a base64 ed25519 private key", *keyPath)
	}

	manifestRaw, err := os.ReadFile(*manifestPath)
	if err != nil {
		return fmt.Errorf("read the manifest: %w", err)
	}
	var manifest update.Manifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		return fmt.Errorf("parse the manifest: %w", err)
	}

	if err := checkManifest(manifest); err != nil {
		return err
	}

	payload, err := update.SigningPayload(manifest)
	if err != nil {
		return err
	}
	manifest.Signature = base64.StdEncoding.EncodeToString(
		ed25519.Sign(ed25519.PrivateKey(privateKey), payload))

	signed, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(*manifestPath, append(signed, '\n'), 0o644); err != nil {
		return err
	}

	// Verify what was just written, not what is in memory: this catches a
	// serialisation difference between signing and publishing, which is the
	// failure mode that would otherwise only show up on a user's machine.
	publicKey := ed25519.PrivateKey(privateKey).Public().(ed25519.PublicKey)
	if _, err := update.Verify(append(signed, '\n'), base64.StdEncoding.EncodeToString(publicKey)); err != nil {
		return fmt.Errorf("the signed manifest does not verify: %w", err)
	}

	fmt.Printf("signed %s for version %s (%d artifact(s))\n",
		*manifestPath, manifest.Version, len(manifest.Artifacts))
	return nil
}

func verify(args []string) error {
	flags := flag.NewFlagSet("verify", flag.ExitOnError)
	publicKey := flags.String("public", "", "base64 public key")
	manifestPath := flags.String("manifest", "", "manifest to check")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *publicKey == "" || *manifestPath == "" {
		return fmt.Errorf("both -public and -manifest are required")
	}

	raw, err := os.ReadFile(*manifestPath)
	if err != nil {
		return err
	}
	key := *publicKey
	// Accept a path to the .public file as well as the key itself.
	if contents, err := os.ReadFile(key); err == nil {
		key = strings.TrimSpace(string(contents))
	}

	manifest, err := update.Verify(raw, key)
	if err != nil {
		return err
	}
	fmt.Printf("%s verifies: version %s\n", *manifestPath, manifest.Version)
	for platform, artifact := range manifest.Artifacts {
		fmt.Printf("  %-16s %s\n", platform, artifact.URL)
	}
	return nil
}

func hash(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("hash needs at least one file")
	}
	for _, path := range args {
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		digest := sha256.New()
		size, err := io.Copy(digest, file)
		file.Close()
		if err != nil {
			return err
		}
		fmt.Printf("%-40s sha256=%s size=%d\n",
			filepath.Base(path), hex.EncodeToString(digest.Sum(nil)), size)
	}
	return nil
}

// checkManifest catches the mistakes that would otherwise be discovered by a
// client refusing to update, which is the worst place to discover them.
func checkManifest(manifest update.Manifest) error {
	var problems []string

	if strings.TrimSpace(manifest.Version) == "" {
		problems = append(problems, "version is empty")
	}
	if len(manifest.Artifacts) == 0 {
		problems = append(problems, "no artifacts")
	}
	for platform, artifact := range manifest.Artifacts {
		if !strings.HasPrefix(artifact.URL, "https://") {
			problems = append(problems, fmt.Sprintf("%s: url must use https", platform))
		}
		if len(artifact.SHA256) != 64 {
			problems = append(problems,
				fmt.Sprintf("%s: sha256 must be 64 hex characters (use `sign-manifest hash`)", platform))
		}
		if artifact.Size <= 0 {
			problems = append(problems, fmt.Sprintf("%s: size must be set", platform))
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("manifest is not publishable:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}
