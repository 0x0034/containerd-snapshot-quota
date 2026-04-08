package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/klog/v2"

	"github.com/0x0034/containerd-snapshot-quota/pkg/manager"
	"github.com/0x0034/containerd-snapshot-quota/pkg/metrics"
)

// Server provides an HTTP API for quota management.
type Server struct {
	qManager *manager.QuotaManager
	srv      *http.Server
	registry *prometheus.Registry
}

// NewServer creates a web server bound to the given host and port.
func NewServer(host string, port int, qm *manager.QuotaManager) *Server {
	s := &Server{qManager: qm}

	// Prometheus registry with quota collector
	reg := prometheus.NewRegistry()
	reg.MustRegister(metrics.NewCollector(qm))
	reg.MustRegister(prometheus.NewGoCollector())
	reg.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	s.registry = reg

	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/api/quotas", s.handleListQuotas)
	mux.HandleFunc("/api/quotas/clean", s.handleClean)
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	s.srv = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", host, port),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	return s
}

// Start begins serving HTTP requests.
func (s *Server) Start() error {
	klog.Infof("Web server listening on %s", s.srv.Addr)
	go func() {
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			klog.Errorf("Web server error: %v", err)
		}
	}()
	return nil
}

// Stop gracefully shuts down the server.
func (s *Server) Stop(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

type listResponse struct {
	Page       int           `json:"page"`
	PageSize   int           `json:"pageSize"`
	Total      int           `json:"total"`
	TotalPages int           `json:"totalPages"`
	Quotas     []interface{} `json:"quotas"`
}

func (s *Server) handleListQuotas(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	quotas, err := s.qManager.ListQuotas()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	total := len(quotas)
	totalPages := (total + pageSize - 1) / pageSize
	start := (page - 1) * pageSize
	end := start + pageSize
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}

	items := make([]interface{}, 0, end-start)
	for _, q := range quotas[start:end] {
		items = append(items, q)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(listResponse{
		Page:       page,
		PageSize:   pageSize,
		Total:      total,
		TotalPages: totalPages,
		Quotas:     items,
	})
}

func (s *Server) handleClean(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	quotas, err := s.qManager.ListQuotas()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	cleaned := 0
	for _, q := range quotas {
		if err := s.qManager.Clear(q.ID); err != nil {
			klog.Warningf("Clean: failed to clear %s: %v", q.ID, err)
			continue
		}
		cleaned++
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"cleaned": cleaned,
		"total":   len(quotas),
	})
}
