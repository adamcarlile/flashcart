package pass

import (
	"slices"
	"strings"
	"testing"

	"github.com/adamcarlile/flashcart/internal/config"
	"github.com/adamcarlile/flashcart/internal/nas"
	"github.com/adamcarlile/flashcart/internal/paths"
)

func testConfig() *config.Config {
	return &config.Config{
		Trees: config.Trees{
			Roms:  config.Tree{Export: "/volume2/retrogaming/roms", Local: "/userdata/roms"},
			Bios:  config.Tree{Export: "/volume2/retrogaming/bios", Local: "/userdata/bios"},
			Saves: config.Tree{Export: "/volume2/retrogaming/saves", Local: "/userdata/saves"},
		},
	}
}

func testMounts() nas.Mounts {
	return nas.Mounts{
		Roms:  "/var/run/flashcart/nas/roms",
		Bios:  "/var/run/flashcart/nas/bios",
		Saves: "/var/run/flashcart/nas/saves",
	}
}

func TestPassOrderAndDirections(t *testing.T) {
	ps := Passes(testConfig(), testMounts())
	wantIDs := []string{
		"bios-pull",
		"roms-content-pull",
		"roms-metadata-pull",
		"roms-metadata-push",
		"saves-pull",
		"saves-push",
	}
	var gotIDs []string
	for _, p := range ps {
		gotIDs = append(gotIDs, p.ID)
	}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("pass order = %v, want %v", gotIDs, wantIDs)
	}

	byID := map[string]Pass{}
	for _, p := range ps {
		byID[p.ID] = p
	}
	if byID["roms-content-pull"].Direction != DirPull {
		t.Error("roms content must pull from the NAS")
	}
	if byID["roms-metadata-push"].Direction != DirPush {
		t.Error("roms metadata must push to the NAS")
	}
	if byID["saves-push"].Direction != DirPush {
		t.Error("saves must push to the NAS")
	}
}

func TestSourceAndDestinationsHaveTrailingSlash(t *testing.T) {
	for _, p := range Passes(testConfig(), testMounts()) {
		if !strings.HasSuffix(p.Src, "/") {
			t.Errorf("%s: Src %q lacks a trailing slash", p.ID, p.Src)
		}
		if !strings.HasSuffix(p.Dst, "/") {
			t.Errorf("%s: Dst %q lacks a trailing slash", p.ID, p.Dst)
		}
	}
}

func TestPullAndPushEndpointsAreCorrect(t *testing.T) {
	byID := map[string]Pass{}
	for _, p := range Passes(testConfig(), testMounts()) {
		byID[p.ID] = p
	}
	if got := byID["roms-content-pull"].Src; got != "/var/run/flashcart/nas/roms/" {
		t.Errorf("content pull Src = %q", got)
	}
	if got := byID["roms-content-pull"].Dst; got != "/userdata/roms/" {
		t.Errorf("content pull Dst = %q", got)
	}
	if got := byID["saves-push"].Src; got != "/userdata/saves/" {
		t.Errorf("saves push Src = %q", got)
	}
	if got := byID["saves-push"].Dst; got != "/var/run/flashcart/nas/saves/" {
		t.Errorf("saves push Dst = %q", got)
	}
	if got := byID["saves-pull"].Src; got != "/var/run/flashcart/nas/saves/" {
		t.Errorf("saves pull Src = %q", got)
	}
	if got := byID["saves-pull"].Dst; got != "/userdata/saves/" {
		t.Errorf("saves pull Dst = %q", got)
	}
}

// CRITICAL 1: saves must be seeded from the NAS before they are pushed,
// exactly mirroring the metadata seed pattern, or a first run against an
// empty local saves tree offers the entire NAS save archive for deletion.
func TestSeedPullsPrecedeTheirPush(t *testing.T) {
	ps := Passes(testConfig(), testMounts())
	var gotIDs []string
	for _, p := range ps {
		gotIDs = append(gotIDs, p.ID)
	}
	idx := func(id string) int {
		for i, got := range gotIDs {
			if got == id {
				return i
			}
		}
		t.Fatalf("pass %q not found in %v", id, gotIDs)
		return -1
	}
	if idx("roms-metadata-pull") >= idx("roms-metadata-push") {
		t.Error("roms-metadata-pull must run before roms-metadata-push")
	}
	if idx("saves-pull") >= idx("saves-push") {
		t.Error("saves-pull must run before saves-push")
	}
}

func TestMetadataPullIgnoresExisting(t *testing.T) {
	byID := map[string]Pass{}
	for _, p := range Passes(testConfig(), testMounts()) {
		byID[p.ID] = p
	}
	if !slices.Contains(byID["roms-metadata-pull"].Extra, "--ignore-existing") {
		t.Error("metadata pull must carry --ignore-existing so the box's copy always wins")
	}
	if slices.Contains(byID["roms-metadata-push"].Extra, "--ignore-existing") {
		t.Error("metadata push must not carry --ignore-existing")
	}
}

// CRITICAL 1: saves-pull must mirror roms-metadata-pull's seed shape
// exactly, since that is the only reason it cancels push-side drift for
// free via the existing projected-state calculation.
func TestSavesPullIgnoresExisting(t *testing.T) {
	byID := map[string]Pass{}
	for _, p := range Passes(testConfig(), testMounts()) {
		byID[p.ID] = p
	}
	if !slices.Contains(byID["saves-pull"].Extra, "--ignore-existing") {
		t.Error("saves-pull must carry --ignore-existing so the box's own saves always win")
	}
	if slices.Contains(byID["saves-push"].Extra, "--ignore-existing") {
		t.Error("saves-push must not carry --ignore-existing")
	}
}

// The single most important safety property in the project.
func TestRealRunsNeverDelete(t *testing.T) {
	for _, p := range Passes(testConfig(), testMounts()) {
		args := p.RunArgs()
		if slices.Contains(args, "--delete") {
			t.Fatalf("%s: RunArgs contains --delete: %v", p.ID, args)
		}
		if slices.Contains(args, "-n") || slices.Contains(args, "--dry-run") {
			t.Fatalf("%s: RunArgs is a dry run: %v", p.ID, args)
		}
	}
}

// --delete is only ever used to enumerate, and only alongside -n, and only
// on passes that run with their tree's ownership grain (see
// TestOwnershipContraryPassesNeverDelete for the other half of this
// property).
func TestDryRunsDeleteOnlyWithDryRunFlag(t *testing.T) {
	for _, p := range Passes(testConfig(), testMounts()) {
		args := p.DryRunArgs()
		if !p.WithGrain {
			continue
		}
		if !slices.Contains(args, "--delete") {
			t.Errorf("%s: DryRunArgs must carry --delete to enumerate drift: %v", p.ID, args)
		}
		if !slices.Contains(args, "-n") {
			t.Fatalf("%s: DryRunArgs carries --delete without -n: %v", p.ID, args)
		}
	}
}

// CRITICAL 2: a pass running against its tree's ownership grain (the
// metadata and saves seed pulls) must never carry --delete, however this
// is decided. Anything present on its destination that is absent from its
// source is new, not-yet-synced work on the ownership-authoritative side
// (a freshly scraped image, a save the box just wrote), not drift — and
// the exact two passes affected are the ones with an empty local tree on a
// first run: roms-metadata-pull and saves-pull. Doing this structurally
// (a field on Pass, read only by DryRunArgs) means a future pass cannot
// reintroduce the bug by omission at some other call site.
func TestOwnershipContraryPassesNeverDelete(t *testing.T) {
	wantNoGrain := map[string]bool{"roms-metadata-pull": true, "saves-pull": true}
	for _, p := range Passes(testConfig(), testMounts()) {
		if !wantNoGrain[p.ID] {
			continue
		}
		if p.WithGrain {
			t.Errorf("%s: WithGrain = true, want false (this pass runs against its tree's ownership)", p.ID)
		}
		args := p.DryRunArgs()
		if slices.Contains(args, "--delete") {
			t.Errorf("%s: DryRunArgs carries --delete despite running against the ownership grain: %v", p.ID, args)
		}
	}
	// The grain-aligned passes are unaffected: still checked in
	// TestDryRunsDeleteOnlyWithDryRunFlag above.
	wantGrain := []string{"bios-pull", "roms-content-pull", "roms-metadata-push", "saves-push"}
	for _, p := range Passes(testConfig(), testMounts()) {
		if !slices.Contains(wantGrain, p.ID) {
			continue
		}
		if !p.WithGrain {
			t.Errorf("%s: WithGrain = false, want true", p.ID)
		}
	}
}

// IMPORTANT 6: -a implies -o -g, which would have rsync chown the NAS's
// own files to the box's uid/gid on every push. Pull passes are unaffected:
// the box legitimately owns its own local mirror.
func TestPushPassesDropOwnerAndGroup(t *testing.T) {
	for _, p := range Passes(testConfig(), testMounts()) {
		for name, args := range map[string][]string{"dry": p.DryRunArgs(), "run": p.RunArgs()} {
			hasNoO := slices.Contains(args, "--no-o")
			hasNoG := slices.Contains(args, "--no-g")
			if p.Direction == DirPush {
				if !hasNoO || !hasNoG {
					t.Errorf("%s %s: push pass must carry --no-o and --no-g: %v", p.ID, name, args)
				}
			} else {
				if hasNoO || hasNoG {
					t.Errorf("%s %s: pull pass must not carry --no-o/--no-g: %v", p.ID, name, args)
				}
			}
		}
	}
}

func TestRunArgsCarryPartialAndProgress(t *testing.T) {
	p := Passes(testConfig(), testMounts())[0]
	args := p.RunArgs()
	for _, want := range []string{"-a", "--info=progress2", "--partial", "--partial-dir=.flashcart-partial"} {
		if !slices.Contains(args, want) {
			t.Errorf("RunArgs missing %q: %v", want, args)
		}
	}
}

func TestArgsEndWithSourceThenDestination(t *testing.T) {
	for _, p := range Passes(testConfig(), testMounts()) {
		for name, args := range map[string][]string{"dry": p.DryRunArgs(), "run": p.RunArgs()} {
			if args[len(args)-2] != p.Src || args[len(args)-1] != p.Dst {
				t.Errorf("%s %s: args must end Src then Dst, got %v", p.ID, name, args[len(args)-2:])
			}
		}
	}
}

// TestAllEndpointMappings verifies exact Src/Dst and Direction for all five passes.
// Catching swapped endpoints (writing box BIOS to NAS) or wrong directions is critical
// to prevent silent data destruction.
func TestAllEndpointMappings(t *testing.T) {
	ps := Passes(testConfig(), testMounts())
	byID := make(map[string]Pass)
	for _, p := range ps {
		byID[p.ID] = p
	}

	tests := []struct {
		id      string
		wantSrc string
		wantDst string
		wantDir Direction
	}{
		{
			id:      "bios-pull",
			wantSrc: "/var/run/flashcart/nas/bios/",
			wantDst: "/userdata/bios/",
			wantDir: DirPull,
		},
		{
			id:      "roms-content-pull",
			wantSrc: "/var/run/flashcart/nas/roms/",
			wantDst: "/userdata/roms/",
			wantDir: DirPull,
		},
		{
			id:      "roms-metadata-pull",
			wantSrc: "/var/run/flashcart/nas/roms/",
			wantDst: "/userdata/roms/",
			wantDir: DirPull,
		},
		{
			id:      "roms-metadata-push",
			wantSrc: "/userdata/roms/",
			wantDst: "/var/run/flashcart/nas/roms/",
			wantDir: DirPush,
		},
		{
			id:      "saves-pull",
			wantSrc: "/var/run/flashcart/nas/saves/",
			wantDst: "/userdata/saves/",
			wantDir: DirPull,
		},
		{
			id:      "saves-push",
			wantSrc: "/userdata/saves/",
			wantDst: "/var/run/flashcart/nas/saves/",
			wantDir: DirPush,
		},
	}

	for _, tc := range tests {
		p, ok := byID[tc.id]
		if !ok {
			t.Errorf("pass %q not found", tc.id)
			continue
		}
		if p.Src != tc.wantSrc {
			t.Errorf("%s: Src = %q, want %q", tc.id, p.Src, tc.wantSrc)
		}
		if p.Dst != tc.wantDst {
			t.Errorf("%s: Dst = %q, want %q", tc.id, p.Dst, tc.wantDst)
		}
		if p.Direction != tc.wantDir {
			t.Errorf("%s: Direction = %v, want %v", tc.id, p.Direction, tc.wantDir)
		}
	}
}

// TestAllFilterSets verifies each pass has the correct filter set.
// Swapping ContentFilters and MetadataFilters, or dropping a filter call,
// would compile and pass without this test.
func TestAllFilterSets(t *testing.T) {
	ps := Passes(testConfig(), testMounts())
	byID := make(map[string]Pass)
	for _, p := range ps {
		byID[p.ID] = p
	}

	tests := []struct {
		id          string
		wantFilters []string
	}{
		{
			id:          "bios-pull",
			wantFilters: paths.PlainFilters(),
		},
		{
			id:          "roms-content-pull",
			wantFilters: paths.ContentFilters(),
		},
		{
			id:          "roms-metadata-pull",
			wantFilters: paths.MetadataFilters(),
		},
		{
			id:          "roms-metadata-push",
			wantFilters: paths.MetadataFilters(),
		},
		{
			id:          "saves-pull",
			wantFilters: paths.PlainFilters(),
		},
		{
			id:          "saves-push",
			wantFilters: paths.PlainFilters(),
		},
	}

	for _, tc := range tests {
		p, ok := byID[tc.id]
		if !ok {
			t.Errorf("pass %q not found", tc.id)
			continue
		}
		if !slices.Equal(p.Filters, tc.wantFilters) {
			t.Errorf("%s: Filters = %v, want %v", tc.id, p.Filters, tc.wantFilters)
		}
	}
}
