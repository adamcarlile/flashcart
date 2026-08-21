package plan

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/adamcarlile/flashcart/internal/config"
	"github.com/adamcarlile/flashcart/internal/nas"
	"github.com/adamcarlile/flashcart/internal/pass"
	"github.com/adamcarlile/flashcart/internal/runner"
)

// stubRunner returns canned dry-run results keyed by pass ID.
type stubRunner struct {
	results map[string]runner.Result
}

func (s stubRunner) DryRun(_ context.Context, p pass.Pass) (runner.Result, error) {
	r := s.results[p.ID]
	r.PassID = p.ID
	return r, nil
}

func (s stubRunner) Run(_ context.Context, p pass.Pass, _ chan<- runner.Event) (runner.Result, error) {
	return runner.Result{PassID: p.ID}, nil
}

func testCfg() *config.Config {
	return &config.Config{
		NAS: config.NAS{Host: "nas", Port: 2049, MountRoot: "/mnt"},
		Trees: config.Trees{
			Roms:  config.Tree{Export: "/e/roms", Local: "/userdata/roms"},
			Bios:  config.Tree{Export: "/e/bios", Local: "/userdata/bios"},
			Saves: config.Tree{Export: "/e/saves", Local: "/userdata/saves"},
		},
		SpaceMarginPercent: 10,
		// Pinned off so these tests do not depend on whether the host
		// happens to be a Batocera box with a real datainit tree.
		FactoryRoot: new(string),
	}
}

func testPasses() []pass.Pass {
	return pass.Passes(testCfg(), nas.Mounts{Roms: "/mnt/roms", Bios: "/mnt/bios", Saves: "/mnt/saves"})
}

func plentyOfSpace(string) (int64, int64, error) { return 400 << 30, 459 << 30, nil }

// TestSeedRunReportsNoDrift is a real-rsync integration test: see
// seed_integration_test.go. CRITICAL 3: this used to be a stub-based test
// right here, and the stub's saves data was simply absent — modelling an
// empty NAS saves tree, i.e. the exact seed-shaped world CRITICAL 1's bug
// lived in. A fix that left that fixture alone would have left the trap
// armed, so the case that matters (a genuinely populated NAS saves archive
// against an empty local tree) is now exercised against real rsync instead
// of a hand-written stub that can silently omit the one tree that mattered.

// Genuine drift — something the source really has lost, not an artifact of
// this run's own seed passes not having "happened" yet in a dry run — must
// still be reported. CRITICAL 3: before saves-pull existed (CRITICAL 1),
// saves-push had no earlier pass to check its deletions against at all, so
// this test's own "genuine" saves-push assertion was indistinguishable from
// the seed-run bug's symptom: any deletion looked the same, whether it came
// from a real, isolated removal or from local simply being empty. Now that
// saves-pull exists, this demonstrates the same discrimination the metadata
// case already relied on: one item is cancelled because the matching pull
// would have re-seeded it, the other survives because it would not.
func TestGenuineDriftIsReported(t *testing.T) {
	r := stubRunner{results: map[string]runner.Result{
		"roms-metadata-pull": {
			Changes: []runner.Change{{Itemize: ">f+++++++++", Size: 10, Path: "snes/gamelist.xml"}},
		},
		"roms-metadata-push": {
			Deletions: []string{"snes/gamelist.xml", "megadrive/gamelist.xml"},
		},
		"saves-pull": {
			Changes: []runner.Change{{Itemize: ">f+++++++++", Size: 10, Path: "psx/Chrono Trigger.mcd"}},
		},
		"saves-push": {
			Deletions: []string{"psx/Chrono Trigger.mcd", "snes/OldGame.srm"},
		},
	}}

	p, err := Build(context.Background(), testCfg(), r, testPasses(), plentyOfSpace)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	byTree := map[string]TreePlan{}
	for _, tp := range p.Trees {
		byTree[tp.Tree] = tp
	}

	romsDrift := byTree["roms"].Drift
	if len(romsDrift) != 1 {
		t.Fatalf("roms drift = %+v, want exactly megadrive/gamelist.xml", romsDrift)
	}
	if romsDrift[0].Rel != "megadrive/gamelist.xml" {
		t.Errorf("roms drift Rel = %q", romsDrift[0].Rel)
	}
	// A push pass writes to the NAS, so its drift lives on the NAS side.
	if romsDrift[0].Side != SideNAS {
		t.Errorf("roms drift Side = %q, want %q", romsDrift[0].Side, SideNAS)
	}

	savesDrift := byTree["saves"].Drift
	if len(savesDrift) != 1 {
		t.Fatalf("saves drift = %+v, want exactly snes/OldGame.srm (psx/Chrono Trigger.mcd must be cancelled by saves-pull)", savesDrift)
	}
	if savesDrift[0].Rel != "snes/OldGame.srm" || savesDrift[0].Side != SideNAS {
		t.Errorf("saves drift = %+v", savesDrift)
	}
}

// A pull pass deletes on the box, so its drift is local.
func TestPullDriftIsLocal(t *testing.T) {
	r := stubRunner{results: map[string]runner.Result{
		"bios-pull": {Deletions: []string{"stray.bin"}},
	}}
	p, err := Build(context.Background(), testCfg(), r, testPasses(), plentyOfSpace)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, tp := range p.Trees {
		if tp.Tree != "bios" {
			continue
		}
		if len(tp.Drift) != 1 || tp.Drift[0].Side != SideLocal {
			t.Fatalf("bios drift = %+v, want one local item", tp.Drift)
		}
	}
}

func TestIncomingAndOutgoingBytesAreSeparated(t *testing.T) {
	r := stubRunner{results: map[string]runner.Result{
		"roms-content-pull": {
			Changes:       []runner.Change{{Itemize: ">f+++++++++", Size: 1000, Path: "snes/New.zip"}},
			TransferBytes: 1000,
		},
		"saves-push": {
			Changes:       []runner.Change{{Itemize: ">f+++++++++", Size: 55, Path: "snes/New.srm"}},
			TransferBytes: 55,
		},
	}}
	p, err := Build(context.Background(), testCfg(), r, testPasses(), plentyOfSpace)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	byTree := map[string]TreePlan{}
	for _, tp := range p.Trees {
		byTree[tp.Tree] = tp
	}
	if byTree["roms"].IncomingBytes != 1000 || byTree["roms"].OutgoingBytes != 0 {
		t.Errorf("roms bytes in=%d out=%d", byTree["roms"].IncomingBytes, byTree["roms"].OutgoingBytes)
	}
	if byTree["saves"].OutgoingBytes != 55 || byTree["saves"].IncomingBytes != 0 {
		t.Errorf("saves bytes in=%d out=%d", byTree["saves"].IncomingBytes, byTree["saves"].OutgoingBytes)
	}
	// Only incoming bytes consume local disk.
	if p.RequiredBytes != 1000 {
		t.Errorf("RequiredBytes = %d, want 1000", p.RequiredBytes)
	}
}

// The UI renders one row per pass, so each tree must carry its passes with
// their own direction and figures rather than a single netted-out total.
func TestTreeCarriesPerPassBreakdown(t *testing.T) {
	r := stubRunner{results: map[string]runner.Result{
		"roms-content-pull": {
			// A directory entry alongside the file pins that the file-count
			// guard excludes directories rather than counting every change.
			Changes: []runner.Change{
				{Itemize: ">f+++++++++", Size: 1000, Path: "snes/New.zip"},
				{Itemize: "cd+++++++++", Size: 4096, Path: "snes/images"},
			},
			TransferBytes: 1000,
		},
		"roms-metadata-push": {
			Changes:       []runner.Change{{Itemize: ">f.st......", Size: 40, Path: "snes/gamelist.xml"}},
			TransferBytes: 40,
		},
	}}
	p, err := Build(context.Background(), testCfg(), r, testPasses(), plentyOfSpace)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, tp := range p.Trees {
		if tp.Tree != "roms" {
			continue
		}
		if len(tp.Passes) != 3 {
			t.Fatalf("roms carries %d passes, want 3: %+v", len(tp.Passes), tp.Passes)
		}
		byID := map[string]PassSummary{}
		for _, ps := range tp.Passes {
			byID[ps.ID] = ps
		}
		if got := byID["roms-content-pull"]; got.Direction != "in" || got.Bytes != 1000 || got.Files != 1 {
			t.Errorf("content pull summary = %+v, want Files=1 (directory entry excluded)", got)
		}
		if got := byID["roms-metadata-push"]; got.Direction != "out" || got.Bytes != 40 {
			t.Errorf("metadata push summary = %+v", got)
		}
		if byID["roms-metadata-pull"].Label == "" {
			t.Error("the metadata seed pass has no label to render")
		}
	}
}

func TestInsufficientSpaceIsRefused(t *testing.T) {
	r := stubRunner{results: map[string]runner.Result{
		"roms-content-pull": {TransferBytes: 100 << 30}, // 100 GiB incoming
	}}
	tight := func(string) (int64, int64, error) {
		return 105 << 30, 459 << 30, nil // 105 GiB free of 459 GiB
	}
	p, err := Build(context.Background(), testCfg(), r, testPasses(), tight)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.Sufficient {
		t.Error("Sufficient = true, but the transfer would leave under the 10% margin")
	}
	if p.Message == "" {
		t.Error("an insufficient plan must carry an explanatory message")
	}
}

// When required space exceeds free space outright (not just the margin),
// "remaining" goes negative. The refusal message must still read as a
// humanized quantity, not fall back to a raw byte count for that one figure.
func TestInsufficientSpaceMessageHumanizesNegativeRemaining(t *testing.T) {
	r := stubRunner{results: map[string]runner.Result{
		"roms-content-pull": {TransferBytes: 100 << 30}, // 100 GiB incoming
	}}
	tight := func(string) (int64, int64, error) {
		return 4 << 30, 459 << 30, nil // only 4 GiB free: remaining goes negative
	}
	p, err := Build(context.Background(), testCfg(), r, testPasses(), tight)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.Sufficient {
		t.Error("Sufficient = true, but required (100 GiB) exceeds free (4 GiB)")
	}
	if !strings.Contains(p.Message, "-96.0 GB") {
		t.Errorf("Message = %q, want a humanized negative remaining like -96.0 GB, not a raw byte count", p.Message)
	}
}

func TestSufficientSpaceIsAccepted(t *testing.T) {
	r := stubRunner{results: map[string]runner.Result{
		"roms-content-pull": {TransferBytes: 93 << 30},
	}}
	p, err := Build(context.Background(), testCfg(), r, testPasses(), plentyOfSpace)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !p.Sufficient {
		t.Errorf("Sufficient = false with 400 GiB free and 93 GiB needed: %s", p.Message)
	}
}

// factoryTree builds a stand-in for Batocera's /usr/share/batocera/datainit:
// the per-system _info.txt files and bios support data the image copies into
// /userdata on every boot.
func factoryTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range []string{
		"roms/snes/_info.txt",
		"roms/snes/gamelist.xml",
		"bios/Machines/msx.rom",
	} {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// Content the appliance image put on the box was never the NAS's to hold, so
// its absence from the NAS is not drift. Reporting it offered hundreds of
// deletions that Batocera's own S12populateshare would partly undo on the
// next boot, and that in the bios tree are the support data emulators load.
func TestFactoryContentIsNotDrift(t *testing.T) {
	cfg := testCfg()
	root := factoryTree(t)
	cfg.FactoryRoot = &root

	r := stubRunner{results: map[string]runner.Result{
		"bios-pull": {
			Deletions: []string{"Machines/", "Machines/msx.rom", "scph5501.bin"},
		},
		"roms-content-pull": {
			Deletions: []string{"snes/_info.txt", "snes/Chrono Trigger.sfc"},
		},
	}}

	p, err := Build(context.Background(), cfg, r, testPasses(), plentyOfSpace)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	byTree := map[string][]string{}
	for _, tp := range p.Trees {
		for _, d := range tp.Drift {
			byTree[tp.Tree] = append(byTree[tp.Tree], d.Rel)
		}
	}

	if got, want := byTree["bios"], []string{"scph5501.bin"}; !slices.Equal(got, want) {
		t.Errorf("bios drift = %v, want %v", got, want)
	}
	if got, want := byTree["roms"], []string{"snes/Chrono Trigger.sfc"}; !slices.Equal(got, want) {
		t.Errorf("roms drift = %v, want %v", got, want)
	}
	// Withheld, not silently dropped: the UI reports this count.
	if p.FactoryExcluded != 3 {
		t.Errorf("FactoryExcluded = %d, want 3", p.FactoryExcluded)
	}
}

// The exclusion is about the box's own copy of the image. A NAS-side path
// that happens to match a factory path is still genuine drift: nothing on the
// NAS came from the appliance image.
func TestFactoryExclusionAppliesOnlyToTheLocalSide(t *testing.T) {
	cfg := testCfg()
	root := factoryTree(t)
	cfg.FactoryRoot = &root

	r := stubRunner{results: map[string]runner.Result{
		"roms-metadata-push": {
			Deletions: []string{"snes/gamelist.xml"},
		},
	}}

	p, err := Build(context.Background(), cfg, r, testPasses(), plentyOfSpace)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, tp := range p.Trees {
		if tp.Tree != "roms" {
			continue
		}
		want := []DriftItem{{Tree: "roms", Side: SideNAS, Rel: "snes/gamelist.xml"}}
		if !slices.Equal(tp.Drift, want) {
			t.Errorf("roms drift = %v, want %v", tp.Drift, want)
		}
	}
}

// With the exclusion disabled, factory content is reported like anything
// else: the off switch has to actually be off.
func TestFactoryExclusionCanBeDisabled(t *testing.T) {
	cfg := testCfg() // FactoryRoot pinned to "".
	r := stubRunner{results: map[string]runner.Result{
		"bios-pull": {Deletions: []string{"Machines/msx.rom"}},
	}}

	p, err := Build(context.Background(), cfg, r, testPasses(), plentyOfSpace)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, tp := range p.Trees {
		if tp.Tree == "bios" && len(tp.Drift) != 1 {
			t.Errorf("bios drift = %v, want the one item reported", tp.Drift)
		}
	}
}
