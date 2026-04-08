package core

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// DetectMountInfo reads /proc/mounts and returns mount info for the given path.
func DetectMountInfo(mountPoint string) (*MountInfo, error) {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil, fmt.Errorf("open /proc/mounts: %w", err)
	}
	defer f.Close()

	var best *MountInfo
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}
		device := fields[0]
		mp := fields[1]
		fsType := fields[2]
		opts := fields[3]

		if !strings.HasPrefix(mountPoint, mp) {
			continue
		}
		if best != nil && len(mp) < len(best.MountPath) {
			continue
		}

		hasPrj := strings.Contains(opts, "prjquota") || strings.Contains(opts, "pquota")
		best = &MountInfo{
			Device:      device,
			MountPath:   mp,
			FSType:      fsType,
			HasPrjQuota: hasPrj,
		}
	}

	if best == nil {
		return nil, fmt.Errorf("no mount found for %s", mountPoint)
	}
	return best, nil
}
