// Package factory identifies content that came from the appliance image
// rather than from the NAS or the user.
//
// Batocera ships a skeleton at /usr/share/batocera/datainit and copies it
// into /userdata on every boot (see /etc/init.d/S12populateshare): per-system
// _info.txt files, a handful of bundled homebrew ROMs, and a large set of
// emulator support data under bios (the bluemsx Machines set, FBNeo and MAME
// dat files, NstDatabase.xml). While the NAS was mounted over those
// directories none of it was visible; once the box owns its own copy, all of
// it is present locally and absent from the NAS.
//
// That reading is literally true and entirely useless: drift means "the box
// holds something the NAS no longer has", and these files were never the
// NAS's to hold. Reporting them offers the user hundreds of deletions that
// are at best pointless, since S12populateshare restores parts of the tree on
// the next boot, and at worst harmful, since the bios support data is what
// those emulators load.
package factory

import (
	"os"
	"strings"
)

// DefaultRoot is where Batocera keeps the skeleton it populates /userdata
// from.
const DefaultRoot = "/usr/share/batocera/datainit"

// trees are the managed trees, which are also the subdirectory names the
// factory root uses: datainit mirrors the layout of /userdata.
var trees = []string{"roms", "bios", "saves"}

// Set answers whether a relative path within a tree is factory content.
//
// The zero value and a nil *Set both report nothing as factory, which is the
// correct behaviour off the appliance: developer machines and fake mode have
// no image to compare against.
type Set struct {
	roots map[string]*os.Root
}

// Open prepares a Set from a factory root, one open root per managed tree.
//
// It cannot fail. A root that is empty, missing, or unreadable yields no
// exclusions, so the failure direction is always toward reporting drift the
// user can then decline rather than silently hiding drift they needed to see.
func Open(root string) *Set {
	if root == "" {
		return &Set{}
	}
	s := &Set{roots: make(map[string]*os.Root, len(trees))}
	for _, tree := range trees {
		r, err := os.OpenRoot(root + string(os.PathSeparator) + tree)
		if err != nil {
			continue
		}
		s.roots[tree] = r
	}
	return s
}

// Has reports whether rel, relative to the given tree, exists in the factory
// tree. Callers pass rsync's own deletion paths, so directories arrive with a
// trailing slash.
//
// Containment is enforced by os.Root: every path segment is resolved inside
// the open root at the syscall boundary, so a symlink leading out of the
// factory tree cannot make an unrelated file look like factory content. An
// escape is reported as "not factory", which keeps the item visible as drift.
func (s *Set) Has(tree, rel string) bool {
	if s == nil || len(s.roots) == 0 {
		return false
	}
	r, ok := s.roots[tree]
	if !ok {
		return false
	}
	// rsync reports directory deletions with a trailing slash. Trimming
	// leaves the guard below able to recognise a path that names the tree
	// root rather than something inside it: Lstat(".") succeeds against the
	// root itself, which would mask a whole tree's drift behind one entry.
	rel = strings.Trim(rel, "/")
	if rel == "" || rel == "." {
		return false
	}
	_, err := r.Lstat(rel)
	return err == nil
}

// Close releases the open roots. A Set remains safe to use afterwards; it
// simply reports nothing as factory.
func (s *Set) Close() error {
	if s == nil {
		return nil
	}
	for _, r := range s.roots {
		_ = r.Close()
	}
	s.roots = nil
	return nil
}
