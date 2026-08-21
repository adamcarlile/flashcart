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

// saves synthesises the box's per-game save files, mirroring the ~0.6 GB
// measured on the real box: one save per game, at realistic save/memory
// card sizes. psx uses the memory-card extension observed on the real
// library; everything else uses the generic emulator save-state extension.
func saves() []runner.Change {
	var out []runner.Change
	for _, s := range library {
		ext := ".srm"
		if s.name == "psx" {
			ext = ".mcd"
		}
		for i := 0; i < s.games; i++ {
			out = append(out, runner.Change{
				Itemize: ">f+++++++++",
				Size:    32 * kib,
				Path:    fmt.Sprintf("%s/Game %03d (USA)%s", s.name, i+1, ext),
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
