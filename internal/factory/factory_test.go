package factory

import (
	"os"
	"path/filepath"
	"testing"
)

// datainit builds a factory tree resembling Batocera's own:
// <root>/roms/snes/_info.txt, <root>/bios/Machines/msx.rom.
func datainit(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range []string{
		"roms/snes/_info.txt",
		"roms/gba/SpaceTwins.gba",
		"bios/Machines/msx.rom",
		"bios/NstDatabase.xml",
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

func TestHasMatchesFactoryFile(t *testing.T) {
	s := Open(datainit(t))
	defer s.Close()

	for _, tc := range []struct{ tree, rel string }{
		{"roms", "snes/_info.txt"},
		{"roms", "gba/SpaceTwins.gba"},
		{"bios", "NstDatabase.xml"},
		{"bios", "Machines/msx.rom"},
	} {
		if !s.Has(tc.tree, tc.rel) {
			t.Errorf("Has(%q, %q) = false, want true", tc.tree, tc.rel)
		}
	}
}

// rsync reports directory deletions with a trailing slash.
func TestHasMatchesFactoryDirectoryWithTrailingSlash(t *testing.T) {
	s := Open(datainit(t))
	defer s.Close()

	if !s.Has("bios", "Machines/") {
		t.Error(`Has("bios", "Machines/") = false, want true`)
	}
}

func TestHasFalseForPathNotInFactoryTree(t *testing.T) {
	s := Open(datainit(t))
	defer s.Close()

	// A real game the user put on the box, and a real BIOS from the NAS.
	for _, tc := range []struct{ tree, rel string }{
		{"roms", "snes/Chrono Trigger.sfc"},
		{"bios", "scph5501.bin"},
		{"saves", "snes/Chrono Trigger.srm"},
	} {
		if s.Has(tc.tree, tc.rel) {
			t.Errorf("Has(%q, %q) = true, want false", tc.tree, tc.rel)
		}
	}
}

// The three managed trees are the only ones with factory content. Anything
// else must fall through to being reported as drift.
func TestHasFalseForUnknownTree(t *testing.T) {
	s := Open(datainit(t))
	defer s.Close()

	if s.Has("themes", "snes/_info.txt") {
		t.Error(`Has("themes", ...) = true, want false`)
	}
}

// flashcart runs on developer machines and in fake mode, where no appliance
// image exists. A missing root must simply exclude nothing.
func TestOpenMissingRootExcludesNothing(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "no-such-dir"))
	defer s.Close()

	if s.Has("roms", "snes/_info.txt") {
		t.Error("Has = true against a missing factory root, want false")
	}
}

func TestOpenEmptyRootExcludesNothing(t *testing.T) {
	s := Open("")
	defer s.Close()

	if s.Has("roms", "snes/_info.txt") {
		t.Error("Has = true against a disabled factory root, want false")
	}
}

// Containment: a symlink inside the factory tree pointing outside it must
// not make an out-of-tree path look like factory content.
func TestHasRefusesSymlinkEscape(t *testing.T) {
	root := datainit(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "bios", "escape")); err != nil {
		t.Fatal(err)
	}

	s := Open(root)
	defer s.Close()

	if s.Has("bios", "escape/secret.bin") {
		t.Error("Has followed a symlink out of the factory tree, want false")
	}
	if s.Has("bios", "../roms/snes/_info.txt") {
		t.Error("Has resolved a parent traversal, want false")
	}
}

func TestNilSetIsUsable(t *testing.T) {
	var s *Set
	if s.Has("roms", "snes/_info.txt") {
		t.Error("nil Set reported a factory path")
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close on nil Set: %v", err)
	}
}

// A path that names the tree root itself, rather than something inside it,
// must never be reported as factory content: that would mask an entire
// tree's drift behind one entry.
func TestHasFalseForTreeRootItself(t *testing.T) {
	s := Open(datainit(t))
	defer s.Close()

	for _, rel := range []string{"", "/", ".", "./"} {
		if s.Has("bios", rel) {
			t.Errorf("Has(%q) = true, want false", rel)
		}
	}
}
