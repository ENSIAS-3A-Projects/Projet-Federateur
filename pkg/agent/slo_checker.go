package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// SLOChecker checks SLO violations by querying Prometheus
type SLOChecker struct {
	prometheusURL string
	httpClient    *http.Client
}

// NewSLOChecker creates a new SLO checker
func NewSLOChecker(prometheusURL string) *SLOChecker {
	if prometheusURL == "" {
		return nil
	}
	return &SLOChecker{
		prometheusURL: prometheusURL,
		httpClient:    &http.Client{Timeout: 5 * time.Second},
	}
}

// CheckViolation checks if a pod is violating its SLO target
func (s *SLOChecker) CheckViolation(ctx context.Context, pod *corev1.Pod, targetMs float64) (bool, error) {
	if s == nil || targetMs <= 0 {
		return false, nil
	}

	query := fmt.Sprintf(
		`histogram_quantile(0.99, sum(rate(http_request_duration_seconds_bucket{pod="%s"}[1m])) by (le))`,
		pod.Name,
	)
	
	resp, err := s.httpClient.Get(fmt.Sprintf("%s/api/v1/query?query=%s", 
		s.prometheusURL, url.QueryEscape(query)))
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	var result struct {
		Data struct {
			Result []struct {
				Value []interface{} `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, err
	}

	if len(result.Data.Result) == 0 {
		return false, nil
	}

	p99Str, ok := result.Data.Result[0].Value[1].(string)
	if !ok {
		return false, nil
	}
	p99, err := strconv.ParseFloat(p99Str, 64)
	if err != nil {
		return false, nil
	}

	p99Ms := p99 * 1000
	return p99Ms > targetMs, nil
}
