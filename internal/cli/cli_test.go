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
