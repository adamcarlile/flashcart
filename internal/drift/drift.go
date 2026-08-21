// Package drift removes paths the user has explicitly confirmed.
//
// It deliberately does not use rsync --delete. Both sides are ordinary
// filesystem paths while the NAS is mounted, so removal names exactly what
// the user ticked. No filter expression sits between their intent and an
// irreversible delete.
package drift

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/adamcarlile/flashcart/internal/config"
	"github.com/adamcarlile/flashcart/internal/nas"
	"github.com/adamcarlile/flashcart/internal/plan"
)

// Roots maps tree name to the directory that bounds deletion on each side.
// NAS roots are only valid while a mount is held, which is why drift items
// store relative paths.
type Roots struct {
	Local map[string]string
	NAS   map[string]string
}

// RootsFor builds the containment roots for the current mounts.
func RootsFor(cfg *config.Config, m nas.Mounts) Roots {
	return Roots{
		Local: map[string]string{
			"roms":  filepath.Clean(cfg.Trees.Roms.Local),
			"bios":  filepath.Clean(cfg.Trees.Bios.Local),
			"saves": filepath.Clean(cfg.Trees.Saves.Local),
		},
		NAS: map[string]string{
			"roms":  filepath.Clean(m.Roms),
			"bios":  filepath.Clean(m.Bios),
			"saves": filepath.Clean(m.Saves),
		},
	}
}

// ErrOutsideRoot is returned when a path would escape its tree.
var ErrOutsideRoot = errors.New("path resolves outside its tree root")

// Resolve turns a drift item into an absolute path, refusing anything that
// escapes its root.
func Resolve(roots Roots, item plan.DriftItem) (string, error) {
	var table map[string]string
	switch item.Side {
	case plan.SideLocal:
		table = roots.Local
	case plan.SideNAS:
		table = roots.NAS
	default:
		return "", fmt.Errorf("unknown side %q", item.Side)
	}

	root, ok := table[item.Tree]
	if !ok || root == "" {
		return "", fmt.Errorf("unknown tree %q", item.Tree)
	}

	rel := strings.TrimSpace(item.Rel)
	if rel == "" {
		return "", errors.New("refusing an empty path")
	}
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("%w: %q is absolute", ErrOutsideRoot, rel)
	}

	full := filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))

	// Cleaning collapses "..", so compare the result against the root. The
	// separator suffix stops "/userdata/roms-backup" passing as a child of
	// "/userdata/roms".
	if full == root {
		return "", fmt.Errorf("%w: %q is the tree root itself", ErrOutsideRoot, rel)
	}
	if !strings.HasPrefix(full, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: %q", ErrOutsideRoot, rel)
	}
	return full, nil
}

// Delete removes every confirmed item. The whole batch is validated before
// anything is removed, so one bad entry cannot leave a half-applied deletion.
func Delete(roots Roots, items []plan.DriftItem) ([]string, error) {
	resolved := make([]string, 0, len(items))
	for _, item := range items {
		full, err := Resolve(roots, item)
		if err != nil {
			return nil, fmt.Errorf("refusing the whole batch: %w", err)
		}
		resolved = append(resolved, full)
	}

	deleted := make([]string, 0, len(resolved))
	for _, full := range resolved {
		if err := os.RemoveAll(full); err != nil {
			return deleted, fmt.Errorf("delete %s: %w", full, err)
		}
		deleted = append(deleted, full)
	}
	return deleted, nil
}
