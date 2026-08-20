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

// --delete is only ever used to enumerate, and only alongside -n.
func TestDryRunsDeleteOnlyWithDryRunFlag(t *testing.T) {
	for _, p := range Passes(testConfig(), testMounts()) {
		args := p.DryRunArgs()
		if !slices.Contains(args, "--delete") {
			t.Errorf("%s: DryRunArgs must carry --delete to enumerate drift: %v", p.ID, args)
		}
		if !slices.Contains(args, "-n") {
			t.Fatalf("%s: DryRunArgs carries --delete without -n: %v", p.ID, args)
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
		id        string
		wantSrc   string
		wantDst   string
		wantDir   Direction
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
		id         string
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
