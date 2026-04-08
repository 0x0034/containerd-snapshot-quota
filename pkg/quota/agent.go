package quota

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"k8s.io/klog/v2"

	"github.com/0x0034/containerd-snapshot-quota/pkg/config"
	"github.com/0x0034/containerd-snapshot-quota/pkg/manager"
	"github.com/0x0034/containerd-snapshot-quota/pkg/runtime/containerd"
	"github.com/0x0034/containerd-snapshot-quota/pkg/storage"
	"github.com/0x0034/containerd-snapshot-quota/pkg/web"
)

// Agent orchestrates the entire quota system lifecycle.
type Agent struct {
	cfg      *config.Config
	store    *storage.Store
	qManager *manager.QuotaManager
	watchers []*containerd.Watcher
	webSrv   *web.Server
	ctx      context.Context
	cancel   context.CancelFunc
}

// New creates a quota agent from configuration.
func New(cfg *config.Config) (*Agent, error) {
	ctx, cancel := context.WithCancel(context.Background())
	return &Agent{
		cfg:    cfg,
		ctx:    ctx,
		cancel: cancel,
	}, nil
}

// Start initializes all components and begins operation.
func (a *Agent) Start() error {
	// Initialize storage
	dbDir := filepath.Join(a.cfg.PersistDir, "badger")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return fmt.Errorf("create db dir: %w", err)
	}

	store, err := storage.New(dbDir)
	if err != nil {
		return fmt.Errorf("init storage: %w", err)
	}
	a.store = store

	// Start storage GC
	go a.storageGC()

	// Initialize quota manager
	qm, err := manager.New(
		a.cfg.Manager.MountPoint,
		a.cfg.Manager.ProjIDMin,
		a.cfg.Manager.ProjIDMax,
		a.store,
	)
	if err != nil {
		return fmt.Errorf("init quota manager: %w", err)
	}
	a.qManager = qm

	// Start web server if enabled
	if a.cfg.Web.Enabled {
		a.webSrv = web.NewServer(a.cfg.Web.Host, a.cfg.Web.Port, qm)
		if err := a.webSrv.Start(); err != nil {
			return fmt.Errorf("start web server: %w", err)
		}
	} else {
		klog.Info("Web server disabled by config")
	}

	// Start containerd watchers for each namespace
	for _, ns := range a.cfg.Manager.Namespaces {
		client, err := containerd.NewClient(a.cfg.Manager.Socket, ns)
		if err != nil {
			klog.Warningf("Failed to connect for namespace %s: %v", ns, err)
			continue
		}

		watcher := containerd.NewWatcher(client, qm, a.cfg)
		if err := watcher.Start(a.ctx); err != nil {
			klog.Warningf("Failed to start watcher for namespace %s: %v", ns, err)
			continue
		}
		a.watchers = append(a.watchers, watcher)
	}

	if len(a.watchers) == 0 {
		return fmt.Errorf("no watchers started, check containerd connection")
	}

	klog.Infof("Quota agent started: mode=%d namespaces=%v", a.cfg.Mode(), a.cfg.Manager.Namespaces)
	return nil
}

// WaitForShutdown blocks until a termination signal is received.
func (a *Agent) WaitForShutdown() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigCh
	klog.Infof("Received signal %v, shutting down...", sig)
	a.Stop()
}

// Stop gracefully shuts down all components.
func (a *Agent) Stop() {
	a.cancel()

	for _, w := range a.watchers {
		if err := w.Stop(); err != nil {
			klog.Warningf("Watcher stop error: %v", err)
		}
	}

	if a.webSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		a.webSrv.Stop(ctx)
	}

	if a.store != nil {
		a.store.Close()
	}

	klog.Info("Quota agent stopped")
}

func (a *Agent) storageGC() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			_ = a.store.RunGC()
		}
	}
}
