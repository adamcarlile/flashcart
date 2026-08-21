//go:build !linux

package plan

import "errors"

// FreeSpace is only implemented on Linux, which is the only platform
// flashcart is deployed to.
func FreeSpace(string) (int64, int64, error) {
	return 0, 0, errors.New("free space checks are only supported on Linux")
}
