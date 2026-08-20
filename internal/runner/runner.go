// Package runner executes rsync passes and parses their output. The Runner
// interface is the seam that lets fake mode drive the application without
// invoking rsync at all.
package runner

import (
	"context"

	"github.com/adamcarlile/flashcart/internal/pass"
)

// Change is one entry from rsync's itemized output.
type Change struct {
	Itemize string
	Size    int64
	Path    string
}

// Result is the outcome of one pass.
type Result struct {
	PassID string
	// Changes are files and directories that were, or would be, transferred.
	Changes []Change
	// TransferBytes counts only regular file transfers.
	TransferBytes int64
	// Deletions are paths present on the destination but absent from the
	// source. They are reported, never acted upon by this package.
	Deletions []string
}

// Event is a progress update emitted during a real run.
type Event struct {
	PassID  string
	Percent int
	Message string
}

// Runner executes passes.
type Runner interface {
	// DryRun enumerates what a pass would do without changing anything.
	DryRun(ctx context.Context, p pass.Pass) (Result, error)
	// Run performs the pass, emitting progress on events. It does not close
	// the channel.
	Run(ctx context.Context, p pass.Pass, events chan<- Event) (Result, error)
}
