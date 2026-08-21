package plan

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/adamcarlile/flashcart/internal/config"
	"github.com/adamcarlile/flashcart/internal/nas"
	"github.com/adamcarlile/flashcart/internal/pass"
	"github.com/adamcarlile/flashcart/internal/runner"
)

func requireRealRsync(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync not installed, skipping integration test")
	}
	return bin
}

func writeFixture(t *testing.T, root, rel string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSeedRunReportsNoDrift is a real-rsync integration test over two temp
// directory trees: a populated NAS (ROM content, box-owned metadata, BIOS,
// and — the tree CRITICAL 1 found undefended — a populated saves archive)
// against a completely empty local tree, exactly the shape of a first run
// once the rollout runbook unmounts /userdata/{roms,bios,saves} to expose
// the empty local directories underneath.
//
// Before CRITICAL 1 was fixed, pass.Passes defined saves-push only, with no
// saves-pull to seed the box first. Reproduced against real rsync at
// review time:
//
//	$ rsync -a -n --delete --out-format='%i|%l|%n' <filters> empty_local_saves/ nas_saves/
//	*deleting  |0|psx/Final Fantasy VII.mcd
//	*deleting  |0|psx/Chrono Trigger.srm
//	*deleting  |0|psx/
//
// i.e. on a first run, every save on the NAS was offered for deletion in
// the drift panel at the exact moment no local copy existed. This test
// must report zero drift across every tree, using the real rsync binary
// rather than a stub that can silently omit the one tree that mattered.
func TestSeedRunReportsNoDrift(t *testing.T) {
	bin := requireRealRsync(t)

	nasRoot := t.TempDir()
	localRoot := t.TempDir()

	nasRoms := filepath.Join(nasRoot, "roms")
	nasBios := filepath.Join(nasRoot, "bios")
	nasSaves := filepath.Join(nasRoot, "saves")
	localRoms := filepath.Join(localRoot, "roms")
	localBios := filepath.Join(localRoot, "bios")
	localSaves := filepath.Join(localRoot, "saves")

	// Populate a realistic NAS side: ROM content, box-owned metadata
	// (gamelist plus scraped media), BIOS, and — the tree that mattered —
	// a populated saves archive.
	for _, rel := range []string{
		"snes/ActRaiser (USA).zip",
		"psx/Final Fantasy VII (USA).bin",
	} {
		writeFixture(t, nasRoms, rel)
	}
	for _, rel := range []string{
		"snes/gamelist.xml",
		"snes/images/ActRaiser (USA)-image.png",
	} {
		writeFixture(t, nasRoms, rel)
	}
	writeFixture(t, nasBios, "scph1001.bin")
	for _, rel := range []string{
		"psx/Final Fantasy VII.mcd",
		"psx/Chrono Trigger.srm",
		"snes/ActRaiser (USA).srm",
	} {
		writeFixture(t, nasSaves, rel)
	}

	// Local is left completely empty: os.MkdirAll below creates the bare
	// directories and nothing else, mirroring what "umount
	// /userdata/{roms,bios,saves}" exposes on the real box.
	for _, d := range []string{localRoms, localBios, localSaves} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &config.Config{
		Trees: config.Trees{
			Roms:  config.Tree{Export: "/unused", Local: localRoms},
			Bios:  config.Tree{Export: "/unused", Local: localBios},
			Saves: config.Tree{Export: "/unused", Local: localSaves},
		},
		SpaceMarginPercent: 10,
	}
	mounts := nas.Mounts{Roms: nasRoms, Bios: nasBios, Saves: nasSaves}
	ps := pass.Passes(cfg, mounts)

	r := runner.NewExec(bin)
	free := func(string) (int64, int64, error) { return 400 << 30, 459 << 30, nil }

	p, err := Build(context.Background(), cfg, r, ps, free)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, tp := range p.Trees {
		if len(tp.Drift) != 0 {
			t.Errorf("tree %s reported drift on a real seed run: %+v", tp.Tree, tp.Drift)
		}
	}
}
