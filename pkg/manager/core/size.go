package core

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	KB = 1024
	MB = 1024 * KB
	GB = 1024 * MB
	TB = 1024 * GB
)

// ParseSize converts a human-readable size string (e.g., "10G", "500M") to bytes.
func ParseSize(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size string")
	}

	s = strings.ToUpper(s)
	var multiplier uint64 = 1
	var numStr string

	switch {
	case strings.HasSuffix(s, "TB") || strings.HasSuffix(s, "T"):
		multiplier = TB
		numStr = strings.TrimRight(s, "TB")
	case strings.HasSuffix(s, "GB") || strings.HasSuffix(s, "G"):
		multiplier = GB
		numStr = strings.TrimRight(s, "GB")
	case strings.HasSuffix(s, "MB") || strings.HasSuffix(s, "M"):
		multiplier = MB
		numStr = strings.TrimRight(s, "MB")
	case strings.HasSuffix(s, "KB") || strings.HasSuffix(s, "K"):
		multiplier = KB
		numStr = strings.TrimRight(s, "KB")
	default:
		numStr = s
	}

	numStr = strings.TrimSpace(numStr)
	val, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0, fmt.Errorf("parse size %q: %w", s, err)
	}
	if val < 0 {
		return 0, fmt.Errorf("negative size %q", s)
	}

	return uint64(val * float64(multiplier)), nil
}

// FormatSize converts bytes to a human-readable string compatible with xfs_quota.
// xfs_quota does not accept decimal values like "100.0G", so we output integers
// (e.g. "100g") and fall back to a smaller unit when not evenly divisible.
func FormatSize(bytes uint64) string {
	switch {
	case bytes >= TB && bytes%TB == 0:
		return fmt.Sprintf("%dt", bytes/TB)
	case bytes >= GB && bytes%GB == 0:
		return fmt.Sprintf("%dg", bytes/GB)
	case bytes >= MB && bytes%MB == 0:
		return fmt.Sprintf("%dm", bytes/MB)
	case bytes >= KB && bytes%KB == 0:
		return fmt.Sprintf("%dk", bytes/KB)
	default:
		return fmt.Sprintf("%d", bytes)
	}
}
