package xfs

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/0x0034/containerd-snapshot-quota/pkg/manager/core"
)

var projIDRegex = regexp.MustCompile(`projid\s*=\s*(\d+)`)

// Operations implements core.QuotaOperations for XFS filesystems.
type Operations struct{}

func NewOperations() *Operations {
	return &Operations{}
}

// IsPQuotaEnabled checks if project quota is enabled on the given mount.
func (o *Operations) IsPQuotaEnabled(mountPath string) (bool, error) {
	out, err := exec.Command("xfs_quota", "-x", "-c", "state", mountPath).CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("xfs_quota state: %s: %w", string(out), err)
	}
	output := string(out)
	return strings.Contains(output, "Project quota state on") &&
		strings.Contains(output, "Accounting: ON") &&
		strings.Contains(output, "Enforcement: ON"), nil
}

// GetProjectID reads the project ID assigned to a path.
func (o *Operations) GetProjectID(path string) (uint32, error) {
	out, err := exec.Command("xfs_io", "-r", "-c", "stat", path).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("xfs_io stat %s: %s: %w", path, string(out), err)
	}

	matches := projIDRegex.FindStringSubmatch(string(out))
	if len(matches) < 2 {
		return 0, fmt.Errorf("no projid found in xfs_io output for %s", path)
	}

	id, err := strconv.ParseUint(matches[1], 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parse projid %q: %w", matches[1], err)
	}
	return uint32(id), nil
}

// SetProjectID assigns a project ID to a directory.
func (o *Operations) SetProjectID(path string, projID uint32) error {
	// Write to /etc/projects and /etc/projid for xfs_quota compatibility
	if err := writeProjectFiles(projID, path); err != nil {
		return err
	}

	cmd := exec.Command("xfs_quota", "-x", "-c",
		fmt.Sprintf("project -s -p %s %d", path, projID))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("xfs_quota set project %d on %s: %s: %w", projID, path, string(out), err)
	}
	return nil
}

// RemoveProjectID clears a project configuration.
func (o *Operations) RemoveProjectID(path string, projID uint32) error {
	// Clear quota limits first
	cmd := exec.Command("xfs_quota", "-x", "-c",
		fmt.Sprintf("limit -p bsoft=0 bhard=0 %d", projID))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("xfs_quota clear limit %d: %s: %w", projID, string(out), err)
	}

	// Remove project from /etc/projects and /etc/projid
	cleanProjectFiles(projID)
	return nil
}

// SetProjectQuota sets soft and hard block limits for a project.
func (o *Operations) SetProjectQuota(info *core.QuotaInfo, mountPath string) error {
	softStr := formatQuotaSize(info.BSoft)
	hardStr := formatQuotaSize(info.BHard)

	cmd := exec.Command("xfs_quota", "-x", "-c",
		fmt.Sprintf("limit -p bsoft=%s bhard=%s %d", softStr, hardStr, info.ProjectID),
		mountPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("xfs_quota limit proj %d: %s: %w", info.ProjectID, string(out), err)
	}
	return nil
}

// GetQuotaWithID queries the current quota usage for a project ID.
func (o *Operations) GetQuotaWithID(projID uint32, mountPath string) (*core.QuotaInfo, error) {
	cmd := exec.Command("xfs_quota", "-x", "-c", "report -h -p", mountPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("xfs_quota report: %s: %w", string(out), err)
	}

	info, err := parseQuotaReport(string(out), projID)
	if err != nil {
		return nil, err
	}
	info.MountPath = mountPath
	return info, nil
}

// parseQuotaReport extracts quota info from xfs_quota report output.
func parseQuotaReport(output string, targetID uint32) (*core.QuotaInfo, error) {
	targetPrefix := fmt.Sprintf("#%d", targetID)
	lines := strings.Split(output, "\n")

	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		if fields[0] != targetPrefix {
			continue
		}

		used, _ := parseReportSize(fields[1])
		soft, _ := parseReportSize(fields[2])
		hard, _ := parseReportSize(fields[3])

		return &core.QuotaInfo{
			ProjectID: targetID,
			BUsed:     used,
			BSoft:     soft,
			BHard:     hard,
		}, nil
	}
	return nil, fmt.Errorf("project %d not found in quota report", targetID)
}

// parseReportSize handles sizes like "10G", "500M", "0" from xfs_quota report.
func parseReportSize(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "0" || s == "" {
		return 0, nil
	}
	return core.ParseSize(s)
}

// formatQuotaSize converts bytes to xfs_quota limit format.
func formatQuotaSize(bytes uint64) string {
	if bytes == 0 {
		return "0"
	}
	return core.FormatSize(bytes)
}

// writeProjectFiles adds entries to /etc/projects and /etc/projid.
func writeProjectFiles(projID uint32, path string) error {
	projEntry := fmt.Sprintf("%d:%s\n", projID, path)
	projIDEntry := fmt.Sprintf("project%d:%d\n", projID, projID)

	if err := appendLineIfMissing("/etc/projects", projEntry); err != nil {
		return fmt.Errorf("write /etc/projects: %w", err)
	}
	if err := appendLineIfMissing("/etc/projid", projIDEntry); err != nil {
		return fmt.Errorf("write /etc/projid: %w", err)
	}
	return nil
}

// cleanProjectFiles removes entries from /etc/projects and /etc/projid.
func cleanProjectFiles(projID uint32) {
	prefix := fmt.Sprintf("%d:", projID)
	namePrefix := fmt.Sprintf("project%d:", projID)

	removeLineByPrefix("/etc/projects", prefix)
	removeLineByPrefix("/etc/projid", namePrefix)
}

func appendLineIfMissing(filePath, entry string) error {
	content, _ := exec.Command("cat", filePath).Output()
	if strings.Contains(string(content), strings.TrimSpace(entry)) {
		return nil
	}
	cmd := exec.Command("bash", "-c", fmt.Sprintf("echo -n %q >> %s", entry, filePath))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w", string(out), err)
	}
	return nil
}

func removeLineByPrefix(filePath, prefix string) {
	content, err := exec.Command("cat", filePath).Output()
	if err != nil {
		return
	}
	var kept []string
	for _, line := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, prefix) {
			kept = append(kept, line)
		}
	}
	result := strings.Join(kept, "\n") + "\n"
	_ = exec.Command("bash", "-c", fmt.Sprintf("echo -n %q > %s", result, filePath)).Run()
}
