// Package syncer runs the six passes in order for real, emitting progress
// and stopping at the first failure.
package syncer

import (
	"context"

	"github.com/adamcarlile/flashcart/internal/pass"
	"github.com/adamcarlile/flashcart/internal/runner"
)

// PassResult records the outcome of one pass.
type PassResult struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	OK    bool   `json:"ok"`
	Err   string `json:"err,omitempty"`
	// Warning carries a tolerated non-fatal rsync condition (exit 23 or
	// 24) surfaced to the user rather than swallowed. The pass is still OK.
	Warning string `json:"warning,omitempty"`
}

// Summary is the outcome of a whole run.
type Summary struct {
	Passes []PassResult `json:"passes"`
	OK     bool         `json:"ok"`
	Err    string       `json:"err,omitempty"`
}

// Run executes every pass in order. A failure abandons the remaining passes:
// continuing would leave the trees in a state nobody reasoned about, and the
// caller unmounts either way.
func Run(ctx context.Context, r runner.Runner, ps []pass.Pass, events chan<- runner.Event) Summary {
	var sum Summary
	for _, p := range ps {
		if err := ctx.Err(); err != nil {
			sum.Err = err.Error()
			return sum
		}
		res, err := r.Run(ctx, p, events)
		pr := PassResult{ID: p.ID, Label: p.Label, OK: err == nil, Warning: res.Warning}
		if err != nil {
			pr.Err = err.Error()
			sum.Passes = append(sum.Passes, pr)
			sum.Err = err.Error()
			return sum
		}
		sum.Passes = append(sum.Passes, pr)
	}
	sum.OK = true
	return sum
}
