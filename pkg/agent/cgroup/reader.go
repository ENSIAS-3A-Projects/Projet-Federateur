package cgroup

// Package cgroup implements reading of cgroup v2 statistics for pods.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

const (
	// CgroupV2BasePath is the base path for cgroup v2.
	CgroupV2BasePath = "/sys/fs/cgroup"

	// CPUStatFile is the filename for CPU statistics in cgroup v2.
	CPUStatFile = "cpu.stat"
)

// PodSample holds a sample of pod cgroup statistics.
type PodSample struct {
	Timestamp     time.Time
	ThrottledTime int64 // microseconds
	UsageTime     int64 // microseconds
}

// DemandResult holds both demand and actual usage metrics.
type DemandResult struct {
	Demand           float64 // Normalized demand [0,1] from throttling ratio
	ActualUsageMilli int64   // Actual CPU usage in millicores
}

// Reader reads cgroup statistics for pods.
// ReadPodDemand is safe for concurrent use (protected by mu).
type Reader struct {
	mu sync.RWMutex
	// Last samples per pod (for delta calculation)
	samples map[string]*PodSample
	// Cache of discovered cgroup paths (to avoid repeated glob operations)
	pathCache map[string]string
}

// NewReader creates a new cgroup reader.
func NewReader() (*Reader, error) {
	// Verify cgroup v2 is available
	if _, err := os.Stat(CgroupV2BasePath); err != nil {
		return nil, fmt.Errorf("cgroup v2 not available at %s: %w", CgroupV2BasePath, err)
	}

	// Log cgroup structure for debugging
	klog.InfoS("Initializing cgroup reader", "basePath", CgroupV2BasePath)
	entries, err := os.ReadDir(CgroupV2BasePath)
	if err == nil {
		var dirs []string
		for _, e := range entries {
			if e.IsDir() {
				dirs = append(dirs, e.Name())
			}
		}
		klog.InfoS("Cgroup base directories", "dirs", dirs)
	}

	return &Reader{
		samples:   make(map[string]*PodSample),
		pathCache: make(map[string]string),
		mu:        sync.RWMutex{},
	}, nil
}

// ValidateAccess attempts to detect cgroups for at least one running container.
// Returns nil if cgroup detection is working, error otherwise.
// This should be called at agent startup to fail fast if the environment is broken.
func (r *Reader) ValidateAccess() error {
	entries, err := os.ReadDir(CgroupV2BasePath)
	if err != nil {
		return fmt.Errorf("cannot read cgroup base path %s: %w", CgroupV2BasePath, err)
	}

	hasKubepods := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "kubepods") {
			hasKubepods = true
			break
		}
	}

	if !hasKubepods {
		return fmt.Errorf("no kubepods cgroup hierarchy found in %s; found: %v", CgroupV2BasePath, directoryNames(entries))
	}

	pattern := filepath.Join(CgroupV2BasePath, "kubepods*", "**", CPUStatFile)
	matches, globErr := filepath.Glob(pattern)
	if globErr != nil {
		klog.V(4).InfoS("Glob pattern failed, trying alternative", "error", globErr)
	}

	if len(matches) == 0 {
		pattern = filepath.Join(CgroupV2BasePath, "kubepods*", "*", "*", CPUStatFile)
		matches, _ = filepath.Glob(pattern)
	}

	if len(matches) == 0 {
		return fmt.Errorf("no cpu.stat files found in kubepods hierarchy; cgroup detection will not work")
	}

	klog.InfoS("Cgroup validation passed", "cpuStatFilesFound", len(matches))
	return nil
}

func directoryNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

// ReadPodDemand reads the demand signal for a pod from its cgroup.
// Returns a normalized demand value in [0, 1] based on throttling ratio.
//
// Phase 3: Uses cgroup throttling ratio only (PSI not yet integrated).
// The demand is computed at pod level (pod-level cgroup).
// For multi-container pods, this aggregates across containers implicitly.
//
// Thread-safe: protected by internal mutex.
// Implements retry logic with exponential backoff for transient failures.
func (r *Reader) ReadPodDemand(pod *corev1.Pod) (float64, error) {
	cgroupPath, err := r.findPodCgroupPathWithRetry(pod)
	if err != nil {
		// FIXED: Log at higher verbosity to make failures visible
		klog.V(2).InfoS("Cgroup path not found for pod after retries",
			"pod", pod.Name,
			"namespace", pod.Namespace,
			"uid", pod.UID,
			"error", err)
		return 0.0, fmt.Errorf("find cgroup path: %w", err)
	}

	// Read current CPU statistics with retry
	sample, err := r.readCPUSampleWithRetry(cgroupPath, pod)
	if err != nil {
		return 0.0, fmt.Errorf("read CPU sample: %w", err)
	}

	// Calculate delta from last sample (with mutex protection)
	key := string(pod.UID)

	r.mu.Lock()
	lastSample := r.samples[key]

	var demand float64
	if lastSample != nil {
		// Compute throttling ratio: delta throttled / delta usage
		deltaThrottled := sample.ThrottledTime - lastSample.ThrottledTime
		deltaUsage := sample.UsageTime - lastSample.UsageTime

		// P1 Fix: Add minimum usage threshold to avoid numerical instability
		// If deltaUsage is below threshold, treat sample as invalid and return 0
		// (the Tracker will retain previous smoothed demand via EMA)
		const MinUsageUsec = int64(1000) // 1ms minimum usage for valid sample

		if deltaUsage < MinUsageUsec {
			// Insufficient usage data for stable ratio calculation
			// Return 0; Tracker's EMA will smooth this appropriately
			klog.V(4).InfoS("Skipping demand sample: insufficient usage",
				"pod", pod.Name,
				"namespace", pod.Namespace,
				"deltaUsage", deltaUsage,
				"minRequired", MinUsageUsec)
			demand = 0.0
		} else if deltaUsage > 0 {
			// Throttling ratio: how much time was throttled vs used
			throttlingRatio := float64(deltaThrottled) / float64(deltaUsage)

			// Normalize to [0, 1] range
			// Threshold: 0.1 (10% throttling) = 1.0 demand
			threshold := 0.1
			demand = throttlingRatio / threshold
			if demand > 1.0 {
				demand = 1.0
			}
			if demand < 0.0 {
				demand = 0.0
			}

			klog.V(4).InfoS("Pod throttling metrics",
				"pod", pod.Name,
				"namespace", pod.Namespace,
				"deltaThrottled", deltaThrottled,
				"deltaUsage", deltaUsage,
				"throttlingRatio", throttlingRatio,
				"normalizedDemand", demand)
			
		} else {
			// deltaUsage <= 0: counter reset or no activity
			demand = 0.0
		}
	} else {
		// First sample, no demand yet
		klog.V(4).InfoS("First cgroup sample for pod", "pod", pod.Name, "namespace", pod.Namespace)
		demand = 0.0
	}

	// Store sample for next delta calculation
	r.samples[key] = sample
	r.mu.Unlock()

	return demand, nil
}

// ReadPodMetrics reads both demand signal and actual CPU usage for a pod.
// Returns DemandResult with normalized demand [0,1] and actual usage in millicores.
// Thread-safe: protected by internal mutex.
// Implements retry logic with exponential backoff for transient failures.
func (r *Reader) ReadPodMetrics(pod *corev1.Pod, sampleIntervalSeconds float64) (DemandResult, error) {
	result := DemandResult{}

	cgroupPath, err := r.findPodCgroupPathWithRetry(pod)
	if err != nil {
		klog.V(2).InfoS("Cgroup path not found for pod after retries",
			"pod", pod.Name,
			"namespace", pod.Namespace,
			"uid", pod.UID,
			"error", err)
		return result, fmt.Errorf("find cgroup path: %w", err)
	}

	// Read current CPU statistics with retry
	sample, err := r.readCPUSampleWithRetry(cgroupPath, pod)
	if err != nil {
		return result, fmt.Errorf("read CPU sample: %w", err)
	}

	key := string(pod.UID)

	r.mu.Lock()
	defer r.mu.Unlock()

	lastSample := r.samples[key]

	if lastSample != nil {
		deltaThrottled := sample.ThrottledTime - lastSample.ThrottledTime
		deltaUsage := sample.UsageTime - lastSample.UsageTime
		deltaTime := sample.Timestamp.Sub(lastSample.Timestamp).Seconds()

		// Use actual sample interval if available, otherwise use parameter
		if deltaTime <= 0 {
			deltaTime = sampleIntervalSeconds
		}

		const MinUsageUsec = int64(1000) // 1ms minimum

		if deltaUsage >= MinUsageUsec {
			// Calculate actual CPU usage in millicores
			// deltaUsage is in microseconds, convert to millicores
			// millicores = (usageUsec / 1e6) / deltaTimeSeconds * 1000
			result.ActualUsageMilli = int64(float64(deltaUsage) / 1e6 / deltaTime * 1000)

			// Calculate throttling ratio for demand signal
			throttlingRatio := float64(deltaThrottled) / float64(deltaUsage)
			threshold := 0.1
			result.Demand = throttlingRatio / threshold
			if result.Demand > 1.0 {
				result.Demand = 1.0
			}
			if result.Demand < 0.0 {
				result.Demand = 0.0
			}

			klog.V(4).InfoS("Pod metrics",
				"pod", pod.Name,
				"namespace", pod.Namespace,
				"deltaUsage", deltaUsage,
				"deltaTime", deltaTime,
				"actualUsageMilli", result.ActualUsageMilli,
				"demand", result.Demand)
		}
	}

	// Store sample for next calculation
	r.samples[key] = sample

	return result, nil
}

// findPodCgroupPath finds the cgroup path for a pod.
// Supports cgroup v2 with kubelet conventions.
// FIXED: Added more patterns and better logging.
func (r *Reader) findPodCgroupPath(pod *corev1.Pod) (string, error) {
	podUID := string(pod.UID)

	// Check cache first
	r.mu.RLock()
	if cached, ok := r.pathCache[podUID]; ok {
		r.mu.RUnlock()
		return cached, nil
	}
	r.mu.RUnlock()

	// Sanitize UID for cgroup path (replace - with _)
	sanitizedUID := strings.ReplaceAll(podUID, "-", "_")

	// Try common cgroup v2 patterns (expanded list)
	patterns := []string{
		// Pattern 1: systemd cgroup driver with slice hierarchy (most common)
		fmt.Sprintf("kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod%s.slice", sanitizedUID),
		fmt.Sprintf("kubepods.slice/kubepods-besteffort.slice/kubepods-besteffort-pod%s.slice", sanitizedUID),
		fmt.Sprintf("kubepods.slice/kubepods-guaranteed.slice/kubepods-guaranteed-pod%s.slice", sanitizedUID),

		// Pattern 2: systemd with original UID format (dashes)
		fmt.Sprintf("kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod%s.slice", podUID),
		fmt.Sprintf("kubepods.slice/kubepods-besteffort.slice/kubepods-besteffort-pod%s.slice", podUID),

		// Pattern 3: cgroupfs driver (no slices)
		fmt.Sprintf("kubepods/burstable/pod%s", podUID),
		fmt.Sprintf("kubepods/besteffort/pod%s", podUID),
		fmt.Sprintf("kubepods/guaranteed/pod%s", podUID),

		// Pattern 4: Minikube specific patterns
		fmt.Sprintf("kubepods.slice/kubepods-pod%s.slice", sanitizedUID),
		fmt.Sprintf("kubepods/pod%s", podUID),

		// Pattern 5: Wildcard glob patterns (fallback)
		fmt.Sprintf("kubepods.slice/kubepods-*.slice/kubepods-*-pod%s.slice", sanitizedUID),
		fmt.Sprintf("kubepods/kubepods-*/kubepods-*-pod%s", podUID),
	}

	for _, pattern := range patterns {
		fullPattern := filepath.Join(CgroupV2BasePath, pattern)
		matches, err := filepath.Glob(fullPattern)
		if err != nil {
			continue
		}
		for _, match := range matches {
			// Verify it has cpu.stat
			cpuStatPath := filepath.Join(match, CPUStatFile)
			if _, err := os.Stat(cpuStatPath); err == nil {
				// Cache the result
				r.mu.Lock()
				r.pathCache[podUID] = match
				r.mu.Unlock()
				klog.V(3).InfoS("Found cgroup path for pod", "pod", pod.Name, "path", match)
				return match, nil
			}
		}
	}

	return "", fmt.Errorf("cgroup path not found for pod %s", pod.UID)
}

// readCPUSampleWithRetry reads CPU statistics with retry logic for transient failures.
func (r *Reader) readCPUSampleWithRetry(cgroupPath string, pod *corev1.Pod) (*PodSample, error) {
	const maxRetries = 3
	backoffDurations := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond}

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		sample, err := r.readCPUSample(cgroupPath)
		if err == nil {
			return sample, nil
		}

		lastErr = err

		// Check if error is retryable (transient filesystem issues)
		if !isRetryableError(err) {
			// Non-retryable error (e.g., parse error), return immediately
			return nil, err
		}

		// If not the last attempt, wait before retrying
		if attempt < maxRetries-1 {
			backoff := backoffDurations[attempt]
			klog.V(3).InfoS("Retrying cgroup read after transient error",
				"pod", pod.Name,
				"namespace", pod.Namespace,
				"attempt", attempt+1,
				"maxRetries", maxRetries,
				"backoff", backoff,
				"error", err)
			time.Sleep(backoff)
		}
	}

	return nil, fmt.Errorf("failed after %d retries: %w", maxRetries, lastErr)
}

// findPodCgroupPathWithRetry finds cgroup path with retry logic.
func (r *Reader) findPodCgroupPathWithRetry(pod *corev1.Pod) (string, error) {
	const maxRetries = 2
	backoffDurations := []time.Duration{50 * time.Millisecond, 100 * time.Millisecond}

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		path, err := r.findPodCgroupPath(pod)
		if err == nil {
			return path, nil
		}

		lastErr = err

		// If not the last attempt, wait before retrying
		if attempt < maxRetries-1 {
			backoff := backoffDurations[attempt]
			klog.V(3).InfoS("Retrying cgroup path discovery",
				"pod", pod.Name,
				"namespace", pod.Namespace,
				"attempt", attempt+1,
				"backoff", backoff)
			time.Sleep(backoff)
		}
	}

	return "", lastErr
}

// isRetryableError checks if an error is transient and retryable.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	// Check for transient filesystem errors
	retryablePatterns := []string{
		"no such file",
		"permission denied",
		"temporary failure",
		"resource temporarily unavailable",
		"i/o timeout",
	}

	for _, pattern := range retryablePatterns {
		if strings.Contains(strings.ToLower(errStr), pattern) {
			return true
		}
	}

	// Check for filesystem-related errors (likely retryable)
	if strings.Contains(errStr, "path") || strings.Contains(errStr, "file") {
		// Likely a filesystem error, retryable
		return true
	}

	// Parse errors and other non-transient errors are not retryable
	if strings.Contains(errStr, "parse") || strings.Contains(errStr, "invalid") {
		return false
	}

	// Default: assume retryable for filesystem operations
	return true
}

// readCPUSample reads CPU statistics from a cgroup path.
func (r *Reader) readCPUSample(cgroupPath string) (*PodSample, error) {
	cpuStatPath := filepath.Join(cgroupPath, CPUStatFile)
	data, err := os.ReadFile(cpuStatPath)
	if err != nil {
		return nil, fmt.Errorf("read cpu.stat: %w", err)
	}

	sample := &PodSample{
		Timestamp: time.Now(),
	}

	// Parse cpu.stat file
	// Format: key value (one per line)
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}

		key := fields[0]
		value, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}

		switch key {
		case "throttled_usec":
			sample.ThrottledTime = value
		case "usage_usec":
			sample.UsageTime = value
		}
	}

	return sample, nil
}

// Cleanup removes samples and path cache entries for pods that no longer exist.
func (r *Reader) Cleanup(existingPods map[string]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Clean samples
	for key := range r.samples {
		if !existingPods[key] {
			delete(r.samples, key)
			klog.V(5).InfoS("Cleaned up cgroup sample", "podUID", key)
		}
	}

	// Clean path cache
	for key := range r.pathCache {
		if !existingPods[key] {
			delete(r.pathCache, key)
			klog.V(5).InfoS("Cleaned up cgroup path cache", "podUID", key)
		}
	}
}
