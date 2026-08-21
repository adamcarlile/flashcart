// Package pass defines the six ordered rsync passes that reconcile the box
// with the NAS, and builds their argument vectors.
package pass

import (
	"strings"

	"github.com/adamcarlile/flashcart/internal/config"
	"github.com/adamcarlile/flashcart/internal/nas"
	"github.com/adamcarlile/flashcart/internal/paths"
)

// Direction records which side is authoritative for a pass.
type Direction int

const (
	// DirPull copies from the NAS to the box. The NAS wins.
	DirPull Direction = iota
	// DirPush copies from the box to the NAS. The box wins.
	DirPush
)

func (d Direction) String() string {
	if d == DirPush {
		return "push"
	}
	return "pull"
}

// Pass is one rsync invocation.
type Pass struct {
	ID        string
	Label     string
	Tree      string
	Direction Direction
	Src       string
	Dst       string
	Filters   []string
	Extra     []string

	// WithGrain records whether this pass runs in the direction its tree's
	// ownership already flows: a pull pass for a NAS-owned tree (bios,
	// ROM content) or a push pass for a box-owned tree (ROM metadata,
	// saves). Metadata and saves are box-owned, so their seed passes
	// (roms-metadata-pull, saves-pull) pull AGAINST that grain with
	// --ignore-existing: anything the box has that the NAS lacks there is
	// new box-owned work — a freshly scraped image, a save the box just
	// wrote — not drift. DryRunArgs only emits --delete when WithGrain is
	// true, because only a grain-aligned pass's "present on the
	// destination, absent from the source" reading is actually drift; the
	// same reading from an against-the-grain pass is simply ownership.
	WithGrain bool
}

func slash(p string) string {
	if strings.HasSuffix(p, "/") {
		return p
	}
	return p + "/"
}

// Passes returns the six passes in the order they must run. Metadata and
// saves are both box-owned, so each is pulled with --ignore-existing before
// it is pushed: the pull seeds a tree that has never been seen (including
// an empty local tree, e.g. saves on a first run) rather than the push
// mistaking that emptiness for the NAS's copies having been deleted.
func Passes(cfg *config.Config, m nas.Mounts) []Pass {
	return []Pass{
		{
			ID:        "bios-pull",
			Label:     "BIOS",
			Tree:      "bios",
			Direction: DirPull,
			Src:       slash(m.Bios),
			Dst:       slash(cfg.Trees.Bios.Local),
			Filters:   paths.PlainFilters(),
			WithGrain: true, // bios is NAS-owned; pull matches the grain.
		},
		{
			ID:        "roms-content-pull",
			Label:     "ROM content",
			Tree:      "roms",
			Direction: DirPull,
			Src:       slash(m.Roms),
			Dst:       slash(cfg.Trees.Roms.Local),
			Filters:   paths.ContentFilters(),
			WithGrain: true, // content is NAS-owned; pull matches the grain.
		},
		{
			ID:        "roms-metadata-pull",
			Label:     "Metadata (seed)",
			Tree:      "roms",
			Direction: DirPull,
			Src:       slash(m.Roms),
			Dst:       slash(cfg.Trees.Roms.Local),
			Filters:   paths.MetadataFilters(),
			Extra:     []string{"--ignore-existing"},
			WithGrain: false, // metadata is box-owned; this pull runs against the grain.
		},
		{
			ID:        "roms-metadata-push",
			Label:     "Metadata",
			Tree:      "roms",
			Direction: DirPush,
			Src:       slash(cfg.Trees.Roms.Local),
			Dst:       slash(m.Roms),
			Filters:   paths.MetadataFilters(),
			WithGrain: true, // metadata is box-owned; push matches the grain.
		},
		{
			ID:        "saves-pull",
			Label:     "Saves (seed)",
			Tree:      "saves",
			Direction: DirPull,
			Src:       slash(m.Saves),
			Dst:       slash(cfg.Trees.Saves.Local),
			Filters:   paths.PlainFilters(),
			Extra:     []string{"--ignore-existing"},
			WithGrain: false, // saves are box-owned; this pull runs against the grain.
		},
		{
			ID:        "saves-push",
			Label:     "Saves",
			Tree:      "saves",
			Direction: DirPush,
			Src:       slash(cfg.Trees.Saves.Local),
			Dst:       slash(m.Saves),
			Filters:   paths.PlainFilters(),
			WithGrain: true, // saves are box-owned; push matches the grain.
		},
	}
}

func (p Pass) filterArgs() []string {
	args := make([]string, 0, len(p.Filters))
	for _, f := range p.Filters {
		args = append(args, "--filter="+f)
	}
	return args
}

// ownerArgs strips owner/group preservation from push passes. -a implies
// -o -g, which asks rsync to chown the NAS's files to whatever the
// Batocera box's uid/gid happen to be. The NAS is not this box: imposing
// its ownership on the NAS's own files is wrong on the merits (observed on
// the real export: rsync itemizes ".d..tpog..." and intends a chown on
// every pushed file, against files that are legitimately 1024:users), and
// on a root-squashed export the chown fails outright with exit 23. Pull
// passes are left alone: the box is what runs as root, so preserving
// owner/group on its own local mirror is unremarkable.
func (p Pass) ownerArgs() []string {
	if p.Direction == DirPush {
		return []string{"--no-o", "--no-g"}
	}
	return nil
}

// DryRunArgs builds an enumeration-only invocation. --delete is present
// only on passes that run with their tree's ownership grain, so rsync
// reports what it would remove only where "present on the destination,
// absent from the source" genuinely means drift rather than new,
// not-yet-synced work on the ownership-authoritative side; it is never
// paired with a real transfer. The out-format yields "flags|size|path" per
// line.
func (p Pass) DryRunArgs() []string {
	args := []string{"-a", "-n"}
	if p.WithGrain {
		args = append(args, "--delete")
	}
	args = append(args, "--out-format=%i|%l|%n")
	args = append(args, p.ownerArgs()...)
	args = append(args, p.filterArgs()...)
	args = append(args, p.Extra...)
	return append(args, p.Src, p.Dst)
}

// RunArgs builds a real transfer. It can never carry --delete: deletion is
// performed by the drift package against explicitly confirmed paths.
func (p Pass) RunArgs() []string {
	args := []string{
		"-a",
		"--info=progress2",
		"--partial",
		"--partial-dir=" + paths.PartialDir,
	}
	args = append(args, p.ownerArgs()...)
	args = append(args, p.filterArgs()...)
	args = append(args, p.Extra...)
	return append(args, p.Src, p.Dst)
}
