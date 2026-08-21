package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adamcarlile/flashcart/internal/factory"
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
		"no host":         {strings.Replace(valid, `host = "10.132.1.25"`, `host = ""`, 1), "nas.host"},
		"relative export": {strings.Replace(valid, `export = "/volume2/retrogaming/roms"`, `export = "retrogaming/roms"`, 1), "must be absolute"},
		"relative local":  {strings.Replace(valid, `local = "/userdata/bios"`, `local = "userdata/bios"`, 1), "must be absolute"},
		"missing saves":   {strings.Split(valid, "[trees.saves]")[0], "trees.saves"},
		"duplicate local": {strings.Replace(valid, `local = "/userdata/bios"`, `local = "/userdata/roms"`, 1), "duplicate"},
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

func TestFactoryRootDefaultsToTheApplianceSkeleton(t *testing.T) {
	cfg, err := Load(write(t, valid))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.FactoryRootPath(); got != factory.DefaultRoot {
		t.Errorf("FactoryRootPath() = %q, want %q", got, factory.DefaultRoot)
	}
}

func TestFactoryRootHonoursAnExplicitPath(t *testing.T) {
	cfg, err := Load(write(t, "factory_root = \"/opt/datainit\"\n"+valid))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.FactoryRootPath(); got != "/opt/datainit" {
		t.Errorf("FactoryRootPath() = %q, want /opt/datainit", got)
	}
}

// An empty string is the off switch: it must survive applyDefaults rather
// than being silently replaced by the default path.
func TestFactoryRootEmptyStringDisablesTheExclusion(t *testing.T) {
	cfg, err := Load(write(t, "factory_root = \"\"\n"+valid))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.FactoryRootPath(); got != "" {
		t.Errorf("FactoryRootPath() = %q, want \"\"", got)
	}
}

func TestFactoryRootMustBeAbsolute(t *testing.T) {
	_, err := Load(write(t, "factory_root = \"datainit\"\n"+valid))
	if err == nil || !strings.Contains(err.Error(), "factory_root") {
		t.Errorf("Load error = %v, want one mentioning factory_root", err)
	}
}
