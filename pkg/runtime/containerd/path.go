package containerd

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

var (
	upperdirRegex = regexp.MustCompile(`upperdir=([^, ]+)`)
	workdirRegex  = regexp.MustCompile(`workdir=([^, ]+)`)
)

// getOverlayPaths extracts upperdir and workdir from a container's mount info.
func getOverlayPaths(pid uint32) (upperdir, workdir string, err error) {
	// Try /proc/<pid>/mountinfo first, fall back to /proc/<pid>/mounts
	paths := []string{
		fmt.Sprintf("/proc/%d/mountinfo", pid),
		fmt.Sprintf("/proc/%d/mounts", pid),
	}

	for _, path := range paths {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		content := string(data)
		for _, line := range strings.Split(content, "\n") {
			if !strings.Contains(line, "overlay") {
				continue
			}
			if m := upperdirRegex.FindStringSubmatch(line); len(m) > 1 {
				upperdir = m[1]
			}
			if m := workdirRegex.FindStringSubmatch(line); len(m) > 1 {
				workdir = m[1]
			}
			if upperdir != "" {
				return upperdir, workdir, nil
			}
		}
	}

	return "", "", fmt.Errorf("overlay paths not found for pid %d", pid)
}
