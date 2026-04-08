package core

// QuotaInfo holds the parameters for a project quota.
type QuotaInfo struct {
	ProjectID uint32
	Path      string
	MountPath string
	BUsed     uint64 // current block usage in bytes
	BSoft     uint64 // soft block limit in bytes
	BHard     uint64 // hard block limit in bytes
}

// MountInfo describes a filesystem mount point.
type MountInfo struct {
	Device     string
	MountPath  string
	FSType     string // xfs, ext4
	HasPrjQuota bool
}

// QuotaOperations abstracts filesystem-specific quota commands.
type QuotaOperations interface {
	GetProjectID(path string) (uint32, error)
	GetQuotaWithID(projID uint32, mountPath string) (*QuotaInfo, error)
	SetProjectID(path string, projID uint32) error
	RemoveProjectID(path string, projID uint32) error
	SetProjectQuota(info *QuotaInfo, mountPath string) error
	IsPQuotaEnabled(mountPath string) (bool, error)
}
