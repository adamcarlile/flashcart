// Package fake provides a scripted backend satisfying both nas.Provider and
// runner.Runner, so the whole application can be driven with no NAS, no
// Batocera box and no data.
//
// Nothing above the seam knows which implementation it has, so fake mode
// exercises the real server, plan, syncer and drift code.
package fake

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/adamcarlile/flashcart/internal/nas"
	"github.com/adamcarlile/flashcart/internal/pass"
	"github.com/adamcarlile/flashcart/internal/runner"
)

// Scenario names a scripted state of the world.
type Scenario string

const (
	// ScenarioSeed is an empty box against a full NAS: the first run.
	ScenarioSeed Scenario = "seed"
	// ScenarioSteady is a small realistic delta.
	ScenarioSteady Scenario = "steady"
	// ScenarioDrift has removals pending on both sides.
	ScenarioDrift Scenario = "drift"
	// ScenarioOffline has an unreachable NAS.
	ScenarioOffline Scenario = "offline"
	// ScenarioNoSpace has a transfer that will not fit.
	ScenarioNoSpace Scenario = "nospace"
	// ScenarioFailure fails partway through the ROM content pull.
	ScenarioFailure Scenario = "failure"
)

// Scenarios lists every scenario, in the order the UI should offer them.
var Scenarios = []Scenario{
	ScenarioSeed, ScenarioSteady, ScenarioDrift,
	ScenarioOffline, ScenarioNoSpace, ScenarioFailure,
}

func valid(s Scenario) bool {
	for _, k := range Scenarios {
		if k == s {
			return true
		}
	}
	return false
}

// fakeMountRoot is deliberately a path that does not exist. If this provider
// were ever wired to real rsync by mistake, the mistake fails loudly instead
// of touching real data.
const fakeMountRoot = "/nonexistent/flashcart-fake"

// Backend is a scripted nas.Provider and runner.Runner.
type Backend struct {
	mu       sync.RWMutex
	scenario Scenario

	// Delay is the pause between simulated progress ticks. Tests set it to
	// zero; the server leaves the default so SSE streaming is exercised.
	Delay time.Duration
}

// New returns a Backend in the given scenario.
func New(s Scenario) (*Backend, error) {
	if !valid(s) {
		return nil, fmt.Errorf("unknown fake scenario %q", s)
	}
	return &Backend{scenario: s, Delay: 40 * time.Millisecond}, nil
}

var (
	_ nas.Provider  = (*Backend)(nil)
	_ runner.Runner = (*Backend)(nil)
)

// Scenario returns the current scenario.
func (b *Backend) Scenario() Scenario {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.scenario
}

// SetScenario switches scenario at runtime so states can be compared without
// restarting.
func (b *Backend) SetScenario(s Scenario) error {
	if !valid(s) {
		return fmt.Errorf("unknown fake scenario %q", s)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.scenario = s
	return nil
}

// Probe reports the NAS as unreachable only in the offline scenario.
func (b *Backend) Probe(context.Context) error {
	if b.Scenario() == ScenarioOffline {
		return fmt.Errorf("%w: fake NAS is offline", nas.ErrUnreachable)
	}
	return nil
}

// Mount hands back non-existent paths and a no-op unmount.
func (b *Backend) Mount(context.Context) (nas.Mounts, func() error, error) {
	if b.Scenario() == ScenarioOffline {
		return nas.Mounts{}, func() error { return nil },
			fmt.Errorf("%w: fake NAS is offline", nas.ErrUnreachable)
	}
	return nas.Mounts{
			Roms:  fakeMountRoot + "/roms",
			Bios:  fakeMountRoot + "/bios",
			Saves: fakeMountRoot + "/saves",
		},
		func() error { return nil },
		nil
}

// FreeSpace reports the real box's disk, or a nearly full one in the
// nospace scenario.
func (b *Backend) FreeSpace(string) (int64, int64, error) {
	const total = 459 * gib
	if b.Scenario() == ScenarioNoSpace {
		return 4 * gib, total, nil
	}
	return 433 * gib, total, nil
}

// DryRun returns the scripted result for a pass in the current scenario. A
// real dry run against an unmounted export fails, so this must too: callers
// (including this package's own tests) may reach DryRun without ever
// calling Mount.
func (b *Backend) DryRun(_ context.Context, p pass.Pass) (runner.Result, error) {
	if b.Scenario() == ScenarioOffline {
		return runner.Result{PassID: p.ID}, fmt.Errorf("%w: fake NAS is offline", nas.ErrUnreachable)
	}
	res := b.script()[p.ID]
	res.PassID = p.ID
	return res, nil
}

// Run simulates a transfer, emitting progress over wall-clock time so SSE
// streaming and the single-flight lock are genuinely exercised. The
// scenario is snapshotted once at the top: SetScenario is designed to be
// callable while a run is in flight, and reading b.Scenario() repeatedly
// through this method would let a mid-run switch tear the offline check,
// the failure check and the scripted result apart, each individually
// mutex-safe but mutually inconsistent.
func (b *Backend) Run(ctx context.Context, p pass.Pass, events chan<- runner.Event) (runner.Result, error) {
	s := b.Scenario()

	if s == ScenarioOffline {
		return runner.Result{PassID: p.ID}, fmt.Errorf("%w: fake NAS is offline", nas.ErrUnreachable)
	}

	failAt := -1
	if s == ScenarioFailure && p.ID == "roms-content-pull" {
		failAt = 60
	}

	for pct := 0; pct <= 100; pct += 10 {
		if err := ctx.Err(); err != nil {
			return runner.Result{PassID: p.ID}, err
		}
		if failAt >= 0 && pct > failAt {
			return runner.Result{PassID: p.ID}, errors.New(
				"rsync roms-content-pull: exit status 23: rsync: [receiver] write failed on \"ps3/Game 001 (USA).zip\": No space left on device (28)")
		}
		select {
		case events <- runner.Event{PassID: p.ID, Percent: pct}:
		case <-ctx.Done():
			return runner.Result{PassID: p.ID}, ctx.Err()
		}
		if b.Delay > 0 {
			select {
			case <-time.After(b.Delay):
			case <-ctx.Done():
				return runner.Result{PassID: p.ID}, ctx.Err()
			}
		}
	}

	res := scriptFor(s)[p.ID]
	res.PassID = p.ID
	return res, nil
}

// script returns the per-pass results for the current scenario.
func (b *Backend) script() map[string]runner.Result {
	return scriptFor(b.Scenario())
}

// scriptFor returns the per-pass results for scenario s. It takes the
// scenario as a parameter, rather than reading b.scenario itself, so
// callers that must act on a single consistent scenario value (Run) can
// snapshot it once and thread it through instead of re-reading the
// (possibly since-changed) live scenario.
func scriptFor(s Scenario) map[string]runner.Result {
	switch s {
	case ScenarioSeed:
		// Empty box: everything arrives, on both the ROMs and saves trees.
		// The metadata push and the saves push each see the NAS's own
		// copies as absent from their (still-empty) local source, which
		// the projected-state calculation must cancel out to yield no
		// drift — CRITICAL 1: before saves-pull existed, this scenario
		// modelled an empty NAS saves tree instead (saves-push: result(nil)
		// with nothing on the NAS either), which is exactly the shape that
		// hid the bug: with a genuinely full NAS archive and an empty local
		// tree, every save was offered for deletion at the moment no local
		// copy existed.
		var metaDeletions []string
		for _, c := range metadata() {
			metaDeletions = append(metaDeletions, c.Path)
		}
		var saveDeletions []string
		for _, c := range saves() {
			saveDeletions = append(saveDeletions, c.Path)
		}
		return map[string]runner.Result{
			"bios-pull":          result([]runner.Change{{Itemize: ">f+++++++++", Size: 579 * mib, Path: "bios.pack"}}),
			"roms-content-pull":  result(content()),
			"roms-metadata-pull": result(metadata()),
			"roms-metadata-push": result(nil, metaDeletions...),
			"saves-pull":         result(saves()),
			"saves-push":         result(nil, saveDeletions...),
		}

	case ScenarioSteady:
		return map[string]runner.Result{
			"bios-pull": result(nil),
			"roms-content-pull": result([]runner.Change{
				{Itemize: ">f+++++++++", Size: 4 * gib, Path: "ps2/Ratchet & Clank (USA).iso"},
				{Itemize: ">f+++++++++", Size: 682 * kib, Path: "snes/Terranigma (Europe).zip"},
			}),
			"roms-metadata-pull": result(nil),
			"roms-metadata-push": result([]runner.Change{
				{Itemize: ">f.st......", Size: 502 * kib, Path: "snes/gamelist.xml"},
				{Itemize: ">f.st......", Size: 227 * kib, Path: "megadrive/gamelist.xml"},
			}),
			"saves-pull": result(nil),
			"saves-push": result([]runner.Change{
				{Itemize: ">f.st......", Size: 32 * kib, Path: "snes/Terranigma (Europe).srm"},
			}),
		}

	case ScenarioDrift:
		return map[string]runner.Result{
			// A pull deletes on the box, so this drift is local.
			"bios-pull": result(nil, "stray-bios-file.bin"),
			"roms-content-pull": result(nil,
				"snes/Removed From NAS (USA).zip",
				"ps2/Old Import (Japan).iso",
			),
			"roms-metadata-pull": result(nil),
			// A push deletes on the NAS, so this drift sits on the NAS.
			// saves-pull brings nothing new down, so it does not explain
			// away psx/Retired Save.mcd: the box genuinely no longer has
			// it, so it is genuine drift, not a seed-run artifact.
			"roms-metadata-push": result(nil, "dreamcast/images/Deleted Game-image.png"),
			"saves-pull":         result(nil),
			"saves-push":         result(nil, "psx/Retired Save.mcd"),
		}

	case ScenarioNoSpace:
		return map[string]runner.Result{
			"bios-pull":          result(nil),
			"roms-content-pull":  result(content()),
			"roms-metadata-pull": result(metadata()),
			"roms-metadata-push": result(nil),
			"saves-pull":         result(nil),
			"saves-push":         result(nil),
		}

	case ScenarioFailure:
		return map[string]runner.Result{
			"bios-pull":          result(nil),
			"roms-content-pull":  result(content()),
			"roms-metadata-pull": result(nil),
			"roms-metadata-push": result(nil),
			"saves-pull":         result(nil),
			"saves-push":         result(nil),
		}
	}
	return map[string]runner.Result{}
}
