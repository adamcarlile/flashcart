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

// rootFor looks up the containment root directory for an item's tree and
// side, without touching the filesystem.
func rootFor(roots Roots, item plan.DriftItem) (string, error) {
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
	return root, nil
}

// Resolve turns a drift item into an absolute path.
//
// These are cheap, lexical pre-checks only: empty paths, absolute paths,
// "." and "..", embedded NUL bytes, and unknown trees/sides. They give
// fast, specific error messages and let Delete validate a whole batch
// before touching the filesystem. They are NOT the containment guarantee:
// a path can pass every check here and still escape its root on disk, if
// an intermediate path component is a symlink pointing outside the tree.
// That guarantee is enforced separately, per path segment, at the syscall
// boundary via os.Root in Delete.
func Resolve(roots Roots, item plan.DriftItem) (string, error) {
	root, err := rootFor(roots, item)
	if err != nil {
		return "", err
	}

	rel := strings.TrimSpace(item.Rel)
	if rel == "" {
		return "", errors.New("refusing an empty path")
	}
	if strings.ContainsRune(rel, 0) {
		return "", fmt.Errorf("%w: %q contains a NUL byte", ErrOutsideRoot, rel)
	}
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("%w: %q is absolute", ErrOutsideRoot, rel)
	}

	cleanRel := filepath.Clean(filepath.FromSlash(rel))
	if cleanRel == "." {
		return "", fmt.Errorf("%w: %q is the tree root itself", ErrOutsideRoot, rel)
	}
	if cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: %q", ErrOutsideRoot, rel)
	}

	return filepath.Join(root, cleanRel), nil
}

// Delete removes every confirmed item. The whole batch is validated before
// anything is removed, so one bad entry cannot leave a half-applied
// deletion.
//
// Containment is enforced with os.Root, opened once per distinct tree
// root and reused across items. Root resolves every path segment inside
// the open root's file descriptor at the syscall boundary: a symlink
// (anywhere in the path, not just the leaf) that would lead outside the
// root makes the call fail with "path escapes from parent" instead of
// silently following it. That closes the gap a purely lexical
// (filepath.Clean-and-compare) check cannot: lexical cleaning never
// touches the filesystem, so it cannot see a symlink redirecting an
// in-tree-looking path to somewhere else on disk.
//
// Validation itself is non-destructive: every item is checked with
// Root.Lstat (which enforces the same containment as Root.RemoveAll, but
// performs no mutation) before the second pass calls Root.RemoveAll on
// anything, preserving whole-batch-first atomicity.
func Delete(roots Roots, items []plan.DriftItem) ([]string, error) {
	type validated struct {
		root *os.Root
		rel  string
		full string
	}

	openRoots := make(map[string]*os.Root)
	defer func() {
		for _, r := range openRoots {
			_ = r.Close()
		}
	}()

	resolved := make([]validated, 0, len(items))
	for _, item := range items {
		rootPath, err := rootFor(roots, item)
		if err != nil {
			return nil, fmt.Errorf("refusing the whole batch: %w", err)
		}

		full, err := Resolve(roots, item)
		if err != nil {
			return nil, fmt.Errorf("refusing the whole batch: %w", err)
		}

		rel, err := filepath.Rel(rootPath, full)
		if err != nil {
			return nil, fmt.Errorf("refusing the whole batch: %w", err)
		}

		r, ok := openRoots[rootPath]
		if !ok {
			r, err = os.OpenRoot(rootPath)
			if err != nil {
				return nil, fmt.Errorf("refusing the whole batch: open root %s: %w", rootPath, err)
			}
			openRoots[rootPath] = r
		}

		if _, err := r.Lstat(rel); err != nil {
			return nil, fmt.Errorf("refusing the whole batch: %w: %q under %s (%v)", ErrOutsideRoot, item.Rel, rootPath, err)
		}

		resolved = append(resolved, validated{root: r, rel: rel, full: full})
	}

	deleted := make([]string, 0, len(resolved))
	for _, v := range resolved {
		if err := v.root.RemoveAll(v.rel); err != nil {
			return deleted, fmt.Errorf("delete %s: %w", v.full, err)
		}
		deleted = append(deleted, v.full)
	}
	return deleted, nil
}
