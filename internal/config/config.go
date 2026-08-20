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
