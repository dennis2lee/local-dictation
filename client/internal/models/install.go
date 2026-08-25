package models

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// State is what a scan found for one model.
type State struct {
	Model Model
	// Installed is true only when the file that actually matters is there —
	// see Model.Weights. A directory holding everything but the weights is a
	// download that was interrupted, and calling that installed is how someone
	// ends up debugging a load failure instead of pressing Get again.
	Installed bool
	// Path is where it is, or where it would go.
	Path string
	// Bytes is what is on disk, once installed.
	Bytes int64
}

// Scan reports the state of every catalogue model under a directory.
func Scan(dir string) []State {
	catalogue := Catalogue()
	states := make([]State, 0, len(catalogue))
	for _, model := range catalogue {
		states = append(states, Stat(model, dir))
	}
	return states
}

// Stat reports the state of one model under a directory.
func Stat(model Model, dir string) State {
	state := State{Model: model, Path: filepath.Join(dir, model.Name)}
	if model.IsFile() {
		if info, err := os.Stat(state.Path); err == nil && !info.IsDir() {
			state.Installed, state.Bytes = true, info.Size()
		}
		return state
	}
	if info, err := os.Stat(filepath.Join(state.Path, model.Weights())); err == nil && !info.IsDir() {
		state.Installed = true
		state.Bytes = directorySize(state.Path)
	}
	return state
}

func directorySize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil //nolint:nilerr // an unreadable entry is not worth failing a size for
		}
		if info, err := entry.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// Progress is one step of an install, for showing in the UI.
type Progress struct {
	// File being fetched, and its position in the set.
	File  string
	Index int
	Count int
	// Bytes copied so far across the whole model, and the total when known.
	Bytes      int64
	TotalBytes int64
}

// Endpoint is the model host. HF_ENDPOINT overrides it, which is how an
// internal mirror is used — the same variable the fetch scripts read.
func Endpoint() string {
	if custom := strings.TrimSpace(os.Getenv("HF_ENDPOINT")); custom != "" {
		return strings.TrimRight(custom, "/")
	}
	return "https://huggingface.co"
}

// Install downloads a model into dir.
//
// Files land under a temporary name and are moved into place only once the
// whole set has arrived, so an interrupted download never leaves behind a
// directory that looks installed. A SHA256SUMS is written beside them, which is
// what `fetch-model.sh --verify` reads afterwards.
func Install(ctx context.Context, model Model, dir string, progress func(Progress)) error {
	if progress == nil {
		progress = func(Progress) {}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	client := &http.Client{Timeout: 0} // large files; the context bounds it

	if model.IsFile() {
		progress(Progress{File: model.Name, Index: 1, Count: 1, TotalBytes: model.Bytes})
		target := filepath.Join(dir, model.Name)
		return downloadTo(ctx, client, model.URL, target, model.Bytes,
			func(done int64) {
				progress(Progress{File: model.Name, Index: 1, Count: 1, Bytes: done, TotalBytes: model.Bytes})
			})
	}

	files, err := repositoryFiles(ctx, client, model.Repo)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("%s lists no files to download", model.Repo)
	}

	// Staged next to the destination rather than in the system temp directory:
	// a model is gigabytes, temp is often a different and smaller filesystem,
	// and a rename across filesystems is a copy of the whole thing.
	staging, err := os.MkdirTemp(dir, "."+model.Name+".partial-")
	if err != nil {
		return fmt.Errorf("create a staging directory in %s: %w", dir, err)
	}
	defer os.RemoveAll(staging)

	sums := make(map[string]string, len(files))
	var copied int64
	for index, name := range files {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		progress(Progress{File: name, Index: index + 1, Count: len(files), Bytes: copied, TotalBytes: model.Bytes})
		target := filepath.Join(staging, name)
		url := fmt.Sprintf("%s/%s/resolve/main/%s", Endpoint(), model.Repo, name)
		before := copied
		sum, err := download(ctx, client, url, target, func(done int64) {
			copied = before + done
			progress(Progress{
				File: name, Index: index + 1, Count: len(files),
				Bytes: copied, TotalBytes: model.Bytes,
			})
		})
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		sums[name] = sum
		copied = before + fileSize(target)
	}

	if err := writeSums(filepath.Join(staging, "SHA256SUMS"), sums); err != nil {
		return err
	}

	final := filepath.Join(dir, model.Name)
	// Replacing an existing install: move the old one aside, put the new one
	// in, then delete. A remove-then-rename would leave nothing at all if the
	// rename failed.
	previous := ""
	if _, err := os.Stat(final); err == nil {
		previous = final + ".replaced"
		_ = os.RemoveAll(previous)
		if err := os.Rename(final, previous); err != nil {
			return fmt.Errorf("move the existing %s aside: %w", model.Name, err)
		}
	}
	if err := os.Rename(staging, final); err != nil {
		if previous != "" {
			_ = os.Rename(previous, final)
		}
		return fmt.Errorf("install %s: %w", model.Name, err)
	}
	if previous != "" {
		_ = os.RemoveAll(previous)
	}
	return nil
}

func fileSize(path string) int64 {
	if info, err := os.Stat(path); err == nil {
		return info.Size()
	}
	return 0
}

// repositoryFiles asks the repository what it holds, skipping its furniture.
//
// Asked rather than assumed because the file set differs between conversions —
// the large-v3 repositories carry a preprocessor config and the smaller ones do
// not — and a fixed list would either miss a file or fetch one that is not
// there.
func repositoryFiles(ctx context.Context, client *http.Client, repo string) ([]string, error) {
	url := fmt.Sprintf("%s/api/models/%s", Endpoint(), repo)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("ask %s what it holds: %w", repo, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %s", url, response.Status)
	}

	var listing struct {
		Siblings []struct {
			Name string `json:"rfilename"`
		} `json:"siblings"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&listing); err != nil {
		return nil, fmt.Errorf("read the file list for %s: %w", repo, err)
	}

	var files []string
	for _, sibling := range listing.Siblings {
		if skipped(sibling.Name) {
			continue
		}
		files = append(files, sibling.Name)
	}
	sort.Strings(files)
	return files, nil
}

// skipped drops repository furniture and anything that would escape the
// destination directory. A repository is not a trusted source of file paths.
func skipped(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") || name == "README.md" {
		return true
	}
	if strings.Contains(name, "/") || strings.Contains(name, `\`) {
		// Every conversion this catalogue names is flat. A nested path would
		// be a repository this code has not been checked against, and joining
		// it blindly is how ../ gets written outside the directory.
		return true
	}
	return name != filepath.Base(name)
}

func download(ctx context.Context, client *http.Client, url, target string, onProgress func(int64)) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s returned %s", url, response.Status)
	}

	file, err := os.Create(target)
	if err != nil {
		return "", err
	}
	defer file.Close()

	digest := sha256.New()
	counter := &progressWriter{onProgress: onProgress, every: 512 << 10}
	if _, err := io.Copy(io.MultiWriter(file, digest, counter), response.Body); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), file.Sync()
}

func downloadTo(ctx context.Context, client *http.Client, url, target string, _ int64, onProgress func(int64)) error {
	staged := target + ".partial"
	if _, err := download(ctx, client, url, staged, onProgress); err != nil {
		_ = os.Remove(staged)
		return err
	}
	return os.Rename(staged, target)
}

// progressWriter reports every `every` bytes rather than every chunk: a
// gigabyte at 32 KiB a read is thirty thousand UI updates, and the window
// spends longer laying them out than the download takes.
type progressWriter struct {
	onProgress func(int64)
	every      int64
	total      int64
	reported   int64
	last       time.Time
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.total += int64(len(p))
	if w.total-w.reported >= w.every || time.Since(w.last) > 200*time.Millisecond {
		w.reported = w.total
		w.last = time.Now()
		if w.onProgress != nil {
			w.onProgress(w.total)
		}
	}
	return len(p), nil
}

func writeSums(path string, sums map[string]string) error {
	names := make([]string, 0, len(sums))
	for name := range sums {
		names = append(names, name)
	}
	sort.Strings(names)

	var builder strings.Builder
	for _, name := range names {
		fmt.Fprintf(&builder, "%s  %s\n", sums[name], name)
	}
	return os.WriteFile(path, []byte(builder.String()), 0o644)
}
