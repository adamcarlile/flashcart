// Package buildinfo carries the version stamped in at link time.
package buildinfo

// Version is overridden by the release build with -ldflags.
var Version = "dev"
