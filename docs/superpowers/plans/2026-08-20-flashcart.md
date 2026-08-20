# flashcart Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Go web application that runs on a Batocera games box, maintaining a complete local mirror of a ROM library that normally lives on an NFS share, so the box works with no network attached.

**Architecture:** Five ordered rsync passes move data between three trees, with directions differing per file class inside the ROMs tree (ROM binaries pull from the NAS, EmulationStation metadata pushes to it). The NAS is mounted only for the duration of a run, never at boot. Two interfaces, `nas.Provider` and `runner.Runner`, form a seam so a scripted fake backend can drive the whole application without a NAS, a Batocera box, or 93 GB of data.

**Tech Stack:** Go 1.22+, `github.com/BurntSushi/toml`, `rsync` 3.x invoked via `exec.Command`, embedded vanilla JavaScript UI via `embed.FS`, GoReleaser for releases, `batocera-services` for process supervision.

**Spec:** `docs/superpowers/specs/2026-08-20-flashcart-design.md`

## Global Constraints

- Go module path is `github.com/adamcarlile/flashcart`. Go 1.22 minimum.
- Built with `CGO_ENABLED=0`. The target is buildroot-based Batocera v42 and the binary must be static.
- **No shell interpolation anywhere.** Every external command goes through `exec.Command` with an argument slice. Library filenames contain spaces, ampersands, apostrophes, commas and brackets, for example `Adventures of Batman & Robin, The (USA).zip`.
- **`--delete` may only ever appear on an rsync invocation that also carries `-n`.** It is used solely to enumerate what would be deleted. Real transfers never carry it. This is enforced by construction and by test.
- Drift deletion removes explicitly named paths via `os.RemoveAll`, never via rsync.
- Filter rules are anchored to `/<system>/<dir>/`. Never match `images/`, `videos/` or `manuals/` at arbitrary depth.
- `@eaDir` (Synology indexer) and `.flashcart-partial` are excluded in both directions at any depth.
- Listen port default `8474`. Port 8473 belongs to roadie and must stay free.
- Everything the installer writes lives under `/userdata/`. The Batocera root filesystem is a read-only squashfs and is reset on OS update.
- Fake mode is a command-line flag only, never a config key.
- Real paths on the target box: NAS `10.132.1.25`, exports `/volume2/retrogaming/{roms,bios,saves}`, locals `/userdata/{roms,bios,saves}`.

## File Structure

| Path | Responsibility |
| --- | --- |
| `main.go` | Flag parsing, subcommand dispatch, dependency wiring |
| `internal/config/config.go` | TOML load, defaults, validation |
| `internal/paths/classify.go` | Classify a ROMs-relative path as content, metadata or ignored |
| `internal/paths/filters.go` | Generate rsync filter rules matching the classifier |
| `internal/pass/pass.go` | The five pass definitions and their rsync argument vectors |
| `internal/runner/runner.go` | `Runner` interface, `Change`, `Result`, `Event` types |
| `internal/runner/exec.go` | Real rsync execution |
| `internal/runner/parse.go` | Itemize and progress output parsing |
| `internal/nas/nas.go` | `Provider` interface, `Mounts` type |
| `internal/nas/nfs.go` | Real probe, mount and unmount |
| `internal/plan/plan.go` | Dry-run orchestration, projected-state drift, space precheck |
| `internal/syncer/syncer.go` | Real-run orchestration and event emission |
| `internal/drift/drift.go` | Confirmed-path deletion with root containment checks |
| `internal/fake/fake.go` | Scripted `Provider` and `Runner`, scenario definitions |
| `internal/server/server.go` | HTTP routes and handlers |
| `internal/server/sse.go` | Server-sent events hub |
| `internal/server/assets/` | `index.html`, `app.js`, `style.css`, embedded |
| `internal/service/service.go` | `batocera-services` install and uninstall |
| `internal/buildinfo/buildinfo.go` | Version stamp and checksum-verified self-update |
| `testdata/paths.txt` | Real path fixture captured from the box |

---

### Task 1: Scaffold and configuration

**Files:**
- Create: `go.mod`, `internal/config/config.go`, `internal/config/config_test.go`, `flashcart.toml.example`

**Interfaces:**
- Consumes: nothing
- Produces: `config.Config` with fields `NAS config.NAS`, `Server config.Server`, `Trees config.Trees`, `SpaceMarginPercent int`. `config.NAS{Host string, Port int, MountRoot string}`. `config.Server{Listen string}`. `config.Trees{Roms, Bios, Saves config.Tree}`. `config.Tree{Export, Local string}`. `func config.Load(path string) (*config.Config, error)`.

- [ ] **Step 1: Initialise the module**

```bash
cd /home/adam/Work/Personal/flashcart
go mod init github.com/adamcarlile/flashcart
go get github.com/BurntSushi/toml@latest
```

`go mod init` stamps the local toolchain's version into `go.mod`. CI pins Go
1.22, so pin the module to match, and confirm it:

```bash
go mod edit -go=1.22
grep '^go ' go.mod    # must read: go 1.22
```

- [ ] **Step 2: Write the failing test**

Create `internal/config/config_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "flashcart.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const valid = `
[nas]
host = "10.132.1.25"

[server]
listen = ":8474"

[trees.roms]
export = "/volume2/retrogaming/roms"
local = "/userdata/roms"

[trees.bios]
export = "/volume2/retrogaming/bios"
local = "/userdata/bios"

[trees.saves]
export = "/volume2/retrogaming/saves"
local = "/userdata/saves"
`

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := Load(write(t, valid))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.NAS.Port != 2049 {
		t.Errorf("Port = %d, want 2049", cfg.NAS.Port)
	}
	if cfg.NAS.MountRoot != "/var/run/flashcart/nas" {
		t.Errorf("MountRoot = %q, want /var/run/flashcart/nas", cfg.NAS.MountRoot)
	}
	if cfg.SpaceMarginPercent != 10 {
		t.Errorf("SpaceMarginPercent = %d, want 10", cfg.SpaceMarginPercent)
	}
	if cfg.Trees.Roms.Local != "/userdata/roms" {
		t.Errorf("Roms.Local = %q", cfg.Trees.Roms.Local)
	}
}

func TestLoadRejectsBadConfigs(t *testing.T) {
	cases := map[string]struct{ body, want string }{
		"no host":          {strings.Replace(valid, `host = "10.132.1.25"`, `host = ""`, 1), "nas.host"},
		"relative export":  {strings.Replace(valid, `export = "/volume2/retrogaming/roms"`, `export = "retrogaming/roms"`, 1), "must be absolute"},
		"relative local":   {strings.Replace(valid, `local = "/userdata/bios"`, `local = "userdata/bios"`, 1), "must be absolute"},
		"missing saves":    {strings.Split(valid, "[trees.saves]")[0], "trees.saves"},
		"duplicate local":  {strings.Replace(valid, `local = "/userdata/bios"`, `local = "/userdata/roms"`, 1), "duplicate"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(write(t, tc.body))
			if err == nil {
				t.Fatal("Load succeeded, want error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/config/ -v`
Expected: FAIL, `undefined: Load`

- [ ] **Step 4: Write the implementation**

Create `internal/config/config.go`:

```go
// Package config loads and validates the flashcart configuration file.
package config

import (
	"fmt"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Tree is one NAS export paired with its local mirror directory.
type Tree struct {
	Export string `toml:"export"`
	Local  string `toml:"local"`
}

// Trees is the fixed set of three trees flashcart manages.
type Trees struct {
	Roms  Tree `toml:"roms"`
	Bios  Tree `toml:"bios"`
	Saves Tree `toml:"saves"`
}

// NAS describes how to reach and mount the share.
type NAS struct {
	Host      string `toml:"host"`
	Port      int    `toml:"port"`
	MountRoot string `toml:"mount_root"`
}

// Server describes the HTTP listener.
type Server struct {
	Listen string `toml:"listen"`
}

// Config is the whole validated configuration.
type Config struct {
	NAS                NAS    `toml:"nas"`
	Server             Server `toml:"server"`
	Trees              Trees  `toml:"trees"`
	SpaceMarginPercent int    `toml:"space_margin_percent"`
}

// Load reads, defaults and validates a configuration file.
func Load(path string) (*Config, error) {
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(path); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.NAS.Port == 0 {
		c.NAS.Port = 2049
	}
	if c.NAS.MountRoot == "" {
		c.NAS.MountRoot = "/var/run/flashcart/nas"
	}
	if c.Server.Listen == "" {
		c.Server.Listen = ":8474"
	}
	if c.SpaceMarginPercent == 0 {
		c.SpaceMarginPercent = 10
	}
}

func (c *Config) validate(path string) error {
	if c.NAS.Host == "" {
		return fmt.Errorf("config %s: nas.host is required", path)
	}
	if c.NAS.Port < 1 || c.NAS.Port > 65535 {
		return fmt.Errorf("config %s: nas.port %d out of range", path, c.NAS.Port)
	}
	if !filepath.IsAbs(c.NAS.MountRoot) {
		return fmt.Errorf("config %s: nas.mount_root must be absolute", path)
	}
	seenLocal := map[string]string{}
	seenExport := map[string]string{}
	for _, t := range []struct {
		key  string
		tree Tree
	}{
		{"trees.roms", c.Trees.Roms},
		{"trees.bios", c.Trees.Bios},
		{"trees.saves", c.Trees.Saves},
	} {
		if t.tree.Export == "" || t.tree.Local == "" {
			return fmt.Errorf("config %s: %s requires both export and local", path, t.key)
		}
		if !filepath.IsAbs(t.tree.Export) {
			return fmt.Errorf("config %s: %s.export must be absolute", path, t.key)
		}
		if !filepath.IsAbs(t.tree.Local) {
			return fmt.Errorf("config %s: %s.local must be absolute", path, t.key)
		}
		clean := filepath.Clean(t.tree.Local)
		if prev, ok := seenLocal[clean]; ok {
			return fmt.Errorf("config %s: %s.local is a duplicate of %s.local", path, t.key, prev)
		}
		seenLocal[clean] = t.key
		cleanExp := filepath.Clean(t.tree.Export)
		if prev, ok := seenExport[cleanExp]; ok {
			return fmt.Errorf("config %s: %s.export is a duplicate of %s.export", path, t.key, prev)
		}
		seenExport[cleanExp] = t.key
	}
	return nil
}

// LocalRoots returns the three local mirror directories, used by drift
// deletion to bound which paths may be removed.
func (c *Config) LocalRoots() []string {
	return []string{
		filepath.Clean(c.Trees.Roms.Local),
		filepath.Clean(c.Trees.Bios.Local),
		filepath.Clean(c.Trees.Saves.Local),
	}
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/config/ -v`
Expected: PASS, all subtests green

- [ ] **Step 6: Write the example config**

Create `flashcart.toml.example`:

```toml
# flashcart — copy to /userdata/system/flashcart/flashcart.toml on the Batocera box.

[nas]
host = "10.132.1.25"
# port = 2049
# mount_root = "/var/run/flashcart/nas"

[server]
listen = ":8474"

[trees.roms]
export = "/volume2/retrogaming/roms"
local = "/userdata/roms"

[trees.bios]
export = "/volume2/retrogaming/bios"
local = "/userdata/bios"

[trees.saves]
export = "/volume2/retrogaming/saves"
local = "/userdata/saves"

# Refuse to sync if the plan would leave less than this percentage of the
# filesystem free.
# space_margin_percent = 10
```

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/config/ flashcart.toml.example
git commit -m "Add configuration loading and validation"
```

---

### Task 2: Path classification

This is the highest-risk component in the project. The ROMs tree contains game directories at the same depth as metadata directories, so a rule matching `images/` at any depth would misclassify game content as metadata and push it in the wrong direction.

**Files:**
- Create: `internal/paths/classify.go`, `internal/paths/classify_test.go`, `testdata/paths.txt`

**Interfaces:**
- Consumes: nothing
- Produces: `paths.Class` (`paths.ClassContent`, `paths.ClassMetadata`, `paths.ClassIgnored`), `func paths.Classify(rel string) paths.Class`, `var paths.MetadataDirs = []string{"images", "videos", "manuals"}`, `const paths.MetadataFile = "gamelist.xml"`, `const paths.PartialDir = ".flashcart-partial"`

- [ ] **Step 1: Capture the real path fixture from the box**

The box may not be reachable when this task runs. Write the fixture by hand from the paths recorded in the spec, which were captured on 2026-08-20.

Create `testdata/paths.txt`:

```
snes/ActRaiser (USA).zip
snes/Adventures of Batman & Robin, The (USA).zip
snes/gamelist.xml
snes/_info.txt
snes/images
snes/images/ActRaiser (USA)-image.png
snes/images/ActRaiser (USA)-marquee.png
snes/images/ActRaiser (USA)-thumb.png
snes/videos/ActRaiser (USA)-video.mp4
snes/manuals/ActRaiser (USA)-manual.pdf
ps3/God of War Collection.ps3/USRDIR/EBOOT.BIN
ps3/Skate 3.ps3/PS3_GAME/ICON0.PNG
ps3/Tiger Woods PGA Tour 14.ps3/USRDIR/data.psarc
ps3/gamelist.xml
ports/main/game.dat
ports/Prince of Persia/PRINCE.EXE
ports/Wolfenstein 3D/WOLF3D.EXE
mame/mame2003/samples.zip
fbneo/fbneo/roms.zip
pygame/pygun/main.py
lcdgames/game-musics/track.vgm
prboom/Doom/doom1.wad
neogeo/data/neogeo.zip
snes/@eaDir/ActRaiser (USA).zip@SynoResource
@eaDir/thumb.jpg
snes/images/@eaDir/ActRaiser (USA)-image.png@SynoResource
snes/.flashcart-partial/ActRaiser (USA).zip
readme.txt
```

- [ ] **Step 2: Write the failing test**

Create `internal/paths/classify_test.go`:

```go
package paths

import (
	"bufio"
	"os"
	"path/filepath"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		rel  string
		want Class
	}{
		// Content: ROM files at system level.
		{"snes/ActRaiser (USA).zip", ClassContent},
		{"snes/Adventures of Batman & Robin, The (USA).zip", ClassContent},
		{"snes/_info.txt", ClassContent},

		// Metadata: the gamelist and the three media directories, anchored at depth 2.
		{"snes/gamelist.xml", ClassMetadata},
		{"snes/images", ClassMetadata},
		{"snes/images/ActRaiser (USA)-image.png", ClassMetadata},
		{"snes/videos/ActRaiser (USA)-video.mp4", ClassMetadata},
		{"snes/manuals/ActRaiser (USA)-manual.pdf", ClassMetadata},

		// Content: game directories sit at the same depth as metadata directories
		// and must not be confused with them.
		{"ps3/God of War Collection.ps3", ClassContent},
		{"ps3/God of War Collection.ps3/USRDIR/EBOOT.BIN", ClassContent},
		{"ps3/Skate 3.ps3/PS3_GAME/ICON0.PNG", ClassContent},
		{"ports/main/game.dat", ClassContent},
		{"ports/Prince of Persia/PRINCE.EXE", ClassContent},
		{"mame/mame2003/samples.zip", ClassContent},
		{"pygame/pygun/main.py", ClassContent},
		{"neogeo/data/neogeo.zip", ClassContent},

		// A gamelist.xml nested deeper than depth 2 belongs to a game, not a system.
		{"ps3/God of War Collection.ps3/gamelist.xml", ClassContent},

		// A directory named images deeper than depth 2 is game content.
		{"ps3/God of War Collection.ps3/images/logo.png", ClassContent},

		// Ignored at any depth.
		{"@eaDir/thumb.jpg", ClassIgnored},
		{"snes/@eaDir/ActRaiser (USA).zip@SynoResource", ClassIgnored},
		{"snes/images/@eaDir/ActRaiser (USA)-image.png@SynoResource", ClassIgnored},
		{"snes/.flashcart-partial/ActRaiser (USA).zip", ClassIgnored},

		// Top-level files are content.
		{"readme.txt", ClassContent},
	}
	for _, tc := range cases {
		if got := Classify(tc.rel); got != tc.want {
			t.Errorf("Classify(%q) = %v, want %v", tc.rel, got, tc.want)
		}
	}
}

// Every line of the captured fixture must classify without panicking, and the
// fixture must exercise all three classes.
func TestClassifyRealFixture(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "testdata", "paths.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	seen := map[Class]int{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		seen[Classify(line)]++
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []Class{ClassContent, ClassMetadata, ClassIgnored} {
		if seen[c] == 0 {
			t.Errorf("fixture never produced class %v", c)
		}
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/paths/ -v`
Expected: FAIL, `undefined: Classify`

- [ ] **Step 4: Write the implementation**

Create `internal/paths/classify.go`:

```go
// Package paths decides which side of a sync owns a given file inside the
// ROMs tree, and generates the rsync filter rules that enforce that decision.
package paths

import "strings"

// Class is the sync ownership of a path relative to the ROMs tree root.
type Class int

const (
	// ClassContent is NAS-owned: ROM binaries and everything unclassified.
	ClassContent Class = iota
	// ClassMetadata is box-owned: gamelists and scraped media, which
	// EmulationStation rewrites as games are played and scraped.
	ClassMetadata
	// ClassIgnored is never transferred in either direction.
	ClassIgnored
)

func (c Class) String() string {
	switch c {
	case ClassContent:
		return "content"
	case ClassMetadata:
		return "metadata"
	case ClassIgnored:
		return "ignored"
	}
	return "unknown"
}

// MetadataDirs are the per-system directories EmulationStation writes into.
// They are only metadata directly beneath a system directory.
var MetadataDirs = []string{"images", "videos", "manuals"}

// MetadataFile is the per-system gamelist EmulationStation rewrites on exit.
const MetadataFile = "gamelist.xml"

// PartialDir holds rsync's partially transferred files between runs.
const PartialDir = ".flashcart-partial"

// ignoredComponent is the Synology indexer directory, which may appear at
// any depth and must never be transferred.
const ignoredComponent = "@eaDir"

// Classify returns the ownership of a slash-separated path relative to the
// ROMs tree root, for example "snes/images/ActRaiser (USA)-image.png".
//
// Metadata rules are anchored to depth two, directly beneath a system
// directory. The tree contains game directories at that same depth, such as
// "ps3/God of War Collection.ps3", so an unanchored match would misclassify
// game content as metadata and send it in the wrong direction.
func Classify(rel string) Class {
	parts := strings.Split(strings.Trim(rel, "/"), "/")

	for _, p := range parts {
		if p == ignoredComponent || p == PartialDir {
			return ClassIgnored
		}
	}

	if len(parts) < 2 {
		return ClassContent
	}

	if len(parts) == 2 && parts[1] == MetadataFile {
		return ClassMetadata
	}

	for _, d := range MetadataDirs {
		if parts[1] == d {
			return ClassMetadata
		}
	}

	return ClassContent
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/paths/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/paths/ testdata/
git commit -m "Add ROMs path classification anchored at system depth"
```

---

### Task 3: rsync filter rules

The classifier decides ownership in Go. rsync must be told the same thing in its own filter language. These are two independent expressions of one rule, and Task 6 asserts they agree by running real rsync over a fixture tree.

**Files:**
- Create: `internal/paths/filters.go`, `internal/paths/filters_test.go`

**Interfaces:**
- Consumes: `paths.MetadataDirs`, `paths.MetadataFile`, `paths.PartialDir` from Task 2
- Produces: `func paths.ContentFilters() []string`, `func paths.MetadataFilters() []string`, `func paths.PlainFilters() []string`

- [ ] **Step 1: Write the failing test**

Create `internal/paths/filters_test.go`:

```go
package paths

import (
	"strings"
	"testing"
)

// The Synology and partial-dir exclusions must precede any include rule,
// because rsync applies filters in order and the first match wins.
func TestIgnoredRulesComeFirst(t *testing.T) {
	for name, rules := range map[string][]string{
		"content":  ContentFilters(),
		"metadata": MetadataFilters(),
		"plain":    PlainFilters(),
	} {
		t.Run(name, func(t *testing.T) {
			if len(rules) < 2 {
				t.Fatalf("expected at least two rules, got %v", rules)
			}
			if !strings.Contains(rules[0], "@eaDir") {
				t.Errorf("first rule %q does not exclude @eaDir", rules[0])
			}
			if !strings.Contains(rules[1], PartialDir) {
				t.Errorf("second rule %q does not exclude the partial dir", rules[1])
			}
			for i, r := range rules {
				if strings.HasPrefix(r, "+ ") && i < 2 {
					t.Errorf("include rule %q appears before the exclusions", r)
				}
			}
		})
	}
}

// Content filters exclude metadata, anchored so that only depth-two
// directories are affected.
func TestContentFiltersExcludeAnchoredMetadata(t *testing.T) {
	got := strings.Join(ContentFilters(), "\n")
	for _, want := range []string{
		"- /*/gamelist.xml",
		"- /*/images/",
		"- /*/videos/",
		"- /*/manuals/",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("content filters missing %q\ngot:\n%s", want, got)
		}
	}
	if strings.Contains(got, "- images/") {
		t.Error("content filters contain an unanchored images rule")
	}
}

// Metadata filters are include-only, ending in a catch-all exclusion.
func TestMetadataFiltersAreIncludeOnly(t *testing.T) {
	rules := MetadataFilters()
	last := rules[len(rules)-1]
	if last != "- *" {
		t.Errorf("last metadata rule = %q, want %q", last, "- *")
	}
	got := strings.Join(rules, "\n")
	for _, want := range []string{
		"+ /*/",
		"+ /*/gamelist.xml",
		"+ /*/images/",
		"+ /*/images/**",
		"+ /*/videos/",
		"+ /*/videos/**",
		"+ /*/manuals/",
		"+ /*/manuals/**",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("metadata filters missing %q\ngot:\n%s", want, got)
		}
	}
}

// Plain filters carry no metadata rules at all, for trees with no split.
func TestPlainFiltersHaveNoMetadataRules(t *testing.T) {
	got := strings.Join(PlainFilters(), "\n")
	if strings.Contains(got, "gamelist") || strings.Contains(got, "images") {
		t.Errorf("plain filters should not mention metadata: %s", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/paths/ -run Filters -v`
Expected: FAIL, `undefined: ContentFilters`

- [ ] **Step 3: Write the implementation**

Create `internal/paths/filters.go`:

```go
package paths

import "fmt"

// ignoredRules exclude the Synology indexer and rsync's partial directory at
// any depth. rsync applies rules in order and the first match wins, so these
// must lead every rule set.
func ignoredRules() []string {
	return []string{
		fmt.Sprintf("- %s/", ignoredComponent),
		fmt.Sprintf("- %s/", PartialDir),
	}
}

// PlainFilters are for trees with no content/metadata split, namely bios and
// saves. They exclude only the never-transferred paths.
func PlainFilters() []string {
	return ignoredRules()
}

// ContentFilters select NAS-owned ROM content by excluding the box-owned
// metadata. The leading slash anchors each pattern to the transfer root, so
// "/*/images/" matches "snes/images" but never
// "ps3/God of War Collection.ps3/images".
func ContentFilters() []string {
	rules := ignoredRules()
	rules = append(rules, fmt.Sprintf("- /*/%s", MetadataFile))
	for _, d := range MetadataDirs {
		rules = append(rules, fmt.Sprintf("- /*/%s/", d))
	}
	return rules
}

// MetadataFilters select box-owned metadata and nothing else. System
// directories are included so rsync descends into them; everything not
// explicitly included is then excluded by the trailing catch-all.
func MetadataFilters() []string {
	rules := ignoredRules()
	rules = append(rules, "+ /*/")
	rules = append(rules, fmt.Sprintf("+ /*/%s", MetadataFile))
	for _, d := range MetadataDirs {
		rules = append(rules, fmt.Sprintf("+ /*/%s/", d))
		rules = append(rules, fmt.Sprintf("+ /*/%s/**", d))
	}
	rules = append(rules, "- *")
	return rules
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/paths/ -v`
Expected: PASS, all tests in the package

- [ ] **Step 5: Commit**

```bash
git add internal/paths/filters.go internal/paths/filters_test.go
git commit -m "Add rsync filter rules matching the path classifier"
```

---

### Task 4: Pass definitions and rsync argument vectors

Encodes the five ordered passes and, critically, makes it structurally impossible for `--delete` to appear on a real transfer.

**Files:**
- Create: `internal/pass/pass.go`, `internal/pass/pass_test.go`

**Interfaces:**
- Consumes: `config.Config` (Task 1), `paths.ContentFilters`, `paths.MetadataFilters`, `paths.PlainFilters` (Task 3)
- Produces: `pass.Direction` (`pass.DirPull`, `pass.DirPush`), `pass.Pass{ID, Label, Tree string, Direction Direction, Src, Dst string, Filters, Extra []string}`, `func pass.Passes(cfg *config.Config, m nas.Mounts) []pass.Pass`, `func (Pass) DryRunArgs() []string`, `func (Pass) RunArgs() []string`

- [ ] **Step 1: Write the failing test**

Create `internal/pass/pass_test.go`:

```go
package pass

import (
	"slices"
	"strings"
	"testing"

	"github.com/adamcarlile/flashcart/internal/config"
	"github.com/adamcarlile/flashcart/internal/nas"
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/pass/ -v`
Expected: FAIL, `undefined: Passes`

- [ ] **Step 3: Write the implementation**

Create `internal/pass/pass.go`:

```go
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
```

- [ ] **Step 4: Create the nas.Mounts type the test depends on**

Create `internal/nas/nas.go`. The real provider lands in Task 7; this task only needs the value types.

```go
// Package nas probes and mounts the NFS exports for the duration of a run.
package nas

import "context"

// Mounts are the local paths at which the three exports are mounted.
type Mounts struct {
	Roms  string
	Bios  string
	Saves string
}

// Provider probes and mounts the NAS. It is the seam that lets fake mode
// drive the application with no network.
type Provider interface {
	// Probe reports whether the NAS is reachable. It must be cheap and
	// must not mount anything.
	Probe(ctx context.Context) error
	// Mount mounts all three exports and returns them together with an
	// unmount function that must always be called.
	Mount(ctx context.Context) (Mounts, func() error, error)
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/pass/ ./internal/nas/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/pass/ internal/nas/
git commit -m "Add the five pass definitions and rsync argument construction

--delete is emitted only alongside -n, purely to enumerate drift. Real
transfers can never carry it."
```

---

### Task 5: rsync execution and output parsing

**Files:**
- Create: `internal/runner/runner.go`, `internal/runner/parse.go`, `internal/runner/parse_test.go`, `internal/runner/exec.go`

**Interfaces:**
- Consumes: `pass.Pass` (Task 4)
- Produces: `runner.Change{Itemize string, Size int64, Path string, Deleting bool}`, `runner.Result{PassID string, Changes []Change, TransferBytes int64, Deletions []string}`, `runner.Event{PassID string, Percent int, Message string}`, `runner.Runner` interface with `DryRun(ctx, pass.Pass) (Result, error)` and `Run(ctx, pass.Pass, chan<- Event) (Result, error)`, `func runner.NewExec(binary string) *runner.Exec`, `func runner.ParseItemize(passID, out string) Result`, `func runner.ParseProgress(line string) (int, bool)`

- [ ] **Step 1: Write the failing test**

Create `internal/runner/parse_test.go`:

```go
package runner

import "testing"

// Real rsync output captured with --out-format=%i|%l|%n. Deletions are
// reported as "*deleting" with a zero length.
const sample = `>f+++++++++|685275|snes/ActRaiser (USA).zip
>f+++++++++|1108505|snes/ActRaiser 2 (USA).zip
cd+++++++++|4096|snes/images
>f.st......|512|snes/gamelist.xml
*deleting  |0|snes/Old Game (USA).zip
*deleting  |0|snes/images/Old Game (USA)-image.png
`

func TestParseItemizeSeparatesTransfersFromDeletions(t *testing.T) {
	got := ParseItemize("roms-content-pull", sample)

	if got.PassID != "roms-content-pull" {
		t.Errorf("PassID = %q", got.PassID)
	}
	// Three file transfers plus one directory creation.
	if len(got.Changes) != 4 {
		t.Fatalf("len(Changes) = %d, want 4: %+v", len(got.Changes), got.Changes)
	}
	// Only regular file transfers count toward bytes; directories do not.
	const wantBytes = 685275 + 1108505 + 512
	if got.TransferBytes != wantBytes {
		t.Errorf("TransferBytes = %d, want %d", got.TransferBytes, wantBytes)
	}
	if len(got.Deletions) != 2 {
		t.Fatalf("len(Deletions) = %d, want 2: %v", len(got.Deletions), got.Deletions)
	}
	if got.Deletions[0] != "snes/Old Game (USA).zip" {
		t.Errorf("Deletions[0] = %q", got.Deletions[0])
	}
	if got.Deletions[1] != "snes/images/Old Game (USA)-image.png" {
		t.Errorf("Deletions[1] = %q", got.Deletions[1])
	}
}

// Paths may contain the separator character; only the first two are real.
func TestParseItemizeHandlesPipeInFilename(t *testing.T) {
	got := ParseItemize("x", ">f+++++++++|100|snes/Weird | Name.zip\n")
	if len(got.Changes) != 1 {
		t.Fatalf("len(Changes) = %d", len(got.Changes))
	}
	if got.Changes[0].Path != "snes/Weird | Name.zip" {
		t.Errorf("Path = %q", got.Changes[0].Path)
	}
}

func TestParseItemizeIgnoresNoiseLines(t *testing.T) {
	got := ParseItemize("x", "sending incremental file list\n\nsent 1,234 bytes\n")
	if len(got.Changes) != 0 || len(got.Deletions) != 0 {
		t.Errorf("noise produced changes: %+v", got)
	}
}

func TestParseProgress(t *testing.T) {
	cases := map[string]struct {
		want int
		ok   bool
	}{
		"    1,234,567  42%   11.20MB/s    0:00:31": {42, true},
		"   93,000,000 100%   98.00MB/s    0:00:00 (xfr#3, to-chk=0/4)": {100, true},
		"sending incremental file list":                                 {0, false},
		"":                                                              {0, false},
	}
	for line, tc := range cases {
		got, ok := ParseProgress(line)
		if ok != tc.ok {
			t.Errorf("ParseProgress(%q) ok = %v, want %v", line, ok, tc.ok)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("ParseProgress(%q) = %d, want %d", line, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/runner/ -v`
Expected: FAIL, `undefined: ParseItemize`

- [ ] **Step 3: Write the types**

Create `internal/runner/runner.go`:

```go
// Package runner executes rsync passes and parses their output. The Runner
// interface is the seam that lets fake mode drive the application without
// invoking rsync at all.
package runner

import (
	"context"

	"github.com/adamcarlile/flashcart/internal/pass"
)

// Change is one entry from rsync's itemized output.
type Change struct {
	Itemize string
	Size    int64
	Path    string
}

// Result is the outcome of one pass.
type Result struct {
	PassID string
	// Changes are files and directories that were, or would be, transferred.
	Changes []Change
	// TransferBytes counts only regular file transfers.
	TransferBytes int64
	// Deletions are paths present on the destination but absent from the
	// source. They are reported, never acted upon by this package.
	Deletions []string
}

// Event is a progress update emitted during a real run.
type Event struct {
	PassID  string
	Percent int
	Message string
}

// Runner executes passes.
type Runner interface {
	// DryRun enumerates what a pass would do without changing anything.
	DryRun(ctx context.Context, p pass.Pass) (Result, error)
	// Run performs the pass, emitting progress on events. It does not close
	// the channel.
	Run(ctx context.Context, p pass.Pass, events chan<- Event) (Result, error)
}
```

- [ ] **Step 4: Write the parser**

Create `internal/runner/parse.go`:

```go
package runner

import (
	"strconv"
	"strings"
)

const deletingPrefix = "*deleting"

// ParseItemize reads rsync output produced with --out-format=%i|%l|%n.
// Lines that are not itemized output, such as rsync's own summary, are
// ignored.
func ParseItemize(passID, out string) Result {
	res := Result{PassID: passID}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		// A path may itself contain the separator, so only the first two
		// fields are split off.
		fields := strings.SplitN(line, "|", 3)
		if len(fields) != 3 {
			continue
		}
		flags, sizeStr, path := fields[0], fields[1], fields[2]

		if strings.HasPrefix(flags, deletingPrefix) {
			res.Deletions = append(res.Deletions, path)
			continue
		}
		// Itemize flags are eleven characters describing the update type.
		// Anything shorter is not an itemized line.
		if len(strings.TrimSpace(flags)) < 2 {
			continue
		}
		size, err := strconv.ParseInt(sizeStr, 10, 64)
		if err != nil {
			continue
		}
		res.Changes = append(res.Changes, Change{Itemize: flags, Size: size, Path: path})
		// Only regular files contribute bytes. The second flag character is
		// the entry type: 'f' for file, 'd' for directory.
		if len(flags) > 1 && flags[1] == 'f' {
			res.TransferBytes += size
		}
	}
	return res
}

// ParseProgress extracts the overall percentage from an --info=progress2
// line. It returns false for any line that is not a progress update.
func ParseProgress(line string) (int, bool) {
	for _, f := range strings.Fields(line) {
		if !strings.HasSuffix(f, "%") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSuffix(f, "%"))
		if err != nil || n < 0 || n > 100 {
			continue
		}
		return n, true
	}
	return 0, false
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/runner/ -v`
Expected: PASS

- [ ] **Step 6: Write the real executor**

Create `internal/runner/exec.go`:

```go
package runner

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/adamcarlile/flashcart/internal/pass"
)

// Exec runs real rsync. Arguments are always passed as a slice: the library
// contains filenames with spaces, ampersands, apostrophes and brackets, and
// no part of this path may go through a shell.
type Exec struct {
	Binary string
}

// NewExec returns an Exec using the given rsync binary.
func NewExec(binary string) *Exec {
	if binary == "" {
		binary = "rsync"
	}
	return &Exec{Binary: binary}
}

var _ Runner = (*Exec)(nil)

// DryRun enumerates a pass without changing anything.
func (e *Exec) DryRun(ctx context.Context, p pass.Pass) (Result, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, e.Binary, p.DryRunArgs()...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return Result{PassID: p.ID}, fmt.Errorf("rsync dry run %s: %w: %s", p.ID, err, strings.TrimSpace(stderr.String()))
	}
	return ParseItemize(p.ID, stdout.String()), nil
}

// Run performs a pass, forwarding progress percentages as they arrive.
// rsync writes progress with carriage returns rather than newlines, so the
// stream is split on both.
func (e *Exec) Run(ctx context.Context, p pass.Pass, events chan<- Event) (Result, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, e.Binary, p.RunArgs()...)
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{PassID: p.ID}, fmt.Errorf("rsync %s: %w", p.ID, err)
	}
	if err := cmd.Start(); err != nil {
		return Result{PassID: p.ID}, fmt.Errorf("rsync %s: %w", p.ID, err)
	}

	sc := bufio.NewScanner(stdout)
	sc.Split(scanLinesOrCR)
	for sc.Scan() {
		if pct, ok := ParseProgress(sc.Text()); ok {
			select {
			case events <- Event{PassID: p.ID, Percent: pct}:
			case <-ctx.Done():
			}
		}
	}
	if err := cmd.Wait(); err != nil {
		return Result{PassID: p.ID}, fmt.Errorf("rsync %s: %w: %s", p.ID, err, strings.TrimSpace(stderr.String()))
	}
	return Result{PassID: p.ID}, nil
}

// scanLinesOrCR splits on either a newline or a carriage return, because
// rsync's progress output overwrites a single line using CR.
func scanLinesOrCR(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}
```

- [ ] **Step 7: Run the full package tests**

Run: `go build ./... && go test ./internal/runner/ -v`
Expected: PASS, and the build succeeds

- [ ] **Step 8: Commit**

```bash
git add internal/runner/
git commit -m "Add rsync execution and itemize/progress parsing"
```

---

### Task 6: Prove the filters and the classifier agree

`paths.Classify` and the rsync filter rules express the same decision in two different languages. Nothing so far guarantees they agree. This task builds a fixture tree, runs real rsync over it, and asserts that what rsync actually moves matches what the classifier predicted.

Requires `rsync` on the development machine. Skip the test if absent rather than failing.

**Files:**
- Create: `internal/pass/integration_test.go`

**Interfaces:**
- Consumes: `paths.Classify` (Task 2), `pass.Passes` (Task 4), `runner.NewExec` (Task 5)
- Produces: nothing consumed by later tasks

- [ ] **Step 1: Write the failing test**

Create `internal/pass/integration_test.go`:

```go
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

	freshNAS := t.TempDir()
	freshLocal := t.TempDir()
	buildTree(t, freshNAS, rels)

	for _, p := range pass.Passes(cfgFor(t, freshLocal), nas.Mounts{Roms: freshNAS}) {
		if p.Tree != "roms" {
			continue
		}
		if _, err := ex.Run(context.Background(), p, events); err != nil {
			t.Fatalf("%s: %v", p.ID, err)
		}
	}

	for rel := range walkRel(t, freshLocal) {
		if paths.Classify(rel) == paths.ClassIgnored {
			t.Errorf("ignored path was transferred: %q", rel)
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
```

- [ ] **Step 2: Run the test**

Run: `go test ./internal/pass/ -run 'Classifier|Ignored|Cycle' -v`
Expected: PASS. If any case fails, the filter rules in `internal/paths/filters.go` disagree with `paths.Classify` and one of the two is wrong. Fix the filters first, since the classifier is the specification.

- [ ] **Step 3: Commit**

```bash
git add internal/pass/integration_test.go
git commit -m "Verify rsync filters agree with the path classifier

Runs real rsync over a fixture tree built from paths captured on the box."
```

---

### Task 7: NAS probe and mount lifecycle

**Files:**
- Create: `internal/nas/nfs.go`, `internal/nas/nfs_test.go`
- Modify: `internal/nas/nas.go` (add `MountError`)

**Interfaces:**
- Consumes: `config.Config` (Task 1), `nas.Mounts`, `nas.Provider` (Task 4)
- Produces: `func nas.NewNFS(cfg *config.Config) *nas.NFS`, `nas.NFS` implementing `nas.Provider`, `nas.ErrUnreachable`, `nas.ErrExportMissing`, `nas.ErrPermission`, `func nas.ClassifyMountError(stderr string, exit int) error`

- [ ] **Step 1: Write the failing test**

Create `internal/nas/nfs_test.go`:

```go
package nas

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/adamcarlile/flashcart/internal/config"
)

func cfgWith(host string, port int) *config.Config {
	return &config.Config{
		NAS: config.NAS{Host: host, Port: port, MountRoot: "/var/run/flashcart/nas"},
		Trees: config.Trees{
			Roms:  config.Tree{Export: "/volume2/retrogaming/roms", Local: "/userdata/roms"},
			Bios:  config.Tree{Export: "/volume2/retrogaming/bios", Local: "/userdata/bios"},
			Saves: config.Tree{Export: "/volume2/retrogaming/saves", Local: "/userdata/saves"},
		},
	}
}

func TestProbeSucceedsAgainstAListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	port := ln.Addr().(*net.TCPAddr).Port

	n := NewNFS(cfgWith("127.0.0.1", port))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := n.Probe(ctx); err != nil {
		t.Fatalf("Probe: %v", err)
	}
}

func TestProbeFailsWhenNothingListens(t *testing.T) {
	// Port 1 on loopback: reserved and never listening.
	n := NewNFS(cfgWith("127.0.0.1", 1))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := n.Probe(ctx)
	if err == nil {
		t.Fatal("Probe succeeded against a closed port")
	}
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("Probe error = %v, want ErrUnreachable", err)
	}
}

// Mount failures need different messages because they need different fixes.
func TestClassifyMountError(t *testing.T) {
	cases := []struct {
		stderr string
		want   error
	}{
		{"mount.nfs4: Connection timed out", ErrUnreachable},
		{"mount.nfs4: No route to host", ErrUnreachable},
		{"mount.nfs4: Connection refused", ErrUnreachable},
		{"mount.nfs4: mounting 10.132.1.25:/volume2/nope failed, reason given by server: No such file or directory", ErrExportMissing},
		{"mount.nfs4: access denied by server while mounting", ErrPermission},
		{"mount.nfs4: Operation not permitted", ErrPermission},
	}
	for _, tc := range cases {
		got := ClassifyMountError(tc.stderr, 32)
		if !errors.Is(got, tc.want) {
			t.Errorf("ClassifyMountError(%q) = %v, want %v", tc.stderr, got, tc.want)
		}
		if got.Error() == "" {
			t.Errorf("ClassifyMountError(%q) produced an empty message", tc.stderr)
		}
	}
}

func TestClassifyMountErrorKeepsUnknownStderr(t *testing.T) {
	err := ClassifyMountError("mount.nfs4: something entirely new", 32)
	if err == nil {
		t.Fatal("want an error")
	}
	if got := err.Error(); got == "" || !contains(got, "something entirely new") {
		t.Errorf("unknown stderr was discarded: %q", got)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(needle) == 0 ||
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/nas/ -v`
Expected: FAIL, `undefined: NewNFS`

- [ ] **Step 3: Write the implementation**

Create `internal/nas/nfs.go`:

```go
package nas

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/adamcarlile/flashcart/internal/config"
)

// Mount failure categories. They are distinguished because each needs a
// different fix from the person reading the message.
var (
	ErrUnreachable   = errors.New("NAS unreachable")
	ErrExportMissing = errors.New("export not found on the NAS")
	ErrPermission    = errors.New("permission denied by the NAS")
)

// probeTimeout keeps the status endpoint fast enough to load in a car with
// no network attached.
const probeTimeout = time.Second

// NFS mounts the three exports for the duration of a run and unmounts them
// afterwards. Nothing at boot depends on the NAS existing.
type NFS struct {
	cfg *config.Config
	// mountOpts are deliberately soft rather than hard: a NAS that vanishes
	// mid-run must produce a readable error, not a wedged process.
	mountOpts string
}

// NewNFS returns a Provider backed by real NFS mounts.
func NewNFS(cfg *config.Config) *NFS {
	return &NFS{
		cfg:       cfg,
		mountOpts: "soft,timeo=50,retrans=2,proto=tcp,vers=4.0",
	}
}

var _ Provider = (*NFS)(nil)

// Probe dials the NFS port. It mounts nothing, so it is safe and fast to
// call on every page load.
func (n *NFS) Probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	addr := net.JoinHostPort(n.cfg.NAS.Host, strconv.Itoa(n.cfg.NAS.Port))
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrUnreachable, addr, err)
	}
	return conn.Close()
}

type mountSpec struct {
	name     string
	export   string
	readOnly bool
}

// Mount mounts all three exports and returns an unmount function that must
// always be called, including on failure. BIOS is read-only because nothing
// on the box ever writes to it.
func (n *NFS) Mount(ctx context.Context) (Mounts, func() error, error) {
	specs := []mountSpec{
		{"roms", n.cfg.Trees.Roms.Export, false},
		{"bios", n.cfg.Trees.Bios.Export, true},
		{"saves", n.cfg.Trees.Saves.Export, false},
	}

	var mounted []string
	unmount := func() error {
		var firstErr error
		// Unmount in reverse order.
		for i := len(mounted) - 1; i >= 0; i-- {
			if err := exec.Command("umount", mounted[i]).Run(); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("umount %s: %w", mounted[i], err)
			}
		}
		return firstErr
	}

	var m Mounts
	for _, s := range specs {
		target := filepath.Join(n.cfg.NAS.MountRoot, s.name)
		if err := os.MkdirAll(target, 0o755); err != nil {
			unmount()
			return Mounts{}, func() error { return nil }, fmt.Errorf("create mount point %s: %w", target, err)
		}

		opts := n.mountOpts
		if s.readOnly {
			opts = "ro," + opts
		}
		src := n.cfg.NAS.Host + ":" + s.export

		out, err := exec.CommandContext(ctx, "mount", "-t", "nfs4", "-o", opts, src, target).CombinedOutput()
		if err != nil {
			unmount()
			exit := 0
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				exit = ee.ExitCode()
			}
			return Mounts{}, func() error { return nil }, fmt.Errorf("mount %s: %w", s.name, ClassifyMountError(string(out), exit))
		}
		mounted = append(mounted, target)

		switch s.name {
		case "roms":
			m.Roms = target
		case "bios":
			m.Bios = target
		case "saves":
			m.Saves = target
		}
	}

	return m, unmount, nil
}

// ClassifyMountError maps mount.nfs4 stderr onto a category, preserving the
// original text so an unrecognised failure is still actionable.
func ClassifyMountError(stderr string, exit int) error {
	s := strings.ToLower(stderr)
	trimmed := strings.TrimSpace(stderr)

	switch {
	case strings.Contains(s, "timed out"),
		strings.Contains(s, "no route to host"),
		strings.Contains(s, "connection refused"),
		strings.Contains(s, "network is unreachable"):
		return fmt.Errorf("%w: %s", ErrUnreachable, trimmed)
	case strings.Contains(s, "no such file or directory"),
		strings.Contains(s, "does not exist"):
		return fmt.Errorf("%w: %s", ErrExportMissing, trimmed)
	case strings.Contains(s, "access denied"),
		strings.Contains(s, "permission denied"),
		strings.Contains(s, "operation not permitted"):
		return fmt.Errorf("%w: %s", ErrPermission, trimmed)
	}
	return fmt.Errorf("mount failed (exit %d): %s", exit, trimmed)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/nas/ -v && go vet ./internal/nas/`
Expected: PASS, vet clean

- [ ] **Step 5: Commit**

```bash
git add internal/nas/
git commit -m "Add NFS probe and on-demand mount lifecycle

Mounts are soft rather than hard so an absent NAS fails readably instead
of wedging, and exist only for the duration of a run."
```

---

### Task 8: Plan orchestration, projected-state drift and the space precheck

The subtle part. On a first run the local tree is empty, so pass 4's dry run would report every gamelist and all scraped media on the NAS as drift, because pass 3's pull has not actually happened. Drift for pass N must be computed against the state projected after passes 1 to N-1.

**Files:**
- Create: `internal/plan/plan.go`, `internal/plan/plan_test.go`, `internal/plan/freespace_linux.go`, `internal/plan/freespace_other.go`

**Interfaces:**
- Consumes: `config.Config` (Task 1), `pass.Pass`, `pass.Passes` (Task 4), `runner.Runner`, `runner.Result` (Task 5)
- Produces: `plan.DriftItem{Tree, Side, Rel string}`, `plan.PassSummary{ID, Label, Direction string, Files int, Bytes int64}`, `plan.TreePlan{Tree, Label string, IncomingFiles int, IncomingBytes int64, OutgoingFiles int, OutgoingBytes int64, Passes []PassSummary, Drift []DriftItem}`, `plan.Plan{Trees []TreePlan, RequiredBytes, FreeBytes, TotalBytes int64, Sufficient bool, Message string}`, `plan.FreeSpaceFunc func(path string) (free, total int64, err error)`, `func plan.Build(ctx context.Context, cfg *config.Config, r runner.Runner, ps []pass.Pass, free plan.FreeSpaceFunc) (plan.Plan, error)`, `func plan.FreeSpace(path string) (int64, int64, error)`, `const plan.SideLocal`, `const plan.SideNAS`

- [ ] **Step 1: Write the failing test**

Create `internal/plan/plan_test.go`:

```go
package plan

import (
	"context"
	"testing"

	"github.com/adamcarlile/flashcart/internal/config"
	"github.com/adamcarlile/flashcart/internal/nas"
	"github.com/adamcarlile/flashcart/internal/pass"
	"github.com/adamcarlile/flashcart/internal/runner"
)

// stubRunner returns canned dry-run results keyed by pass ID.
type stubRunner struct {
	results map[string]runner.Result
}

func (s stubRunner) DryRun(_ context.Context, p pass.Pass) (runner.Result, error) {
	r := s.results[p.ID]
	r.PassID = p.ID
	return r, nil
}

func (s stubRunner) Run(_ context.Context, p pass.Pass, _ chan<- runner.Event) (runner.Result, error) {
	return runner.Result{PassID: p.ID}, nil
}

func testCfg() *config.Config {
	return &config.Config{
		NAS: config.NAS{Host: "nas", Port: 2049, MountRoot: "/mnt"},
		Trees: config.Trees{
			Roms:  config.Tree{Export: "/e/roms", Local: "/userdata/roms"},
			Bios:  config.Tree{Export: "/e/bios", Local: "/userdata/bios"},
			Saves: config.Tree{Export: "/e/saves", Local: "/userdata/saves"},
		},
		SpaceMarginPercent: 10,
	}
}

func testPasses() []pass.Pass {
	return pass.Passes(testCfg(), nas.Mounts{Roms: "/mnt/roms", Bios: "/mnt/bios", Saves: "/mnt/saves"})
}

func plentyOfSpace(string) (int64, int64, error) { return 400 << 30, 459 << 30, nil }

// The behaviour that makes a first run legible instead of alarming.
func TestSeedRunReportsNoDrift(t *testing.T) {
	// Local is empty. The metadata pull would create both files; the
	// metadata push therefore sees them as absent from its source and would
	// naively call them drift.
	r := stubRunner{results: map[string]runner.Result{
		"roms-metadata-pull": {
			Changes: []runner.Change{
				{Itemize: ">f+++++++++", Size: 502062, Path: "snes/gamelist.xml"},
				{Itemize: ">f+++++++++", Size: 181000, Path: "snes/images/ActRaiser (USA)-image.png"},
			},
			TransferBytes: 683062,
		},
		"roms-metadata-push": {
			Deletions: []string{"snes/gamelist.xml", "snes/images/ActRaiser (USA)-image.png"},
		},
	}}

	p, err := Build(context.Background(), testCfg(), r, testPasses(), plentyOfSpace)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, tp := range p.Trees {
		if len(tp.Drift) != 0 {
			t.Errorf("tree %s reported drift on a seed run: %+v", tp.Tree, tp.Drift)
		}
	}
}

// Genuine drift, not covered by any earlier pass, must still be reported.
func TestGenuineDriftIsReported(t *testing.T) {
	r := stubRunner{results: map[string]runner.Result{
		"roms-metadata-pull": {
			Changes: []runner.Change{{Itemize: ">f+++++++++", Size: 10, Path: "snes/gamelist.xml"}},
		},
		"roms-metadata-push": {
			Deletions: []string{"snes/gamelist.xml", "megadrive/gamelist.xml"},
		},
		"saves-push": {
			Deletions: []string{"snes/OldGame.srm"},
		},
	}}

	p, err := Build(context.Background(), testCfg(), r, testPasses(), plentyOfSpace)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	byTree := map[string]TreePlan{}
	for _, tp := range p.Trees {
		byTree[tp.Tree] = tp
	}

	romsDrift := byTree["roms"].Drift
	if len(romsDrift) != 1 {
		t.Fatalf("roms drift = %+v, want exactly megadrive/gamelist.xml", romsDrift)
	}
	if romsDrift[0].Rel != "megadrive/gamelist.xml" {
		t.Errorf("roms drift Rel = %q", romsDrift[0].Rel)
	}
	// A push pass writes to the NAS, so its drift lives on the NAS side.
	if romsDrift[0].Side != SideNAS {
		t.Errorf("roms drift Side = %q, want %q", romsDrift[0].Side, SideNAS)
	}

	savesDrift := byTree["saves"].Drift
	if len(savesDrift) != 1 || savesDrift[0].Side != SideNAS {
		t.Errorf("saves drift = %+v", savesDrift)
	}
}

// A pull pass deletes on the box, so its drift is local.
func TestPullDriftIsLocal(t *testing.T) {
	r := stubRunner{results: map[string]runner.Result{
		"bios-pull": {Deletions: []string{"stray.bin"}},
	}}
	p, err := Build(context.Background(), testCfg(), r, testPasses(), plentyOfSpace)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, tp := range p.Trees {
		if tp.Tree != "bios" {
			continue
		}
		if len(tp.Drift) != 1 || tp.Drift[0].Side != SideLocal {
			t.Fatalf("bios drift = %+v, want one local item", tp.Drift)
		}
	}
}

func TestIncomingAndOutgoingBytesAreSeparated(t *testing.T) {
	r := stubRunner{results: map[string]runner.Result{
		"roms-content-pull": {
			Changes:       []runner.Change{{Itemize: ">f+++++++++", Size: 1000, Path: "snes/New.zip"}},
			TransferBytes: 1000,
		},
		"saves-push": {
			Changes:       []runner.Change{{Itemize: ">f+++++++++", Size: 55, Path: "snes/New.srm"}},
			TransferBytes: 55,
		},
	}}
	p, err := Build(context.Background(), testCfg(), r, testPasses(), plentyOfSpace)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	byTree := map[string]TreePlan{}
	for _, tp := range p.Trees {
		byTree[tp.Tree] = tp
	}
	if byTree["roms"].IncomingBytes != 1000 || byTree["roms"].OutgoingBytes != 0 {
		t.Errorf("roms bytes in=%d out=%d", byTree["roms"].IncomingBytes, byTree["roms"].OutgoingBytes)
	}
	if byTree["saves"].OutgoingBytes != 55 || byTree["saves"].IncomingBytes != 0 {
		t.Errorf("saves bytes in=%d out=%d", byTree["saves"].IncomingBytes, byTree["saves"].OutgoingBytes)
	}
	// Only incoming bytes consume local disk.
	if p.RequiredBytes != 1000 {
		t.Errorf("RequiredBytes = %d, want 1000", p.RequiredBytes)
	}
}

// The UI renders one row per pass, so each tree must carry its passes with
// their own direction and figures rather than a single netted-out total.
func TestTreeCarriesPerPassBreakdown(t *testing.T) {
	r := stubRunner{results: map[string]runner.Result{
		"roms-content-pull": {
			Changes:       []runner.Change{{Itemize: ">f+++++++++", Size: 1000, Path: "snes/New.zip"}},
			TransferBytes: 1000,
		},
		"roms-metadata-push": {
			Changes:       []runner.Change{{Itemize: ">f.st......", Size: 40, Path: "snes/gamelist.xml"}},
			TransferBytes: 40,
		},
	}}
	p, err := Build(context.Background(), testCfg(), r, testPasses(), plentyOfSpace)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, tp := range p.Trees {
		if tp.Tree != "roms" {
			continue
		}
		if len(tp.Passes) != 3 {
			t.Fatalf("roms carries %d passes, want 3: %+v", len(tp.Passes), tp.Passes)
		}
		byID := map[string]PassSummary{}
		for _, ps := range tp.Passes {
			byID[ps.ID] = ps
		}
		if got := byID["roms-content-pull"]; got.Direction != "in" || got.Bytes != 1000 || got.Files != 1 {
			t.Errorf("content pull summary = %+v", got)
		}
		if got := byID["roms-metadata-push"]; got.Direction != "out" || got.Bytes != 40 {
			t.Errorf("metadata push summary = %+v", got)
		}
		if byID["roms-metadata-pull"].Label == "" {
			t.Error("the metadata seed pass has no label to render")
		}
	}
}

func TestInsufficientSpaceIsRefused(t *testing.T) {
	r := stubRunner{results: map[string]runner.Result{
		"roms-content-pull": {TransferBytes: 100 << 30}, // 100 GiB incoming
	}}
	tight := func(string) (int64, int64, error) {
		return 105 << 30, 459 << 30, nil // 105 GiB free of 459 GiB
	}
	p, err := Build(context.Background(), testCfg(), r, testPasses(), tight)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.Sufficient {
		t.Error("Sufficient = true, but the transfer would leave under the 10% margin")
	}
	if p.Message == "" {
		t.Error("an insufficient plan must carry an explanatory message")
	}
}

func TestSufficientSpaceIsAccepted(t *testing.T) {
	r := stubRunner{results: map[string]runner.Result{
		"roms-content-pull": {TransferBytes: 93 << 30},
	}}
	p, err := Build(context.Background(), testCfg(), r, testPasses(), plentyOfSpace)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !p.Sufficient {
		t.Errorf("Sufficient = false with 400 GiB free and 93 GiB needed: %s", p.Message)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plan/ -v`
Expected: FAIL, `undefined: Build`

- [ ] **Step 3: Write the implementation**

Create `internal/plan/plan.go`:

```go
// Package plan turns dry runs of the five passes into a reviewable summary:
// what would move, in which direction, and what would be left behind.
package plan

import (
	"context"
	"fmt"

	"github.com/adamcarlile/flashcart/internal/config"
	"github.com/adamcarlile/flashcart/internal/pass"
	"github.com/adamcarlile/flashcart/internal/runner"
)

// Which side of the sync a drift item sits on. Paths are stored relative
// because NAS mounts are transient: the absolute path is only meaningful
// while a mount is held.
const (
	SideLocal = "local"
	SideNAS   = "nas"
)

// DriftItem is a path present on a destination but absent from its source.
type DriftItem struct {
	Tree string `json:"tree"`
	Side string `json:"side"`
	Rel  string `json:"rel"`
}

// PassSummary is one pass's contribution to a tree. The UI renders each as
// its own labelled row, so the ROMs tree visibly pulls content and pushes
// metadata rather than showing a single netted-out figure.
type PassSummary struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Direction string `json:"direction"` // "in" or "out"
	Files     int    `json:"files"`
	Bytes     int64  `json:"bytes"`
}

// TreePlan summarises one tree.
type TreePlan struct {
	Tree          string        `json:"tree"`
	Label         string        `json:"label"`
	IncomingFiles int           `json:"incomingFiles"`
	IncomingBytes int64         `json:"incomingBytes"`
	OutgoingFiles int           `json:"outgoingFiles"`
	OutgoingBytes int64         `json:"outgoingBytes"`
	Passes        []PassSummary `json:"passes"`
	Drift         []DriftItem   `json:"drift"`
}

// Plan is the whole reviewable summary.
type Plan struct {
	Trees         []TreePlan `json:"trees"`
	RequiredBytes int64      `json:"requiredBytes"`
	FreeBytes     int64      `json:"freeBytes"`
	TotalBytes    int64      `json:"totalBytes"`
	Sufficient    bool       `json:"sufficient"`
	Message       string     `json:"message"`
}

// FreeSpaceFunc reports free and total bytes for the filesystem holding a
// path. Injected so the space precheck is testable without a real disk.
type FreeSpaceFunc func(path string) (free, total int64, err error)

var treeLabels = map[string]string{
	"roms":  "ROMs",
	"bios":  "BIOS",
	"saves": "Saves",
}

// Build dry-runs every pass in order and assembles the summary.
//
// Drift is computed against projected state rather than current state. A dry
// run copies nothing, so on a first run the metadata push would see the whole
// NAS metadata set as absent from its empty local source and report all of it
// as drift. Paths an earlier pass would create at this pass's source are
// therefore subtracted before drift is reported.
func Build(ctx context.Context, cfg *config.Config, r runner.Runner, ps []pass.Pass, free FreeSpaceFunc) (Plan, error) {
	// Keyed by destination directory: the set of relative paths that
	// earlier passes would create there.
	projected := map[string]map[string]bool{}

	trees := map[string]*TreePlan{}
	order := []string{}

	for _, p := range ps {
		res, err := r.DryRun(ctx, p)
		if err != nil {
			return Plan{}, fmt.Errorf("plan %s: %w", p.ID, err)
		}

		tp, ok := trees[p.Tree]
		if !ok {
			tp = &TreePlan{Tree: p.Tree, Label: treeLabels[p.Tree]}
			trees[p.Tree] = tp
			order = append(order, p.Tree)
		}

		files := 0
		for _, c := range res.Changes {
			if len(c.Itemize) > 1 && c.Itemize[1] == 'f' {
				files++
			}
		}
		dir := "in"
		if p.Direction == pass.DirPull {
			tp.IncomingFiles += files
			tp.IncomingBytes += res.TransferBytes
		} else {
			dir = "out"
			tp.OutgoingFiles += files
			tp.OutgoingBytes += res.TransferBytes
		}
		tp.Passes = append(tp.Passes, PassSummary{
			ID: p.ID, Label: p.Label, Direction: dir,
			Files: files, Bytes: res.TransferBytes,
		})

		side := SideLocal
		if p.Direction == pass.DirPush {
			side = SideNAS
		}
		alreadyPlanned := projected[p.Src]
		for _, rel := range res.Deletions {
			if alreadyPlanned[rel] {
				continue
			}
			tp.Drift = append(tp.Drift, DriftItem{Tree: p.Tree, Side: side, Rel: rel})
		}

		// Record what this pass would create at its destination, so later
		// passes reading from that destination see the projected state.
		if projected[p.Dst] == nil {
			projected[p.Dst] = map[string]bool{}
		}
		for _, c := range res.Changes {
			projected[p.Dst][c.Path] = true
		}
	}

	out := Plan{}
	for _, name := range order {
		tp := trees[name]
		out.RequiredBytes += tp.IncomingBytes
		out.Trees = append(out.Trees, *tp)
	}

	freeBytes, totalBytes, err := free(cfg.Trees.Roms.Local)
	if err != nil {
		return Plan{}, fmt.Errorf("check free space on %s: %w", cfg.Trees.Roms.Local, err)
	}
	out.FreeBytes = freeBytes
	out.TotalBytes = totalBytes

	margin := totalBytes * int64(cfg.SpaceMarginPercent) / 100
	remaining := freeBytes - out.RequiredBytes
	out.Sufficient = remaining >= margin
	if !out.Sufficient {
		out.Message = fmt.Sprintf(
			"Not enough space: %s incoming against %s free would leave %s, below the %d%% margin of %s.",
			humanBytes(out.RequiredBytes), humanBytes(freeBytes), humanBytes(remaining),
			cfg.SpaceMarginPercent, humanBytes(margin),
		)
	}
	return out, nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
```

- [ ] **Step 4: Write the free space implementations**

Create `internal/plan/freespace_linux.go`:

```go
//go:build linux

package plan

import "syscall"

// FreeSpace reports free and total bytes for the filesystem holding path.
func FreeSpace(path string) (int64, int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	// Bavail is space available to an unprivileged process, which is the
	// honest number to plan against.
	return int64(st.Bavail) * int64(st.Bsize), int64(st.Blocks) * int64(st.Bsize), nil
}
```

Create `internal/plan/freespace_other.go`:

```go
//go:build !linux

package plan

import "errors"

// FreeSpace is only implemented on Linux, which is the only platform
// flashcart is deployed to.
func FreeSpace(string) (int64, int64, error) {
	return 0, 0, errors.New("free space checks are only supported on Linux")
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/plan/ -v`
Expected: PASS, including `TestSeedRunReportsNoDrift`

- [ ] **Step 6: Commit**

```bash
git add internal/plan/
git commit -m "Add plan orchestration with projected-state drift

Drift for pass N is computed against the state projected after passes
1..N-1, so a seed run reports no drift instead of flagging the entire
NAS metadata set."
```

---

### Task 9: Sync orchestration

**Files:**
- Create: `internal/syncer/syncer.go`, `internal/syncer/syncer_test.go`

**Interfaces:**
- Consumes: `pass.Pass` (Task 4), `runner.Runner`, `runner.Event` (Task 5)
- Produces: `syncer.PassResult{ID, Label string, OK bool, Err string}`, `syncer.Summary{Passes []PassResult, OK bool, Err string}`, `func syncer.Run(ctx context.Context, r runner.Runner, ps []pass.Pass, events chan<- runner.Event) syncer.Summary`

- [ ] **Step 1: Write the failing test**

Create `internal/syncer/syncer_test.go`:

```go
package syncer

import (
	"context"
	"errors"
	"testing"

	"github.com/adamcarlile/flashcart/internal/config"
	"github.com/adamcarlile/flashcart/internal/nas"
	"github.com/adamcarlile/flashcart/internal/pass"
	"github.com/adamcarlile/flashcart/internal/runner"
)

type scriptRunner struct {
	ran     []string
	failOn  string
	failErr error
}

func (s *scriptRunner) DryRun(context.Context, pass.Pass) (runner.Result, error) {
	return runner.Result{}, nil
}

func (s *scriptRunner) Run(_ context.Context, p pass.Pass, events chan<- runner.Event) (runner.Result, error) {
	s.ran = append(s.ran, p.ID)
	events <- runner.Event{PassID: p.ID, Percent: 100}
	if p.ID == s.failOn {
		return runner.Result{PassID: p.ID}, s.failErr
	}
	return runner.Result{PassID: p.ID}, nil
}

func testPasses() []pass.Pass {
	cfg := &config.Config{Trees: config.Trees{
		Roms:  config.Tree{Export: "/e/roms", Local: "/l/roms"},
		Bios:  config.Tree{Export: "/e/bios", Local: "/l/bios"},
		Saves: config.Tree{Export: "/e/saves", Local: "/l/saves"},
	}}
	return pass.Passes(cfg, nas.Mounts{Roms: "/m/roms", Bios: "/m/bios", Saves: "/m/saves"})
}

func drain(events chan runner.Event) {
	go func() {
		for range events {
		}
	}()
}

func TestRunExecutesAllPassesInOrder(t *testing.T) {
	r := &scriptRunner{}
	events := make(chan runner.Event, 64)
	drain(events)

	sum := Run(context.Background(), r, testPasses(), events)

	if !sum.OK {
		t.Fatalf("Summary.OK = false: %s", sum.Err)
	}
	want := []string{"bios-pull", "roms-content-pull", "roms-metadata-pull", "roms-metadata-push", "saves-push"}
	if len(r.ran) != len(want) {
		t.Fatalf("ran %v, want %v", r.ran, want)
	}
	for i := range want {
		if r.ran[i] != want[i] {
			t.Fatalf("ran %v, want %v", r.ran, want)
		}
	}
	if len(sum.Passes) != len(want) {
		t.Errorf("len(Summary.Passes) = %d, want %d", len(sum.Passes), len(want))
	}
}

// A failing pass abandons the remainder rather than pressing on with a
// half-synced tree.
func TestFailureAbandonsRemainingPasses(t *testing.T) {
	r := &scriptRunner{failOn: "roms-content-pull", failErr: errors.New("disk full")}
	events := make(chan runner.Event, 64)
	drain(events)

	sum := Run(context.Background(), r, testPasses(), events)

	if sum.OK {
		t.Fatal("Summary.OK = true after a pass failed")
	}
	if len(r.ran) != 2 {
		t.Errorf("ran %v, want to stop after roms-content-pull", r.ran)
	}
	if sum.Err == "" {
		t.Error("Summary.Err is empty after a failure")
	}
	if got := sum.Passes[1]; got.OK || got.Err == "" {
		t.Errorf("failing pass recorded as %+v", got)
	}
	if got := sum.Passes[0]; !got.OK {
		t.Errorf("earlier successful pass recorded as %+v", got)
	}
}

func TestCancellationStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r := &scriptRunner{}
	events := make(chan runner.Event, 64)
	drain(events)

	sum := Run(ctx, r, testPasses(), events)
	if sum.OK {
		t.Error("Summary.OK = true despite a cancelled context")
	}
	if len(r.ran) != 0 {
		t.Errorf("ran %v after cancellation", r.ran)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/syncer/ -v`
Expected: FAIL, `undefined: Run`

- [ ] **Step 3: Write the implementation**

Create `internal/syncer/syncer.go`:

```go
// Package syncer runs the five passes in order for real, emitting progress
// and stopping at the first failure.
package syncer

import (
	"context"

	"github.com/adamcarlile/flashcart/internal/pass"
	"github.com/adamcarlile/flashcart/internal/runner"
)

// PassResult records the outcome of one pass.
type PassResult struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	OK    bool   `json:"ok"`
	Err   string `json:"err,omitempty"`
}

// Summary is the outcome of a whole run.
type Summary struct {
	Passes []PassResult `json:"passes"`
	OK     bool         `json:"ok"`
	Err    string       `json:"err,omitempty"`
}

// Run executes every pass in order. A failure abandons the remaining passes:
// continuing would leave the trees in a state nobody reasoned about, and the
// caller unmounts either way.
func Run(ctx context.Context, r runner.Runner, ps []pass.Pass, events chan<- runner.Event) Summary {
	var sum Summary
	for _, p := range ps {
		if err := ctx.Err(); err != nil {
			sum.Err = err.Error()
			return sum
		}
		_, err := r.Run(ctx, p, events)
		pr := PassResult{ID: p.ID, Label: p.Label, OK: err == nil}
		if err != nil {
			pr.Err = err.Error()
			sum.Passes = append(sum.Passes, pr)
			sum.Err = err.Error()
			return sum
		}
		sum.Passes = append(sum.Passes, pr)
	}
	sum.OK = true
	return sum
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/syncer/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/syncer/
git commit -m "Add sync orchestration with fail-fast pass ordering"
```

---

### Task 10: Drift deletion

The most dangerous code in the project. It deletes files the user can never get back. Every safeguard here is deliberate: no rsync, no filters, no globs, and a containment check that rejects any path escaping its tree root.

**Files:**
- Create: `internal/drift/drift.go`, `internal/drift/drift_test.go`

**Interfaces:**
- Consumes: `plan.DriftItem`, `plan.SideLocal`, `plan.SideNAS` (Task 8), `config.Config` (Task 1), `nas.Mounts` (Task 4)
- Produces: `drift.Roots{Local, NAS map[string]string}`, `func drift.RootsFor(cfg *config.Config, m nas.Mounts) drift.Roots`, `func drift.Resolve(roots drift.Roots, item plan.DriftItem) (string, error)`, `func drift.Delete(roots drift.Roots, items []plan.DriftItem) ([]string, error)`

- [ ] **Step 1: Write the failing test**

Create `internal/drift/drift_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/drift/ -v`
Expected: FAIL, `undefined: RootsFor`

- [ ] **Step 3: Write the implementation**

Create `internal/drift/drift.go`:

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/drift/ -v`
Expected: PASS, every escape case refused

- [ ] **Step 5: Commit**

```bash
git add internal/drift/
git commit -m "Add drift deletion with batch validation and root containment

Deletes explicitly named paths via os.RemoveAll, never rsync --delete.
The whole batch is validated before anything is removed."
```

---

### Task 11: Fake backend

One type implements both seams, so `server`, `plan`, `syncer` and `drift` are the identical code in both modes. Fake mode exercises real logic; only the far side of the seam is scripted.

**Files:**
- Create: `internal/fake/fake.go`, `internal/fake/library.go`, `internal/fake/fake_test.go`

**Interfaces:**
- Consumes: `nas.Provider`, `nas.Mounts` (Task 4), `runner.Runner`, `runner.Result`, `runner.Change`, `runner.Event` (Task 5), `pass.Pass` (Task 4)
- Produces: `fake.Scenario` string type with constants `fake.ScenarioSeed`, `fake.ScenarioSteady`, `fake.ScenarioDrift`, `fake.ScenarioOffline`, `fake.ScenarioNoSpace`, `fake.ScenarioFailure`; `fake.Scenarios []Scenario`; `func fake.New(s Scenario) (*fake.Backend, error)`; `*fake.Backend` implementing `nas.Provider` and `runner.Runner`; `func (*Backend) Scenario() Scenario`; `func (*Backend) SetScenario(Scenario) error`; `func (*Backend) FreeSpace(string) (int64, int64, error)`

- [ ] **Step 1: Write the fixture library**

Create `internal/fake/library.go`. Figures are the real ones measured on the box on 2026-08-20, so the UI is laid out against a realistic distribution rather than three toy rows.

```go
package fake

import (
	"fmt"

	"github.com/adamcarlile/flashcart/internal/runner"
)

const (
	kib = 1 << 10
	mib = 1 << 20
	gib = 1 << 30
)

// system describes one ROM system as measured on the real box.
type system struct {
	name  string
	games int
	bytes int64
	media int // scraped media files
}

// library approximates the real collection: 91.6 GB across 232 system
// directories, of which nine hold 99% of the bytes.
var library = []system{
	{"ps3", 6, 34 * gib, 12},
	{"ps2", 11, 20*gib + 800*mib, 22},
	{"xbox", 9, 9 * gib, 18},
	{"xbox360", 3, 7*gib + 300*mib, 6},
	{"gamecube", 14, 6*gib + 200*mib, 28},
	{"psx", 10, 4*gib + 300*mib, 20},
	{"wii", 7, 4*gib + 100*mib, 14},
	{"neogeo", 144, 2*gib + 400*mib, 288},
	{"dreamcast", 8, 2*gib + 200*mib, 16},
	{"snes", 336, 536 * mib, 1008},
	{"n64", 16, 254 * mib, 32},
	{"megadrive", 156, 231 * mib, 312},
	{"nes", 278, 200 * mib, 556},
	{"genesis", 24, 129 * mib, 48},
	{"prboom", 3, 12 * mib, 6},
}

// systemsWithGamelists matches the 24 populated systems on the real box.
var systemsWithGamelists = []string{
	"c64", "cannonball", "devilutionx", "dreamcast", "gamecube", "megadrive",
	"mrboom", "n64", "neogeo", "nes", "odcommander", "ports", "prboom",
	"ps2", "ps3", "psx", "pygame", "sdlpop", "snes", "steam", "wii",
	"xash3d_fwgs", "xbox", "xbox360",
}

// content synthesises the ROM binaries for the whole library.
func content() []runner.Change {
	var out []runner.Change
	for _, s := range library {
		if s.games == 0 {
			continue
		}
		per := s.bytes / int64(s.games)
		for i := 0; i < s.games; i++ {
			out = append(out, runner.Change{
				Itemize: ">f+++++++++",
				Size:    per,
				Path:    fmt.Sprintf("%s/Game %03d (USA).zip", s.name, i+1),
			})
		}
	}
	return out
}

// metadata synthesises gamelists and scraped media.
func metadata() []runner.Change {
	var out []runner.Change
	for _, name := range systemsWithGamelists {
		out = append(out, runner.Change{
			Itemize: ">f+++++++++",
			Size:    64 * kib,
			Path:    name + "/gamelist.xml",
		})
	}
	for _, s := range library {
		for i := 0; i < s.media; i++ {
			out = append(out, runner.Change{
				Itemize: ">f+++++++++",
				Size:    180 * kib,
				Path:    fmt.Sprintf("%s/images/Game %03d (USA)-image.png", s.name, i+1),
			})
		}
	}
	return out
}

func bytesOf(cs []runner.Change) int64 {
	var n int64
	for _, c := range cs {
		n += c.Size
	}
	return n
}

func result(cs []runner.Change, deletions ...string) runner.Result {
	return runner.Result{Changes: cs, TransferBytes: bytesOf(cs), Deletions: deletions}
}
```

- [ ] **Step 2: Write the failing test**

Create `internal/fake/fake_test.go`:

```go
package fake

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/adamcarlile/flashcart/internal/config"
	"github.com/adamcarlile/flashcart/internal/nas"
	"github.com/adamcarlile/flashcart/internal/pass"
	"github.com/adamcarlile/flashcart/internal/plan"
	"github.com/adamcarlile/flashcart/internal/runner"
)

func cfg() *config.Config {
	return &config.Config{
		NAS: config.NAS{Host: "fake", Port: 2049, MountRoot: "/mnt"},
		Trees: config.Trees{
			Roms:  config.Tree{Export: "/e/roms", Local: "/userdata/roms"},
			Bios:  config.Tree{Export: "/e/bios", Local: "/userdata/bios"},
			Saves: config.Tree{Export: "/e/saves", Local: "/userdata/saves"},
		},
		SpaceMarginPercent: 10,
	}
}

func mustNew(t *testing.T, s Scenario) *Backend {
	t.Helper()
	b, err := New(s)
	if err != nil {
		t.Fatal(err)
	}
	b.Delay = 0
	return b
}

func TestSatisfiesBothSeams(t *testing.T) {
	b := mustNew(t, ScenarioSteady)
	var _ nas.Provider = b
	var _ runner.Runner = b
}

func TestNewRejectsUnknownScenario(t *testing.T) {
	if _, err := New("nonsense"); err == nil {
		t.Fatal("New accepted an unknown scenario")
	}
}

func TestSetScenarioRejectsUnknown(t *testing.T) {
	b := mustNew(t, ScenarioSteady)
	if err := b.SetScenario("nonsense"); err == nil {
		t.Fatal("SetScenario accepted an unknown scenario")
	}
	if b.Scenario() != ScenarioSteady {
		t.Error("a rejected scenario still changed state")
	}
}

// If the fake provider were ever wired to real rsync by mistake, the paths
// it hands out must not exist, so the mistake fails loudly rather than
// touching real data.
func TestMountPathsDoNotExist(t *testing.T) {
	b := mustNew(t, ScenarioSteady)
	m, unmount, err := b.Mount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unmount()
	for _, p := range []string{m.Roms, m.Bios, m.Saves} {
		if !strings.Contains(p, "flashcart-fake") {
			t.Errorf("mount path %q is not obviously fake", p)
		}
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("fake mount path %q exists on disk", p)
		}
	}
}

func TestOfflineScenarioFailsProbeAndMount(t *testing.T) {
	b := mustNew(t, ScenarioOffline)
	if err := b.Probe(context.Background()); err == nil {
		t.Error("Probe succeeded in the offline scenario")
	}
	if _, _, err := b.Mount(context.Background()); err == nil {
		t.Error("Mount succeeded in the offline scenario")
	}
}

// The scenario that matters most: a first run must show a large incoming
// transfer and no drift at all.
func TestSeedScenarioPlansLargeIncomingAndNoDrift(t *testing.T) {
	b := mustNew(t, ScenarioSeed)
	ps := pass.Passes(cfg(), nas.Mounts{Roms: "/m/roms", Bios: "/m/bios", Saves: "/m/saves"})

	p, err := plan.Build(context.Background(), cfg(), b, ps, b.FreeSpace)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.RequiredBytes < 80*gib {
		t.Errorf("RequiredBytes = %d, want a realistic seed of at least 80 GiB", p.RequiredBytes)
	}
	if !p.Sufficient {
		t.Errorf("seed scenario should fit: %s", p.Message)
	}
	for _, tp := range p.Trees {
		if len(tp.Drift) != 0 {
			t.Errorf("seed scenario reported drift on %s: %+v", tp.Tree, tp.Drift)
		}
	}
}

func TestDriftScenarioReportsBothSides(t *testing.T) {
	b := mustNew(t, ScenarioDrift)
	ps := pass.Passes(cfg(), nas.Mounts{Roms: "/m/roms", Bios: "/m/bios", Saves: "/m/saves"})

	p, err := plan.Build(context.Background(), cfg(), b, ps, b.FreeSpace)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	sides := map[string]bool{}
	for _, tp := range p.Trees {
		for _, d := range tp.Drift {
			sides[d.Side] = true
		}
	}
	if !sides[plan.SideLocal] || !sides[plan.SideNAS] {
		t.Errorf("drift scenario produced sides %v, want both local and nas", sides)
	}
}

func TestNoSpaceScenarioIsRefused(t *testing.T) {
	b := mustNew(t, ScenarioNoSpace)
	ps := pass.Passes(cfg(), nas.Mounts{Roms: "/m/roms", Bios: "/m/bios", Saves: "/m/saves"})

	p, err := plan.Build(context.Background(), cfg(), b, ps, b.FreeSpace)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.Sufficient {
		t.Error("nospace scenario reported sufficient space")
	}
	if p.Message == "" {
		t.Error("nospace scenario produced no message")
	}
}

func TestFailureScenarioFailsMidRun(t *testing.T) {
	b := mustNew(t, ScenarioFailure)
	ps := pass.Passes(cfg(), nas.Mounts{Roms: "/m/roms", Bios: "/m/bios", Saves: "/m/saves"})

	events := make(chan runner.Event, 4096)
	go func() {
		for range events {
		}
	}()

	var failed string
	for _, p := range ps {
		if _, err := b.Run(context.Background(), p, events); err != nil {
			failed = p.ID
			break
		}
	}
	if failed != "roms-content-pull" {
		t.Errorf("failure scenario failed on %q, want roms-content-pull", failed)
	}
}

func TestRunEmitsProgress(t *testing.T) {
	b := mustNew(t, ScenarioSteady)
	ps := pass.Passes(cfg(), nas.Mounts{Roms: "/m/roms", Bios: "/m/bios", Saves: "/m/saves"})

	events := make(chan runner.Event, 4096)
	if _, err := b.Run(context.Background(), ps[0], events); err != nil {
		t.Fatal(err)
	}
	close(events)

	var last int
	n := 0
	for e := range events {
		if e.Percent < last {
			t.Errorf("progress went backwards: %d then %d", last, e.Percent)
		}
		last = e.Percent
		n++
	}
	if n < 2 {
		t.Errorf("only %d progress events emitted", n)
	}
	if last != 100 {
		t.Errorf("final progress = %d, want 100", last)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/fake/ -v`
Expected: FAIL, `undefined: New`

- [ ] **Step 4: Write the implementation**

Create `internal/fake/fake.go`:

```go
// Package fake provides a scripted backend satisfying both nas.Provider and
// runner.Runner, so the whole application can be driven with no NAS, no
// Batocera box and no data.
//
// Nothing above the seam knows which implementation it has, so fake mode
// exercises the real server, plan, syncer and drift code.
package fake

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/adamcarlile/flashcart/internal/nas"
	"github.com/adamcarlile/flashcart/internal/pass"
	"github.com/adamcarlile/flashcart/internal/runner"
)

// Scenario names a scripted state of the world.
type Scenario string

const (
	// ScenarioSeed is an empty box against a full NAS: the first run.
	ScenarioSeed Scenario = "seed"
	// ScenarioSteady is a small realistic delta.
	ScenarioSteady Scenario = "steady"
	// ScenarioDrift has removals pending on both sides.
	ScenarioDrift Scenario = "drift"
	// ScenarioOffline has an unreachable NAS.
	ScenarioOffline Scenario = "offline"
	// ScenarioNoSpace has a transfer that will not fit.
	ScenarioNoSpace Scenario = "nospace"
	// ScenarioFailure fails partway through the ROM content pull.
	ScenarioFailure Scenario = "failure"
)

// Scenarios lists every scenario, in the order the UI should offer them.
var Scenarios = []Scenario{
	ScenarioSeed, ScenarioSteady, ScenarioDrift,
	ScenarioOffline, ScenarioNoSpace, ScenarioFailure,
}

func valid(s Scenario) bool {
	for _, k := range Scenarios {
		if k == s {
			return true
		}
	}
	return false
}

// fakeMountRoot is deliberately a path that does not exist. If this provider
// were ever wired to real rsync by mistake, the mistake fails loudly instead
// of touching real data.
const fakeMountRoot = "/nonexistent/flashcart-fake"

// Backend is a scripted nas.Provider and runner.Runner.
type Backend struct {
	mu       sync.RWMutex
	scenario Scenario

	// Delay is the pause between simulated progress ticks. Tests set it to
	// zero; the server leaves the default so SSE streaming is exercised.
	Delay time.Duration
}

// New returns a Backend in the given scenario.
func New(s Scenario) (*Backend, error) {
	if !valid(s) {
		return nil, fmt.Errorf("unknown fake scenario %q", s)
	}
	return &Backend{scenario: s, Delay: 40 * time.Millisecond}, nil
}

var (
	_ nas.Provider  = (*Backend)(nil)
	_ runner.Runner = (*Backend)(nil)
)

// Scenario returns the current scenario.
func (b *Backend) Scenario() Scenario {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.scenario
}

// SetScenario switches scenario at runtime so states can be compared without
// restarting.
func (b *Backend) SetScenario(s Scenario) error {
	if !valid(s) {
		return fmt.Errorf("unknown fake scenario %q", s)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.scenario = s
	return nil
}

// Probe reports the NAS as unreachable only in the offline scenario.
func (b *Backend) Probe(context.Context) error {
	if b.Scenario() == ScenarioOffline {
		return fmt.Errorf("%w: fake NAS is offline", nas.ErrUnreachable)
	}
	return nil
}

// Mount hands back non-existent paths and a no-op unmount.
func (b *Backend) Mount(context.Context) (nas.Mounts, func() error, error) {
	if b.Scenario() == ScenarioOffline {
		return nas.Mounts{}, func() error { return nil },
			fmt.Errorf("%w: fake NAS is offline", nas.ErrUnreachable)
	}
	return nas.Mounts{
			Roms:  fakeMountRoot + "/roms",
			Bios:  fakeMountRoot + "/bios",
			Saves: fakeMountRoot + "/saves",
		},
		func() error { return nil },
		nil
}

// FreeSpace reports the real box's disk, or a nearly full one in the
// nospace scenario.
func (b *Backend) FreeSpace(string) (int64, int64, error) {
	const total = 459 * gib
	if b.Scenario() == ScenarioNoSpace {
		return 4 * gib, total, nil
	}
	return 433 * gib, total, nil
}

// DryRun returns the scripted result for a pass in the current scenario.
func (b *Backend) DryRun(_ context.Context, p pass.Pass) (runner.Result, error) {
	res := b.script()[p.ID]
	res.PassID = p.ID
	return res, nil
}

// Run simulates a transfer, emitting progress over wall-clock time so SSE
// streaming and the single-flight lock are genuinely exercised.
func (b *Backend) Run(ctx context.Context, p pass.Pass, events chan<- runner.Event) (runner.Result, error) {
	if b.Scenario() == ScenarioOffline {
		return runner.Result{PassID: p.ID}, fmt.Errorf("%w: fake NAS is offline", nas.ErrUnreachable)
	}

	failAt := -1
	if b.Scenario() == ScenarioFailure && p.ID == "roms-content-pull" {
		failAt = 60
	}

	for pct := 0; pct <= 100; pct += 10 {
		if err := ctx.Err(); err != nil {
			return runner.Result{PassID: p.ID}, err
		}
		if failAt >= 0 && pct > failAt {
			return runner.Result{PassID: p.ID}, errors.New(
				"rsync roms-content-pull: exit status 23: rsync: [receiver] write failed on \"ps3/Game 001 (USA).zip\": No space left on device (28)")
		}
		select {
		case events <- runner.Event{PassID: p.ID, Percent: pct}:
		case <-ctx.Done():
			return runner.Result{PassID: p.ID}, ctx.Err()
		}
		if b.Delay > 0 {
			select {
			case <-time.After(b.Delay):
			case <-ctx.Done():
				return runner.Result{PassID: p.ID}, ctx.Err()
			}
		}
	}

	res := b.script()[p.ID]
	res.PassID = p.ID
	return res, nil
}

// script returns the per-pass results for the current scenario.
func (b *Backend) script() map[string]runner.Result {
	switch b.Scenario() {
	case ScenarioSeed:
		// Empty box: everything arrives. The metadata push sees the NAS
		// copies as absent from its source, which the projected-state
		// calculation must cancel out to yield no drift.
		var deletions []string
		for _, c := range metadata() {
			deletions = append(deletions, c.Path)
		}
		return map[string]runner.Result{
			"bios-pull":          result([]runner.Change{{Itemize: ">f+++++++++", Size: 579 * mib, Path: "bios.pack"}}),
			"roms-content-pull":  result(content()),
			"roms-metadata-pull": result(metadata()),
			"roms-metadata-push": result(nil, deletions...),
			"saves-push":         result(nil),
		}

	case ScenarioSteady:
		return map[string]runner.Result{
			"bios-pull": result(nil),
			"roms-content-pull": result([]runner.Change{
				{Itemize: ">f+++++++++", Size: 4 * gib, Path: "ps2/Ratchet & Clank (USA).iso"},
				{Itemize: ">f+++++++++", Size: 682 * kib, Path: "snes/Terranigma (Europe).zip"},
			}),
			"roms-metadata-pull": result(nil),
			"roms-metadata-push": result([]runner.Change{
				{Itemize: ">f.st......", Size: 502 * kib, Path: "snes/gamelist.xml"},
				{Itemize: ">f.st......", Size: 227 * kib, Path: "megadrive/gamelist.xml"},
			}),
			"saves-push": result([]runner.Change{
				{Itemize: ">f.st......", Size: 32 * kib, Path: "snes/Terranigma (Europe).srm"},
			}),
		}

	case ScenarioDrift:
		return map[string]runner.Result{
			// A pull deletes on the box, so this drift is local.
			"bios-pull": result(nil, "stray-bios-file.bin"),
			"roms-content-pull": result(nil,
				"snes/Removed From NAS (USA).zip",
				"ps2/Old Import (Japan).iso",
			),
			"roms-metadata-pull": result(nil),
			// A push deletes on the NAS, so this drift sits on the NAS.
			"roms-metadata-push": result(nil, "dreamcast/images/Deleted Game-image.png"),
			"saves-push":         result(nil, "psx/Retired Save.mcd"),
		}

	case ScenarioNoSpace:
		return map[string]runner.Result{
			"bios-pull":          result(nil),
			"roms-content-pull":  result(content()),
			"roms-metadata-pull": result(metadata()),
			"roms-metadata-push": result(nil),
			"saves-push":         result(nil),
		}

	case ScenarioFailure:
		return map[string]runner.Result{
			"bios-pull":          result(nil),
			"roms-content-pull":  result(content()),
			"roms-metadata-pull": result(nil),
			"roms-metadata-push": result(nil),
			"saves-push":         result(nil),
		}
	}
	return map[string]runner.Result{}
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/fake/ -v`
Expected: PASS, including `TestSeedScenarioPlansLargeIncomingAndNoDrift`

- [ ] **Step 6: Run the whole suite**

Run: `go test ./... && go vet ./...`
Expected: PASS, vet clean

- [ ] **Step 7: Commit**

```bash
git add internal/fake/
git commit -m "Add scripted fake backend with named scenarios

One type satisfies both nas.Provider and runner.Runner, so server, plan,
syncer and drift run identical code in fake and real modes."
```

---

### Task 12: HTTP server, SSE and single-flight

**Files:**
- Create: `internal/server/sse.go`, `internal/server/server.go`, `internal/server/server_test.go`

**Interfaces:**
- Consumes: everything from Tasks 1, 4, 5, 8, 9, 10, 11
- Produces: `server.Options{Cfg, Provider, Runner, Free, Fake, Version, Assets}`, `func server.New(server.Options) *server.App`, `*server.App` implementing `http.Handler`, `server.Hub`, `func server.NewHub() *server.Hub`

- [ ] **Step 1: Write the SSE hub**

Create `internal/server/sse.go`:

```go
package server

import (
	"encoding/json"
	"net/http"
	"sync"
)

// message is one server-sent event payload.
type message struct {
	Type    string `json:"type"`
	PassID  string `json:"passId,omitempty"`
	Label   string `json:"label,omitempty"`
	Percent int    `json:"percent,omitempty"`
	OK      bool   `json:"ok,omitempty"`
	Err     string `json:"err,omitempty"`
	Message string `json:"message,omitempty"`
}

// Hub fans one broadcast out to every connected browser.
type Hub struct {
	mu   sync.Mutex
	subs map[chan message]struct{}
}

// NewHub returns an empty hub.
func NewHub() *Hub {
	return &Hub{subs: map[chan message]struct{}{}}
}

func (h *Hub) subscribe() chan message {
	// Buffered generously: a slow browser must never stall a sync.
	ch := make(chan message, 256)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *Hub) unsubscribe(ch chan message) {
	h.mu.Lock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}

// broadcast delivers to every subscriber, dropping messages for any
// subscriber that has fallen behind rather than blocking the sync.
func (h *Hub) broadcast(m message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- m:
		default:
		}
	}
}

// serveSSE streams messages to one browser until it disconnects.
func (h *Hub) serveSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := h.subscribe()
	defer h.unsubscribe(ch)

	for {
		select {
		case <-r.Context().Done():
			return
		case m, ok := <-ch:
			if !ok {
				return
			}
			b, err := json.Marshal(m)
			if err != nil {
				continue
			}
			if _, err := w.Write([]byte("data: " + string(b) + "\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
```

- [ ] **Step 2: Write the failing test**

Create `internal/server/server_test.go`:

```go
package server

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/adamcarlile/flashcart/internal/config"
	"github.com/adamcarlile/flashcart/internal/fake"
	"github.com/adamcarlile/flashcart/internal/plan"
)

func testAssets() fs.FS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>flashcart</html>")},
		"app.js":     &fstest.MapFile{Data: []byte("// app")},
		"style.css":  &fstest.MapFile{Data: []byte("body{}")},
	}
}

func newApp(t *testing.T, scenario fake.Scenario, localRoms string) (*App, *fake.Backend) {
	t.Helper()
	b, err := fake.New(scenario)
	if err != nil {
		t.Fatal(err)
	}
	b.Delay = 0
	cfg := &config.Config{
		NAS: config.NAS{Host: "fake", Port: 2049, MountRoot: "/mnt"},
		Trees: config.Trees{
			Roms:  config.Tree{Export: "/e/roms", Local: localRoms},
			Bios:  config.Tree{Export: "/e/bios", Local: t.TempDir()},
			Saves: config.Tree{Export: "/e/saves", Local: t.TempDir()},
		},
		SpaceMarginPercent: 10,
	}
	app := New(Options{
		Cfg: cfg, Provider: b, Runner: b, Free: b.FreeSpace,
		Fake: b, Version: "test", Assets: testAssets(),
	})
	return app, b
}

func do(t *testing.T, app *App, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	return w
}

func TestStatusReportsReachabilityAndFakeMode(t *testing.T) {
	app, _ := newApp(t, fake.ScenarioSteady, t.TempDir())
	w := do(t, app, http.MethodGet, "/api/status", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d", w.Code)
	}
	var got struct {
		Reachable bool     `json:"reachable"`
		Fake      bool     `json:"fake"`
		Scenario  string   `json:"scenario"`
		Scenarios []string `json:"scenarios"`
		Version   string   `json:"version"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Reachable {
		t.Error("Reachable = false in the steady scenario")
	}
	if !got.Fake {
		t.Error("Fake = false while running the fake backend")
	}
	if got.Scenario != string(fake.ScenarioSteady) {
		t.Errorf("Scenario = %q", got.Scenario)
	}
	if len(got.Scenarios) != len(fake.Scenarios) {
		t.Errorf("Scenarios = %v", got.Scenarios)
	}
	if got.Version != "test" {
		t.Errorf("Version = %q", got.Version)
	}
}

func TestStatusReportsOffline(t *testing.T) {
	app, _ := newApp(t, fake.ScenarioOffline, t.TempDir())
	w := do(t, app, http.MethodGet, "/api/status", "")
	var got struct {
		Reachable bool   `json:"reachable"`
		Err       string `json:"err"`
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.Reachable {
		t.Error("Reachable = true in the offline scenario")
	}
	if got.Err == "" {
		t.Error("offline status carries no explanation")
	}
}

func TestPlanReturnsTreesAndDrift(t *testing.T) {
	app, _ := newApp(t, fake.ScenarioDrift, t.TempDir())
	w := do(t, app, http.MethodPost, "/api/plan", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body)
	}
	var got plan.Plan
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Trees) != 3 {
		t.Fatalf("got %d trees, want 3", len(got.Trees))
	}
	total := 0
	for _, tp := range got.Trees {
		total += len(tp.Drift)
	}
	if total == 0 {
		t.Error("drift scenario produced no drift")
	}
}

func TestPlanRefusedWhenOffline(t *testing.T) {
	app, _ := newApp(t, fake.ScenarioOffline, t.TempDir())
	w := do(t, app, http.MethodPost, "/api/plan", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestSyncIsSingleFlight(t *testing.T) {
	app, b := newApp(t, fake.ScenarioSteady, t.TempDir())
	b.Delay = 50 * time.Millisecond // keep the first run in flight

	if w := do(t, app, http.MethodPost, "/api/sync", ""); w.Code != http.StatusAccepted {
		t.Fatalf("first sync status = %d", w.Code)
	}
	if w := do(t, app, http.MethodPost, "/api/sync", ""); w.Code != http.StatusConflict {
		t.Errorf("second sync status = %d, want 409", w.Code)
	}
	if w := do(t, app, http.MethodPost, "/api/plan", ""); w.Code != http.StatusConflict {
		t.Errorf("plan during sync status = %d, want 409", w.Code)
	}
}

func TestEventsStreamProgress(t *testing.T) {
	app, b := newApp(t, fake.ScenarioSteady, t.TempDir())
	b.Delay = 5 * time.Millisecond

	srv := httptest.NewServer(app)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}

	if _, err := http.Post(srv.URL+"/api/sync", "application/json", nil); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 4096)
	deadline := time.Now().Add(5 * time.Second)
	var seen string
	for time.Now().Before(deadline) && !strings.Contains(seen, "progress") {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			seen += string(buf[:n])
		}
		if err != nil {
			break
		}
	}
	if !strings.Contains(seen, "data: ") || !strings.Contains(seen, "progress") {
		t.Errorf("no progress events received, got: %q", seen)
	}
}

func TestDriftConfirmDeletesLocalPaths(t *testing.T) {
	localRoms := t.TempDir()
	target := filepath.Join(localRoms, "snes", "Old Game (USA).zip")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	app, _ := newApp(t, fake.ScenarioDrift, localRoms)
	body := `{"items":[{"tree":"roms","side":"local","rel":"snes/Old Game (USA).zip"}]}`
	w := do(t, app, http.MethodPost, "/api/drift/confirm", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Error("confirmed drift path still exists")
	}
}

func TestDriftConfirmRefusesEscapingPaths(t *testing.T) {
	localRoms := t.TempDir()
	app, _ := newApp(t, fake.ScenarioDrift, localRoms)
	body := `{"items":[{"tree":"roms","side":"local","rel":"../../etc/passwd"}]}`
	w := do(t, app, http.MethodPost, "/api/drift/confirm", body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestScenarioSwitchOnlyExistsInFakeMode(t *testing.T) {
	app, _ := newApp(t, fake.ScenarioSteady, t.TempDir())
	if w := do(t, app, http.MethodPost, "/api/fake/scenario", `{"scenario":"drift"}`); w.Code != http.StatusOK {
		t.Fatalf("scenario switch status = %d body = %s", w.Code, w.Body)
	}
	w := do(t, app, http.MethodGet, "/api/status", "")
	var got struct {
		Scenario string `json:"scenario"`
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.Scenario != "drift" {
		t.Errorf("Scenario = %q after switch", got.Scenario)
	}
}

func TestIndexIsServed(t *testing.T) {
	app, _ := newApp(t, fake.ScenarioSteady, t.TempDir())
	w := do(t, app, http.MethodGet, "/", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "flashcart") {
		t.Errorf("index body = %q", w.Body.String())
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/server/ -v`
Expected: FAIL, `undefined: New`

- [ ] **Step 4: Write the implementation**

Create `internal/server/server.go`:

```go
// Package server exposes the HTTP API and the embedded UI.
package server

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"sync"
	"time"

	"github.com/adamcarlile/flashcart/internal/config"
	"github.com/adamcarlile/flashcart/internal/drift"
	"github.com/adamcarlile/flashcart/internal/fake"
	"github.com/adamcarlile/flashcart/internal/nas"
	"github.com/adamcarlile/flashcart/internal/pass"
	"github.com/adamcarlile/flashcart/internal/plan"
	"github.com/adamcarlile/flashcart/internal/runner"
	"github.com/adamcarlile/flashcart/internal/syncer"
)

// Options are the dependencies the server needs. Provider and Runner are the
// seam: real implementations in production, one fake.Backend in fake mode.
type Options struct {
	Cfg      *config.Config
	Provider nas.Provider
	Runner   runner.Runner
	Free     plan.FreeSpaceFunc
	Fake     *fake.Backend
	Version  string
	Assets   fs.FS
}

// App is the HTTP handler.
type App struct {
	opts Options
	hub  *Hub
	mux  *http.ServeMux

	mu          sync.Mutex
	busy        bool
	lastSummary *syncer.Summary
	lastSyncAt  time.Time
}

// New wires the routes.
func New(o Options) *App {
	a := &App{opts: o, hub: NewHub(), mux: http.NewServeMux()}

	a.mux.Handle("GET /", http.FileServer(http.FS(o.Assets)))
	a.mux.HandleFunc("GET /api/status", a.handleStatus)
	a.mux.HandleFunc("GET /api/events", a.hub.serveSSE)
	a.mux.HandleFunc("POST /api/plan", a.handlePlan)
	a.mux.HandleFunc("POST /api/sync", a.handleSync)
	a.mux.HandleFunc("POST /api/drift/confirm", a.handleDriftConfirm)

	// Registered only when the fake backend is present, so the route simply
	// does not exist in production.
	if o.Fake != nil {
		a.mux.HandleFunc("POST /api/fake/scenario", a.handleScenario)
	}
	return a
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) { a.mux.ServeHTTP(w, r) }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"err": msg})
}

// acquire enforces single-flight. Plan and sync cannot overlap, and a second
// browser tab cannot start a parallel run.
func (a *App) acquire() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return false
	}
	a.busy = true
	return true
}

func (a *App) release() {
	a.mu.Lock()
	a.busy = false
	a.mu.Unlock()
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	type resp struct {
		Reachable   bool            `json:"reachable"`
		Err         string          `json:"err,omitempty"`
		NASHost     string          `json:"nasHost"`
		Fake        bool            `json:"fake"`
		Scenario    string          `json:"scenario,omitempty"`
		Scenarios   []string        `json:"scenarios,omitempty"`
		Version     string          `json:"version"`
		Busy        bool            `json:"busy"`
		LastSyncAt  string          `json:"lastSyncAt,omitempty"`
		LastSummary *syncer.Summary `json:"lastSummary,omitempty"`
	}

	out := resp{Version: a.opts.Version, NASHost: a.opts.Cfg.NAS.Host}

	a.mu.Lock()
	out.Busy = a.busy
	out.LastSummary = a.lastSummary
	if !a.lastSyncAt.IsZero() {
		out.LastSyncAt = a.lastSyncAt.UTC().Format(time.RFC3339)
	}
	a.mu.Unlock()

	// Probe mounts nothing, so this stays fast and safe with no network.
	if err := a.opts.Provider.Probe(r.Context()); err != nil {
		out.Err = err.Error()
	} else {
		out.Reachable = true
	}

	if a.opts.Fake != nil {
		out.Fake = true
		out.Scenario = string(a.opts.Fake.Scenario())
		for _, s := range fake.Scenarios {
			out.Scenarios = append(out.Scenarios, string(s))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// withMounts holds the NAS for the duration of fn and always unmounts, even
// on failure. A leaked mount is how the next boot gets slow and confusing.
func (a *App) withMounts(ctx context.Context, fn func(nas.Mounts) error) error {
	m, unmount, err := a.opts.Provider.Mount(ctx)
	if err != nil {
		return err
	}
	defer unmount()
	return fn(m)
}

func (a *App) handlePlan(w http.ResponseWriter, r *http.Request) {
	if !a.acquire() {
		writeErr(w, http.StatusConflict, "a plan or sync is already running")
		return
	}
	defer a.release()

	var out plan.Plan
	err := a.withMounts(r.Context(), func(m nas.Mounts) error {
		p, err := plan.Build(r.Context(), a.opts.Cfg, a.opts.Runner, pass.Passes(a.opts.Cfg, m), a.opts.Free)
		out = p
		return err
	})
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) handleSync(w http.ResponseWriter, r *http.Request) {
	if !a.acquire() {
		writeErr(w, http.StatusConflict, "a plan or sync is already running")
		return
	}

	// The run outlives the request: progress arrives over SSE.
	go func() {
		defer a.release()
		ctx := context.Background()

		events := make(chan runner.Event, 256)
		done := make(chan struct{})
		go func() {
			defer close(done)
			for e := range events {
				a.hub.broadcast(message{Type: "progress", PassID: e.PassID, Percent: e.Percent, Message: e.Message})
			}
		}()

		var sum syncer.Summary
		err := a.withMounts(ctx, func(m nas.Mounts) error {
			sum = syncer.Run(ctx, a.opts.Runner, pass.Passes(a.opts.Cfg, m), events)
			return nil
		})
		close(events)
		<-done

		if err != nil {
			sum.OK = false
			sum.Err = err.Error()
		}
		for _, p := range sum.Passes {
			a.hub.broadcast(message{Type: "pass", PassID: p.ID, Label: p.Label, OK: p.OK, Err: p.Err})
		}

		a.mu.Lock()
		a.lastSummary = &sum
		a.lastSyncAt = time.Now()
		a.mu.Unlock()

		a.hub.broadcast(message{Type: "done", OK: sum.OK, Err: sum.Err})
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

func (a *App) handleDriftConfirm(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []plan.DriftItem `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request body")
		return
	}
	if len(body.Items) == 0 {
		writeErr(w, http.StatusBadRequest, "no items to delete")
		return
	}

	if !a.acquire() {
		writeErr(w, http.StatusConflict, "a plan or sync is already running")
		return
	}
	defer a.release()

	var deleted []string
	var deleteErr error
	mountErr := a.withMounts(r.Context(), func(m nas.Mounts) error {
		deleted, deleteErr = drift.Delete(drift.RootsFor(a.opts.Cfg, m), body.Items)
		return nil
	})
	if mountErr != nil {
		writeErr(w, http.StatusServiceUnavailable, mountErr.Error())
		return
	}
	if deleteErr != nil {
		writeErr(w, http.StatusBadRequest, deleteErr.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
}

func (a *App) handleScenario(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Scenario string `json:"scenario"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request body")
		return
	}
	if err := a.opts.Fake.SetScenario(fake.Scenario(body.Scenario)); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"scenario": body.Scenario})
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/server/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/server/server.go internal/server/sse.go internal/server/server_test.go
git commit -m "Add HTTP API, SSE hub and single-flight locking

Mounts are held only for the duration of a request and always released.
The fake scenario route is registered only when the fake backend is present."
```

---

### Task 13: Embedded web UI

The design was reviewed and approved as an interactive mockup covering all eight states before this task was written. Reproduce it faithfully; the decisions below are deliberate.

**Design decisions, and why**

- **Indigo accent, deliberately outside the green/amber/red register.** Semantic state colour must never be confused with "this is a button".
- **Three type roles.** `JetBrains Mono` carries every path, byte count and percentage, because paths and sizes *are* the content. `Bricolage Grotesque` for headings, `Public Sans` for labels.
- **Each tree renders one row per pass, with an explicit arrow.** BIOS shows only `↓`, Saves only `↑`, ROMs shows both. That asymmetry teaches the split, which is the one genuinely counterintuitive thing about this tool. Direction is encoded in the arrow glyph as well as colour, so it survives colourblindness.
- **The five passes are always listed, dimmed at 0% before a run.** The sequence is the mental model; the progress display should not be the first time the user meets it.
- **Drift is visually quarantined**: its own bordered block outside the normal card rhythm, `LOCAL` and `NAS` chips so the side being deleted from is never inferred, and a footer that counts what is ticked before you commit.
- **Offline is presented as normal, not as an error.** In a car it is the working state. The copy reads "Nothing to do until you are home. The library on this box is complete and playable."

**Files:**
- Create: `internal/server/assets/index.html`, `internal/server/assets/style.css`, `internal/server/assets/app.js`, `internal/server/assets/assets.go`
- Modify: `internal/server/server_test.go` (add the embedded-assets tests)

**Interfaces:**
- Consumes: the JSON API from Task 12, including `nasHost` on `/api/status` and `passes` on each `plan.TreePlan`
- Produces: `assets.FS`

- [ ] **Step 1: Write the markup**

Create `internal/server/assets/index.html`:

```html
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>flashcart</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Bricolage+Grotesque:opsz,wght@12..96,500;12..96,700&family=JetBrains+Mono:wght@400;500;700&family=Public+Sans:wght@400;500;600&display=swap">
<link rel="stylesheet" href="/style.css">
</head>
<body>

<div class="fake-bar" id="fake-bar" hidden>
  <span class="fake-tag">Fake mode</span>
  <span class="fake-note">Nothing is mounted and no data moves.</span>
  <label class="fake-pick">Scenario
    <select id="scenario"></select>
  </label>
</div>

<div class="app">
  <header>
    <div>
      <h1 class="wordmark">flashcart</h1>
      <div class="tagline" id="tagline"></div>
    </div>
    <div class="head-right">
      <span class="pill" id="nas-pill">checking</span>
      <div class="stamp">
        <div id="last-sync">Never synced</div>
        <div id="version"></div>
      </div>
    </div>
  </header>

  <div class="banner" id="banner" hidden></div>

  <section>
    <div class="section-head">
      <h2>Manifest</h2>
      <span class="hint">&#8595; from NAS &middot; &#8593; to NAS</span>
    </div>
    <div class="trees" id="trees"></div>
  </section>

  <div class="actions">
    <button class="act primary" id="plan-btn">Plan</button>
    <button class="act" id="sync-btn">Sync</button>
    <span class="act-note" id="act-note"></span>
  </div>

  <section>
    <div class="section-head">
      <h2>Passes</h2>
      <span class="hint">metadata is pulled before it is pushed, so an empty box is seeded</span>
    </div>
    <div class="passes" id="passes"></div>
  </section>

  <section id="drift-section" hidden>
    <div class="drift">
      <div class="drift-head">
        <h2 id="drift-title">Drift</h2>
        <p>Present on the destination, absent from the source. Deletion is permanent and only ticked rows are removed.</p>
      </div>
      <div class="drift-rows" id="drift-rows"></div>
      <div class="drift-foot">
        <button class="act danger" id="drift-btn" disabled>Delete ticked</button>
        <span class="act-note" id="drift-note">Nothing ticked</span>
      </div>
    </div>
  </section>

  <section>
    <div class="section-head"><h2>Log</h2></div>
    <div class="log" id="log"></div>
  </section>
</div>

<script src="/app.js"></script>
</body>
</html>
```

- [ ] **Step 2: Write the stylesheet**

Create `internal/server/assets/style.css`. Colours are defined token-level: the bare `:root` carries the complete light palette, the media query is guarded with `:root:not([data-theme="light"])` so an explicit light choice beats a dark OS, and `:root[data-theme="dark"]` repeats it so the toggle wins in both directions. No colour is declared only inside a media or `[data-theme]` block.

```css
:root {
  --bg:        #F1F1F6;
  --surface:   #FFFFFF;
  --surface-2: #E8E8F0;
  --line:      #D6D6E2;
  --line-soft: #E4E4EE;
  --text:      #16171F;
  --dim:       #64667A;
  --faint:     #8E90A4;
  --accent:    #4A52B8;
  --accent-ink:#FFFFFF;
  --accent-soft:rgba(74,82,184,.10);
  --ok:        #2E7D53;
  --ok-soft:   rgba(46,125,83,.12);
  --warn:      #98600F;
  --warn-soft: rgba(152,96,15,.12);
  --danger:    #A9332A;
  --danger-soft:rgba(169,51,42,.10);
  --shadow:    0 1px 2px rgba(22,23,31,.06), 0 8px 24px -12px rgba(22,23,31,.18);

  --display: "Bricolage Grotesque", ui-sans-serif, system-ui, sans-serif;
  --ui:      "Public Sans", ui-sans-serif, system-ui, -apple-system, sans-serif;
  --mono:    "JetBrains Mono", ui-monospace, "SF Mono", Menlo, monospace;
}

@media (prefers-color-scheme: dark) {
  :root:not([data-theme="light"]) {
    --bg:        #0E1016;
    --surface:   #161923;
    --surface-2: #1D212D;
    --line:      #2A2F3E;
    --line-soft: #222634;
    --text:      #E9EAF1;
    --dim:       #949AAC;
    --faint:     #6E7488;
    --accent:    #8B93F0;
    --accent-ink:#12141C;
    --accent-soft:rgba(139,147,240,.14);
    --ok:        #5FB97F;
    --ok-soft:   rgba(95,185,127,.14);
    --warn:      #D9A155;
    --warn-soft: rgba(217,161,85,.14);
    --danger:    #E0736A;
    --danger-soft:rgba(224,115,106,.13);
    --shadow:    0 1px 2px rgba(0,0,0,.4), 0 8px 24px -12px rgba(0,0,0,.7);
  }
}

:root[data-theme="dark"] {
  --bg:        #0E1016;
  --surface:   #161923;
  --surface-2: #1D212D;
  --line:      #2A2F3E;
  --line-soft: #222634;
  --text:      #E9EAF1;
  --dim:       #949AAC;
  --faint:     #6E7488;
  --accent:    #8B93F0;
  --accent-ink:#12141C;
  --accent-soft:rgba(139,147,240,.14);
  --ok:        #5FB97F;
  --ok-soft:   rgba(95,185,127,.14);
  --warn:      #D9A155;
  --warn-soft: rgba(217,161,85,.14);
  --danger:    #E0736A;
  --danger-soft:rgba(224,115,106,.13);
  --shadow:    0 1px 2px rgba(0,0,0,.4), 0 8px 24px -12px rgba(0,0,0,.7);
}

* { box-sizing: border-box; }

body {
  margin: 0;
  background: var(--bg);
  color: var(--text);
  font-family: var(--ui);
  font-size: 15px;
  line-height: 1.5;
  -webkit-font-smoothing: antialiased;
}

:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; border-radius: 3px; }

/* fake mode */
.fake-bar {
  display: flex; align-items: center; gap: .9rem; flex-wrap: wrap;
  padding: .7rem 1.25rem;
  background: var(--warn-soft);
  border-bottom: 1px solid var(--warn);
  color: var(--warn);
  font-size: .82rem;
}
.fake-tag {
  font-family: var(--mono); font-size: .62rem; font-weight: 700;
  letter-spacing: .12em; text-transform: uppercase;
  border: 1px solid var(--warn); border-radius: 3px; padding: .2rem .45rem;
}
.fake-note { color: var(--dim); }
.fake-pick { margin-left: auto; font-family: var(--mono); font-size: .72rem; display: flex; gap: .4rem; align-items: center; }
.fake-pick select {
  font-family: var(--mono); font-size: .72rem;
  background: var(--surface); color: var(--text);
  border: 1px solid var(--line); border-radius: 4px; padding: .25rem .4rem;
}

/* shell */
.app { max-width: 62rem; margin: 0 auto; padding: 2rem 1.25rem 5rem; display: flex; flex-direction: column; gap: 1.75rem; }

header { display: flex; justify-content: space-between; align-items: flex-end; gap: 1rem; flex-wrap: wrap; }

.wordmark { font-family: var(--display); font-weight: 700; font-size: 1.7rem; letter-spacing: -.02em; margin: 0; line-height: 1; }
.tagline { font-family: var(--mono); font-size: .7rem; color: var(--faint); letter-spacing: .04em; margin-top: .4rem; }

.head-right { display: flex; align-items: center; gap: 1rem; flex-wrap: wrap; }

.pill {
  display: inline-flex; align-items: center; gap: .4rem;
  font-family: var(--mono); font-size: .72rem; font-weight: 500;
  padding: .3rem .6rem; border-radius: 999px; border: 1px solid transparent; white-space: nowrap;
  color: var(--dim); background: var(--surface-2);
}
.pill::before { content: ""; width: 6px; height: 6px; border-radius: 50%; background: currentColor; flex: none; }
.pill.ok  { color: var(--ok);     background: var(--ok-soft);     border-color: var(--ok-soft); }
.pill.bad { color: var(--danger); background: var(--danger-soft); border-color: var(--danger-soft); }

.stamp { font-family: var(--mono); font-size: .7rem; color: var(--faint); text-align: right; }

.banner {
  display: flex; gap: .75rem; align-items: flex-start;
  padding: .8rem 1rem; border-radius: 6px; border-left: 3px solid; font-size: .875rem;
}
.banner strong { font-family: var(--mono); font-size: .72rem; letter-spacing: .1em; text-transform: uppercase; }
.banner.bad  { color: var(--danger); background: var(--danger-soft); border-left-color: var(--danger); }
.banner.warn { color: var(--warn);   background: var(--warn-soft);   border-left-color: var(--warn); }

/* manifest */
.trees { display: grid; gap: 1rem; grid-template-columns: 1.4fr 1fr 1fr; }
@media (max-width: 46rem) { .trees { grid-template-columns: 1fr; } }

.tree {
  background: var(--surface); border: 1px solid var(--line); border-radius: 8px;
  padding: 1rem; display: flex; flex-direction: column; gap: .85rem; box-shadow: var(--shadow);
}
.tree-head { display: flex; align-items: baseline; justify-content: space-between; gap: .5rem; }
.tree-name { font-family: var(--display); font-weight: 700; font-size: 1.05rem; letter-spacing: -.01em; }
.tree-tag {
  font-family: var(--mono); font-size: .58rem; font-weight: 700;
  letter-spacing: .1em; text-transform: uppercase;
  color: var(--accent); background: var(--accent-soft); padding: .18rem .4rem; border-radius: 3px;
}

.flows { display: flex; flex-direction: column; gap: .5rem; }
.flow { display: grid; grid-template-columns: 1.1rem 1fr auto; align-items: baseline; gap: .55rem; }
.flow-arrow { font-family: var(--mono); font-weight: 700; font-size: .95rem; line-height: 1; }
.flow-arrow.in  { color: var(--accent); }
.flow-arrow.out { color: var(--ok); }
.flow-label { font-size: .8rem; color: var(--dim); }
.flow-label b { display: block; color: var(--text); font-weight: 600; font-size: .82rem; }
.flow-val { font-family: var(--mono); font-size: .8rem; font-variant-numeric: tabular-nums; text-align: right; white-space: nowrap; }
.flow-val span { display: block; color: var(--faint); font-size: .68rem; }
.flow.nil .flow-val, .flow.nil .flow-label { color: var(--faint); }
.flow.nil .flow-arrow { color: var(--faint); opacity: .5; }

.tree-foot {
  border-top: 1px solid var(--line-soft); padding-top: .6rem;
  display: flex; justify-content: space-between;
  font-family: var(--mono); font-size: .7rem; color: var(--faint);
}
.tree-foot .drifted { color: var(--danger); font-weight: 700; }

/* actions */
.actions { display: flex; gap: .6rem; align-items: center; flex-wrap: wrap; }
button.act {
  font-family: var(--ui); font-size: .9rem; font-weight: 600;
  padding: .6rem 1.35rem; border-radius: 6px;
  border: 1px solid var(--line); background: var(--surface); color: var(--text);
  cursor: pointer; transition: background .12s, border-color .12s, opacity .12s;
}
button.act:hover:not(:disabled) { border-color: var(--faint); }
button.act.primary { background: var(--accent); border-color: var(--accent); color: var(--accent-ink); }
button.act.primary:hover:not(:disabled) { filter: brightness(1.08); }
button.act.danger { background: var(--danger); border-color: var(--danger); color: #fff; }
button.act:disabled { opacity: .38; cursor: not-allowed; }
.act-note { font-family: var(--mono); font-size: .72rem; color: var(--faint); }

/* passes */
.section-head { display: flex; align-items: baseline; gap: .7rem; margin-bottom: .8rem; }
.section-head h2 { font-family: var(--display); font-weight: 700; font-size: 1rem; margin: 0; letter-spacing: -.01em; }
.section-head .hint { font-family: var(--mono); font-size: .68rem; color: var(--faint); }

.passes { background: var(--surface); border: 1px solid var(--line); border-radius: 8px; overflow: hidden; box-shadow: var(--shadow); }
.pass {
  display: grid; grid-template-columns: 1.3rem 1fr 7rem 3rem;
  align-items: center; gap: .75rem; padding: .65rem 1rem; border-bottom: 1px solid var(--line-soft);
}
.pass:last-child { border-bottom: 0; }
.pass-n { font-family: var(--mono); font-size: .7rem; color: var(--faint); font-variant-numeric: tabular-nums; }
.pass-name { font-size: .85rem; display: flex; align-items: center; gap: .5rem; }
.pass-name .dir { font-family: var(--mono); font-weight: 700; font-size: .8rem; }
.pass-name .dir.in { color: var(--accent); }
.pass-name .dir.out { color: var(--ok); }
.track { height: 4px; background: var(--surface-2); border-radius: 2px; overflow: hidden; }
.fill { height: 100%; width: 0; background: var(--accent); border-radius: 2px; transition: width .35s ease; }
.pass.done .fill { background: var(--ok); }
.pass.failed .fill { background: var(--danger); }
.pass-state { font-family: var(--mono); font-size: .7rem; font-variant-numeric: tabular-nums; text-align: right; color: var(--faint); }
.pass.done .pass-state { color: var(--ok); }
.pass.failed .pass-state { color: var(--danger); font-weight: 700; }
.pass.idle { opacity: .45; }
.pass-err {
  grid-column: 2 / -1; font-family: var(--mono); font-size: .7rem;
  color: var(--danger); background: var(--danger-soft);
  padding: .4rem .55rem; border-radius: 4px; margin-top: .4rem;
  overflow-x: auto; white-space: pre;
}

/* drift quarantine */
.drift { border: 1px solid var(--danger); border-radius: 8px; overflow: hidden; background: var(--surface); }
.drift-head { background: var(--danger-soft); padding: .85rem 1rem; border-bottom: 1px solid var(--danger); }
.drift-head h2 { font-family: var(--display); font-size: 1rem; font-weight: 700; margin: 0 0 .2rem; color: var(--danger); letter-spacing: -.01em; }
.drift-head p { margin: 0; font-size: .8rem; color: var(--dim); }
.drift-rows { display: flex; flex-direction: column; }
.drift-row {
  display: grid; grid-template-columns: auto 3.6rem 1fr; align-items: center; gap: .7rem;
  padding: .5rem 1rem; border-bottom: 1px solid var(--line-soft); cursor: pointer; font-size: .8rem;
}
.drift-row:last-child { border-bottom: 0; }
.drift-row:hover { background: var(--surface-2); }
.drift-row input { accent-color: var(--danger); width: 15px; height: 15px; cursor: pointer; }
.side {
  font-family: var(--mono); font-size: .58rem; font-weight: 700; letter-spacing: .08em;
  text-align: center; padding: .16rem 0; border-radius: 3px;
  border: 1px solid var(--line); color: var(--dim);
}
.side.nas { color: var(--accent); border-color: var(--accent-soft); background: var(--accent-soft); }
.drift-path { font-family: var(--mono); font-size: .76rem; overflow-x: auto; white-space: nowrap; }
.drift-foot { padding: .85rem 1rem; display: flex; gap: .8rem; align-items: center; flex-wrap: wrap; }

/* log */
.log {
  font-family: var(--mono); font-size: .72rem;
  background: var(--surface-2); border: 1px solid var(--line); border-radius: 6px;
  padding: .75rem 1rem; max-height: 11rem; overflow: auto; color: var(--dim); white-space: pre-wrap;
}
.log .t { color: var(--faint); }
.log .e { color: var(--danger); }
.log .g { color: var(--ok); }

[hidden] { display: none !important; }

@media (prefers-reduced-motion: reduce) { * { transition: none !important; animation: none !important; } }
```

- [ ] **Step 3: Write the script**

Create `internal/server/assets/app.js`:

```js
const $ = (id) => document.getElementById(id);

// The pass sequence is the mental model, so it is rendered before any run
// rather than appearing for the first time during one.
const PASSES = [
  { id: "bios-pull",          n: 1, name: "BIOS",          dir: "in"  },
  { id: "roms-content-pull",  n: 2, name: "ROM content",   dir: "in"  },
  { id: "roms-metadata-pull", n: 3, name: "Metadata seed", dir: "in"  },
  { id: "roms-metadata-push", n: 4, name: "Metadata",      dir: "out" },
  { id: "saves-push",         n: 5, name: "Saves",         dir: "out" },
];

// BIOS pulls only, Saves pushes only, ROMs does both. The asymmetry is what
// teaches the split.
const TREES = [
  { key: "roms",  name: "ROMs",  tag: "split" },
  { key: "bios",  name: "BIOS",  tag: "pull"  },
  { key: "saves", name: "Saves", tag: "push"  },
];

const state = { status: null, plan: null, progress: {}, err: {}, busy: false };

function humanBytes(n) {
  if (!n) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
  return `${n.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

function esc(s) {
  return String(s).replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

function log(kind, text) {
  const el = $("log");
  const div = document.createElement("div");
  div.className = kind;
  div.textContent = text;
  el.appendChild(div);
  el.scrollTop = el.scrollHeight;
}

/* ── rendering ─────────────────────────────────────────────── */

function renderTrees() {
  const byTree = {};
  for (const t of (state.plan?.trees || [])) byTree[t.tree] = t;

  $("trees").innerHTML = TREES.map(({ key, name, tag }) => {
    const t = byTree[key];
    let flows;

    if (!t) {
      flows = `<div class="flow nil">
        <span class="flow-arrow in">&middot;</span>
        <span class="flow-label"><b>not planned</b></span>
        <span class="flow-val">&mdash;</span></div>`;
    } else {
      flows = (t.passes || []).map((p) => {
        const nil = !p.files;
        return `<div class="flow ${nil ? "nil" : ""}">
          <span class="flow-arrow ${p.direction}">${p.direction === "in" ? "&#8595;" : "&#8593;"}</span>
          <span class="flow-label"><b>${esc(p.label)}</b></span>
          <span class="flow-val">${nil ? "&mdash;" : p.files.toLocaleString() + " files"}
            ${nil ? "" : `<span>${humanBytes(p.bytes)}</span>`}</span>
        </div>`;
      }).join("");
    }

    const n = t ? (t.drift || []).length : 0;
    const drift = n
      ? `<span class="drifted">${n} drifted</span>`
      : `<span>${t ? "no drift" : "&mdash;"}</span>`;

    return `<div class="tree">
      <div class="tree-head"><span class="tree-name">${name}</span><span class="tree-tag">${tag}</span></div>
      <div class="flows">${flows}</div>
      <div class="tree-foot"><span>/userdata/${key}</span>${drift}</div>
    </div>`;
  }).join("");
}

function renderPasses() {
  $("passes").innerHTML = PASSES.map((p) => {
    const v = state.progress[p.id];
    let cls = "idle", pct = 0, label = "&mdash;";

    if (v === "done")        { cls = "done";   pct = 100; label = "done"; }
    else if (v === "failed") { cls = "failed"; pct = 100; label = "failed"; }
    else if (typeof v === "number") { cls = "running"; pct = v; label = v + "%"; }
    else if (state.busy)     { label = "waiting"; }

    const err = state.err[p.id] ? `<div class="pass-err">${esc(state.err[p.id])}</div>` : "";

    return `<div class="pass ${cls}">
      <span class="pass-n">${p.n}</span>
      <span class="pass-name"><span class="dir ${p.dir}">${p.dir === "in" ? "&#8595;" : "&#8593;"}</span>${p.name}</span>
      <span class="track"><span class="fill" style="width:${pct}%"></span></span>
      <span class="pass-state">${label}</span>
      ${err}
    </div>`;
  }).join("");
}

function renderDrift() {
  const items = (state.plan?.trees || []).flatMap((t) => t.drift || []);
  $("drift-section").hidden = items.length === 0;
  if (!items.length) return;

  $("drift-title").textContent = `Drift — ${items.length} path${items.length === 1 ? "" : "s"}`;
  $("drift-rows").innerHTML = items.map((d, i) => `
    <label class="drift-row">
      <input type="checkbox" data-i="${i}">
      <span class="side ${esc(d.side)}">${esc(d.side).toUpperCase()}</span>
      <span class="drift-path">${esc(d.tree)}/${esc(d.rel)}</span>
    </label>`).join("");

  const boxes = () => [...$("drift-rows").querySelectorAll("input")];
  const update = () => {
    const n = boxes().filter((b) => b.checked).length;
    $("drift-btn").disabled = n === 0;
    $("drift-note").textContent = n === 0
      ? "Nothing ticked"
      : `${n} path${n === 1 ? "" : "s"} will be permanently deleted`;
  };
  boxes().forEach((b) => b.addEventListener("change", update));
  update();

  $("drift-btn").onclick = async () => {
    const chosen = boxes().filter((b) => b.checked).map((b) => items[+b.dataset.i]);
    if (!chosen.length) return;
    if (!confirm(`Permanently delete ${chosen.length} path(s)? This cannot be undone.`)) return;

    const res = await fetch("/api/drift/confirm", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ items: chosen }),
    });
    const body = await res.json();
    if (!res.ok) { log("e", "delete refused: " + body.err); return; }
    log("g", `deleted ${body.deleted.length} path(s)`);
    doPlan();
  };
}

function setBanner(kind, title, text) {
  const b = $("banner");
  if (!kind) { b.hidden = true; return; }
  b.hidden = false;
  b.className = "banner " + kind;
  b.innerHTML = `<strong>${esc(title)}</strong><span>${esc(text)}</span>`;
}

function renderControls() {
  const s = state.status;
  const reachable = s?.reachable;
  const blocked = !reachable || state.busy || (state.plan && !state.plan.sufficient);

  $("plan-btn").disabled = !reachable || state.busy;
  $("sync-btn").disabled = blocked;

  if (state.busy) {
    $("act-note").textContent = "Sync in progress — plan and sync are locked";
  } else if (!reachable) {
    $("act-note").textContent = "";
  } else if (!state.plan) {
    $("act-note").textContent = "Plan first to see what would move.";
  } else if (state.plan.sufficient) {
    $("act-note").textContent =
      `${humanBytes(state.plan.requiredBytes)} incoming · ${humanBytes(state.plan.freeBytes)} free`;
  } else {
    $("act-note").textContent = "";
  }
}

/* ── data ──────────────────────────────────────────────────── */

async function refreshStatus() {
  let s;
  try {
    s = await (await fetch("/api/status")).json();
  } catch {
    return;
  }
  state.status = s;
  state.busy = s.busy;

  const pill = $("nas-pill");
  pill.className = "pill " + (s.reachable ? "ok" : "bad");
  pill.textContent = s.reachable ? "NAS reachable" : "NAS unreachable";

  $("tagline").textContent = s.nasHost ? "NAS · " + s.nasHost : "";
  $("version").textContent = s.version ? "v" + s.version : "";
  $("last-sync").textContent = s.lastSyncAt
    ? "Last sync " + new Date(s.lastSyncAt).toLocaleString()
    : "Never synced";

  // Offline is the normal working state in a car, not a failure.
  if (!s.reachable) {
    setBanner("bad", "NAS unreachable",
      "Nothing to do until you are home. The library on this box is complete and playable.");
  } else if (state.plan && !state.plan.sufficient) {
    setBanner("bad", "Not enough space", state.plan.message);
  } else {
    setBanner(null);
  }

  if (s.fake) {
    $("fake-bar").hidden = false;
    const sel = $("scenario");
    if (!sel.options.length) {
      for (const name of s.scenarios || []) {
        const opt = document.createElement("option");
        opt.value = opt.textContent = name;
        sel.appendChild(opt);
      }
      sel.addEventListener("change", async () => {
        await fetch("/api/fake/scenario", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ scenario: sel.value }),
        });
        state.plan = null;
        state.progress = {};
        state.err = {};
        log("t", "scenario switched to " + sel.value);
        renderTrees(); renderPasses(); renderDrift();
        refreshStatus();
      });
    }
    sel.value = s.scenario;
  }

  renderControls();
}

async function doPlan() {
  state.busy = true;
  renderControls();
  log("t", "planning…");
  try {
    const res = await fetch("/api/plan", { method: "POST" });
    const body = await res.json();
    if (!res.ok) { log("e", "plan failed: " + body.err); return; }
    state.plan = body;
    renderTrees();
    renderDrift();
    const drifted = (body.trees || []).reduce((n, t) => n + (t.drift || []).length, 0);
    log(body.sufficient ? "g" : "e",
      `plan complete — ${humanBytes(body.requiredBytes)} in, ${drifted} drifted path(s)` +
      (body.sufficient ? "" : " — refused, insufficient space"));
  } finally {
    state.busy = false;
    await refreshStatus();
  }
}

async function doSync() {
  state.progress = {};
  state.err = {};
  state.busy = true;
  renderPasses();
  renderControls();
  log("t", "sync started");

  const res = await fetch("/api/sync", { method: "POST" });
  if (!res.ok) {
    const body = await res.json();
    log("e", "sync refused: " + body.err);
    state.busy = false;
    renderControls();
  }
}

function connectEvents() {
  const es = new EventSource("/api/events");
  es.onmessage = (ev) => {
    const m = JSON.parse(ev.data);
    if (m.type === "progress") {
      state.progress[m.passId] = m.percent;
      renderPasses();
    } else if (m.type === "pass") {
      state.progress[m.passId] = m.ok ? "done" : "failed";
      if (!m.ok) state.err[m.passId] = m.err;
      log(m.ok ? "g" : "e", `${m.label || m.passId}: ${m.ok ? "ok" : "FAILED"}`);
      renderPasses();
    } else if (m.type === "done") {
      state.busy = false;
      if (!m.ok) {
        setBanner("bad", "Sync failed",
          "Remaining passes were abandoned. Nothing was deleted. The NAS was unmounted.");
      }
      log(m.ok ? "g" : "e", m.ok ? "sync complete" : "sync failed: " + m.err);
      refreshStatus();
    }
  };
  es.onerror = () => log("t", "event stream interrupted, retrying");
}

$("plan-btn").addEventListener("click", doPlan);
$("sync-btn").addEventListener("click", doSync);

renderTrees();
renderPasses();
connectEvents();
refreshStatus();
setInterval(refreshStatus, 15000);
```

- [ ] **Step 4: Embed the assets**

Create `internal/server/assets/assets.go`:

```go
// Package assets holds the embedded single-page UI.
package assets

import "embed"

// FS carries index.html, style.css and app.js.
//
//go:embed index.html style.css app.js
var FS embed.FS
```

- [ ] **Step 5: Write the failing tests for the real assets**

Append to `internal/server/server_test.go`, adding the imports `"io/fs"` and `"github.com/adamcarlile/flashcart/internal/server/assets"`:

```go
func TestEmbeddedAssetsAreComplete(t *testing.T) {
	for _, name := range []string{"index.html", "style.css", "app.js"} {
		b, err := fs.ReadFile(assets.FS, name)
		if err != nil {
			t.Fatalf("embedded asset %s: %v", name, err)
		}
		if len(b) == 0 {
			t.Errorf("embedded asset %s is empty", name)
		}
	}
}

// The UI must never let fake mode pass unnoticed.
func TestIndexCarriesFakeBanner(t *testing.T) {
	b, err := fs.ReadFile(assets.FS, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "fake-bar") {
		t.Error("index.html has no fake mode banner")
	}
}

// Every colour must be reachable in the un-stamped theme state, where only
// prefers-color-scheme applies. A token defined solely inside a media or
// [data-theme] block renders one theme's text on the other theme's ground.
func TestStylesheetDefinesEveryTokenAtRoot(t *testing.T) {
	b, err := fs.ReadFile(assets.FS, "style.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(b)

	bare := css[strings.Index(css, ":root {"):strings.Index(css, "@media (prefers-color-scheme: dark)")]
	for _, token := range []string{
		"--bg:", "--surface:", "--surface-2:", "--line:", "--line-soft:",
		"--text:", "--dim:", "--faint:", "--accent:", "--accent-ink:",
		"--accent-soft:", "--ok:", "--ok-soft:", "--warn:", "--warn-soft:",
		"--danger:", "--danger-soft:", "--shadow:",
	} {
		if !strings.Contains(bare, token) {
			t.Errorf("token %s is not defined on bare :root", token)
		}
	}

	// The dark media query must not beat an explicit light choice.
	if !strings.Contains(css, `:root:not([data-theme="light"])`) {
		t.Error("the dark media query is not guarded against an explicit light theme")
	}
	// The toggle must win in the other direction too.
	if !strings.Contains(css, `:root[data-theme="dark"]`) {
		t.Error("there is no explicit dark theme block")
	}
	// A transparent body borrows the host's ground.
	if !strings.Contains(css, "background: var(--bg)") {
		t.Error("body does not paint an explicit background token")
	}
}
```

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/server/... -v`
Expected: PASS

- [ ] **Step 7: Check it against the approved design**

Run: `go run . --fake --config=flashcart.toml.example --listen=:8474 serve`

Step through every scenario in the dropdown and compare against the approved mockup. Check in both light and dark, and at a narrow viewport where the tree grid collapses to one column. Confirm specifically:

- BIOS shows only `↓`, Saves only `↑`, ROMs shows both directions
- the five passes are listed and dimmed before any run
- the drift block is bordered in red, and its button stays disabled until a row is ticked
- the offline scenario reads as normal rather than as an error

- [ ] **Step 8: Commit**

```bash
git add internal/server/assets/ internal/server/server_test.go
git commit -m "Add embedded single-page UI

Direction-forward manifest: each tree renders one row per pass with an
explicit arrow, so the ROMs content-pull/metadata-push split is visible
at a glance. Drift is quarantined behind an explicit tick and confirm."
```

---

### Task 14: CLI wiring and fake-mode guardrails

Logic lives in `internal/cli` so it can be tested; `main.go` is a three-line shim.

**Files:**
- Create: `internal/buildinfo/buildinfo.go`, `internal/cli/cli.go`, `internal/cli/cli_test.go`, `main.go`

**Interfaces:**
- Consumes: everything from Tasks 1, 5, 7, 8, 11, 12, 13
- Produces: `buildinfo.Version`, `func cli.Run(args []string, stdout, stderr io.Writer) int`, `cli.Options{Command, ConfigPath, Fake, Listen, RsyncBinary string}`, `func cli.Parse(args []string) (cli.Options, error)`

- [ ] **Step 1: Write buildinfo**

Create `internal/buildinfo/buildinfo.go`:

```go
// Package buildinfo carries the version stamped in at link time.
package buildinfo

// Version is overridden by the release build with -ldflags.
var Version = "dev"
```

- [ ] **Step 2: Write the failing test**

Create `internal/cli/cli_test.go`:

```go
package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDefaults(t *testing.T) {
	o, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.Command != "serve" {
		t.Errorf("Command = %q, want serve", o.Command)
	}
	if o.Fake != "" {
		t.Errorf("Fake = %q, want empty by default", o.Fake)
	}
	if o.ConfigPath != DefaultConfigPath {
		t.Errorf("ConfigPath = %q, want %q", o.ConfigPath, DefaultConfigPath)
	}
}

func TestParseFakeScenario(t *testing.T) {
	o, err := Parse([]string{"--fake=drift"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Fake != "drift" {
		t.Errorf("Fake = %q", o.Fake)
	}
}

// A bare --fake means the default scenario rather than an empty one.
func TestBareFakeFlagPicksASensibleScenario(t *testing.T) {
	o, err := Parse([]string{"--fake"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Fake != "steady" {
		t.Errorf("Fake = %q, want steady", o.Fake)
	}
}

func TestParseSubcommands(t *testing.T) {
	for _, cmd := range []string{"serve", "version", "install-service", "uninstall-service", "update"} {
		o, err := Parse([]string{cmd})
		if err != nil {
			t.Fatalf("Parse(%q): %v", cmd, err)
		}
		if o.Command != cmd {
			t.Errorf("Command = %q, want %q", o.Command, cmd)
		}
	}
}

func TestParseRejectsUnknownSubcommand(t *testing.T) {
	if _, err := Parse([]string{"frobnicate"}); err == nil {
		t.Fatal("Parse accepted an unknown subcommand")
	}
}

func TestVersionCommandPrints(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"version"}, &out, &errOut); code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, errOut.String())
	}
	if out.Len() == 0 {
		t.Error("version printed nothing")
	}
}

func TestUnknownSubcommandExitsNonZero(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"frobnicate"}, &out, &errOut); code == 0 {
		t.Error("unknown subcommand exited zero")
	}
	if !strings.Contains(errOut.String(), "frobnicate") {
		t.Errorf("stderr does not name the bad subcommand: %s", errOut.String())
	}
}

func TestBadFakeScenarioExitsNonZero(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"--fake=nonsense", "serve"}, &out, &errOut); code == 0 {
		t.Error("an unknown fake scenario exited zero")
	}
}

// Fake mode is a flag only. A config file must never be able to turn it on,
// because the config file is the thing that lives on the box.
func TestConfigFileCannotEnableFakeMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flashcart.toml")
	body := `
fake = true
fake_mode = "seed"

[nas]
host = "10.132.1.25"

[trees.roms]
export = "/volume2/retrogaming/roms"
local = "/userdata/roms"

[trees.bios]
export = "/volume2/retrogaming/bios"
local = "/userdata/bios"

[trees.saves]
export = "/volume2/retrogaming/saves"
local = "/userdata/saves"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	o, err := Parse([]string{"--config=" + path, "serve"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Fake != "" {
		t.Fatalf("Fake = %q: a config file enabled fake mode", o.Fake)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/cli/ -v`
Expected: FAIL, `undefined: Parse`

- [ ] **Step 4: Write the implementation**

Create `internal/cli/cli.go`:

```go
// Package cli parses arguments and wires dependencies. Keeping this out of
// main makes the wiring, and its guardrails, testable.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/adamcarlile/flashcart/internal/buildinfo"
	"github.com/adamcarlile/flashcart/internal/config"
	"github.com/adamcarlile/flashcart/internal/fake"
	"github.com/adamcarlile/flashcart/internal/nas"
	"github.com/adamcarlile/flashcart/internal/plan"
	"github.com/adamcarlile/flashcart/internal/runner"
	"github.com/adamcarlile/flashcart/internal/server"
	"github.com/adamcarlile/flashcart/internal/server/assets"
	"github.com/adamcarlile/flashcart/internal/service"
)

// DefaultConfigPath is under /userdata because the Batocera root filesystem
// is a read-only squashfs that is reset on OS update.
const DefaultConfigPath = "/userdata/system/flashcart/flashcart.toml"

// defaultFakeScenario is used when --fake is given without a value.
const defaultFakeScenario = string(fake.ScenarioSteady)

var commands = map[string]bool{
	"serve":             true,
	"version":           true,
	"install-service":   true,
	"uninstall-service": true,
	"update":            true,
}

// Options is the parsed command line.
type Options struct {
	Command     string
	ConfigPath  string
	Fake        string
	Listen      string
	RsyncBinary string
}

// Parse reads arguments. Fake mode is deliberately reachable only from here:
// there is no configuration key for it, so editing a file on the box cannot
// turn it on.
func Parse(args []string) (Options, error) {
	var o Options
	fs := flag.NewFlagSet("flashcart", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.ConfigPath, "config", DefaultConfigPath, "path to flashcart.toml")
	fs.StringVar(&o.Listen, "listen", "", "override the configured listen address")
	fs.StringVar(&o.RsyncBinary, "rsync", "rsync", "path to the rsync binary")
	fakeFlag := fs.String("fake", "", "run against the scripted fake backend (scenario name)")

	if err := fs.Parse(args); err != nil {
		return Options{}, err
	}

	// flag records an empty string both for an absent flag and for a bare
	// "--fake", so distinguish them by inspecting what was actually set.
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "fake" && *fakeFlag == "" {
			*fakeFlag = defaultFakeScenario
		}
	})
	o.Fake = *fakeFlag

	o.Command = "serve"
	if rest := fs.Args(); len(rest) > 0 {
		if !commands[rest[0]] {
			return Options{}, fmt.Errorf("unknown subcommand %q", rest[0])
		}
		o.Command = rest[0]
	}
	return o, nil
}

// Run executes the command and returns a process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	o, err := Parse(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	switch o.Command {
	case "version":
		fmt.Fprintln(stdout, buildinfo.Version)
		return 0
	case "install-service":
		if err := service.Install(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "service installed and enabled")
		return 0
	case "uninstall-service":
		if err := service.Uninstall(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "service removed")
		return 0
	case "update":
		if err := selfUpdate(stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}

	if err := serve(o, stdout); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func serve(o Options, stdout io.Writer) error {
	cfg, err := config.Load(o.ConfigPath)
	if err != nil {
		return err
	}
	if o.Listen != "" {
		cfg.Server.Listen = o.Listen
	}

	opts := server.Options{
		Cfg:     cfg,
		Version: buildinfo.Version,
		Assets:  assets.FS,
	}

	if o.Fake != "" {
		b, err := fake.New(fake.Scenario(o.Fake))
		if err != nil {
			return err
		}
		opts.Provider, opts.Runner, opts.Free, opts.Fake = b, b, b.FreeSpace, b
		fmt.Fprintf(stdout, "FAKE MODE (%s): nothing will be mounted and no data will move\n", o.Fake)
	} else {
		opts.Provider = nas.NewNFS(cfg)
		opts.Runner = runner.NewExec(o.RsyncBinary)
		opts.Free = plan.FreeSpace
	}

	srv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           server.New(opts),
		ReadHeaderTimeout: 10 * time.Second,
	}
	fmt.Fprintf(stdout, "flashcart %s listening on %s\n", buildinfo.Version, cfg.Server.Listen)

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
```

- [ ] **Step 5: Add a temporary self-update stub**

Task 16 replaces this. Append to `internal/cli/cli.go`:

```go
// selfUpdate is implemented in Task 16.
func selfUpdate(stdout io.Writer) error {
	return errors.New("self-update is not available in this build")
}
```

- [ ] **Step 6: Write main.go**

Create `main.go`:

```go
// Command flashcart maintains a local mirror of a ROM library that normally
// lives on an NFS share, so a Batocera box works with no network attached.
package main

import (
	"os"

	"github.com/adamcarlile/flashcart/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
```

- [ ] **Step 7: Run the tests**

Run: `go build ./... && go test ./internal/cli/ -v`
Expected: PASS. `service.Install` and `service.Uninstall` do not exist yet, so this step depends on Task 15. Implement Task 15 first if the build fails on those symbols.

- [ ] **Step 8: Try it by hand**

Run: `go run . --fake --config=flashcart.toml.example --listen=:8474 serve`
Then open `http://localhost:8474`. Confirm the fake banner is visible, the scenario dropdown switches states, Plan populates the three cards, and Sync animates the progress bars.

- [ ] **Step 9: Commit**

```bash
git add internal/cli/ internal/buildinfo/ main.go
git commit -m "Add CLI wiring with fake mode as a flag-only option

Fake mode has no configuration key, so a file on the box cannot enable it."
```

---

### Task 15: Batocera service integration

Batocera has no systemd. Since v38, startup scripts live in `/userdata/system/services/` and are toggled from EmulationStation under Settings, Services.

**Files:**
- Create: `internal/service/service.go`, `internal/service/service_test.go`

**Interfaces:**
- Consumes: nothing
- Produces: `func service.Install() error`, `func service.Uninstall() error`, `func service.Script(binary, config string) string`, `const service.Name`, `const service.Dir`

- [ ] **Step 1: Write the failing test**

Create `internal/service/service_test.go`:

```go
package service

import (
	"strings"
	"testing"
)

func TestScriptHandlesStartAndStop(t *testing.T) {
	s := Script("/userdata/system/flashcart/flashcart", "/userdata/system/flashcart/flashcart.toml")
	for _, want := range []string{"start)", "stop)", "restart)", "status)"} {
		if !strings.Contains(s, want) {
			t.Errorf("service script has no %q case", want)
		}
	}
}

func TestScriptIsShebangedAndQuoted(t *testing.T) {
	s := Script("/userdata/system/flashcart/flashcart", "/userdata/system/flashcart/flashcart.toml")
	if !strings.HasPrefix(s, "#!/bin/sh") {
		t.Errorf("script does not start with a shebang: %.20q", s)
	}
	// Paths must be quoted so a directory containing a space cannot split
	// into two arguments.
	if !strings.Contains(s, `"/userdata/system/flashcart/flashcart"`) {
		t.Error("binary path is not quoted in the script")
	}
	if !strings.Contains(s, `"/userdata/system/flashcart/flashcart.toml"`) {
		t.Error("config path is not quoted in the script")
	}
}

// The service must never start the application in fake mode.
func TestScriptNeverEnablesFakeMode(t *testing.T) {
	s := Script("/userdata/system/flashcart/flashcart", "/userdata/system/flashcart/flashcart.toml")
	if strings.Contains(s, "--fake") || strings.Contains(s, "-fake") {
		t.Error("the installed service script passes --fake")
	}
}

func TestScriptUsesTheServiceName(t *testing.T) {
	s := Script("/bin/flashcart", "/etc/flashcart.toml")
	if !strings.Contains(s, Name) {
		t.Errorf("script never mentions the service name %q", Name)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/service/ -v`
Expected: FAIL, `undefined: Script`

- [ ] **Step 3: Write the implementation**

Create `internal/service/service.go`:

```go
// Package service installs flashcart as a Batocera service.
//
// Batocera has no systemd. Since v38, executable scripts in
// /userdata/system/services are started at boot and can be toggled from
// EmulationStation under Settings, Services.
package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	// Name is how the service appears in EmulationStation.
	Name = "flashcart"
	// Dir is where Batocera looks for service scripts.
	Dir = "/userdata/system/services"
	// InstallDir holds the binary and config. It is under /userdata so it
	// survives Batocera OS updates.
	InstallDir = "/userdata/system/flashcart"
)

// Script renders the service script. Every path is quoted so a directory
// containing a space cannot split into two arguments, and fake mode is never
// passed: it is a development flag only.
func Script(binary, config string) string {
	return fmt.Sprintf(`#!/bin/sh
# flashcart — mirrors the ROM library locally so the box works offline.
# Managed by "flashcart install-service". Toggle from EmulationStation:
# Settings, Services, %s.

BIN=%q
CFG=%q
LOG="/userdata/system/logs/%s.log"
PIDFILE="/var/run/%s.pid"

start() {
    mkdir -p "$(dirname "$LOG")"
    "$BIN" --config="$CFG" serve >>"$LOG" 2>&1 &
    echo $! > "$PIDFILE"
}

stop() {
    [ -f "$PIDFILE" ] || return 0
    kill "$(cat "$PIDFILE")" 2>/dev/null
    rm -f "$PIDFILE"
}

status() {
    [ -f "$PIDFILE" ] || { echo "stopped"; return 1; }
    kill -0 "$(cat "$PIDFILE")" 2>/dev/null && echo "running" || { echo "stopped"; return 1; }
}

case "$1" in
    start)   start ;;
    stop)    stop ;;
    restart) stop; start ;;
    status)  status ;;
    *)       echo "usage: $0 {start|stop|restart|status}" >&2; exit 1 ;;
esac
`, Name, binary, config, Name, Name)
}

// Install writes the service script and enables it.
func Install() error {
	binary, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate the running binary: %w", err)
	}
	cfgPath := filepath.Join(InstallDir, "flashcart.toml")

	if err := os.MkdirAll(Dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", Dir, err)
	}
	target := filepath.Join(Dir, Name)
	if err := os.WriteFile(target, []byte(Script(binary, cfgPath)), 0o755); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}

	// batocera-services is absent on a development machine, so a failure
	// here is reported without undoing the script that was written.
	if _, err := exec.LookPath("batocera-services"); err == nil {
		if out, err := exec.Command("batocera-services", "enable", Name).CombinedOutput(); err != nil {
			return fmt.Errorf("batocera-services enable %s: %w: %s", Name, err, out)
		}
	}
	return nil
}

// Uninstall stops the service and removes the script.
func Uninstall() error {
	if _, err := exec.LookPath("batocera-services"); err == nil {
		exec.Command("batocera-services", "stop", Name).Run()
		exec.Command("batocera-services", "disable", Name).Run()
	}
	target := filepath.Join(Dir, Name)
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", target, err)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/service/ ./internal/cli/ -v && go build ./...`
Expected: PASS, build clean

- [ ] **Step 5: Commit**

```bash
git add internal/service/
git commit -m "Add Batocera service installation

Writes a toggleable script to /userdata/system/services and enables it
via batocera-services. The script never passes --fake."
```

---

### Task 16: Releases, self-update, installer and README

**Files:**
- Create: `internal/update/update.go`, `internal/update/update_test.go`, `.goreleaser.yaml`, `.github/workflows/ci.yml`, `.github/workflows/release.yml`, `install.sh`, `README.md`, `.gitignore`
- Modify: `internal/cli/cli.go` (replace the `selfUpdate` stub)

**Interfaces:**
- Consumes: `buildinfo.Version` (Task 14), `service.InstallDir`, `service.Name` (Task 15)
- Produces: `func update.Latest(ctx context.Context, repo string) (update.Release, error)`, `update.Release{Tag string, Assets map[string]string}`, `func update.VerifyAndSwap(binaryPath string, payload []byte, wantSHA string) error`, `func update.ParseChecksums(body string) map[string]string`

- [ ] **Step 1: Write the failing test**

Create `internal/update/update_test.go`:

```go
package update

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseChecksums(t *testing.T) {
	body := `
a1b2c3  flashcart_linux_amd64
d4e5f6  flashcart_linux_arm64
`
	got := ParseChecksums(body)
	if got["flashcart_linux_amd64"] != "a1b2c3" {
		t.Errorf("amd64 = %q", got["flashcart_linux_amd64"])
	}
	if len(got) != 2 {
		t.Errorf("parsed %d entries, want 2", len(got))
	}
}

func TestVerifyAndSwapReplacesBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flashcart")
	if err := os.WriteFile(path, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	payload := []byte("new binary")
	sum := sha256.Sum256(payload)

	if err := VerifyAndSwap(path, payload, hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("VerifyAndSwap: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new binary" {
		t.Errorf("binary content = %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Error("replaced binary is not executable")
	}
}

// A mismatched checksum must leave the running binary untouched.
func TestVerifyAndSwapRefusesBadChecksum(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flashcart")
	if err := os.WriteFile(path, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := VerifyAndSwap(path, []byte("tampered"), strings.Repeat("0", 64))
	if err == nil {
		t.Fatal("VerifyAndSwap accepted a mismatched checksum")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "old" {
		t.Error("the existing binary was replaced despite a checksum mismatch")
	}
	// No debris left behind.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temporary files left behind: %d entries", len(entries))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/update/ -v`
Expected: FAIL, `undefined: ParseChecksums`

- [ ] **Step 3: Write the implementation**

Create `internal/update/update.go`:

```go
// Package update fetches and installs newer releases, verifying SHA-256
// before replacing the running binary.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Release is a published GitHub release.
type Release struct {
	Tag string
	// Assets maps asset name to download URL.
	Assets map[string]string
}

var client = &http.Client{Timeout: 60 * time.Second}

// Latest returns the newest release for a repo such as "adamcarlile/flashcart".
func Latest(ctx context.Context, repo string) (Release, error) {
	url := "https://api.github.com/repos/" + repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("fetch latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("fetch latest release: HTTP %d", resp.StatusCode)
	}

	var payload struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Release{}, err
	}

	rel := Release{Tag: payload.TagName, Assets: map[string]string{}}
	for _, a := range payload.Assets {
		rel.Assets[a.Name] = a.URL
	}
	return rel, nil
}

// Fetch downloads an asset.
func Fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// ParseChecksums reads a GoReleaser checksums.txt into name to hex digest.
func ParseChecksums(body string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		out[strings.TrimPrefix(fields[1], "*")] = fields[0]
	}
	return out
}

// VerifyAndSwap checks the payload against wantSHA and, only if it matches,
// atomically replaces the binary. On any failure the existing binary is left
// exactly as it was, with no debris beside it.
func VerifyAndSwap(binaryPath string, payload []byte, wantSHA string) error {
	sum := sha256.Sum256(payload)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, wantSHA) {
		return fmt.Errorf("checksum mismatch: got %s, want %s", got, wantSHA)
	}

	dir := filepath.Dir(binaryPath)
	tmp, err := os.CreateTemp(dir, ".flashcart-update-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}

	if _, err := tmp.Write(payload); err != nil {
		cleanup()
		return fmt.Errorf("write update: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		cleanup()
		return fmt.Errorf("chmod update: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close update: %w", err)
	}

	// Rename within the same directory is atomic, so the binary is never
	// half-written.
	if err := os.Rename(tmpName, binaryPath); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("replace %s: %w", binaryPath, err)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/update/ -v`
Expected: PASS

- [ ] **Step 5: Replace the self-update stub**

In `internal/cli/cli.go`, delete the stub `selfUpdate` from Task 14 and add:

```go
// Repo is the GitHub repository self-update reads releases from.
const Repo = "adamcarlile/flashcart"

func selfUpdate(stdout io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	rel, err := update.Latest(ctx, Repo)
	if err != nil {
		return err
	}
	if strings.TrimPrefix(rel.Tag, "v") == strings.TrimPrefix(buildinfo.Version, "v") {
		fmt.Fprintf(stdout, "already on %s\n", buildinfo.Version)
		return nil
	}

	asset := fmt.Sprintf("flashcart_%s_%s", runtime.GOOS, runtime.GOARCH)
	binURL, ok := rel.Assets[asset]
	if !ok {
		return fmt.Errorf("release %s has no asset %q", rel.Tag, asset)
	}
	sumsURL, ok := rel.Assets["checksums.txt"]
	if !ok {
		return fmt.Errorf("release %s has no checksums.txt", rel.Tag)
	}

	fmt.Fprintf(stdout, "downloading %s (%s)\n", asset, rel.Tag)
	sumsBody, err := update.Fetch(ctx, sumsURL)
	if err != nil {
		return err
	}
	payload, err := update.Fetch(ctx, binURL)
	if err != nil {
		return err
	}

	sums := update.ParseChecksums(string(sumsBody))
	want, ok := sums[asset]
	if !ok {
		return fmt.Errorf("checksums.txt has no entry for %q", asset)
	}

	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err := update.VerifyAndSwap(self, payload, want); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "updated to %s\n", rel.Tag)

	if _, err := exec.LookPath("batocera-services"); err == nil {
		exec.Command("batocera-services", "restart", service.Name).Run()
		fmt.Fprintln(stdout, "service restarted")
	}
	return nil
}
```

Add the imports `"context"`, `"os"`, `"os/exec"`, `"runtime"`, `"strings"`, `"time"` and `"github.com/adamcarlile/flashcart/internal/update"` to `internal/cli/cli.go`.

- [ ] **Step 6: Write the release configuration**

Create `.goreleaser.yaml`. Only Linux is built: Batocera is the sole deployment target.

```yaml
version: 2

builds:
  - id: flashcart
    main: .
    binary: flashcart
    env:
      - CGO_ENABLED=0
    goos: [linux]
    goarch: [amd64, arm64]
    ldflags:
      - -s -w -X github.com/adamcarlile/flashcart/internal/buildinfo.Version={{.Version}}

archives:
  - id: bare
    formats: [binary]
    name_template: "flashcart_{{ .Os }}_{{ .Arch }}"

checksum:
  name_template: checksums.txt

release:
  draft: false
```

Create `.gitignore`:

```
/flashcart
/dist/
*.test
```

- [ ] **Step 7: Write the workflows**

Create `.github/workflows/ci.yml`:

```yaml
name: ci
on:
  push:
    branches: [main]
  pull_request:

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.22"
      # The pass package runs real rsync to prove the filters agree with the
      # classifier, so it must be installed.
      - run: sudo apt-get update && sudo apt-get install -y rsync
      - run: go vet ./...
      - run: go test ./... -race
```

Create `.github/workflows/release.yml`:

```yaml
name: release
on:
  push:
    tags: ["v*"]

permissions:
  contents: write

jobs:
  goreleaser:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - uses: actions/setup-go@v5
        with:
          go-version: "1.22"
      - uses: goreleaser/goreleaser-action@v6
        with:
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

- [ ] **Step 8: Write the installer**

Create `install.sh`:

```sh
#!/bin/sh
# flashcart installer, for the Batocera box.
#   curl -sSL https://raw.githubusercontent.com/adamcarlile/flashcart/main/install.sh | sh
set -eu

REPO="adamcarlile/flashcart"
INSTALL_DIR="/userdata/system/flashcart"
BIN="$INSTALL_DIR/flashcart"
CFG="$INSTALL_DIR/flashcart.toml"

case "$(uname -m)" in
    x86_64)  ARCH=amd64 ;;
    aarch64) ARCH=arm64 ;;
    *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
ASSET="flashcart_linux_${ARCH}"

echo "==> fetching the latest release"
TAG=$(wget -qO- "https://api.github.com/repos/$REPO/releases/latest" \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)
[ -n "$TAG" ] || { echo "could not determine the latest release" >&2; exit 1; }
echo "    $TAG"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

BASE="https://github.com/$REPO/releases/download/$TAG"
wget -qO "$TMP/$ASSET" "$BASE/$ASSET"
wget -qO "$TMP/checksums.txt" "$BASE/checksums.txt"

echo "==> verifying checksum"
WANT=$(grep " $ASSET\$" "$TMP/checksums.txt" | awk '{print $1}')
GOT=$(sha256sum "$TMP/$ASSET" | awk '{print $1}')
[ "$WANT" = "$GOT" ] || { echo "checksum mismatch: got $GOT, want $WANT" >&2; exit 1; }

echo "==> installing to $INSTALL_DIR"
mkdir -p "$INSTALL_DIR"
install -m 0755 "$TMP/$ASSET" "$BIN"

if [ ! -f "$CFG" ]; then
    echo "==> writing a default config to $CFG"
    cat > "$CFG" <<'TOML'
[nas]
host = "10.132.1.25"

[server]
listen = ":8474"

[trees.roms]
export = "/volume2/retrogaming/roms"
local = "/userdata/roms"

[trees.bios]
export = "/volume2/retrogaming/bios"
local = "/userdata/bios"

[trees.saves]
export = "/volume2/retrogaming/saves"
local = "/userdata/saves"
TOML
    echo "    review it before starting the service"
else
    echo "==> keeping the existing config at $CFG"
fi

echo "==> installing the service"
"$BIN" install-service

echo
echo "flashcart $TAG installed."
echo "UI: http://$(hostname):8474"
echo "Update later with: $BIN update"
```

- [ ] **Step 9: Write the README**

Create `README.md`:

```markdown
# flashcart

Keeps a complete local copy of a Batocera ROM library that normally lives on
an NFS share, so the box works with no network attached.

*A flashcart is the thing you load your library onto at home so it plays
anywhere.*

Sibling to [roadie](https://github.com/adamcarlile/roadie), which does the
equivalent job for an in-car media server.

## How it works

The NAS is no longer mounted at boot. flashcart mounts it only for the
duration of a run, reconciles the two copies, and unmounts. Nothing about
booting depends on the network.

Five rsync passes run in order:

1. BIOS pull
2. ROM content pull
3. ROM metadata pull, `--ignore-existing`
4. ROM metadata push
5. Saves push

Directions differ per file class because two different things share the ROMs
tree. ROM binaries are NAS-owned, since that is where new games are added.
`gamelist.xml` and scraped media are box-owned, because EmulationStation
rewrites them as you play and scrape. Getting this backwards silently
destroys play counts and favourites.

Every pass is additive. Anything present on a destination but absent from its
source is surfaced as drift and deleted only when you tick it and confirm.
`rsync --delete` is never used for a real transfer.

## Constraint

**Scrape on the box, not on the NAS.** Pointing Skraper at the share from a
desktop would have its entries overwritten by the next metadata push.

## Install

On the Batocera box:

```sh
curl -sSL https://raw.githubusercontent.com/adamcarlile/flashcart/main/install.sh | sh
```

Then edit `/userdata/system/flashcart/flashcart.toml` and enable the service
from EmulationStation under Settings, Services, flashcart. The UI is at
`http://<box>:8474`.

## Update

```sh
/userdata/system/flashcart/flashcart update
```

Downloads the latest release, verifies its SHA-256, swaps the binary
atomically and restarts the service.

## Development

Fake mode runs the whole application with no NAS, no Batocera box and no data:

```sh
go run . --fake --config=flashcart.toml.example --listen=:8474 serve
```

Scenarios are switchable live from the UI: `seed`, `steady`, `drift`,
`offline`, `nospace`, `failure`. Only the far side of the `nas.Provider` and
`runner.Runner` seams is scripted, so the server, plan, sync and drift code
being exercised is the real thing.

```sh
go test ./... -race
```

The `internal/pass` integration tests run real rsync over a fixture tree to
prove the filter rules agree with `paths.Classify`.

## Docs

- [Design spec](docs/superpowers/specs/2026-08-20-flashcart-design.md)
- [Implementation plan](docs/superpowers/plans/2026-08-20-flashcart.md)
```

- [ ] **Step 10: Run everything**

Run: `go build ./... && go vet ./... && go test ./... -race`
Expected: PASS

- [ ] **Step 11: Commit**

```bash
git add internal/update/ internal/cli/cli.go .goreleaser.yaml .github/ install.sh README.md .gitignore
git commit -m "Add releases, checksum-verified self-update, installer and README"
```

---

### Task 17: Cutover on the box

Operational rather than code. The boot config is edited **last**: until that point every step is undone by a reboot.

Do not start this until `go test ./... -race` is green and fake mode has been driven through every scenario.

**Files:**
- Create: `docs/runbook.md` (a record of what was actually done and observed)

- [ ] **Step 1: Install, with boot behaviour unchanged**

```bash
ssh root@10.132.1.151 'curl -sSL https://raw.githubusercontent.com/adamcarlile/flashcart/main/install.sh | sh'
ssh root@10.132.1.151 'cat /userdata/system/flashcart/flashcart.toml'
```

Confirm the config matches the real exports: host `10.132.1.25`, exports `/volume2/retrogaming/{roms,bios,saves}`, locals `/userdata/{roms,bios,saves}`.

Boot behaviour is untouched at this point. Rollback: `rm -rf /userdata/system/flashcart /userdata/system/services/flashcart`.

- [ ] **Step 2: Confirm the status page loads and sees the NAS**

```bash
ssh root@10.132.1.151 'batocera-services start flashcart'
curl -s http://10.132.1.151:8474/api/status
```

Expected: `"reachable":true`, `"fake":false`.

- [ ] **Step 3: Expose the empty local directories**

The three paths are currently NFS mount points shadowing empty local directories. Unmounting reveals the local ext4 underneath. Stop EmulationStation first so nothing is reading the tree.

```bash
ssh root@10.132.1.151 '
  batocera-es-swissknife --emukill 2>/dev/null
  /etc/init.d/S31emulationstation stop 2>/dev/null
  umount /userdata/roms /userdata/bios /userdata/saves
  mount | grep -c nfs4
  ls /userdata/roms | wc -l
'
```

Expected: zero NFS mounts remain, and `/userdata/roms` is empty.

**Rollback at any point from here: `reboot`.** The boot config still mounts NFS exactly as before.

- [ ] **Step 4: Run the first seed**

Plan first, and read it before syncing.

```bash
curl -s -X POST http://10.132.1.151:8474/api/plan | head -c 2000
```

Expected: roughly 93 GB incoming, `"sufficient":true`, and **zero drift**. Non-empty drift on a seed run means the projected-state calculation from Task 8 is wrong. Stop and fix it rather than proceeding.

```bash
curl -s -X POST http://10.132.1.151:8474/api/sync
```

Watch progress in the browser at `http://10.132.1.151:8474`. Budget 30 to 60 minutes: the 34 GB of PS3 streams near line rate, the roughly half a million small scraper PNGs do not.

- [ ] **Step 5: Verify the copy**

```bash
ssh root@10.132.1.151 '
  du -sh /userdata/roms /userdata/bios /userdata/saves
  ls /userdata/roms | wc -l
  ls /userdata/roms/*/gamelist.xml | wc -l
  grep -c "<playcount>" /userdata/roms/snes/gamelist.xml
  df -h /userdata | tail -1
'
```

Expected: roms about 91.6 G, bios about 579 M, saves about 654 M, 24 gamelists, and the snes play counts intact. Then start EmulationStation, confirm the systems populate, launch a PS2 game, and load an existing save.

- [ ] **Step 6: Edit the boot config**

Only now, and only if step 5 was clean.

```bash
ssh root@10.132.1.151 '
  mount -o remount,rw /boot
  cp /boot/batocera-boot.conf /boot/batocera-boot.conf.pre-flashcart
  sed -i "s/^sharedevice=NETWORK/sharedevice=INTERNAL/" /boot/batocera-boot.conf
  sed -i "s/^sharenetwork_nfs/#sharenetwork_nfs/" /boot/batocera-boot.conf
  grep -iE "^[^#]*share" /boot/batocera-boot.conf
  mount -o remount,ro /boot
'
```

Expected: only `sharedevice=INTERNAL` remains uncommented.

**Rollback:** remount `/boot` read-write, `cp /boot/batocera-boot.conf.pre-flashcart /boot/batocera-boot.conf`, reboot. This shadows the local copy rather than deleting it, so nothing is lost in either direction.

- [ ] **Step 7: Reboot and verify**

```bash
ssh root@10.132.1.151 'reboot'
```

Then, once it is back:

```bash
ssh root@10.132.1.151 'mount | grep -c nfs4; ls /userdata/roms | wc -l'
```

Expected: **zero** NFS mounts, and the ROM tree still fully populated.

- [ ] **Step 8: The actual test**

Pull the network cable, reboot, and confirm the box comes up to a complete library with no hang. The old `hard` NFS mounts would have wedged here; that is the failure this whole project removes.

Then take it out, play something, bring it home, and run a sync. Confirm the new save appears on the NAS:

```bash
ssh root@10.132.1.151 'ls -la --time-style=long-iso /userdata/saves/snes | head'
```

- [ ] **Step 9: Record what happened**

Write `docs/runbook.md` with the date, the observed seed duration, the final `du -sh` figures, anything that differed from this plan, and the rollback commands, so the next person (probably you, in a year) does not have to re-derive any of it.

- [ ] **Step 10: Commit**

```bash
git add docs/runbook.md
git commit -m "Record the cutover runbook and observed results"
```

---

## Spec coverage notes

Two spec requirements are satisfied by a combination of tasks rather than a single named test, recorded here so a reviewer does not read them as gaps.

**"A locally deleted ROM is reported as drift and is not deleted from the NAS."** Reporting is `TestGenuineDriftIsReported` (Task 8). The not-deleted half is proven more strongly by `TestRealRunsNeverDelete` (Task 4): no real transfer can carry `--delete` at all, so no pass can delete anything on either side. Deletion exists only in `internal/drift`, reachable only through an explicit confirmed path list.

**"Unmount always happens, including on failure or panic."** Implemented as the deferred `unmount()` inside `App.withMounts` (Task 12), which every mounting handler routes through. There is no code path that mounts without it.

## Deviations from the spec

**`internal/update` was split out of `internal/buildinfo`.** The spec listed self-update under `buildinfo`. Version stamping and release downloading have nothing in common beyond both mentioning versions, and the update code needs network mocking that a version constant does not. `buildinfo` stays a single variable; `update` carries the fetch, verify and swap logic.

**Releases are Linux only.** The spec did not name target platforms. Batocera on the x86_64 mini PC is the sole deployment target, so `.goreleaser.yaml` builds `linux/amd64` and `linux/arm64` and nothing else. `internal/plan.FreeSpace` is correspondingly Linux-only, with a build-tagged stub that returns an error elsewhere so the package still compiles on a Mac.
