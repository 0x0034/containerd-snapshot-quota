package manager

import (
	"fmt"
	"os"
	"time"

	"k8s.io/klog/v2"

	"github.com/0x0034/containerd-snapshot-quota/pkg/manager/core"
	"github.com/0x0034/containerd-snapshot-quota/pkg/manager/xfs"
	"github.com/0x0034/containerd-snapshot-quota/pkg/model"
	"github.com/0x0034/containerd-snapshot-quota/pkg/storage"
)

// QuotaManager handles quota lifecycle for containers.
type QuotaManager struct {
	mountInfo  *core.MountInfo
	quotaOps   core.QuotaOperations
	pool       *core.ProjectIDPool
	store      *storage.Store
	managerKey string
}

// New creates a QuotaManager for the given mount point.
func New(mountPoint string, projMin, projMax uint32, store *storage.Store) (*QuotaManager, error) {
	mi, err := core.DetectMountInfo(mountPoint)
	if err != nil {
		return nil, fmt.Errorf("detect mount info: %w", err)
	}

	if mi.FSType != "xfs" {
		return nil, fmt.Errorf("filesystem type %q not supported, only xfs is supported", mi.FSType)
	}

	ops := xfs.NewOperations()
	enabled, err := ops.IsPQuotaEnabled(mi.MountPath)
	if err != nil {
		return nil, fmt.Errorf("check prjquota: %w", err)
	}
	if !enabled && !mi.HasPrjQuota {
		return nil, fmt.Errorf("project quota not enabled on %s (mount with prjquota option)", mi.MountPath)
	}

	klog.Infof("XFS project quota active on %s (device: %s)", mi.MountPath, mi.Device)

	m := &QuotaManager{
		mountInfo:  mi,
		quotaOps:   ops,
		pool:       core.NewProjectIDPool(projMin, projMax),
		store:      store,
		managerKey: "xfs-quota",
	}

	if err := m.recovery(); err != nil {
		klog.Warningf("Recovery completed with errors: %v", err)
	}

	return m, nil
}

// Set applies a quota to a container's overlay directories.
func (m *QuotaManager) Set(req model.QuotaRequest) error {
	// Check if already set
	existing, err := m.store.GetQuota(m.dbKey(req.ContainerID))
	if err == nil && existing.Status == model.QuotaStatusSucceed {
		klog.V(2).Infof("Quota already set for container %s, skipping", req.ContainerID)
		return nil
	}

	sizeBytes, err := core.ParseSize(req.Size)
	if err != nil {
		return fmt.Errorf("parse size %q: %w", req.Size, err)
	}

	// Create pending quota record
	quota := model.Quota{
		ID:        req.ContainerID,
		Status:    model.QuotaStatusPending,
		Size:      req.Size,
		CInfo:     req.CInfo,
		CreatedAt: time.Now(),
	}
	if err := m.store.SetQuota(m.dbKey(req.ContainerID), quota); err != nil {
		return fmt.Errorf("persist pending quota: %w", err)
	}

	// Allocate project ID
	projID, err := m.pool.Allocate()
	if err != nil {
		m.markFailed(req.ContainerID, err)
		return err
	}
	quota.ProjectID = projID

	// Apply to upperdir
	if req.Upperdir != "" {
		if err := m.applyQuota(req.Upperdir, projID, sizeBytes); err != nil {
			m.pool.Release(projID)
			m.markFailed(req.ContainerID, err)
			return fmt.Errorf("set quota on upperdir: %w", err)
		}
	}

	// Apply to workdir
	if req.Workdir != "" {
		if err := m.applyQuota(req.Workdir, projID, sizeBytes); err != nil {
			// Best effort: upperdir quota already set, log but continue
			klog.Warningf("Failed to set quota on workdir %s: %v", req.Workdir, err)
		}
	}

	quota.Status = model.QuotaStatusSucceed
	if err := m.store.SetQuota(m.dbKey(req.ContainerID), quota); err != nil {
		klog.Errorf("Failed to persist succeeded quota for %s: %v", req.ContainerID, err)
	}

	klog.Infof("Quota set: container=%s projID=%d size=%s upperdir=%s",
		req.ContainerID[:min(12, len(req.ContainerID))], projID, req.Size, req.Upperdir)
	return nil
}

// Clear removes a quota for a container.
func (m *QuotaManager) Clear(containerID string) error {
	quota, err := m.store.GetQuota(m.dbKey(containerID))
	if err != nil {
		klog.V(2).Infof("No quota record for container %s, skipping clear", containerID)
		return nil
	}

	if quota.ProjectID > 0 {
		if err := m.quotaOps.RemoveProjectID("", quota.ProjectID); err != nil {
			klog.Warningf("Failed to remove project ID %d: %v", quota.ProjectID, err)
		}
		m.pool.Release(quota.ProjectID)
	}

	if err := m.store.DeleteQuota(m.dbKey(containerID)); err != nil {
		return fmt.Errorf("delete quota record: %w", err)
	}

	klog.Infof("Quota cleared: container=%s projID=%d",
		containerID[:min(12, len(containerID))], quota.ProjectID)
	return nil
}

// ListQuotas returns all stored quotas.
func (m *QuotaManager) ListQuotas() ([]model.Quota, error) {
	return m.store.ListQuotas(m.managerKey)
}

// QuotaUsage holds real-time disk usage for a project quota.
type QuotaUsage struct {
	ProjectID uint32
	UsedBytes uint64
	SoftLimit uint64
	HardLimit uint64
}

// GetQuotaUsage queries the live disk usage for a project ID from XFS.
func (m *QuotaManager) GetQuotaUsage(projID uint32) (QuotaUsage, error) {
	info, err := m.quotaOps.GetQuotaWithID(projID, m.mountInfo.MountPath)
	if err != nil {
		return QuotaUsage{ProjectID: projID}, err
	}
	return QuotaUsage{
		ProjectID: projID,
		UsedBytes: info.BUsed,
		SoftLimit: info.BSoft,
		HardLimit: info.BHard,
	}, nil
}

func (m *QuotaManager) applyQuota(path string, projID uint32, sizeBytes uint64) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("path %s: %w", path, err)
	}

	if err := m.quotaOps.SetProjectID(path, projID); err != nil {
		return fmt.Errorf("set project ID: %w", err)
	}

	info := &core.QuotaInfo{
		ProjectID: projID,
		Path:      path,
		MountPath: m.mountInfo.MountPath,
		BSoft:     sizeBytes,
		BHard:     sizeBytes,
	}
	if err := m.quotaOps.SetProjectQuota(info, m.mountInfo.MountPath); err != nil {
		return fmt.Errorf("set project quota: %w", err)
	}
	return nil
}

func (m *QuotaManager) recovery() error {
	quotas, err := m.store.ListQuotas(m.managerKey)
	if err != nil {
		return fmt.Errorf("list quotas for recovery: %w", err)
	}

	var recovered, cleaned int
	for _, q := range quotas {
		// If paths no longer exist, clean up
		if q.CInfo.Upperdir != "" {
			if _, err := os.Stat(q.CInfo.Upperdir); err != nil {
				_ = m.store.DeleteQuota(m.dbKey(q.ID))
				cleaned++
				continue
			}
		}

		// Mark project ID as used
		if q.ProjectID > 0 {
			m.pool.MarkUsed(q.ProjectID)
		}

		// Re-apply if not yet succeeded
		if q.Status != model.QuotaStatusSucceed && q.CInfo.Upperdir != "" {
			sizeBytes, err := core.ParseSize(q.Size)
			if err != nil {
				klog.Warningf("Recovery: invalid size %q for %s: %v", q.Size, q.ID, err)
				continue
			}
			if err := m.applyQuota(q.CInfo.Upperdir, q.ProjectID, sizeBytes); err != nil {
				klog.Warningf("Recovery: failed to re-apply quota for %s: %v", q.ID, err)
				continue
			}
			q.Status = model.QuotaStatusSucceed
			_ = m.store.SetQuota(m.dbKey(q.ID), q)
		}
		recovered++
	}

	klog.Infof("Recovery complete: recovered=%d cleaned=%d", recovered, cleaned)
	return nil
}

func (m *QuotaManager) markFailed(containerID string, reason error) {
	q, err := m.store.GetQuota(m.dbKey(containerID))
	if err != nil {
		return
	}
	q.Status = model.QuotaStatusFailed
	q.Message = reason.Error()
	_ = m.store.SetQuota(m.dbKey(containerID), q)
}

func (m *QuotaManager) dbKey(containerID string) string {
	return m.managerKey + containerID
}
