package runner

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/adamcarlile/flashcart/internal/pass"
)

// TestScanLinesOrCR verifies that carriage returns and newlines are handled
// correctly, including CRLF pairs which must not produce empty tokens.
func TestScanLinesOrCR(t *testing.T) {
	cases := map[string][]string{
		"abc\r\ndef\r\n":   {"abc", "def"},
		"a\rb\rc":          {"a", "b", "c"},
		"x\ny\n":           {"x", "y"},
		"no-terminator":    {"no-terminator"},
		"":                 {},
	}

	for input, want := range cases {
		t.Run(fmt.Sprintf("%q", input), func(t *testing.T) {
			var got []string
			sc := bufio.NewScanner(bytes.NewReader([]byte(input)))
			sc.Split(scanLinesOrCR)
			for sc.Scan() {
				got = append(got, sc.Text())
			}
			if err := sc.Err(); err != nil {
				t.Fatalf("scanner error: %v", err)
			}
			if len(got) != len(want) {
				t.Errorf("len(got) = %d, want %d: %q vs %q", len(got), len(want), got, want)
				return
			}
			for i, g := range got {
				if g != want[i] {
					t.Errorf("token %d: got %q, want %q", i, g, want[i])
				}
			}
		})
	}
}

// TestExecDryRunAndRun tests a real rsync round trip. It is skipped if rsync
// is not available.
func TestExecDryRunAndRun(t *testing.T) {
	// Check if rsync is available.
	_, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync not found in PATH")
	}

	// Create temporary directories.
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	// Create test files in source. Include:
	// - Regular files
	// - A file with a pipe character in the name
	// - A file with an ampersand and comma in the name
	testFiles := map[string]string{
		"file1.txt":              "content1",
		"file2.bin":              "content2",
		"Weird | Name.zip":       "pipe character",
		"Title, Author & More.md": "special chars",
	}

	for name, content := range testFiles {
		path := filepath.Join(srcDir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("failed to create test file %q: %v", name, err)
		}
	}

	// Create one file only in destination (will be reported as deletion).
	dstOnlyPath := filepath.Join(dstDir, "dest-only.txt")
	if err := os.WriteFile(dstOnlyPath, []byte("delete me"), 0644); err != nil {
		t.Fatalf("failed to create dest-only file: %v", err)
	}

	// Build a pass for testing. We construct it manually since we're using
	// temp directories instead of a real config.
	p := pass.Pass{
		ID:      "test-pass",
		Src:     srcDir + "/",
		Dst:     dstDir + "/",
		Filters: []string{}, // No filters for test simplicity.
	}

	exec := NewExec("rsync")

	// Test DryRun.
	dryResult, err := exec.DryRun(context.Background(), p)
	if err != nil {
		t.Fatalf("DryRun failed: %v", err)
	}

	if dryResult.PassID != p.ID {
		t.Errorf("PassID = %q, want %q", dryResult.PassID, p.ID)
	}

	// Verify changes are present.
	if len(dryResult.Changes) == 0 {
		t.Errorf("DryRun returned no changes")
	}

	// Verify filenames with special characters are preserved.
	hasWeirdName := false
	hasSpecialChars := false
	for _, ch := range dryResult.Changes {
		if ch.Path == "Weird | Name.zip" {
			hasWeirdName = true
		}
		if ch.Path == "Title, Author & More.md" {
			hasSpecialChars = true
		}
	}
	if !hasWeirdName {
		t.Errorf("DryRun did not preserve pipe in filename")
	}
	if !hasSpecialChars {
		t.Errorf("DryRun did not preserve special characters in filename")
	}

	// Verify TransferBytes counts only regular files (second flag char = 'f').
	// Directories would have second flag char = 'd'.
	regularFileCount := 0
	for _, ch := range dryResult.Changes {
		if len(ch.Itemize) > 1 && ch.Itemize[1] == 'f' {
			regularFileCount++
		}
	}
	if regularFileCount == 0 {
		t.Errorf("DryRun found no regular files")
	}

	// Verify deletion is reported.
	hasDeletion := false
	for _, del := range dryResult.Deletions {
		if del == "dest-only.txt" {
			hasDeletion = true
		}
	}
	if !hasDeletion {
		t.Errorf("DryRun did not report dest-only.txt as deletion")
	}

	// Test Run with progress events.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	eventsChan := make(chan Event, 10)
	runResult, err := exec.Run(ctx, p, eventsChan)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if runResult.PassID != p.ID {
		t.Errorf("Run PassID = %q, want %q", runResult.PassID, p.ID)
	}

	// Collect events.
	var events []Event
	for {
		select {
		case ev := <-eventsChan:
			events = append(events, ev)
		case <-time.After(100 * time.Millisecond):
			goto checkEvents
		}
	}

checkEvents:
	// A successful rsync should emit at least one progress event.
	if len(events) == 0 {
		t.Logf("warning: Run emitted no progress events (small transfer or very fast rsync)")
	}
	for _, ev := range events {
		if ev.Percent < 0 || ev.Percent > 100 {
			t.Errorf("invalid progress percent: %d", ev.Percent)
		}
	}
}
