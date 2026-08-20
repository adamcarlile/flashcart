package pass_test

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adamcarlile/flashcart/internal/config"
	"github.com/adamcarlile/flashcart/internal/nas"
	"github.com/adamcarlile/flashcart/internal/pass"
	"github.com/adamcarlile/flashcart/internal/paths"
	"github.com/adamcarlile/flashcart/internal/runner"
)

func requireRsync(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync not installed, skipping integration test")
	}
	return bin
}

// fixturePaths reads the captured real paths, dropping bare directory
// entries so every line becomes a file we can create.
func fixturePaths(t *testing.T) []string {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "testdata", "paths.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.Contains(filepath.Base(line), ".") {
			continue
		}
		out = append(out, line)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func buildTree(t *testing.T, root string, rels []string) {
	t.Helper()
	for _, rel := range rels {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// walkRel lists every regular file under root, relative and slash-separated.
func walkRel(t *testing.T, root string) map[string]bool {
	t.Helper()
	got := map[string]bool{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		got[filepath.ToSlash(rel)] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func cfgFor(t *testing.T, localRoms string) *config.Config {
	t.Helper()
	return &config.Config{
		Trees: config.Trees{
			Roms:  config.Tree{Export: "/unused", Local: localRoms},
			Bios:  config.Tree{Export: "/unused", Local: filepath.Join(t.TempDir(), "bios")},
			Saves: config.Tree{Export: "/unused", Local: filepath.Join(t.TempDir(), "saves")},
		},
	}
}

func passByID(ps []pass.Pass, id string) pass.Pass {
	for _, p := range ps {
		if p.ID == id {
			return p
		}
	}
	panic("no such pass: " + id)
}

// The content pull must move exactly the paths the classifier calls content.
func TestContentPullMatchesClassifier(t *testing.T) {
	bin := requireRsync(t)
	rels := fixturePaths(t)

	nasRoot := t.TempDir()
	localRoot := t.TempDir()
	buildTree(t, nasRoot, rels)

	ps := pass.Passes(cfgFor(t, localRoot), nas.Mounts{Roms: nasRoot})
	p := passByID(ps, "roms-content-pull")

	if _, err := runner.NewExec(bin).Run(context.Background(), p, make(chan runner.Event, 1024)); err != nil {
		t.Fatalf("content pull: %v", err)
	}

	got := walkRel(t, localRoot)
	for _, rel := range rels {
		want := paths.Classify(rel) == paths.ClassContent
		if got[rel] != want {
			t.Errorf("content pull: %q present=%v, classifier says content=%v", rel, got[rel], want)
		}
	}
}

// The metadata push must move exactly the paths the classifier calls
// metadata, and nothing else.
func TestMetadataPushMatchesClassifier(t *testing.T) {
	bin := requireRsync(t)
	rels := fixturePaths(t)

	nasRoot := t.TempDir()
	localRoot := t.TempDir()
	buildTree(t, localRoot, rels)

	ps := pass.Passes(cfgFor(t, localRoot), nas.Mounts{Roms: nasRoot})
	p := passByID(ps, "roms-metadata-push")

	if _, err := runner.NewExec(bin).Run(context.Background(), p, make(chan runner.Event, 1024)); err != nil {
		t.Fatalf("metadata push: %v", err)
	}

	got := walkRel(t, nasRoot)
	for _, rel := range rels {
		want := paths.Classify(rel) == paths.ClassMetadata
		if got[rel] != want {
			t.Errorf("metadata push: %q present=%v, classifier says metadata=%v", rel, got[rel], want)
		}
	}
}

// Neither direction may ever move an ignored path.
func TestIgnoredPathsNeverMove(t *testing.T) {
	bin := requireRsync(t)
	rels := fixturePaths(t)
	ex := runner.NewExec(bin)
	events := make(chan runner.Event, 1024)

	// Phase 1: Pull direction. Build NAS with full fixture including ignored paths.
	// Leave local empty. Pull passes must not deliver any ignored paths to local.
	{
		nasRoot := t.TempDir()
		localRoot := t.TempDir()
		buildTree(t, nasRoot, rels)

		for _, p := range pass.Passes(cfgFor(t, localRoot), nas.Mounts{Roms: nasRoot}) {
			if p.Tree != "roms" {
				continue
			}
			if _, err := ex.Run(context.Background(), p, events); err != nil {
				t.Fatalf("pull phase %s: %v", p.ID, err)
			}
		}

		for rel := range walkRel(t, localRoot) {
			if paths.Classify(rel) == paths.ClassIgnored {
				t.Errorf("pull phase: ignored path reached local: %q", rel)
			}
		}
	}

	// Phase 2: Push direction. Seed local with genuine metadata plus ignored paths.
	// Push must transfer metadata to NAS but never transfer ignored paths.
	{
		nasRoot := t.TempDir()
		localRoot := t.TempDir()

		// Genuine metadata that should push: system gamelists and image metadata.
		buildTree(t, localRoot, []string{
			"snes/gamelist.xml",
			"snes/images/ActRaiser-image.png",
			"ps3/gamelist.xml",
		})

		// Ignored paths that must not push: Synology indexer and partial transfer dir.
		buildTree(t, localRoot, []string{
			"snes/@eaDir/ActRaiser (USA).zip@SynoResource",
			"snes/images/@eaDir/ActRaiser-image.png@SynoResource",
			"snes/.flashcart-partial/ActRaiser (USA).zip",
		})

		// Run only the metadata push pass, since it tests the push direction specifically.
		ps := pass.Passes(cfgFor(t, localRoot), nas.Mounts{Roms: nasRoot})
		p := passByID(ps, "roms-metadata-push")
		if _, err := ex.Run(context.Background(), p, events); err != nil {
			t.Fatalf("push phase: %v", err)
		}

		// Assert: no ignored paths reached NAS.
		for rel := range walkRel(t, nasRoot) {
			if paths.Classify(rel) == paths.ClassIgnored {
				t.Errorf("push phase: ignored path reached NAS: %q", rel)
			}
		}

		// Assert: genuine metadata did arrive (so we're not just filtering everything).
		for _, want := range []string{"snes/gamelist.xml", "snes/images/ActRaiser-image.png", "ps3/gamelist.xml"} {
			if !walkRel(t, nasRoot)[want] {
				t.Errorf("push phase: genuine metadata did not arrive: %q", want)
			}
		}
	}
}

// A gamelist modified on the box survives a full ROMs cycle unchanged, which
// is the behaviour that protects play counts and favourites.
func TestLocalGamelistSurvivesFullCycle(t *testing.T) {
	bin := requireRsync(t)

	nasRoot := t.TempDir()
	localRoot := t.TempDir()
	buildTree(t, nasRoot, []string{"snes/ActRaiser (USA).zip", "snes/gamelist.xml"})
	buildTree(t, localRoot, []string{"snes/gamelist.xml"})

	const played = "<gameList><game><playcount>6</playcount></game></gameList>"
	local := filepath.Join(localRoot, "snes", "gamelist.xml")
	if err := os.WriteFile(local, []byte(played), 0o644); err != nil {
		t.Fatal(err)
	}

	ex := runner.NewExec(bin)
	events := make(chan runner.Event, 1024)
	for _, p := range pass.Passes(cfgFor(t, localRoot), nas.Mounts{Roms: nasRoot}) {
		if p.Tree != "roms" {
			continue
		}
		if _, err := ex.Run(context.Background(), p, events); err != nil {
			t.Fatalf("%s: %v", p.ID, err)
		}
	}

	after, err := os.ReadFile(local)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != played {
		t.Errorf("local gamelist was overwritten: %q", after)
	}
	onNAS, err := os.ReadFile(filepath.Join(nasRoot, "snes", "gamelist.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(onNAS) != played {
		t.Errorf("NAS gamelist was not updated from the box: %q", onNAS)
	}
	if _, err := os.Stat(filepath.Join(localRoot, "snes", "ActRaiser (USA).zip")); err != nil {
		t.Errorf("new ROM did not arrive locally: %v", err)
	}
}
