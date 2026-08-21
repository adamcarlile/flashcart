//go:build linux

package plan

import "syscall"

// FreeSpace reports free and total bytes for the filesystem holding path.
func FreeSpace(path string) (int64, int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	// Bavail is space available to an unprivileged process, which is the
	// honest number to plan against.
	return int64(st.Bavail) * int64(st.Bsize), int64(st.Blocks) * int64(st.Bsize), nil
}
