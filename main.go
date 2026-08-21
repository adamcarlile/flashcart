// Command flashcart maintains a local mirror of a ROM library that normally
// lives on an NFS share, so a Batocera box works with no network attached.
package main

import (
	"os"

	"github.com/adamcarlile/flashcart/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
