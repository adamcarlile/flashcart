package fake

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adamcarlile/flashcart/internal/config"
	"github.com/adamcarlile/flashcart/internal/nas"
	"github.com/adamcarlile/flashcart/internal/pass"
	"github.com/adamcarlile/flashcart/internal/plan"
	"github.com/adamcarlile/flashcart/internal/runner"
)

func cfg() *config.Config {
	return &config.Config{
		NAS: config.NAS{Host: "fake", Port: 2049, MountRoot: "/mnt"},
		Trees: config.Trees{
			Roms:  config.Tree{Export: "/e/roms", Local: "/userdata/roms"},
			Bios:  config.Tree{Export: "/e/bios", Local: "/userdata/bios"},
			Saves: config.Tree{Export: "/e/saves", Local: "/userdata/saves"},
		},
		SpaceMarginPercent: 10,
	}
}

func mustNew(t *testing.T, s Scenario) *Backend {
	t.Helper()
	b, err := New(s)
	if err != nil {
		t.Fatal(err)
	}
	b.Delay = 0
	return b
}

func TestSatisfiesBothSeams(t *testing.T) {
	b := mustNew(t, ScenarioSteady)
	var _ nas.Provider = b
	var _ runner.Runner = b
}

func TestNewRejectsUnknownScenario(t *testing.T) {
	if _, err := New("nonsense"); err == nil {
		t.Fatal("New accepted an unknown scenario")
	}
}

func TestSetScenarioRejectsUnknown(t *testing.T) {
	b := mustNew(t, ScenarioSteady)
	if err := b.SetScenario("nonsense"); err == nil {
		t.Fatal("SetScenario accepted an unknown scenario")
	}
	if b.Scenario() != ScenarioSteady {
		t.Error("a rejected scenario still changed state")
	}
}

// If the fake provider were ever wired to real rsync by mistake, the paths
// it hands out must not exist, so the mistake fails loudly rather than
// touching real data.
func TestMountPathsDoNotExist(t *testing.T) {
	b := mustNew(t, ScenarioSteady)
	m, unmount, err := b.Mount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unmount()
	for _, p := range []string{m.Roms, m.Bios, m.Saves} {
		if !strings.Contains(p, "flashcart-fake") {
			t.Errorf("mount path %q is not obviously fake", p)
		}
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("fake mount path %q exists on disk", p)
		}
	}
}

func TestOfflineScenarioFailsProbeAndMount(t *testing.T) {
	b := mustNew(t, ScenarioOffline)
	if err := b.Probe(context.Background()); err == nil {
		t.Error("Probe succeeded in the offline scenario")
	}
	if _, _, err := b.Mount(context.Background()); err == nil {
		t.Error("Mount succeeded in the offline scenario")
	}
}

// DryRun never mounts, so a caller that reaches it without calling Mount
// first (this package's own scenario tests do exactly that, via
// plan.Build) must still see the NAS as unreachable rather than getting a
// silent, empty "nothing to sync" result.
func TestOfflineScenarioFailsDryRun(t *testing.T) {
	b := mustNew(t, ScenarioOffline)
	ps := pass.Passes(cfg(), nas.Mounts{Roms: "/m/roms", Bios: "/m/bios", Saves: "/m/saves"})
	_, err := b.DryRun(context.Background(), ps[0])
	if err == nil {
		t.Fatal("DryRun succeeded in the offline scenario")
	}
	if !errors.Is(err, nas.ErrUnreachable) {
		t.Errorf("DryRun error = %v, want it to wrap nas.ErrUnreachable", err)
	}
}

// The scenario that matters most: a first run must show a large incoming
// transfer and no drift at all.
func TestSeedScenarioPlansLargeIncomingAndNoDrift(t *testing.T) {
	b := mustNew(t, ScenarioSeed)
	ps := pass.Passes(cfg(), nas.Mounts{Roms: "/m/roms", Bios: "/m/bios", Saves: "/m/saves"})

	p, err := plan.Build(context.Background(), cfg(), b, ps, b.FreeSpace)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.RequiredBytes < 80*gib {
		t.Errorf("RequiredBytes = %d, want a realistic seed of at least 80 GiB", p.RequiredBytes)
	}
	if !p.Sufficient {
		t.Errorf("seed scenario should fit: %s", p.Message)
	}
	for _, tp := range p.Trees {
		if len(tp.Drift) != 0 {
			t.Errorf("seed scenario reported drift on %s: %+v", tp.Tree, tp.Drift)
		}
	}
}

func TestDriftScenarioReportsBothSides(t *testing.T) {
	b := mustNew(t, ScenarioDrift)
	ps := pass.Passes(cfg(), nas.Mounts{Roms: "/m/roms", Bios: "/m/bios", Saves: "/m/saves"})

	p, err := plan.Build(context.Background(), cfg(), b, ps, b.FreeSpace)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	sides := map[string]bool{}
	for _, tp := range p.Trees {
		for _, d := range tp.Drift {
			sides[d.Side] = true
		}
	}
	if !sides[plan.SideLocal] || !sides[plan.SideNAS] {
		t.Errorf("drift scenario produced sides %v, want both local and nas", sides)
	}
}

func TestNoSpaceScenarioIsRefused(t *testing.T) {
	b := mustNew(t, ScenarioNoSpace)
	ps := pass.Passes(cfg(), nas.Mounts{Roms: "/m/roms", Bios: "/m/bios", Saves: "/m/saves"})

	p, err := plan.Build(context.Background(), cfg(), b, ps, b.FreeSpace)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.Sufficient {
		t.Error("nospace scenario reported sufficient space")
	}
	if p.Message == "" {
		t.Error("nospace scenario produced no message")
	}
}

func TestFailureScenarioFailsMidRun(t *testing.T) {
	b := mustNew(t, ScenarioFailure)
	ps := pass.Passes(cfg(), nas.Mounts{Roms: "/m/roms", Bios: "/m/bios", Saves: "/m/saves"})

	events := make(chan runner.Event, 4096)
	var collected []runner.Event
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for e := range events {
			collected = append(collected, e)
		}
	}()

	var failed string
	for _, p := range ps {
		if _, err := b.Run(context.Background(), p, events); err != nil {
			failed = p.ID
			break
		}
	}
	close(events)
	<-drained

	if failed != "roms-content-pull" {
		t.Errorf("failure scenario failed on %q, want roms-content-pull", failed)
	}
	// The injected failure sits at percent 60: no progress event for the
	// failing pass may be emitted past that point, or the fake would be
	// claiming progress it did not make.
	for _, e := range collected {
		if e.PassID == "roms-content-pull" && e.Percent > 60 {
			t.Errorf("roms-content-pull emitted progress %d beyond the injected failure point of 60", e.Percent)
		}
	}
}

// The brief requires scenario switching to be safe while a request is in
// flight. This pins that: a run that streams progress all the way to 100%
// must return the result of the scenario it started under, never a result
// torn from whatever scenario happened to be live when script() last read
// it. Run under -race: the bug this guards was a logic race across three
// individually mutex-safe reads, which -race alone cannot see, but the
// concurrent SetScenario calls here still exercise the mutex under -race
// to catch any accidental unguarded access.
func TestScenarioSwitchDuringRunIsConsistent(t *testing.T) {
	b := mustNew(t, ScenarioSteady)
	b.Delay = 2 * time.Millisecond
	ps := pass.Passes(cfg(), nas.Mounts{Roms: "/m/roms", Bios: "/m/bios", Saves: "/m/saves"})
	target := ps[1] // roms-content-pull: steady has a real, non-empty result here.

	events := make(chan runner.Event)
	firstEvent := make(chan struct{})
	var once sync.Once
	drained := make(chan struct{})

	var mu sync.Mutex
	var lastPercent, count int

	go func() {
		defer close(drained)
		for e := range events {
			mu.Lock()
			lastPercent = e.Percent
			count++
			mu.Unlock()
			once.Do(func() { close(firstEvent) })
		}
	}()

	type runOut struct {
		res runner.Result
		err error
	}
	runDone := make(chan runOut, 1)
	go func() {
		res, err := b.Run(context.Background(), target, events)
		runDone <- runOut{res, err}
		close(events)
	}()

	<-firstEvent
	// Switch repeatedly while the run above continues in the background.
	// It started under ScenarioSteady, so none of these should change what
	// it ultimately reports.
	for _, sc := range []Scenario{ScenarioOffline, ScenarioFailure, ScenarioDrift, ScenarioNoSpace, ScenarioSteady} {
		if err := b.SetScenario(sc); err != nil {
			t.Fatalf("SetScenario(%s): %v", sc, err)
		}
	}

	out := <-runDone
	<-drained

	mu.Lock()
	finalPercent, seen := lastPercent, count
	mu.Unlock()

	if out.err != nil {
		t.Fatalf("Run failed unexpectedly: %v", out.err)
	}
	if finalPercent != 100 {
		t.Fatalf("progress did not reach completion: last = %d (%d events)", finalPercent, seen)
	}

	want := scriptFor(ScenarioSteady)[target.ID]
	if out.res.TransferBytes != want.TransferBytes || len(out.res.Changes) != len(want.Changes) {
		t.Errorf("Run streamed to completion under its starting scenario but returned %+v, want the steady result %+v (a mid-run scenario switch must not tear the returned result apart from the progress it streamed)", out.res, want)
	}
}

func TestRunEmitsProgress(t *testing.T) {
	b := mustNew(t, ScenarioSteady)
	ps := pass.Passes(cfg(), nas.Mounts{Roms: "/m/roms", Bios: "/m/bios", Saves: "/m/saves"})

	events := make(chan runner.Event, 4096)
	if _, err := b.Run(context.Background(), ps[0], events); err != nil {
		t.Fatal(err)
	}
	close(events)

	var last int
	n := 0
	for e := range events {
		if e.Percent < last {
			t.Errorf("progress went backwards: %d then %d", last, e.Percent)
		}
		last = e.Percent
		n++
	}
	if n < 2 {
		t.Errorf("only %d progress events emitted", n)
	}
	if last != 100 {
		t.Errorf("final progress = %d, want 100", last)
	}
}
