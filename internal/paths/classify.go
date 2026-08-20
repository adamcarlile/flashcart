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
