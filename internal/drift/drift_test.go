package drift

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adamcarlile/flashcart/internal/config"
	"github.com/adamcarlile/flashcart/internal/nas"
	"github.com/adamcarlile/flashcart/internal/plan"
)

func rootsIn(t *testing.T) (Roots, string, string) {
	t.Helper()
	localRoms := t.TempDir()
	nasSaves := t.TempDir()
	cfg := &config.Config{Trees: config.Trees{
		Roms:  config.Tree{Export: "/e/roms", Local: localRoms},
		Bios:  config.Tree{Export: "/e/bios", Local: t.TempDir()},
		Saves: config.Tree{Export: "/e/saves", Local: t.TempDir()},
	}}
	m := nas.Mounts{Roms: t.TempDir(), Bios: t.TempDir(), Saves: nasSaves}
	return RootsFor(cfg, m), localRoms, nasSaves
}

func touch(t *testing.T, root, rel string) string {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return full
}

func TestDeleteRemovesExactlyTheNamedPath(t *testing.T) {
	roots, localRoms, _ := rootsIn(t)

	target := touch(t, localRoms, "snes/Old Game (USA).zip")
	neighbour := touch(t, localRoms, "snes/Old Game (USA) 2.zip")
	unrelated := touch(t, localRoms, "nes/Keep.zip")

	deleted, err := Delete(roots, []plan.DriftItem{
		{Tree: "roms", Side: plan.SideLocal, Rel: "snes/Old Game (USA).zip"},
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(deleted) != 1 {
		t.Fatalf("deleted %v, want one path", deleted)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Error("target survived deletion")
	}
	for _, keep := range []string{neighbour, unrelated} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("collateral damage: %s is gone", keep)
		}
	}
}

func TestDeleteHandlesNASSide(t *testing.T) {
	roots, _, nasSaves := rootsIn(t)
	target := touch(t, nasSaves, "snes/Old.srm")

	if _, err := Delete(roots, []plan.DriftItem{
		{Tree: "saves", Side: plan.SideNAS, Rel: "snes/Old.srm"},
	}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Error("NAS-side target survived deletion")
	}
}

// Containment is the safety net. Anything that resolves outside its tree
// root is refused outright, and nothing is deleted.
func TestDeleteRefusesEscapingPaths(t *testing.T) {
	roots, localRoms, _ := rootsIn(t)
	outside := filepath.Join(filepath.Dir(localRoms), "outside.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, rel := range []string{
		"../outside.txt",
		"snes/../../outside.txt",
		"/etc/passwd",
		"",
		".",
		"..",
	} {
		t.Run(rel, func(t *testing.T) {
			_, err := Delete(roots, []plan.DriftItem{
				{Tree: "roms", Side: plan.SideLocal, Rel: rel},
			})
			if err == nil {
				t.Fatalf("Delete(%q) succeeded, want refusal", rel)
			}
			if !strings.Contains(err.Error(), "outside") && !strings.Contains(err.Error(), "empty") {
				t.Errorf("unhelpful refusal for %q: %v", rel, err)
			}
		})
	}
	if _, err := os.Stat(outside); err != nil {
		t.Error("a refused path was deleted anyway")
	}
}

// Validation happens for the whole batch before anything is removed, so one
// bad entry cannot leave a half-applied deletion.
func TestDeleteValidatesWholeBatchFirst(t *testing.T) {
	roots, localRoms, _ := rootsIn(t)
	good := touch(t, localRoms, "snes/Good.zip")

	_, err := Delete(roots, []plan.DriftItem{
		{Tree: "roms", Side: plan.SideLocal, Rel: "snes/Good.zip"},
		{Tree: "roms", Side: plan.SideLocal, Rel: "../escape.txt"},
	})
	if err == nil {
		t.Fatal("Delete succeeded with an invalid entry in the batch")
	}
	if _, err := os.Stat(good); err != nil {
		t.Error("a valid entry was deleted despite the batch being refused")
	}
}

func TestDeleteRefusesUnknownTreeOrSide(t *testing.T) {
	roots, _, _ := rootsIn(t)
	for _, item := range []plan.DriftItem{
		{Tree: "nope", Side: plan.SideLocal, Rel: "a.zip"},
		{Tree: "roms", Side: "elsewhere", Rel: "a.zip"},
	} {
		if _, err := Delete(roots, []plan.DriftItem{item}); err == nil {
			t.Errorf("Delete(%+v) succeeded, want refusal", item)
		}
	}
}
