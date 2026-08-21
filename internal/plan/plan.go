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
