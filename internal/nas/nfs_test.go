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
