package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/klog/v2"

	"github.com/0x0034/containerd-snapshot-quota/pkg/manager"
	"github.com/0x0034/containerd-snapshot-quota/pkg/model"
)

var (
	quotaLimitBytes = prometheus.NewDesc(
		"container_quota_limit_bytes",
		"Configured quota hard limit in bytes for a container",
		[]string{"container_id", "container_name", "pod", "pod_namespace", "namespace", "image", "status"},
		nil,
	)
	quotaUsedBytes = prometheus.NewDesc(
		"container_quota_used_bytes",
		"Actual disk usage in bytes for a container under quota",
		[]string{"container_id", "container_name", "pod", "pod_namespace", "namespace", "image"},
		nil,
	)
	quotaSoftLimitBytes = prometheus.NewDesc(
		"container_quota_soft_limit_bytes",
		"Configured quota soft limit in bytes for a container",
		[]string{"container_id", "container_name", "pod", "pod_namespace", "namespace", "image"},
		nil,
	)
	quotaUsageRatio = prometheus.NewDesc(
		"container_quota_usage_ratio",
		"Ratio of used bytes to hard limit (0.0 - 1.0+)",
		[]string{"container_id", "container_name", "pod", "pod_namespace", "namespace", "image"},
		nil,
	)
	quotaTotalCount = prometheus.NewDesc(
		"container_quota_total",
		"Total number of container quotas by status",
		[]string{"status"},
		nil,
	)
)

// Collector implements prometheus.Collector and produces per-container quota metrics.
type Collector struct {
	qManager *manager.QuotaManager
	mu       sync.Mutex
}

// NewCollector creates a Prometheus collector backed by the QuotaManager.
func NewCollector(qm *manager.QuotaManager) *Collector {
	return &Collector{qManager: qm}
}

// Describe sends metric descriptors to the channel.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- quotaLimitBytes
	ch <- quotaUsedBytes
	ch <- quotaSoftLimitBytes
	ch <- quotaUsageRatio
	ch <- quotaTotalCount
}

// Collect fetches real-time quota usage and sends metrics.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()

	quotas, err := c.qManager.ListQuotas()
	if err != nil {
		klog.Warningf("Metrics: failed to list quotas: %v", err)
		return
	}

	statusCounts := map[model.QuotaStatus]int{
		model.QuotaStatusPending: 0,
		model.QuotaStatusSucceed: 0,
		model.QuotaStatusFailed:  0,
	}

	for _, q := range quotas {
		statusCounts[q.Status]++

		cid := shortID(q.ID)
		labels := []string{
			cid,
			q.CInfo.Name,
			q.CInfo.PodName,
			q.CInfo.PodNamespace,
			q.CInfo.Namespace,
			q.CInfo.Image,
			string(q.Status),
		}
		usageLabels := []string{
			cid,
			q.CInfo.Name,
			q.CInfo.PodName,
			q.CInfo.PodNamespace,
			q.CInfo.Namespace,
			q.CInfo.Image,
		}

		// Static limit from DB record
		usage, err := c.qManager.GetQuotaUsage(q.ProjectID)
		if err != nil {
			klog.V(4).Infof("Metrics: skip usage for proj %d: %v", q.ProjectID, err)
			// Still emit limit from stored quota
			ch <- prometheus.MustNewConstMetric(quotaLimitBytes, prometheus.GaugeValue, float64(usage.HardLimit), labels...)
			continue
		}

		ch <- prometheus.MustNewConstMetric(quotaLimitBytes, prometheus.GaugeValue, float64(usage.HardLimit), labels...)
		ch <- prometheus.MustNewConstMetric(quotaSoftLimitBytes, prometheus.GaugeValue, float64(usage.SoftLimit), usageLabels...)
		ch <- prometheus.MustNewConstMetric(quotaUsedBytes, prometheus.GaugeValue, float64(usage.UsedBytes), usageLabels...)

		if usage.HardLimit > 0 {
			ratio := float64(usage.UsedBytes) / float64(usage.HardLimit)
			ch <- prometheus.MustNewConstMetric(quotaUsageRatio, prometheus.GaugeValue, ratio, usageLabels...)
		}
	}

	for status, count := range statusCounts {
		ch <- prometheus.MustNewConstMetric(quotaTotalCount, prometheus.GaugeValue, float64(count), string(status))
	}
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
