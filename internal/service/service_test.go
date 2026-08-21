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
