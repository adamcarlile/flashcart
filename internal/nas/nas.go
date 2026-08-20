// Package nas probes and mounts the NFS exports for the duration of a run.
package nas

import "context"

// Mounts are the local paths at which the three exports are mounted.
type Mounts struct {
	Roms  string
	Bios  string
	Saves string
}

// Provider probes and mounts the NAS. It is the seam that lets fake mode
// drive the application with no network.
type Provider interface {
	// Probe reports whether the NAS is reachable. It must be cheap and
	// must not mount anything.
	Probe(ctx context.Context) error
	// Mount mounts all three exports and returns them together with an
	// unmount function that must always be called.
	Mount(ctx context.Context) (Mounts, func() error, error)
}
