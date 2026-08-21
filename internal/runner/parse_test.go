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
		"    1,234,567  42%   11.20MB/s    0:00:31":                     {42, true},
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
