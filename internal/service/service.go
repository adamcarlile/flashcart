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
)

const (
	// Name is how the service appears in EmulationStation.
	Name = "flashcart"
	// Dir is where Batocera looks for service scripts.
	Dir = "/userdata/system/services"
	// InstallDir holds the binary and config. It is under /userdata so it
	// survives Batocera OS updates.
	InstallDir = "/userdata/system/flashcart"
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
    "$BIN" --config="$CFG" serve >>"$LOG" 2>&1 &
    echo $! > "$PIDFILE"
}

stop() {
    [ -f "$PIDFILE" ] || return 0
    pid=$(cat "$PIDFILE")
    kill "$pid" 2>/dev/null
    # Wait for the process to actually exit, with timeout
    n=0
    while kill -0 "$pid" 2>/dev/null && [ $n -lt 50 ]; do
        sleep 0.1
        n=$((n + 1))
    done
    rm -f "$PIDFILE"
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
`, Name, binary, config, Name, Name)
}

// Install writes the service script and enables it.
func Install() error {
	binary, err := os.Executable()
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
	if _, err := exec.LookPath("batocera-services"); err == nil {
		if out, err := exec.Command("batocera-services", "enable", Name).CombinedOutput(); err != nil {
			return fmt.Errorf("batocera-services enable %s: %w: %s", Name, err, out)
		}
	}
	return nil
}

// Uninstall stops the service and removes the script.
func Uninstall() error {
	if _, err := exec.LookPath("batocera-services"); err == nil {
		if err := exec.Command("batocera-services", "stop", Name).Run(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: batocera-services stop failed: %v\n", err)
		}
		if err := exec.Command("batocera-services", "disable", Name).Run(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: batocera-services disable failed: %v\n", err)
		}
	}
	target := filepath.Join(Dir, Name)
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", target, err)
	}
	return nil
}
