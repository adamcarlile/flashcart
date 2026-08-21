package syncer

import (
	"context"
	"errors"
	"testing"

	"github.com/adamcarlile/flashcart/internal/config"
	"github.com/adamcarlile/flashcart/internal/nas"
	"github.com/adamcarlile/flashcart/internal/pass"
	"github.com/adamcarlile/flashcart/internal/runner"
)

type scriptRunner struct {
	ran     []string
	failOn  string
	failErr error
}

func (s *scriptRunner) DryRun(context.Context, pass.Pass) (runner.Result, error) {
	return runner.Result{}, nil
}

func (s *scriptRunner) Run(_ context.Context, p pass.Pass, events chan<- runner.Event) (runner.Result, error) {
	s.ran = append(s.ran, p.ID)
	events <- runner.Event{PassID: p.ID, Percent: 100}
	if p.ID == s.failOn {
		return runner.Result{PassID: p.ID}, s.failErr
	}
	return runner.Result{PassID: p.ID}, nil
}

func testPasses() []pass.Pass {
	cfg := &config.Config{Trees: config.Trees{
		Roms:  config.Tree{Export: "/e/roms", Local: "/l/roms"},
		Bios:  config.Tree{Export: "/e/bios", Local: "/l/bios"},
		Saves: config.Tree{Export: "/e/saves", Local: "/l/saves"},
	}}
	return pass.Passes(cfg, nas.Mounts{Roms: "/m/roms", Bios: "/m/bios", Saves: "/m/saves"})
}

func drain(events chan runner.Event) {
	go func() {
		for range events {
		}
	}()
}

func TestRunExecutesAllPassesInOrder(t *testing.T) {
	r := &scriptRunner{}
	events := make(chan runner.Event, 64)
	drain(events)

	sum := Run(context.Background(), r, testPasses(), events)

	if !sum.OK {
		t.Fatalf("Summary.OK = false: %s", sum.Err)
	}
	want := []string{"bios-pull", "roms-content-pull", "roms-metadata-pull", "roms-metadata-push", "saves-push"}
	if len(r.ran) != len(want) {
		t.Fatalf("ran %v, want %v", r.ran, want)
	}
	for i := range want {
		if r.ran[i] != want[i] {
			t.Fatalf("ran %v, want %v", r.ran, want)
		}
	}
	if len(sum.Passes) != len(want) {
		t.Errorf("len(Summary.Passes) = %d, want %d", len(sum.Passes), len(want))
	}
}

// A failing pass abandons the remainder rather than pressing on with a
// half-synced tree.
func TestFailureAbandonsRemainingPasses(t *testing.T) {
	r := &scriptRunner{failOn: "roms-content-pull", failErr: errors.New("disk full")}
	events := make(chan runner.Event, 64)
	drain(events)

	sum := Run(context.Background(), r, testPasses(), events)

	if sum.OK {
		t.Fatal("Summary.OK = true after a pass failed")
	}
	if len(r.ran) != 2 {
		t.Errorf("ran %v, want to stop after roms-content-pull", r.ran)
	}
	if sum.Err == "" {
		t.Error("Summary.Err is empty after a failure")
	}
	if got := sum.Passes[1]; got.OK || got.Err == "" {
		t.Errorf("failing pass recorded as %+v", got)
	}
	if got := sum.Passes[0]; !got.OK {
		t.Errorf("earlier successful pass recorded as %+v", got)
	}
}

func TestCancellationStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r := &scriptRunner{}
	events := make(chan runner.Event, 64)
	drain(events)

	sum := Run(ctx, r, testPasses(), events)
	if sum.OK {
		t.Error("Summary.OK = true despite a cancelled context")
	}
	if len(r.ran) != 0 {
		t.Errorf("ran %v after cancellation", r.ran)
	}
}

// cancellingRunner cancels context when a specific pass is invoked.
type cancellingRunner struct {
	ran       []string
	cancelOn  string
	cancelCtx context.CancelFunc
}

func (c *cancellingRunner) DryRun(context.Context, pass.Pass) (runner.Result, error) {
	return runner.Result{}, nil
}

func (c *cancellingRunner) Run(_ context.Context, p pass.Pass, events chan<- runner.Event) (runner.Result, error) {
	c.ran = append(c.ran, p.ID)
	events <- runner.Event{PassID: p.ID, Percent: 100}
	if p.ID == c.cancelOn {
		c.cancelCtx()
	}
	return runner.Result{PassID: p.ID}, nil
}

// Cancellation mid-run abandons remaining passes. This test catches regressions
// that move the ctx.Err() check outside the loop.
func TestCancellationStopsMidRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cr := &cancellingRunner{cancelOn: "roms-content-pull", cancelCtx: cancel}

	events := make(chan runner.Event, 64)
	drain(events)

	sum := Run(ctx, cr, testPasses(), events)

	if sum.OK {
		t.Fatal("Summary.OK = true despite cancellation mid-run")
	}
	if len(cr.ran) != 2 {
		t.Errorf("ran %v, want to stop after roms-content-pull (2 passes)", cr.ran)
	}
	if len(cr.ran) >= 2 && cr.ran[1] != "roms-content-pull" {
		t.Errorf("ran %v, second pass should be roms-content-pull", cr.ran)
	}
	if sum.Err == "" {
		t.Error("Summary.Err is empty after mid-run cancellation")
	}
}
