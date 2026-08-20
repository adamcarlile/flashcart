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
