package runner

import (
	"strconv"
	"strings"
)

const deletingPrefix = "*deleting"

// ParseItemize reads rsync output produced with --out-format=%i|%l|%n.
// Lines that are not itemized output, such as rsync's own summary, are
// ignored.
func ParseItemize(passID, out string) Result {
	res := Result{PassID: passID}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		// A path may itself contain the separator, so only the first two
		// fields are split off.
		fields := strings.SplitN(line, "|", 3)
		if len(fields) != 3 {
			continue
		}
		flags, sizeStr, path := fields[0], fields[1], fields[2]

		if strings.HasPrefix(flags, deletingPrefix) {
			res.Deletions = append(res.Deletions, path)
			continue
		}
		// Itemize flags are eleven characters describing the update type.
		// Anything shorter is not an itemized line.
		if len(strings.TrimSpace(flags)) < 2 {
			continue
		}
		size, err := strconv.ParseInt(sizeStr, 10, 64)
		if err != nil {
			continue
		}
		res.Changes = append(res.Changes, Change{Itemize: flags, Size: size, Path: path})
		// Only regular files contribute bytes. The second flag character is
		// the entry type: 'f' for file, 'd' for directory.
		if len(flags) > 1 && flags[1] == 'f' {
			res.TransferBytes += size
		}
	}
	return res
}

// ParseProgress extracts the overall percentage from an --info=progress2
// line. It returns false for any line that is not a progress update.
func ParseProgress(line string) (int, bool) {
	for _, f := range strings.Fields(line) {
		if !strings.HasSuffix(f, "%") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSuffix(f, "%"))
		if err != nil || n < 0 || n > 100 {
			continue
		}
		return n, true
	}
	return 0, false
}
