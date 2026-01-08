package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"k8s.io/klog/v2"
)

// LatencyQuerier queries Prometheus for pod latency metrics.
type LatencyQuerier struct {
	client     v1.API
	cache      map[string]latencyCacheEntry
	cacheMu    sync.RWMutex
	cacheTTL   time.Duration
	prometheusURL string
}

type latencyCacheEntry struct {
	p95Latency float64
	p99Latency float64
	timestamp  time.Time
}

// NewLatencyQuerier creates a new Prometheus latency querier.
// If prometheusURL is empty, returns nil (graceful degradation).
func NewLatencyQuerier(prometheusURL string) (*LatencyQuerier, error) {
	if prometheusURL == "" {
		klog.V(2).InfoS("Prometheus URL not configured, latency queries will be disabled")
		return nil, nil
	}

	client, err := api.NewClient(api.Config{
		Address: prometheusURL,
	})
	if err != nil {
		return nil, fmt.Errorf("create Prometheus client: %w", err)
	}

	v1api := v1.NewAPI(client)

	return &LatencyQuerier{
		client:        v1api,
		cache:         make(map[string]latencyCacheEntry),
		cacheTTL:      5 * time.Second,
		prometheusURL: prometheusURL,
	}, nil
}

// QueryPodLatency queries Prometheus for pod latency metrics.
// Returns p95 and p99 latency in milliseconds.
// Returns 0, 0, nil if metrics unavailable (graceful degradation).
func (lq *LatencyQuerier) QueryPodLatency(ctx context.Context, namespace, pod string) (p95, p99 float64, err error) {
	if lq == nil {
		// Prometheus not configured, return zeros
		return 0, 0, nil
	}

	// Check cache first
	cacheKey := fmt.Sprintf("%s/%s", namespace, pod)
	lq.cacheMu.RLock()
	if entry, ok := lq.cache[cacheKey]; ok {
		if time.Since(entry.timestamp) < lq.cacheTTL {
			p95 := entry.p95Latency
			p99 := entry.p99Latency
			lq.cacheMu.RUnlock()
			return p95, p99, nil
		}
	}
	lq.cacheMu.RUnlock()

	// Query Prometheus for p95 latency
	// Using common HTTP request duration metric pattern
	p95Query := fmt.Sprintf(
		`histogram_quantile(0.95, sum(rate(http_request_duration_seconds_bucket{namespace="%s",pod="%s"}[1m])) by (le)) * 1000`,
		namespace, pod,
	)

	p99Query := fmt.Sprintf(
		`histogram_quantile(0.99, sum(rate(http_request_duration_seconds_bucket{namespace="%s",pod="%s"}[1m])) by (le)) * 1000`,
		namespace, pod,
	)

	// Query p95
	p95Result, warnings, err := lq.client.Query(ctx, p95Query, time.Now())
	if err != nil {
		klog.V(4).InfoS("Failed to query Prometheus for p95 latency",
			"namespace", namespace,
			"pod", pod,
			"error", err)
		// Return cached value if available, otherwise 0
		lq.cacheMu.RLock()
		if entry, ok := lq.cache[cacheKey]; ok {
			p95 = entry.p95Latency
			p99 = entry.p99Latency
		}
		lq.cacheMu.RUnlock()
		return p95, p99, nil // Graceful degradation
	}

	if len(warnings) > 0 {
		klog.V(3).InfoS("Prometheus query warnings", "warnings", warnings)
	}

	// Query p99
	p99Result, _, err := lq.client.Query(ctx, p99Query, time.Now())
	if err != nil {
		klog.V(4).InfoS("Failed to query Prometheus for p99 latency",
			"namespace", namespace,
			"pod", pod,
			"error", err)
		// Use p95 as fallback for p99
		p99Result = p95Result
	}

	// Extract values from Prometheus results
	p95 = extractValueFromResult(p95Result)
	p99 = extractValueFromResult(p99Result)

	// Update cache
	lq.cacheMu.Lock()
	lq.cache[cacheKey] = latencyCacheEntry{
		p95Latency: p95,
		p99Latency: p99,
		timestamp:  time.Now(),
	}
	lq.cacheMu.Unlock()

	return p95, p99, nil
}

// extractValueFromResult extracts a float64 value from Prometheus query result.
func extractValueFromResult(result model.Value) float64 {
	switch v := result.(type) {
	case model.Vector:
		if len(v) > 0 {
			return float64(v[0].Value)
		}
	case *model.Scalar:
		return float64(v.Value)
	}
	return 0
}



