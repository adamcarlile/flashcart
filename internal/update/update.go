// Package update fetches and installs newer releases, verifying SHA-256
// before replacing the running binary.
package update

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
	"strings"
	"time"
)

// Release is a published GitHub release.
type Release struct {
	Tag string
	// Assets maps asset name to download URL.
	Assets map[string]string
}

// client has no per-request Timeout: a bare client-level timeout would
// silently cap every call, including a large binary download, at a fixed
// duration regardless of what a caller's context intends to allow. Callers
// govern their own deadline through the context they pass in.
var client = &http.Client{}

// latestTimeout bounds the release-metadata call, which should always be
// quick. It is layered on top of the caller's context via
// context.WithTimeout, so it can only shorten an already-generous budget,
// never lengthen a short one.
const latestTimeout = 15 * time.Second

// Latest returns the newest release for a repo such as "adamcarlile/flashcart".
func Latest(ctx context.Context, repo string) (Release, error) {
	ctx, cancel := context.WithTimeout(ctx, latestTimeout)
	defer cancel()

	url := "https://api.github.com/repos/" + repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("fetch latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("fetch latest release: HTTP %d", resp.StatusCode)
	}

	var payload struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Release{}, err
	}

	rel := Release{Tag: payload.TagName, Assets: map[string]string{}}
	for _, a := range payload.Assets {
		rel.Assets[a.Name] = a.URL
	}
	return rel, nil
}

// Fetch downloads an asset.
func Fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// ParseChecksums reads a GoReleaser checksums.txt into name to hex digest.
func ParseChecksums(body string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		out[strings.TrimPrefix(fields[1], "*")] = fields[0]
	}
	return out
}

// VerifyAndSwap checks the payload against wantSHA and, only if it matches,
// atomically replaces the binary. On any failure the existing binary is left
// exactly as it was, with no debris beside it.
func VerifyAndSwap(binaryPath string, payload []byte, wantSHA string) error {
	sum := sha256.Sum256(payload)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, wantSHA) {
		return fmt.Errorf("checksum mismatch: got %s, want %s", got, wantSHA)
	}

	dir := filepath.Dir(binaryPath)
	tmp, err := os.CreateTemp(dir, ".flashcart-update-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}

	if _, err := tmp.Write(payload); err != nil {
		cleanup()
		return fmt.Errorf("write update: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		cleanup()
		return fmt.Errorf("chmod update: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close update: %w", err)
	}

	// Rename within the same directory is atomic, so the binary is never
	// half-written.
	if err := os.Rename(tmpName, binaryPath); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("replace %s: %w", binaryPath, err)
	}
	return nil
}
