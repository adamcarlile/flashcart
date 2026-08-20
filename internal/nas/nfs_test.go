package nas

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/adamcarlile/flashcart/internal/config"
)

func cfgWith(host string, port int) *config.Config {
	return &config.Config{
		NAS: config.NAS{Host: host, Port: port, MountRoot: "/var/run/flashcart/nas"},
		Trees: config.Trees{
			Roms:  config.Tree{Export: "/volume2/retrogaming/roms", Local: "/userdata/roms"},
			Bios:  config.Tree{Export: "/volume2/retrogaming/bios", Local: "/userdata/bios"},
			Saves: config.Tree{Export: "/volume2/retrogaming/saves", Local: "/userdata/saves"},
		},
	}
}

func TestProbeSucceedsAgainstAListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	port := ln.Addr().(*net.TCPAddr).Port

	n := NewNFS(cfgWith("127.0.0.1", port))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := n.Probe(ctx); err != nil {
		t.Fatalf("Probe: %v", err)
	}
}

func TestProbeFailsWhenNothingListens(t *testing.T) {
	// Port 1 on loopback: reserved and never listening.
	n := NewNFS(cfgWith("127.0.0.1", 1))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := n.Probe(ctx)
	if err == nil {
		t.Fatal("Probe succeeded against a closed port")
	}
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("Probe error = %v, want ErrUnreachable", err)
	}
}

// Mount failures need different messages because they need different fixes.
func TestClassifyMountError(t *testing.T) {
	cases := []struct {
		stderr string
		want   error
	}{
		{"mount.nfs4: Connection timed out", ErrUnreachable},
		{"mount.nfs4: No route to host", ErrUnreachable},
		{"mount.nfs4: Connection refused", ErrUnreachable},
		{"mount.nfs4: mounting 10.132.1.25:/volume2/nope failed, reason given by server: No such file or directory", ErrExportMissing},
		{"mount.nfs4: access denied by server while mounting", ErrPermission},
		{"mount.nfs4: Operation not permitted", ErrPermission},
	}
	for _, tc := range cases {
		got := ClassifyMountError(tc.stderr, 32)
		if !errors.Is(got, tc.want) {
			t.Errorf("ClassifyMountError(%q) = %v, want %v", tc.stderr, got, tc.want)
		}
		if got.Error() == "" {
			t.Errorf("ClassifyMountError(%q) produced an empty message", tc.stderr)
		}
	}
}

func TestClassifyMountErrorKeepsUnknownStderr(t *testing.T) {
	err := ClassifyMountError("mount.nfs4: something entirely new", 32)
	if err == nil {
		t.Fatal("want an error")
	}
	if got := err.Error(); got == "" || !contains(got, "something entirely new") {
		t.Errorf("unknown stderr was discarded: %q", got)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(needle) == 0 ||
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}())
}

// TestMountHappyPath verifies all three exports mount successfully
// and the returned Mounts struct has the correct paths.
func TestMountHappyPath(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := cfgWith("127.0.0.1", 2049)
	cfg.NAS.MountRoot = tmpDir
	n := NewNFS(cfg)

	var mountCalls []struct {
		opts   string
		src    string
		target string
	}

	n.runMount = func(ctx context.Context, opts, src, target string) ([]byte, error) {
		mountCalls = append(mountCalls, struct {
			opts   string
			src    string
			target string
		}{opts, src, target})
		return nil, nil
	}

	var unmountCalls []string
	n.runUnmount = func(target string) error {
		unmountCalls = append(unmountCalls, target)
		return nil
	}

	ctx := context.Background()
	m, unmountFn, err := n.Mount(ctx)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}

	// Verify paths are populated
	if m.Roms == "" || m.Bios == "" || m.Saves == "" {
		t.Errorf("Mount returned empty paths: Roms=%q, Bios=%q, Saves=%q", m.Roms, m.Bios, m.Saves)
	}

	// Verify three mounts were attempted in order
	if len(mountCalls) != 3 {
		t.Fatalf("Mount called %d times, want 3", len(mountCalls))
	}

	// Verify exports in correct order
	if !contains(mountCalls[0].src, "/roms") {
		t.Errorf("First mount should be roms, got %q", mountCalls[0].src)
	}
	if !contains(mountCalls[1].src, "/bios") {
		t.Errorf("Second mount should be bios, got %q", mountCalls[1].src)
	}
	if !contains(mountCalls[2].src, "/saves") {
		t.Errorf("Third mount should be saves, got %q", mountCalls[2].src)
	}

	// Call returned unmount function
	if err := unmountFn(); err != nil {
		t.Errorf("unmountFn: %v", err)
	}

	// Verify all mounts were unmounted
	if len(unmountCalls) != 3 {
		t.Fatalf("Unmount called %d times, want 3", len(unmountCalls))
	}
}

// TestMountReverseOrderUnmount verifies that unmount happens in reverse
// order of mounting. This property is critical for cleanup safety.
func TestMountReverseOrderUnmount(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := cfgWith("127.0.0.1", 2049)
	cfg.NAS.MountRoot = tmpDir
	n := NewNFS(cfg)

	var mountOrder []string
	n.runMount = func(ctx context.Context, opts, src, target string) ([]byte, error) {
		mountOrder = append(mountOrder, "mount:"+target)
		return nil, nil
	}

	var unmountOrder []string
	n.runUnmount = func(target string) error {
		unmountOrder = append(unmountOrder, "unmount:"+target)
		return nil
	}

	ctx := context.Background()
	_, unmountFn, err := n.Mount(ctx)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}

	unmountFn()

	// Verify unmount order is reverse of mount order
	if len(mountOrder) != 3 || len(unmountOrder) != 3 {
		t.Fatalf("mount ops: %v, unmount ops: %v", mountOrder, unmountOrder)
	}

	// Extract just the target paths from mount order
	expectReverse := []string{mountOrder[2], mountOrder[1], mountOrder[0]}

	for i, um := range unmountOrder {
		expectedPath := expectReverse[i]
		if !contains(um, expectedPath) {
			t.Errorf("unmount[%d] = %q, expected to contain %q (reverse of mount[%d])",
				i, um, expectedPath, 2-i)
		}
	}
}

// TestMountPartialFailureWithCleanupError verifies that if mount fails
// after some success, cleanup errors are reported alongside the mount error.
func TestMountPartialFailureWithCleanupError(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := cfgWith("127.0.0.1", 2049)
	cfg.NAS.MountRoot = tmpDir
	n := NewNFS(cfg)

	mountAttempt := 0
	n.runMount = func(ctx context.Context, opts, src, target string) ([]byte, error) {
		mountAttempt++
		if mountAttempt == 3 { // Fail on the third mount (saves)
			return []byte("mount.nfs4: Connection timed out"), errors.New("exit status 32")
		}
		return nil, nil
	}

	cleanupAttempt := 0
	n.runUnmount = func(target string) error {
		cleanupAttempt++
		if cleanupAttempt == 1 { // Fail on first unmount
			return errors.New("device or resource busy")
		}
		return nil
	}

	ctx := context.Background()
	_, _, err := n.Mount(ctx)
	if err == nil {
		t.Fatal("Mount should have failed")
	}

	// The error must mention both the mount failure and the cleanup failure
	errStr := err.Error()
	if !contains(errStr, "mount saves") {
		t.Errorf("Error should mention mount saves failure, got: %v", err)
	}
	if !contains(errStr, "device or resource busy") {
		t.Errorf("Error should mention cleanup failure (device or resource busy), got: %v", err)
	}
}

// TestMountBIOSReadOnly verifies that BIOS export is mounted read-only
// while roms and saves are mounted read-write.
func TestMountBIOSReadOnly(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := cfgWith("127.0.0.1", 2049)
	cfg.NAS.MountRoot = tmpDir
	n := NewNFS(cfg)

	var mountOpts map[string]string = make(map[string]string)

	n.runMount = func(ctx context.Context, opts, src, target string) ([]byte, error) {
		// Extract export name from target path
		var name string
		if contains(target, "roms") {
			name = "roms"
		} else if contains(target, "bios") {
			name = "bios"
		} else if contains(target, "saves") {
			name = "saves"
		}
		mountOpts[name] = opts
		return nil, nil
	}

	n.runUnmount = func(target string) error {
		return nil
	}

	ctx := context.Background()
	_, _, err := n.Mount(ctx)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}

	// Check BIOS starts with ro,
	biosOpts := mountOpts["bios"]
	if !contains(biosOpts, "ro,") {
		t.Errorf("BIOS mount options should start with 'ro,', got: %q", biosOpts)
	}

	// Check roms and saves do NOT start with ro,
	romsOpts := mountOpts["roms"]
	if contains(romsOpts, "ro,") {
		t.Errorf("ROMS mount options should not start with 'ro,', got: %q", romsOpts)
	}

	savesOpts := mountOpts["saves"]
	if contains(savesOpts, "ro,") {
		t.Errorf("SAVES mount options should not start with 'ro,', got: %q", savesOpts)
	}
}
