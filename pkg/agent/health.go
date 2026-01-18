package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/klog/v2"
)

// HealthStatus represents the agent's health state.
type HealthStatus struct {
	Healthy           bool      `json:"healthy"`
	CgroupDetection   string    `json:"cgroupDetection"`
	PodsTracked       int       `json:"podsTracked"`
	LastSampleTime    time.Time `json:"lastSampleTime"`
	LastWriteTime     time.Time `json:"lastWriteTime"`
	SamplesSinceStart int64     `json:"samplesSinceStart"`
	WritesSinceStart  int64     `json:"writesSinceStart"`
	StartTime         time.Time `json:"startTime"`
	Uptime            string    `json:"uptime"`
	InGracePeriod     bool      `json:"inGracePeriod"`
}

// HealthServer provides HTTP health endpoints.
type HealthServer struct {
	agent *Agent
	mu    sync.RWMutex

	// Counters updated by agent
	lastSampleTime time.Time
	lastWriteTime  time.Time
	sampleCount    int64
	writeCount     int64
	cgroupStatus   string
}

// NewHealthServer creates a health server for the given agent.
func NewHealthServer(agent *Agent) *HealthServer {
	return &HealthServer{
		agent:        agent,
		cgroupStatus: "unknown",
	}
}

// RecordSample records a successful sample cycle.
func (h *HealthServer) RecordSample() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastSampleTime = time.Now()
	h.sampleCount++
}

// RecordWrite records a successful write cycle.
func (h *HealthServer) RecordWrite() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastWriteTime = time.Now()
	h.writeCount++
}

// SetCgroupStatus sets the cgroup detection status.
func (h *HealthServer) SetCgroupStatus(status string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cgroupStatus = status
}

// GetStatus returns the current health status.
func (h *HealthServer) GetStatus() HealthStatus {
	h.mu.RLock()
	defer h.mu.RUnlock()

	h.agent.mu.RLock()
	podsTracked := len(h.agent.podDemands)
	h.agent.mu.RUnlock()

	healthy := true
	uptime := time.Since(h.agent.startTime)

	// During startup grace period (first 60 seconds), only check cgroup status
	// This prevents probe failures during initial API discovery which can be slow
	if uptime < 60*time.Second {
		if h.cgroupStatus != "ok" {
			healthy = false
		}
	} else {
		// After grace period, require samples within last 30 seconds
		// (increased from 10s to handle slow API calls and high pod counts)
		if time.Since(h.lastSampleTime) > 30*time.Second && h.sampleCount > 0 {
			healthy = false
		}

		if h.cgroupStatus != "ok" {
			healthy = false
		}
	}

	return HealthStatus{
		Healthy:           healthy,
		CgroupDetection:   h.cgroupStatus,
		PodsTracked:       podsTracked,
		LastSampleTime:    h.lastSampleTime,
		LastWriteTime:     h.lastWriteTime,
		SamplesSinceStart: h.sampleCount,
		WritesSinceStart:  h.writeCount,
		StartTime:         h.agent.startTime,
		Uptime:            time.Since(h.agent.startTime).Round(time.Second).String(),
		InGracePeriod:     h.agent.isInGracePeriod(),
	}
}

// ServeHTTP handles health check requests.
func (h *HealthServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	status := h.GetStatus()

	w.Header().Set("Content-Type", "application/json")

	if status.Healthy {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	_ = json.NewEncoder(w).Encode(status)
}

// Start starts the health server on the given port.
func (h *HealthServer) Start(port int) {
	mux := http.NewServeMux()
	mux.Handle("/healthz", h)
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		status := h.GetStatus()
		if status.Healthy && status.SamplesSinceStart > 0 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ready"))
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready"))
		}
	})

	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		status := h.GetStatus()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(status)
	})
	mux.Handle("/", NewDashboardHandler(h.agent))

	addr := fmt.Sprintf(":%d", port)
	klog.InfoS("Starting health server", "address", addr)

	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil {
			klog.ErrorS(err, "Health server failed")
		}
	}()
}
