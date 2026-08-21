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

// Fix 1: stop() must wait for process to exit before returning
func TestStopWaitsForProcessExit(t *testing.T) {
	s := Script("/userdata/system/flashcart/flashcart", "/userdata/system/flashcart/flashcart.toml")
	// The wait loop checks if process exists with kill -0
	if !strings.Contains(s, "kill -0") {
		t.Error("stop() does not check if process still exists")
	}
	// Must sleep in the loop
	if !strings.Contains(s, "sleep") {
		t.Error("stop() does not sleep in the wait loop")
	}
}

// Fix 2: start() must be idempotent and guard against double-start
func TestStartGuardsAgainstDoubleStart(t *testing.T) {
	s := Script("/userdata/system/flashcart/flashcart", "/userdata/system/flashcart/flashcart.toml")
	// Must check if already running
	if !strings.Contains(s, "is_running") {
		t.Error("start() does not check if already running")
	}
}

// Fix 3: PID must be validated before trusting it
func TestScriptValidatesPIDBeforeTrusting(t *testing.T) {
	s := Script("/userdata/system/flashcart/flashcart", "/userdata/system/flashcart/flashcart.toml")
	// Must have is_running function
	if !strings.Contains(s, "is_running()") {
		t.Error("script does not define is_running() function")
	}
	// Must check /proc for validation
	if !strings.Contains(s, "/proc") {
		t.Error("script does not validate PID against /proc")
	}
	// Must check comm file
	if !strings.Contains(s, "comm") {
		t.Error("script does not check process comm file")
	}
}
