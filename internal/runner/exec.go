package runner

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/adamcarlile/flashcart/internal/pass"
)

// Exec runs real rsync. Arguments are always passed as a slice: the library
// contains filenames with spaces, ampersands, apostrophes and brackets, and
// no part of this path may go through a shell.
type Exec struct {
	Binary string
}

// NewExec returns an Exec using the given rsync binary.
func NewExec(binary string) *Exec {
	if binary == "" {
		binary = "rsync"
	}
	return &Exec{Binary: binary}
}

var _ Runner = (*Exec)(nil)

// DryRun enumerates a pass without changing anything.
func (e *Exec) DryRun(ctx context.Context, p pass.Pass) (Result, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, e.Binary, p.DryRunArgs()...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return Result{PassID: p.ID}, fmt.Errorf("rsync dry run %s: %w: %s", p.ID, err, strings.TrimSpace(stderr.String()))
	}
	return ParseItemize(p.ID, stdout.String()), nil
}

// Run performs a pass, forwarding progress percentages as they arrive.
// rsync writes progress with carriage returns rather than newlines, so the
// stream is split on both.
func (e *Exec) Run(ctx context.Context, p pass.Pass, events chan<- Event) (Result, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, e.Binary, p.RunArgs()...)
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{PassID: p.ID}, fmt.Errorf("rsync %s: %w", p.ID, err)
	}
	if err := cmd.Start(); err != nil {
		return Result{PassID: p.ID}, fmt.Errorf("rsync %s: %w", p.ID, err)
	}

	sc := bufio.NewScanner(stdout)
	sc.Split(scanLinesOrCR)
	for sc.Scan() {
		if pct, ok := ParseProgress(sc.Text()); ok {
			select {
			case events <- Event{PassID: p.ID, Percent: pct}:
			case <-ctx.Done():
			default:
			}
		}
	}
	if err := sc.Err(); err != nil {
		return Result{PassID: p.ID}, fmt.Errorf("rsync %s: %w", p.ID, err)
	}
	if err := cmd.Wait(); err != nil {
		return Result{PassID: p.ID}, fmt.Errorf("rsync %s: %w: %s", p.ID, err, strings.TrimSpace(stderr.String()))
	}
	return Result{PassID: p.ID}, nil
}

// scanLinesOrCR splits on either a newline or a carriage return, because
// rsync's progress output overwrites a single line using CR.
func scanLinesOrCR(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		// Consume the terminator. If it's \r and the next byte is \n,
		// consume both. But if \r is at the end and we're not at EOF,
		// we can't tell yet if \n follows, so request more data.
		advance := i + 1
		if data[i] == '\r' && i+1 < len(data) && data[i+1] == '\n' {
			advance = i + 2
		} else if data[i] == '\r' && i+1 == len(data) && !atEOF {
			// \r at end of buffer, not at EOF, so we don't know if \n follows
			return 0, nil, nil
		}
		return advance, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}
