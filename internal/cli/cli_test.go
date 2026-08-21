package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reportRestart must fail the overall update, not just log a warning, when
// a restart was attempted and failed: the operator asked for an update and
// the new binary is staged but never started running.
func TestReportRestartFailurePropagatesAndExplains(t *testing.T) {
	var out bytes.Buffer
	restartErr := errors.New("batocera-services: exit status 1")

	err := reportRestart(&out, true, restartErr)
	if err == nil {
		t.Fatal("reportRestart returned nil for a failed restart")
	}
	if !strings.Contains(err.Error(), "restart failed") {
		t.Errorf("error does not mention the restart failure: %v", err)
	}
	if !strings.Contains(out.String(), "restart failed") {
		t.Errorf("stdout does not report the restart failure: %s", out.String())
	}
	if !strings.Contains(out.String(), "next manual start or reboot") {
		t.Errorf("stdout does not tell the operator what to do next: %s", out.String())
	}
}

// A restart that succeeds is reported and does not fail the command.
func TestReportRestartSuccess(t *testing.T) {
	var out bytes.Buffer
	if err := reportRestart(&out, true, nil); err != nil {
		t.Fatalf("reportRestart: %v", err)
	}
	if !strings.Contains(out.String(), "service restarted") {
		t.Errorf("stdout does not confirm the restart: %s", out.String())
	}
}

// On a development machine (no batocera-services) there is nothing to
// restart. That must not be reported as a restart, successful or otherwise.
func TestReportRestartNotAttempted(t *testing.T) {
	var out bytes.Buffer
	if err := reportRestart(&out, false, nil); err != nil {
		t.Fatalf("reportRestart: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("stdout mentioned a restart that was never attempted: %s", out.String())
	}
}

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

func TestHelpFlagPrintsUsageAndExitsZero(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run([]string{"--help"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %s", code, errOut.String())
	}
	if out.Len() == 0 {
		t.Fatal("--help printed nothing to stdout")
	}
	for _, want := range []string{
		"serve", "version", "install-service", "uninstall-service", "update",
		"--fake", "--config", "--listen",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("usage does not mention %q:\n%s", want, out.String())
		}
	}
	if errOut.Len() != 0 {
		t.Errorf("--help wrote to stderr: %s", errOut.String())
	}
}
