package containerd

import (
	"context"
	"strings"
	"sync"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/api/events"
	cdevents "github.com/containerd/containerd/events"
	"github.com/containerd/typeurl/v2"
	"k8s.io/klog/v2"

	"github.com/0x0034/containerd-snapshot-quota/pkg/config"
	"github.com/0x0034/containerd-snapshot-quota/pkg/manager"
	"github.com/0x0034/containerd-snapshot-quota/pkg/model"
)

// Watcher listens for containerd container events and applies quotas.
type Watcher struct {
	client    *Client
	qManager  *manager.QuotaManager
	cfg       *config.Config
	running   bool
	cancel    context.CancelFunc
	mu        sync.Mutex
}

// NewWatcher creates a new containerd event watcher.
func NewWatcher(client *Client, qm *manager.QuotaManager, cfg *config.Config) *Watcher {
	return &Watcher{
		client:   client,
		qManager: qm,
		cfg:      cfg,
	}
}

// Start begins listening for container events.
func (w *Watcher) Start(ctx context.Context) error {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return nil
	}
	ctx, w.cancel = context.WithCancel(ctx)
	w.running = true
	w.mu.Unlock()

	nsCtx := w.client.NamespacedContext(ctx)

	// Sync existing containers first
	w.syncExistingContainers(nsCtx)

	// Start event listener
	go w.eventLoop(nsCtx)

	klog.Infof("Watcher started for namespace %s", w.client.Namespace())
	return nil
}

// Stop halts the event listener.
func (w *Watcher) Stop() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancel != nil {
		w.cancel()
	}
	w.running = false
	return w.client.Close()
}

// IsRunning reports whether the watcher is active.
func (w *Watcher) IsRunning() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running
}

func (w *Watcher) syncExistingContainers(ctx context.Context) {
	containers, err := w.client.Containerd().Containers(ctx)
	if err != nil {
		klog.Errorf("Failed to list containers in namespace %s: %v", w.client.Namespace(), err)
		return
	}

	for _, c := range containers {
		task, err := c.Task(ctx, nil)
		if err != nil {
			continue // No running task
		}

		status, err := task.Status(ctx)
		if err != nil || status.Status != containerd.Running {
			continue
		}

		w.handleTaskCreate(ctx, c.ID(), task.Pid())
	}
	klog.Infof("Synced %d existing containers in namespace %s", len(containers), w.client.Namespace())
}

func (w *Watcher) eventLoop(ctx context.Context) {
	eventCh, errCh := w.client.Containerd().Subscribe(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case err := <-errCh:
			if err != nil {
				klog.Errorf("Event stream error in namespace %s: %v", w.client.Namespace(), err)
			}
			return
		case envelope := <-eventCh:
			w.handleEvent(ctx, envelope)
		}
	}
}

func (w *Watcher) handleEvent(ctx context.Context, envelope *cdevents.Envelope) {
	if envelope == nil || envelope.Event == nil {
		return
	}

	v, err := typeurl.UnmarshalAny(envelope.Event)
	if err != nil {
		klog.V(4).Infof("Unmarshal event: %v", err)
		return
	}

	switch e := v.(type) {
	case *events.TaskCreate:
		klog.V(2).Infof("TaskCreate: container=%s pid=%d ns=%s",
			e.ContainerID, e.Pid, envelope.Namespace)
		w.handleTaskCreate(ctx, e.ContainerID, e.Pid)

	case *events.TaskDelete:
		klog.V(2).Infof("TaskDelete: container=%s ns=%s",
			e.ContainerID, envelope.Namespace)
		w.handleTaskDelete(e.ContainerID)
	}
}

func (w *Watcher) handleTaskCreate(ctx context.Context, containerID string, pid uint32) {
	upperdir, workdir, err := getOverlayPaths(pid)
	if err != nil {
		klog.V(2).Infof("Skip container %s: %v", containerID[:min(12, len(containerID))], err)
		return
	}

	cInfo := w.populateContainerInfo(ctx, containerID, upperdir, workdir)

	size, matched := w.matchRule(cInfo)
	if !matched {
		mode := w.cfg.Mode()
		if mode == config.ModeOnlyRules {
			klog.V(2).Infof("No rule matched for container %s, skipping (mode=OnlyRules)",
				containerID[:min(12, len(containerID))])
			return
		}
		if mode == config.ModeOnlyAuto || mode == config.ModeAll {
			size = w.cfg.Manager.Size
		}
		if size == "" {
			return
		}
	}

	req := model.QuotaRequest{
		ContainerID: containerID,
		Upperdir:    upperdir,
		Workdir:     workdir,
		Size:        size,
		CInfo:       cInfo,
	}

	if err := w.qManager.Set(req); err != nil {
		klog.Errorf("Failed to set quota for container %s: %v",
			containerID[:min(12, len(containerID))], err)
	}
}

func (w *Watcher) handleTaskDelete(containerID string) {
	if err := w.qManager.Clear(containerID); err != nil {
		klog.Errorf("Failed to clear quota for container %s: %v",
			containerID[:min(12, len(containerID))], err)
	}
}

func (w *Watcher) populateContainerInfo(ctx context.Context, containerID, upperdir, workdir string) model.ContainerInfo {
	cInfo := model.ContainerInfo{
		ID:        containerID,
		Namespace: w.client.Namespace(),
		Upperdir:  upperdir,
		Workdir:   workdir,
	}

	c, err := w.client.Containerd().LoadContainer(ctx, containerID)
	if err != nil {
		return cInfo
	}

	info, err := c.Info(ctx)
	if err != nil {
		return cInfo
	}

	// Extract image
	cInfo.Image = info.Image

	// Parse image tag
	if parts := strings.SplitN(info.Image, ":", 2); len(parts) == 2 {
		cInfo.ImageTag = parts[1]
	}

	// Extract labels for K8s or Docker metadata
	labels := info.Labels
	if name, ok := labels["io.kubernetes.pod.name"]; ok {
		cInfo.PodName = name
	}
	if ns, ok := labels["io.kubernetes.pod.namespace"]; ok {
		cInfo.PodNamespace = ns
	}
	if name, ok := labels["io.kubernetes.container.name"]; ok {
		cInfo.Name = name
	}
	if cInfo.Name == "" {
		if name, ok := labels["nerdctl/name"]; ok {
			cInfo.Name = name
		}
	}

	return cInfo
}

func (w *Watcher) matchRule(cInfo model.ContainerInfo) (string, bool) {
	for _, rule := range w.cfg.Manager.MatchRules {
		if w.ruleMatches(rule, cInfo) {
			return rule.Size, true
		}
	}
	return "", false
}

func (w *Watcher) ruleMatches(rule config.MatchRule, cInfo model.ContainerInfo) bool {
	sel := rule.Selector
	switch rule.Level {
	case "container":
		name, ok := sel["name"]
		return ok && strings.Contains(cInfo.Name, name)
	case "image":
		name, ok := sel["name"]
		if !ok || !strings.Contains(cInfo.Image, name) {
			return false
		}
		if tag, ok := sel["tag"]; ok {
			return strings.Contains(cInfo.ImageTag, tag)
		}
		return true
	case "pod":
		name, ok := sel["name"]
		if !ok || !strings.Contains(cInfo.PodName, name) {
			return false
		}
		if ns, ok := sel["namespace"]; ok {
			return cInfo.PodNamespace == ns
		}
		return true
	default:
		return false
	}
}
