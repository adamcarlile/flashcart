package service

import (
	"errors"
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

// IMPORTANT 8: stop() waiting only 5s (the old 50 * 0.1s loop bound) is
// shorter than flashcart's own 10s graceful shutdown budget, so a
// perfectly normal slow shutdown gets mistaken for a hang: restart then
// calls start() while the old process is still bound to the port, the new
// process dies with EADDRINUSE, and the pidfile is left pointing at a PID
// that means nothing useful. The wait must exceed 10s.
func TestStopWaitExceedsGracefulShutdownBudget(t *testing.T) {
	s := Script("/userdata/system/flashcart/flashcart", "/userdata/system/flashcart/flashcart.toml")
	if strings.Contains(s, "-lt 50") {
		t.Error("stop() still waits only ~5s (-lt 50 at 0.1s), shorter than the 10s graceful shutdown budget")
	}
	if !strings.Contains(s, "-lt 120") {
		t.Error("stop() does not wait past the 10s graceful shutdown budget (want a loop bound of at least 120 at 0.1s)")
	}
}

// IMPORTANT 8: a process that outlives the wait above is genuinely stuck,
// not just slow, and must be forced rather than left to hang stop()
// (and, transitively, restart) forever.
func TestStopSendsSigkillIfStillRunningPastTheWait(t *testing.T) {
	s := Script("/userdata/system/flashcart/flashcart", "/userdata/system/flashcart/flashcart.toml")
	if !strings.Contains(s, "kill -9") {
		t.Error("stop() never sends SIGKILL to a process that outlives the wait")
	}
}

// IMPORTANT 8: the pidfile must not be removed until the process is
// confirmed gone, or restart's start() sees "not running" and launches a
// second instance while the first might still be shutting down.
func TestStopOnlyRemovesPidfileOnceProcessIsConfirmedGone(t *testing.T) {
	s := Script("/userdata/system/flashcart/flashcart", "/userdata/system/flashcart/flashcart.toml")
	if strings.Contains(s, "done\n    rm -f \"$PIDFILE\"") {
		t.Error("stop() removes the pidfile unconditionally after the wait loop, not gated on the process actually being gone")
	}
	if !strings.Contains(s, `kill -0 "$pid" 2>/dev/null || rm -f "$PIDFILE"`) {
		t.Error("stop() must only remove the pidfile once kill -0 confirms the process is gone")
	}
}

// MINOR 9: the service script must pin rsync's path rather than leaving
// flashcart to a bare PATH lookup in whatever (possibly minimal)
// environment batocera-services provides at boot.
func TestScriptPinsRsyncPath(t *testing.T) {
	s := Script("/userdata/system/flashcart/flashcart", "/userdata/system/flashcart/flashcart.toml")
	if !strings.Contains(s, RsyncPath) {
		t.Errorf("script does not pin rsync to %q", RsyncPath)
	}
	if !strings.Contains(s, "--rsync=") {
		t.Error("script does not pass --rsync to the flashcart binary")
	}
}

// Install must START the service, not merely enable it. batocera-services
// treats those as separate verbs: enable registers the service for the next
// boot, start launches it now. Installing and only enabling leaves the box
// with a dead port until it reboots, while install.sh cheerfully prints a URL.
func TestInstallStartsTheServiceAndNotJustEnablesIt(t *testing.T) {
	var got [][]string
	restore := stubCommands(func(name string, args ...string) ([]byte, error) {
		got = append(got, append([]string{name}, args...))
		return nil, nil
	}, func() (string, error) { return "/userdata/system/flashcart/flashcart", nil },
		t.TempDir())
	defer restore()

	if err := Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}

	var verbs []string
	for _, c := range got {
		if len(c) >= 2 && c[0] == "batocera-services" {
			verbs = append(verbs, c[1])
		}
	}
	if len(verbs) != 2 || verbs[0] != "enable" || verbs[1] != "start" {
		t.Fatalf("batocera-services verbs = %v, want [enable start]", verbs)
	}
}

// A failed start must not be reported as a successful install: the whole
// point of this fix is that the installer stops promising a URL that does
// not answer.
func TestInstallSurfacesAFailedStart(t *testing.T) {
	restore := stubCommands(func(name string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "start" {
			return []byte("could not bind :8474"), errors.New("exit status 1")
		}
		return nil, nil
	}, func() (string, error) { return "/userdata/system/flashcart/flashcart", nil },
		t.TempDir())
	defer restore()

	err := Install()
	if err == nil {
		t.Fatal("Install reported success despite the service failing to start")
	}
	if !strings.Contains(err.Error(), "start") || !strings.Contains(err.Error(), "could not bind") {
		t.Errorf("error should name the start failure and its output, got: %v", err)
	}
}

// stubCommands redirects the package's command seam, its notion of where the
// running binary is, and the directories it writes to, returning a function
// that puts all of them back.
func stubCommands(run func(string, ...string) ([]byte, error), exe func() (string, error), dir string) func() {
	oRun, oLook, oExe, oDir, oInstall := runCommand, lookPath, executable, Dir, InstallDir
	runCommand = run
	lookPath = func(string) (string, error) { return "/usr/bin/batocera-services", nil }
	executable = exe
	Dir = dir
	InstallDir = dir
	return func() {
		runCommand, lookPath, executable, Dir, InstallDir = oRun, oLook, oExe, oDir, oInstall
	}
}

// On a machine without batocera-services, Install writes the script and stops
// cleanly rather than erroring — that is what makes it usable in development.
func TestInstallSkipsServiceCommandsWhenBatoceraServicesIsAbsent(t *testing.T) {
	called := 0
	oLook := lookPath
	restore := stubCommands(func(string, ...string) ([]byte, error) { called++; return nil, nil },
		func() (string, error) { return "/tmp/flashcart", nil }, t.TempDir())
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	defer func() { restore(); lookPath = oLook }()

	if err := Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if called != 0 {
		t.Errorf("ran %d service commands on a machine with no batocera-services", called)
	}
}
