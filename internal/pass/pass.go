// Package pass defines the five ordered rsync passes that reconcile the box
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
}

func slash(p string) string {
	if strings.HasSuffix(p, "/") {
		return p
	}
	return p + "/"
}

// Passes returns the five passes in the order they must run. Metadata is
// pulled before it is pushed so that a box with an empty tree is seeded
// rather than pushing its emptiness at the NAS.
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
		},
		{
			ID:        "roms-content-pull",
			Label:     "ROM content",
			Tree:      "roms",
			Direction: DirPull,
			Src:       slash(m.Roms),
			Dst:       slash(cfg.Trees.Roms.Local),
			Filters:   paths.ContentFilters(),
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
		},
		{
			ID:        "roms-metadata-push",
			Label:     "Metadata",
			Tree:      "roms",
			Direction: DirPush,
			Src:       slash(cfg.Trees.Roms.Local),
			Dst:       slash(m.Roms),
			Filters:   paths.MetadataFilters(),
		},
		{
			ID:        "saves-push",
			Label:     "Saves",
			Tree:      "saves",
			Direction: DirPush,
			Src:       slash(cfg.Trees.Saves.Local),
			Dst:       slash(m.Saves),
			Filters:   paths.PlainFilters(),
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

// DryRunArgs builds an enumeration-only invocation. --delete is present
// solely so rsync reports what it would remove; it is never paired with a
// real transfer. The out-format yields "flags|size|path" per line.
func (p Pass) DryRunArgs() []string {
	args := []string{"-a", "-n", "--delete", "--out-format=%i|%l|%n"}
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
	args = append(args, p.filterArgs()...)
	args = append(args, p.Extra...)
	return append(args, p.Src, p.Dst)
}
