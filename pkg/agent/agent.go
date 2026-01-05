package agent

// Package agent implements Phase 3: Node Agent for kernel demand sensing.
// It reads kernel signals (cgroup throttling) and writes desired CPU
// allocations to PodAllocation CRDs.
//
// Phase 3 demand sensing:
//   - Uses cgroup throttling ratio as the primary demand signal
//   - PSI is not yet integrated (will be added as node-level modifier in future)
//   - Demand is computed per pod (pod-level cgroup)
//   - Multi-container pods: demand is aggregated at pod level
//   - Allocation targets the pod as a unit via PodAllocation
//
// Pod discovery:
//   - Currently uses direct API calls (discoverPods) every sampling cycle
//   - Future optimization: use informer/cache for better scalability

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"mbcas/pkg/agent/cgroup"
	"mbcas/pkg/agent/demand"
	"mbcas/pkg/allocation"
)

const (
	// SamplingInterval is how often we sample kernel signals.
	SamplingInterval = 1 * time.Second

	// WriteInterval is how often we write PodAllocation updates to the API.
	WriteInterval = 15 * time.Second

	// MinChangePercent is the minimum change required to write an update (hysteresis).
	// Lowered to 2% to respond faster to demand changes.
	MinChangePercent = 2.0

	// SystemReservePercent is the percentage of node CPU to reserve for system.
	SystemReservePercent = 10.0

	// BaselineCPUPerPod is the minimum CPU to allocate per pod.
	BaselineCPUPerPod = "100m"

	// StartupGracePeriod is the duration after startup during which
	// allocations are only allowed to increase, not decrease.
	// This prevents incorrect scale-downs when demand history is lost.
	StartupGracePeriod = 45 * time.Second
)

// Agent is the main node agent that senses kernel demand and writes allocations.
type Agent struct {
	k8sClient kubernetes.Interface
	nodeName  string
	ctx       context.Context
	cancel    context.CancelFunc

	// State
	podDemands     map[types.UID]*demand.Tracker
	podUsages      map[types.UID]int64  // pod UID -> actual CPU usage in millicores
	allocations    map[types.UID]string // pod UID -> current allocation
	originalLimits map[types.UID]int64  // pod UID -> original CPU limit in millicores (preserved across resizes)
	mu             sync.RWMutex

	// Startup grace period tracking
	startTime       time.Time
	gracePeriodDone bool

	// Components
	cgroupReader *cgroup.Reader
	demandCalc   *demand.Calculator
	writer       *Writer
	restConfig   *rest.Config

	healthServer *HealthServer
	previousMode allocation.AllocationMode
}

// NewAgent creates a new node agent.
func NewAgent(k8sClient kubernetes.Interface, restConfig *rest.Config, nodeName string) (*Agent, error) {
	ctx, cancel := context.WithCancel(context.Background())

	cgroupReader, err := cgroup.NewReader()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create cgroup reader: %w", err)
	}

	// Validate cgroup access before proceeding
	if err := cgroupReader.ValidateAccess(); err != nil {
		cancel()
		return nil, fmt.Errorf("cgroup validation failed: %w", err)
	}

	demandCalc := demand.NewCalculator()

	writer, err := NewWriter(restConfig)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create writer: %w", err)
	}

	agent := &Agent{
		k8sClient:      k8sClient,
		nodeName:       nodeName,
		ctx:            ctx,
		cancel:         cancel,
		podDemands:     make(map[types.UID]*demand.Tracker),
		podUsages:      make(map[types.UID]int64),
		allocations:    make(map[types.UID]string),
		originalLimits: make(map[types.UID]int64),
		cgroupReader:   cgroupReader,
		demandCalc:     demandCalc,
		writer:         writer,
		restConfig:     restConfig,
		startTime:      time.Now(),
		healthServer:   nil,
	}

	agent.healthServer = NewHealthServer(agent)

	return agent, nil
}

// Run starts the agent's main loop.
func (a *Agent) Run() error {
	klog.InfoS("Starting node agent", "node", a.nodeName)

	if a.healthServer != nil {
		a.healthServer.SetCgroupStatus("ok")
		a.healthServer.Start(8082)
	}

	// Start sampling loop
	go a.samplingLoop()

	// Start writing loop
	go a.writingLoop()

	// Wait for context cancellation
	<-a.ctx.Done()
	return a.ctx.Err()
}

// Stop stops the agent.
func (a *Agent) Stop() {
	a.cancel()
}

// samplingLoop samples kernel signals every SamplingInterval.
func (a *Agent) samplingLoop() {
	ticker := time.NewTicker(SamplingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			if err := a.sample(); err != nil {
				klog.ErrorS(err, "Sampling error")
			} else if a.healthServer != nil {
				a.healthServer.RecordSample()
			}
		}
	}
}

// sample performs one sampling cycle: discover pods and read their demand signals.
// Note: discoverPods() is called every sampling cycle (1s). This is acceptable for MVP
// but may be optimized later using informer/cache for better scalability on large nodes.
func (a *Agent) sample() error {
	// Discover pods on this node
	// TODO: Optimize with informer/cache for better scalability
	pods, err := a.discoverPods()
	if err != nil {
		return fmt.Errorf("discover pods: %w", err)
	}

	// Track which pods we've seen
	seen := make(map[types.UID]bool)

	// Sample each pod
	for _, pod := range pods {
		seen[pod.UID] = true

		// P0/P1 Fix: Capture original limit on first discovery (before any MBCAS modifications)
		// Sum limits across all normal containers (not init/ephemeral)
		a.mu.Lock()
		if _, hasOriginal := a.originalLimits[pod.UID]; !hasOriginal {
			totalLimitMilli := int64(0)
			for _, container := range pod.Spec.Containers {
				if limitCPU, ok := container.Resources.Limits[corev1.ResourceCPU]; ok {
					totalLimitMilli += limitCPU.MilliValue()
				}
			}
			if totalLimitMilli > 0 {
				a.originalLimits[pod.UID] = totalLimitMilli
				klog.InfoS("Captured original CPU limit for pod",
					"pod", pod.Name, "namespace", pod.Namespace,
					"originalLimitMilli", a.originalLimits[pod.UID],
					"containerCount", len(pod.Spec.Containers))
			}
		}
		a.mu.Unlock()

		// Read demand signal and actual usage from cgroup
		metrics, err := a.cgroupReader.ReadPodMetrics(pod, SamplingInterval.Seconds())
		rawDemand := metrics.Demand
		actualUsageMilli := metrics.ActualUsageMilli

		// Update demand tracker (applies smoothing)
		a.mu.Lock()
		tracker, exists := a.podDemands[pod.UID]
		if !exists {
			tracker = demand.NewTracker()
			a.podDemands[pod.UID] = tracker
		}

		if err != nil {
			shouldZero := tracker.RecordFailure()
			consecutive, total := tracker.FailureStats()

			if shouldZero {
				klog.ErrorS(err, "Sustained cgroup read failure, treating demand as zero",
					"pod", pod.Name,
					"namespace", pod.Namespace,
					"consecutiveFailures", consecutive,
					"totalFailures", total)
				rawDemand = 0.0
				actualUsageMilli = 0
			} else {
				klog.V(2).InfoS("Transient cgroup read failure, retaining previous demand",
					"pod", pod.Name,
					"namespace", pod.Namespace,
					"consecutiveFailures", consecutive,
					"totalFailures", total,
					"error", err)
				a.mu.Unlock()
				continue
			}
		} else {
			tracker.RecordSuccess()
		}

		smoothedDemand := tracker.Update(rawDemand)

		// Store actual usage for allocation calculation
		a.podUsages[pod.UID] = actualUsageMilli
		a.mu.Unlock()

		RecordDemand(pod.Namespace, pod.Name, rawDemand, smoothedDemand)

		// Log at info level so demand visibility is always on during demo
		klog.InfoS("Pod demand sample", "pod", pod.Name, "namespace", pod.Namespace,
			"raw", rawDemand, "smoothed", smoothedDemand)
	}

	// Remove pods that are no longer present
	a.mu.Lock()
	for uid := range a.podDemands {
		if !seen[uid] {
			delete(a.podDemands, uid)
			delete(a.podUsages, uid)
			delete(a.allocations, uid)
			delete(a.originalLimits, uid)
			klog.V(4).InfoS("Removed pod from tracking", "podUID", uid)
		}
	}
	a.mu.Unlock()

	return nil
}

// writingLoop writes PodAllocation updates every WriteInterval.
func (a *Agent) writingLoop() {
	ticker := time.NewTicker(WriteInterval)
	defer ticker.Stop()

	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			if err := a.writeAllocations(); err != nil {
				klog.ErrorS(err, "Writing allocations error")
			} else if a.healthServer != nil {
				a.healthServer.RecordWrite()
			}
		}
	}
}

// writeAllocations computes allocations and writes PodAllocation CRDs.
// Called every WriteInterval (15s) to reduce API churn.
// Only writes if change >= MinChangePercent (10% hysteresis).
func (a *Agent) writeAllocations() error {
	// Get node capacity
	node, err := a.k8sClient.CoreV1().Nodes().Get(a.ctx, a.nodeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get node: %w", err)
	}

	nodeCapacity, err := extractNodeCPUCapacity(node)
	if err != nil {
		return fmt.Errorf("extract node capacity: %w", err)
	}

	// Reserve system CPU
	systemReserve := nodeCapacity * SystemReservePercent / 100.0
	availableCPU := nodeCapacity - systemReserve
	availableMilli := int64(availableCPU * 1000)

	// Collect current demands
	a.mu.RLock()
	podUIDs := make([]types.UID, 0, len(a.podDemands))
	demands := make(map[types.UID]float64)
	for uid, tracker := range a.podDemands {
		podUIDs = append(podUIDs, uid)
		demands[uid] = tracker.Current()
	}
	a.mu.RUnlock()

	if len(demands) == 0 {
		klog.V(4).InfoS("No pods to allocate")
		return nil
	}

	// Get pod details for allocation
	pods, err := a.discoverPods()
	if err != nil {
		return fmt.Errorf("discover pods: %w", err)
	}
	podMap := make(map[types.UID]*corev1.Pod)
	for _, pod := range pods {
		podMap[pod.UID] = pod
	}

	// Compute allocations using market-clearing (Phase 4)
	result, limitAllocations, requestAllocations, podParams := a.computeAllocations(demands, availableCPU, podMap, nodeCapacity)

	// Mode transition logging
	if result.Mode != a.previousMode {
		klog.InfoS("Allocation mode changed",
			"previous", a.previousMode,
			"current", result.Mode,
			"totalNeed", result.TotalNeed,
			"capacity", availableMilli)
		a.previousMode = result.Mode
	}

	// Record system metrics
	modeInt := 0
	switch result.Mode {
	case allocation.ModeUncongested:
		modeInt = 0
	case allocation.ModeCongested:
		modeInt = 1
	case allocation.ModeOverloaded:
		modeInt = 2
	}

	nashProductLog := 0.0
	for uid, allocMilli := range result.Allocations {
		baseline := podParams[uid].MinMilli
		surplus := allocMilli - baseline
		if surplus > 0 {
			nashProductLog += math.Log(float64(surplus))
		}
	}

	RecordSystemMetrics(a.nodeName, modeInt, nashProductLog, result.TotalNeed, result.TotalAlloc, availableMilli)

	// Log decisions and record metrics before writing
	for uid, cpuLimit := range limitAllocations {
		pod := podMap[uid]
		params := podParams[uid]
		need := result.Needs[uid]
		cpuRequest := requestAllocations[uid]
		if pod != nil {
			RecordAllocation(pod.Namespace, pod.Name, result.Allocations[uid], need)
			klog.InfoS("Allocation decision",
				"pod", pod.Name,
				"namespace", pod.Namespace,
				"demand", fmt.Sprintf("%.3f", params.Demand),
				"actualUsage", params.ActualUsageMilli,
				"weight", params.Weight,
				"min", params.MinMilli,
				"max", params.MaxMilli,
				"need", need,
				"request", cpuRequest,
				"limit", cpuLimit,
				"mode", result.Mode)
		}
	}

	// Write allocations
	for uid, cpuLimit := range limitAllocations {
		pod, exists := podMap[uid]
		if !exists {
			klog.V(4).InfoS("Pod not found, skipping allocation", "podUID", uid)
			continue
		}

		cpuRequest := requestAllocations[uid]

		// Grace period protection: do not decrease allocations during startup
		if a.isInGracePeriod() {
			currentLimit, err := extractCurrentCPULimit(pod)
			if err == nil {
				currentMilli := parseCPUToMilli(currentLimit)
				desiredMilli := parseCPUToMilli(cpuLimit)
				if desiredMilli < currentMilli {
					klog.InfoS("Grace period: preventing allocation decrease",
						"pod", pod.Name,
						"namespace", pod.Namespace,
						"current", currentLimit,
						"desired", cpuLimit)
					continue
				}
			}
		}

		// Check if change is significant (compare both request and limit)
		a.mu.RLock()
		currentAlloc := a.allocations[uid]
		a.mu.RUnlock()

		// Combine request and limit for comparison (format: "request:limit")
		newAlloc := cpuRequest + ":" + cpuLimit
		if shouldWrite(currentAlloc, newAlloc) {
			if err := a.writer.WritePodAllocation(a.ctx, pod, cpuRequest, cpuLimit); err != nil {
				klog.ErrorS(err, "Failed to write PodAllocation", "pod", pod.Name, "namespace", pod.Namespace)
				continue
			}

			// Update tracked allocation
			a.mu.Lock()
			a.allocations[uid] = newAlloc
			a.mu.Unlock()

			klog.InfoS("Updated PodAllocation", "pod", pod.Name, "namespace", pod.Namespace,
				"request", cpuRequest, "limit", cpuLimit, "previous", currentAlloc)
		}
	}

	return nil
}

// ManagedLabel is the label that controls MBCAS management.
const ManagedLabel = "mbcas.io/managed"

// ExcludedNamespaces are namespaces that MBCAS never manages.
var ExcludedNamespaces = map[string]bool{
	"kube-system":     true,
	"kube-public":     true,
	"kube-node-lease": true,
	"mbcas-system":    true,
}

// discoverPods discovers all running pods on this node that MBCAS should manage.
// Implements "manage everyone" with namespace and label exclusions.
func (a *Agent) discoverPods() ([]*corev1.Pod, error) {
	// List all pods, filtered by nodeName
	podList, err := a.k8sClient.CoreV1().Pods("").List(a.ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("spec.nodeName=%s", a.nodeName),
	})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}

	// Filter to running, managed pods only
	var pods []*corev1.Pod
	for i := range podList.Items {
		pod := &podList.Items[i]

		// Skip non-running pods
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}

		// Skip excluded namespaces (kube-system, mbcas-system, etc.)
		if ExcludedNamespaces[pod.Namespace] {
			klog.V(5).InfoS("Skipping pod in excluded namespace", "pod", pod.Name, "namespace", pod.Namespace)
			continue
		}

		// Skip pods with explicit opt-out label
		if val, ok := pod.Labels[ManagedLabel]; ok && val == "false" {
			klog.V(5).InfoS("Skipping pod with opt-out label", "pod", pod.Name, "namespace", pod.Namespace)
			continue
		}

		pods = append(pods, pod)
	}

	return pods, nil
}

// computeAllocations computes CPU allocations using market-clearing (Phase 4).
//
// Phase 4: Market-based allocation using Fisher-market / proportional fairness.
//   - Uses Kubernetes-native primitives (requests, limits, PriorityClass)
//   - Maximizes Nash Social Welfare: Σ log(cpu_i)
//   - Handles bounds correctly (min/max enforcement)
//   - Deterministic water-filling with caps
func (a *Agent) computeAllocations(
	demands map[types.UID]float64,
	availableCPU float64,
	podMap map[types.UID]*corev1.Pod,
	nodeCapacity float64,
) (allocation.AllocationResult, map[types.UID]string, map[types.UID]string, map[types.UID]allocation.PodParams) {
	// Parse baseline once (not in hot loop)
	baselineQty, _ := resource.ParseQuantity(BaselineCPUPerPod)
	baselineMilli := baselineQty.MilliValue()

	// Convert capacities to millicores
	availableMilli := int64(availableCPU * 1000)
	nodeCapMilli := int64(nodeCapacity * 1000)

	// Build PodParams for each pod
	podParams := make(map[types.UID]allocation.PodParams)
	a.mu.RLock()
	for uid, demand := range demands {
		pod, exists := podMap[uid]
		if !exists {
			continue
		}

		// Get actual usage for this pod (0 if not available)
		actualUsageMilli := a.podUsages[uid]

		// Use the new function that includes actual usage
		params := a.demandCalc.ParamsForPodWithUsage(pod, demand, actualUsageMilli, baselineMilli, nodeCapMilli)

		// Override MaxMilli with original limit if we have it stored
		// This prevents feedback loop where applied limits become new max
		if originalLimit, hasOriginal := a.originalLimits[uid]; hasOriginal && originalLimit > 0 {
			// Use original limit as max, but cap at 90% node capacity
			perPodMaxCap := int64(float64(nodeCapMilli) * 0.9)
			if originalLimit > perPodMaxCap {
				params.MaxMilli = perPodMaxCap
			} else {
				params.MaxMilli = originalLimit
			}
			// Ensure max >= min
			if params.MaxMilli < params.MinMilli {
				params.MaxMilli = params.MinMilli
			}
		}

		podParams[uid] = params
	}
	a.mu.RUnlock()

	// Clear market using Phase 4 solver
	result := allocation.ClearMarketWithMetadata(availableMilli, podParams)

	// Convert limits to string format ("${m}m")
	limitAllocations := make(map[types.UID]string)
	for uid, milli := range result.Allocations {
		limitAllocations[uid] = fmt.Sprintf("%dm", milli)
	}

	// Convert requests to string format ("${m}m")
	requestAllocations := make(map[types.UID]string)
	for uid, milli := range result.RequestAllocations {
		requestAllocations[uid] = fmt.Sprintf("%dm", milli)
	}

	return result, limitAllocations, requestAllocations, podParams
}

// extractNodeCPUCapacity extracts CPU capacity from node status.
func extractNodeCPUCapacity(node *corev1.Node) (float64, error) {
	cpuStr, ok := node.Status.Capacity[corev1.ResourceCPU]
	if !ok {
		return 0, fmt.Errorf("node has no CPU capacity")
	}

	cpuMilli := cpuStr.MilliValue()
	return float64(cpuMilli) / 1000.0, nil
}

// isInGracePeriod returns true if the agent is still in the startup grace period.
// During this period, allocations should only increase, not decrease.
func (a *Agent) isInGracePeriod() bool {
	if a.gracePeriodDone {
		return false
	}

	if time.Since(a.startTime) > StartupGracePeriod {
		a.gracePeriodDone = true
		klog.InfoS("Startup grace period ended", "duration", StartupGracePeriod)
		return false
	}

	return true
}

// extractCurrentCPULimit gets the current CPU limit from pod spec.
func extractCurrentCPULimit(pod *corev1.Pod) (string, error) {
	if len(pod.Spec.Containers) == 0 {
		return "", fmt.Errorf("pod has no containers")
	}

	container := pod.Spec.Containers[0]
	if limit, ok := container.Resources.Limits[corev1.ResourceCPU]; ok {
		return limit.String(), nil
	}

	return "", fmt.Errorf("no CPU limit set")
}

// parseCPUToMilli converts a CPU string like "500m" or "1" to millicores.
func parseCPUToMilli(cpu string) int64 {
	qty, err := resource.ParseQuantity(cpu)
	if err != nil {
		return 0
	}
	return qty.MilliValue()
}

// shouldWrite checks if the change is significant enough to write.
func shouldWrite(current, new string) bool {
	if current == "" {
		return true // First write
	}

	currentQty, err1 := resource.ParseQuantity(current)
	newQty, err2 := resource.ParseQuantity(new)
	if err1 != nil || err2 != nil {
		return true // Parse error, write anyway
	}

	currentMilli := currentQty.MilliValue()
	newMilli := newQty.MilliValue()

	if currentMilli == 0 {
		return newMilli > 0 // Avoid division by zero
	}

	changePercent := float64(abs(newMilli-currentMilli)) * 100.0 / float64(abs(currentMilli))
	return changePercent >= MinChangePercent
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
