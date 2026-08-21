// Package service installs flashcart as a Batocera service.
//
// Batocera has no systemd. Since v38, executable scripts in
// /userdata/system/services are started at boot and can be toggled from
// EmulationStation under Settings, Services.
package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	// Name is how the service appears in EmulationStation.
	Name = "flashcart"
	// RsyncPath is where rsync lives on Batocera. Pinned explicitly rather
	// than left to a PATH lookup: the generated script runs at boot, under
	// whatever (possibly minimal) environment batocera-services provides,
	// and runner.Exec otherwise falls back to a bare "rsync" PATH lookup.
	RsyncPath = "/usr/bin/rsync"
)

// Dir is where Batocera looks for service scripts, and InstallDir holds the
// binary and config. Both are under /userdata so they survive Batocera OS
// updates. They are variables rather than constants only so tests can
// redirect them; nothing reassigns them at runtime.
var (
	Dir        = "/userdata/system/services"
	InstallDir = "/userdata/system/flashcart"
)

// The seams Install and Uninstall run through. Defaulted to the real thing;
// swapped in tests. A bug in this plumbing — the installer enabling the
// service but never starting it — is what motivated making it testable.
var (
	runCommand = func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).CombinedOutput()
	}
	lookPath   = exec.LookPath
	executable = os.Executable
)

// Script renders the service script. Every path is quoted so a directory
// containing a space cannot split into two arguments, and fake mode is never
// passed: it is a development flag only.
func Script(binary, config string) string {
	return fmt.Sprintf(`#!/bin/sh
# flashcart — mirrors the ROM library locally so the box works offline.
# Managed by "flashcart install-service". Toggle from EmulationStation:
# Settings, Services, %s.

BIN=%q
CFG=%q
RSYNC=%q
LOG="/userdata/system/logs/%s.log"
PIDFILE="/var/run/%s.pid"

# Check if the process in pidfile is actually flashcart
is_running() {
    [ -f "$PIDFILE" ] || return 1
    pid=$(cat "$PIDFILE")
    kill -0 "$pid" 2>/dev/null || return 1
    # If /proc is available, verify it is actually flashcart
    if [ -r "/proc/$pid/comm" ]; then
        grep -q "^flashcart$" "/proc/$pid/comm" 2>/dev/null || return 1
    fi
    return 0
}

start() {
    # Don't start if already running
    if is_running; then
        return 0
    fi
    mkdir -p "$(dirname "$LOG")"
    "$BIN" --config="$CFG" --rsync="$RSYNC" serve >>"$LOG" 2>&1 &
    echo $! > "$PIDFILE"
}

stop() {
    [ -f "$PIDFILE" ] || return 0
    pid=$(cat "$PIDFILE")
    kill "$pid" 2>/dev/null
    # Wait for the process to actually exit. This must run past
    # flashcart's own graceful shutdown budget (10s: cancel an in-flight
    # sync, release the NFS mount) or a legitimate slow shutdown gets
    # mistaken for a hang, restart proceeds to start() while the old
    # process is still bound to the port, and the new process dies with
    # EADDRINUSE while the pidfile is left pointing at a PID that no
    # longer means anything useful.
    n=0
    while kill -0 "$pid" 2>/dev/null && [ $n -lt 120 ]; do
        sleep 0.1
        n=$((n + 1))
    done
    # Still alive past the budget: it is genuinely stuck, not just slow.
    # Force it, then wait for that to land before trusting it is gone.
    if kill -0 "$pid" 2>/dev/null; then
        kill -9 "$pid" 2>/dev/null
        n=0
        while kill -0 "$pid" 2>/dev/null && [ $n -lt 20 ]; do
            sleep 0.1
            n=$((n + 1))
        done
    fi
    # Only remove the pidfile once the process is confirmed gone. restart
    # calls start() right after this returns; if the pidfile were removed
    # unconditionally, start() would see "not running" and launch a
    # second instance while the first is still shutting down.
    kill -0 "$pid" 2>/dev/null || rm -f "$PIDFILE"
}

status() {
    [ -f "$PIDFILE" ] || { echo "stopped"; return 1; }
    is_running && echo "running" || { echo "stopped"; return 1; }
}

case "$1" in
    start)   start ;;
    stop)    stop ;;
    restart) stop; start ;;
    status)  status ;;
    *)       echo "usage: $0 {start|stop|restart|status}" >&2; exit 1 ;;
esac
`, Name, binary, config, RsyncPath, Name, Name)
}

// Install writes the service script and enables it.
func Install() error {
	binary, err := executable()
	if err != nil {
		return fmt.Errorf("locate the running binary: %w", err)
	}
	cfgPath := filepath.Join(InstallDir, "flashcart.toml")

	if err := os.MkdirAll(Dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", Dir, err)
	}
	target := filepath.Join(Dir, Name)
	if err := os.WriteFile(target, []byte(Script(binary, cfgPath)), 0o755); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}

	// batocera-services is absent on a development machine, so a failure
	// here is reported without undoing the script that was written.
	if _, err := lookPath("batocera-services"); err != nil {
		return nil
	}

	// enable and start are SEPARATE verbs. enable only registers the service
	// for the next boot. Enabling without starting leaves the box with a dead
	// port until it reboots, while the installer prints a URL as though it
	// were live — which is exactly the defect this fixes.
	if out, err := runCommand("batocera-services", "enable", Name); err != nil {
		return fmt.Errorf("batocera-services enable %s: %w: %s", Name, err, strings.TrimSpace(string(out)))
	}
	if out, err := runCommand("batocera-services", "start", Name); err != nil {
		return fmt.Errorf("batocera-services start %s: %w: %s", Name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Uninstall stops the service and removes the script.
func Uninstall() error {
	if _, err := lookPath("batocera-services"); err == nil {
		if out, err := runCommand("batocera-services", "stop", Name); err != nil {
			fmt.Fprintf(os.Stderr, "warning: batocera-services stop failed: %v: %s\n", err, strings.TrimSpace(string(out)))
		}
		if out, err := runCommand("batocera-services", "disable", Name); err != nil {
			fmt.Fprintf(os.Stderr, "warning: batocera-services disable failed: %v: %s\n", err, strings.TrimSpace(string(out)))
		}
	}
	target := filepath.Join(Dir, Name)
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", target, err)
	}
	return nil
}
