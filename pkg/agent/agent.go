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
	"strconv"
	"strings"
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
	"mbcas/pkg/price"
	"mbcas/pkg/stability"
	mbcastypes "mbcas/pkg/types"
)

// Default constants (used as fallback if config loading fails)
const (
	// DefaultSamplingInterval is the default sampling interval.
	DefaultSamplingInterval = 1 * time.Second

	// DefaultWriteInterval is the default write interval.
	DefaultWriteInterval = 15 * time.Second

	// DefaultMinChangePercent is the default minimum change percent.
	DefaultMinChangePercent = 2.0

	// DefaultSystemReservePercent is the default system reserve percent.
	DefaultSystemReservePercent = 10.0

	// DefaultBaselineCPUPerPod is the default baseline CPU per pod.
	DefaultBaselineCPUPerPod = "100m"

	// DefaultStartupGracePeriod is the default startup grace period.
	DefaultStartupGracePeriod = 45 * time.Second
)

// EnableCoalitionFormation controls whether coalition formation and Shapley values are used.
// Set to false initially since no tracing system is available.
// When tracing is integrated, set this to true to enable coalition-based coordination.
const EnableCoalitionFormation = false

// Agent is the main node agent that senses kernel demand and writes allocations.
type Agent struct {
	k8sClient kubernetes.Interface
	nodeName  string
	ctx       context.Context
	cancel    context.CancelFunc

	// State
	podDemands       map[types.UID]*demand.Tracker
	podUsages        map[types.UID]int64  // pod UID -> actual CPU usage in millicores
	allocations      map[types.UID]string // pod UID -> current allocation
	originalLimits   map[types.UID]int64  // pod UID -> original CPU limit in millicores (preserved across resizes)
	originalRequests map[types.UID]int64  // pod UID -> original CPU request in millicores (preserved across resizes)
	podSampled       map[types.UID]bool   // pod UID -> whether the pod has been successfully sampled
	mu               sync.RWMutex

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

	// Configuration
	config *AgentConfig

	// Pod informer for efficient pod discovery
	podInformer *PodInformer

	// Shadow prices for price signal coordination
	shadowPrices *price.ShadowPrices

	// Latency querier for Prometheus metrics
	latencyQuerier *LatencyQuerier

	// Lyapunov stability controller
	lyapunovController *stability.LyapunovController

	// Fast guardrail for SLO protection
	fastGuardrail *FastGuardrail

	// Kalman filter predictor for demand forecasting
	predictor *demand.Predictor

	// Agent-based modeling: per-pod agent states
	agentStates map[types.UID]*PodAgentState
	ql          *QLearning // Q-learning instance for all agents
}

// NewAgent creates a new node agent.
func NewAgent(k8sClient kubernetes.Interface, restConfig *rest.Config, nodeName string) (*Agent, error) {
	ctx, cancel := context.WithCancel(context.Background())

	// Load configuration from ConfigMap or environment variables
	config, err := LoadConfig(ctx, k8sClient)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("load configuration: %w", err)
	}

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

	demandCalc := demand.NewCalculator(k8sClient)

	writer, err := NewWriter(restConfig)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create writer: %w", err)
	}

	// Create pod informer for efficient pod discovery
	podInformer, err := NewPodInformer(ctx, k8sClient, nodeName)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create pod informer: %w", err)
	}

	// Create latency querier (may be nil if Prometheus not configured)
	latencyQuerier, err := NewLatencyQuerier(config.PrometheusURL)
	if err != nil {
		klog.Warningf("Failed to create latency querier, continuing without latency metrics: %v", err)
		latencyQuerier = nil
	}

	// Create Lyapunov stability controller
	lyapunovController := stability.NewLyapunovController(
		0.1, // initialStepSize
		0.2, // minStepSize (increased from 0.01 for faster convergence)
		1.0, // maxStepSize
	)

	// Create Kalman filter predictor for demand forecasting (if enabled)
	var predictor *demand.Predictor
	if config.EnableKalmanPrediction {
		predictor = demand.NewPredictor()
	}

	// Initialize Q-learning if agent-based modeling is enabled
	var ql *QLearning
	if config.EnableAgentBasedModeling {
		ql = NewQLearning(
			config.AgentLearningRate,
			config.AgentDiscountFactor,
			config.AgentExplorationRate,
		)
	}

	agent := &Agent{
		k8sClient:          k8sClient,
		nodeName:           nodeName,
		ctx:                ctx,
		cancel:             cancel,
		podDemands:         make(map[types.UID]*demand.Tracker),
		podUsages:          make(map[types.UID]int64),
		allocations:        make(map[types.UID]string),
		originalLimits:     make(map[types.UID]int64),
		originalRequests:   make(map[types.UID]int64),
		podSampled:         make(map[types.UID]bool),
		cgroupReader:       cgroupReader,
		demandCalc:         demandCalc,
		writer:             writer,
		restConfig:         restConfig,
		startTime:          time.Now(),
		healthServer:       nil,
		config:             config,
		podInformer:        podInformer,
		latencyQuerier:     latencyQuerier,
		lyapunovController: lyapunovController,
		predictor:          predictor,
		agentStates:        make(map[types.UID]*PodAgentState),
		ql:                 ql,
	}

	// Create fast guardrail
	agent.fastGuardrail = NewFastGuardrail(
		config,
		k8sClient,
		writer,
		latencyQuerier,
		ctx,
	)

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

	// Start fast guardrail loop (1-2s)
	go a.fastGuardrailLoop()

	// Start slow optimizer loop (5-15s)
	go a.slowOptimizerLoop()

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
	ticker := time.NewTicker(a.config.SamplingInterval)
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
// Uses informer cache for efficient pod discovery.
func (a *Agent) sample() error {
	// Discover pods on this node using informer cache
	pods, err := a.discoverPods()
	if err != nil {
		return fmt.Errorf("discover pods: %w", err)
	}

	// Track which pods we've seen
	seen := make(map[types.UID]bool)

	// Sample each pod
	for _, pod := range pods {
		seen[pod.UID] = true

		// P0/P1 Fix: Capture original limit and request on first discovery (before any MBCAS modifications)
		// CRITICAL: Check PodAllocation status to see if pod was already modified by MBCAS
		// If modified, use the PodAllocation's initial desired values as "original" (fallback)
		// Sum limits and requests across all normal containers (not init/ephemeral)
		a.mu.Lock()
		if _, hasOriginal := a.originalLimits[pod.UID]; !hasOriginal {
			totalLimitMilli := int64(0)
			totalRequestMilli := int64(0)
			for _, container := range pod.Spec.Containers {
				if limitCPU, ok := container.Resources.Limits[corev1.ResourceCPU]; ok {
					totalLimitMilli += limitCPU.MilliValue()
				}
				if requestCPU, ok := container.Resources.Requests[corev1.ResourceCPU]; ok {
					totalRequestMilli += requestCPU.MilliValue()
				}
			}

			// SAFEGUARD: If current values seem too low (below typical baseline),
			// Check if this might be a modified pod. Use QoS-based defaults as fallback.
			// Guaranteed pods should have requests = limits, Burstable should have requests
			const minExpectedRequest = int64(100) // Minimum expected for any managed pod
			if totalRequestMilli > 0 && totalRequestMilli < minExpectedRequest {
				// Suspiciously low - might be already modified
				// For Guaranteed pods, expect request = limit
				if pod.Status.QOSClass == corev1.PodQOSGuaranteed && totalLimitMilli > 0 {
					klog.InfoS("Guaranteed pod has suspiciously low request, using limit as fallback",
						"pod", pod.Name, "namespace", pod.Namespace,
						"currentRequest", totalRequestMilli,
						"limit", totalLimitMilli)
					totalRequestMilli = totalLimitMilli // Guaranteed: request = limit
				}
			}

			// If still no reasonable value, use QoS-based defaults
			if totalRequestMilli == 0 {
				baselineQty, _ := resource.ParseQuantity(a.config.BaselineCPUPerPod)
				baselineMilli := baselineQty.MilliValue()

				switch pod.Status.QOSClass {
				case corev1.PodQOSGuaranteed:
					// Guaranteed: use limit as request (or baseline if no limit)
					if totalLimitMilli > 0 {
						totalRequestMilli = totalLimitMilli
					} else {
						totalRequestMilli = baselineMilli * 2 // Conservative default
						totalLimitMilli = totalRequestMilli
					}
				case corev1.PodQOSBurstable:
					// Burstable: use baseline as request
					totalRequestMilli = baselineMilli
					if totalLimitMilli == 0 {
						totalLimitMilli = baselineMilli * 2
					}
				default: // BestEffort
					// BestEffort: no request, but set a limit for allocation purposes
					totalRequestMilli = baselineMilli / 2
					if totalLimitMilli == 0 {
						totalLimitMilli = baselineMilli
					}
				}
			}

			if totalLimitMilli > 0 {
				a.originalLimits[pod.UID] = totalLimitMilli
			}
			if totalRequestMilli > 0 {
				a.originalRequests[pod.UID] = totalRequestMilli
			}
			if totalLimitMilli > 0 || totalRequestMilli > 0 {
				klog.InfoS("Captured original CPU resources for pod",
					"pod", pod.Name, "namespace", pod.Namespace,
					"originalLimitMilli", totalLimitMilli,
					"originalRequestMilli", totalRequestMilli,
					"containerCount", len(pod.Spec.Containers))
			}
		}
		a.mu.Unlock()

		// Read demand signal and actual usage from cgroup
		cgroupStart := time.Now()
		metrics, err := a.cgroupReader.ReadPodMetrics(pod, a.config.SamplingInterval.Seconds())
		RecordCgroupReadDuration(pod.Namespace, pod.Name, time.Since(cgroupStart))
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
			a.podSampled[pod.UID] = true
		}

		smoothedDemand := tracker.Update(rawDemand)

		// Store actual usage for allocation calculation
		a.podUsages[pod.UID] = actualUsageMilli
		a.mu.Unlock()

		// Update fast guardrail state
		if a.fastGuardrail != nil {
			a.fastGuardrail.UpdatePodState(pod.UID, smoothedDemand, actualUsageMilli)
		}

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
			delete(a.originalRequests, uid)
			delete(a.podSampled, uid)
			klog.V(4).InfoS("Removed pod from tracking", "podUID", uid)
		}
	}
	a.mu.Unlock()

	// Remove from fast guardrail
	if a.fastGuardrail != nil {
		for uid := range a.podDemands {
			if !seen[uid] {
				a.fastGuardrail.RemovePod(uid)
			}
		}
	}

	return nil
}

// fastGuardrailLoop runs the fast SLO protection loop (1-2s).
// This loop responds quickly to SLO violations and throttling pressure.
func (a *Agent) fastGuardrailLoop() {
	ticker := time.NewTicker(a.config.FastLoopInterval)
	defer ticker.Stop()

	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			if err := a.runFastGuardrail(); err != nil {
				klog.ErrorS(err, "Fast guardrail error")
			}
		}
	}
}

// runFastGuardrail checks all pods for SLO violations and applies fast-up if needed.
func (a *Agent) runFastGuardrail() error {
	pods, err := a.discoverPods()
	if err != nil {
		return fmt.Errorf("discover pods: %w", err)
	}

	for _, pod := range pods {
		if a.fastGuardrail != nil {
			applied, err := a.fastGuardrail.CheckAndApplyFastUp(pod)
			if err != nil {
				klog.ErrorS(err, "Fast guardrail check failed", "pod", pod.Name, "namespace", pod.Namespace)
				continue
			}
			if applied {
				// Allocation was updated by fast guardrail, it will be tracked there
				// The slow loop will pick it up on next cycle
			}
		}
	}

	return nil
}

// slowOptimizerLoop runs the slow economic optimizer loop (5-15s).
// This loop runs market clearing and optimization.
func (a *Agent) slowOptimizerLoop() {
	ticker := time.NewTicker(a.config.SlowLoopInterval)
	defer ticker.Stop()

	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			if err := a.writeAllocations(); err != nil {
				klog.ErrorS(err, "Slow optimizer error")
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
	systemReserve := nodeCapacity * a.config.SystemReservePercent / 100.0
	availableCPU := nodeCapacity - systemReserve
	availableMilli := int64(availableCPU * 1000)

	// Get pod details for allocation FIRST (to include newly discovered pods)
	discoveryStart := time.Now()
	pods, err := a.discoverPods()
	if err != nil {
		return fmt.Errorf("discover pods: %w", err)
	}
	RecordPodDiscoveryDuration(time.Since(discoveryStart))
	podMap := make(map[types.UID]*corev1.Pod)
	for _, pod := range pods {
		podMap[pod.UID] = pod
	}

	// Collect current demands (from sampled pods)
	a.mu.RLock()
	demands := make(map[types.UID]float64)
	for uid, tracker := range a.podDemands {
		demands[uid] = tracker.Current()
	}
	// Initialize demands for newly discovered pods (not yet sampled)
	// This ensures ALL discovered pods get allocations, not just those already sampled
	for uid, pod := range podMap {
		if _, hasDemand := demands[uid]; !hasDemand {
			// Newly discovered pod: initialize with zero demand
			// It will get sampled in the next sampling cycle
			demands[uid] = 0.0
			klog.V(4).InfoS("Initializing demand for newly discovered pod",
				"pod", pod.Name, "namespace", pod.Namespace)
		}
	}
	a.mu.RUnlock()

	if len(demands) == 0 {
		klog.V(4).InfoS("No pods to allocate")
		return nil
	}

	// Compute allocations using market-clearing (Phase 4)
	computeStart := time.Now()
	result, limitAllocations, requestAllocations, podParams := a.computeAllocations(demands, availableCPU, podMap, nodeCapacity)
	RecordAllocationComputeDuration(time.Since(computeStart))
	RecordNashSolverIterations(result.NashIterations)

	// Compute shadow prices after allocation (for price signal coordination)
	allocationParams := a.convertPodParamsToAllocationParams(podParams)
	shadowPrices := price.ComputeShadowPrices(result.Allocations, allocationParams, availableMilli)
	a.shadowPrices = shadowPrices
	RecordShadowPrice(shadowPrices.GetCPU())

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

		// FINAL SAFETY CHECK: Ensure we never write allocations below minimums
		// Minimums are determined by:
		// 1. Annotation mbcas.io/min-cpu (highest priority)
		// 2. QoS class defaults (Guaranteed = request, Burstable/BestEffort = baseline)
		// 3. Contention-aware adjustments (reclaimable pods get lower minimums during contention)

		// Detect contention: check if any Guaranteed pods are throttled
		contentionDetected := false
		a.mu.RLock()
		for otherUID, otherDemand := range demands {
			otherPod, exists := podMap[otherUID]
			if exists && otherPod.Status.QOSClass == corev1.PodQOSGuaranteed && otherDemand > 0.3 {
				contentionDetected = true
				break
			}
		}
		a.mu.RUnlock()

		// Get minimum CPU from annotation (if present)
		workloadMinMilli := getMinCPUFromAnnotation(pod, 0)

		// If no annotation, use QoS-based defaults
		if workloadMinMilli == 0 {
			switch pod.Status.QOSClass {
			case corev1.PodQOSGuaranteed:
				// Guaranteed pods: minimum = original request
				// CRITICAL FIX: Use originalRequests instead of current pod.Spec.Containers[0].Resources.Requests
				if originalReq, ok := a.originalRequests[uid]; ok {
					workloadMinMilli = originalReq
				} else if len(pod.Spec.Containers) > 0 {
					if requestCPU, ok := pod.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]; ok {
						workloadMinMilli = requestCPU.MilliValue()
					}
				}
				// Fallback to baseline if no request
				if workloadMinMilli == 0 {
					baselineQty, _ := resource.ParseQuantity(a.config.BaselineCPUPerPod)
					workloadMinMilli = baselineQty.MilliValue()
				}
			case corev1.PodQOSBurstable:
				// Burstable pods: baseline minimum, lower during contention
				baselineQty, _ := resource.ParseQuantity(a.config.BaselineCPUPerPod)
				if contentionDetected {
					workloadMinMilli = baselineQty.MilliValue() / 2 // Half baseline during contention
				} else {
					workloadMinMilli = baselineQty.MilliValue()
				}
			case corev1.PodQOSBestEffort:
				// BestEffort pods: very low minimum, even lower during contention
				baselineQty, _ := resource.ParseQuantity(a.config.BaselineCPUPerPod)
				if contentionDetected {
					workloadMinMilli = baselineQty.MilliValue() / 4 // Quarter baseline during contention
				} else {
					workloadMinMilli = baselineQty.MilliValue() / 2 // Half baseline normally
				}
			default:
				// Unknown QoS: use baseline
				baselineQty, _ := resource.ParseQuantity(a.config.BaselineCPUPerPod)
				workloadMinMilli = baselineQty.MilliValue()
			}
		}

		limitMilli := parseCPUToMilli(cpuLimit)
		requestMilli := parseCPUToMilli(cpuRequest)

		if workloadMinMilli > 0 {
			if limitMilli < workloadMinMilli {
				klog.InfoS("Allocation below workload minimum, clamping",
					"pod", pod.Name,
					"namespace", pod.Namespace,
					"computedLimit", cpuLimit,
					"workloadMin", workloadMinMilli,
					"clampedLimit", fmt.Sprintf("%dm", workloadMinMilli))
				cpuLimit = fmt.Sprintf("%dm", workloadMinMilli)
				limitMilli = workloadMinMilli
			}
			// Request should be at least 90% of limit (but not below workload min)
			minRequestMilli := int64(float64(limitMilli) * 0.9)
			if minRequestMilli < workloadMinMilli {
				minRequestMilli = workloadMinMilli
			}
			if requestMilli < minRequestMilli {
				cpuRequest = fmt.Sprintf("%dm", minRequestMilli)
			}
		}

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
		shouldWriteResult := shouldWrite(currentAlloc, newAlloc, a.config.MinChangePercent)

		if shouldWriteResult {
			// Get current shadow price for status update
			shadowPrice := 0.0
			if a.shadowPrices != nil {
				shadowPrice = a.shadowPrices.GetCPU()
			}
			if err := a.writer.WritePodAllocation(a.ctx, pod, cpuRequest, cpuLimit, shadowPrice); err != nil {
				klog.ErrorS(err, "Failed to write PodAllocation", "pod", pod.Name, "namespace", pod.Namespace)
				continue
			}

			// Update tracked allocation
			a.mu.Lock()
			a.allocations[uid] = newAlloc
			a.mu.Unlock()

			// Update fast guardrail allocation tracking
			if a.fastGuardrail != nil {
				a.fastGuardrail.UpdateAllocation(uid, newAlloc)
			}

			klog.InfoS("Updated PodAllocation", "pod", pod.Name, "namespace", pod.Namespace,
				"request", cpuRequest, "limit", cpuLimit, "previous", currentAlloc)
		} else {
			// Allocations didn't change, but update shadow price if it changed
			// This ensures shadow price is always current even when allocations are stable
			shadowPrice := 0.0
			if a.shadowPrices != nil {
				shadowPrice = a.shadowPrices.GetCPU()
			}
			// WritePodAllocation will check if shadow price needs updating and update only status if needed
			if err := a.writer.WritePodAllocation(a.ctx, pod, cpuRequest, cpuLimit, shadowPrice); err != nil {
				klog.V(4).InfoS("Failed to update shadow price (non-critical)", "pod", pod.Name, "namespace", pod.Namespace, "error", err)
			}
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
// Uses informer cache for efficient lookups instead of direct API calls.
func (a *Agent) discoverPods() ([]*corev1.Pod, error) {
	// Use informer cache if available (preferred method)
	if a.podInformer != nil && a.podInformer.HasSynced() {
		return a.podInformer.ListPods()
	}

	// Fallback to direct API call if informer not available (backward compatibility)
	klog.V(3).InfoS("Using direct API call for pod discovery (informer not available)")
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

		// Skip pods with explicit opt-out label (EXCEPT noise pods - we want to manage them with lower priority)
		// Noise pods should be included in allocation so we can reclaim CPU from them
		if val, ok := pod.Labels[ManagedLabel]; ok && val == "false" {
			// Special case: Include BestEffort pods even if marked unmanaged, but with lower priority
			// This allows MBCAS to reclaim CPU from low-priority pods during contention
			if pod.Status.QOSClass != corev1.PodQOSBestEffort {
				klog.V(5).InfoS("Skipping pod with opt-out label", "pod", pod.Name, "namespace", pod.Namespace)
				continue
			} else {
				klog.V(4).InfoS("Including BestEffort pod despite opt-out label (for CPU reclamation)",
					"pod", pod.Name, "namespace", pod.Namespace,
					"qos", pod.Status.QOSClass)
			}
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
	baselineQty, _ := resource.ParseQuantity(a.config.BaselineCPUPerPod)
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

		// Use Kalman filter to predict next demand (anticipatory allocation)
		if a.config.EnableKalmanPrediction && a.predictor != nil {
			predictedDemand := a.predictor.PredictNext(uid, demand)
			// Use max of current and predicted to handle both spikes and trends
			demand = math.Max(demand, predictedDemand)
		}

		// Get actual usage for this pod (0 if not available)
		actualUsageMilli := a.podUsages[uid]

		// If no usage tracked yet (unknown pod), estimate from original requests or baseline
		// CRITICAL FIX: Only estimate if NEVER successfully sampled (!a.podSampled[uid]).
		// If sampled but idle (actualUsageMilli == 0), don't estimate - it's truly idle.
		if !a.podSampled[uid] && actualUsageMilli == 0 {
			if originalReq, ok := a.originalRequests[uid]; ok && originalReq > 0 {
				// Estimate from original request (safe baseline)
				actualUsageMilli = originalReq
			} else {
				// Fallback to baseline
				actualUsageMilli = baselineMilli
			}
		}
		// Get shadow price from previous cycle for price response
		shadowPriceCPU := 0.0
		if a.shadowPrices != nil {
			shadowPriceCPU = a.shadowPrices.GetCPU()
		}

		// Use price-responsive function if enabled, otherwise use standard function
		var params allocation.PodParams
		if a.config.EnablePriceResponse && shadowPriceCPU > 0 {
			// Apply price response: agents adjust demand based on shadow prices
			params = a.demandCalc.ParamsForPodWithPrice(
				pod, demand, actualUsageMilli, baselineMilli, nodeCapMilli,
				shadowPriceCPU, true, 0.5) // elasticity = 0.5 (moderate responsiveness)
		} else {
			// Use standard function without price response
			params = a.demandCalc.ParamsForPodWithUsage(pod, demand, actualUsageMilli, baselineMilli, nodeCapMilli)
		}

		// CRITICAL FIX: Enforce minimum floors to prevent feedback loop
		// This ensures allocations never drop below the intended initial values
		// Minimums are determined by annotations or QoS-based defaults

		// Check if high-priority pods (Guaranteed) are throttled (contention detection)
		highPriorityThrottled := false
		for otherUID, otherDemand := range demands {
			otherPod, exists := podMap[otherUID]
			if exists && otherPod.Status.QOSClass == corev1.PodQOSGuaranteed && otherDemand > 0.3 {
				highPriorityThrottled = true
				break
			}
		}

		// Get minimum from annotation (if present), otherwise use QoS-based defaults
		workloadMinMilli := getMinCPUFromAnnotation(pod, 0)

		if workloadMinMilli == 0 {
			// Use QoS-based defaults
			switch pod.Status.QOSClass {
			case corev1.PodQOSGuaranteed:
				// Guaranteed: minimum = original request (cannot go below original request)
				// CRITICAL FIX: Use originalRequests instead of current pod.Spec.Containers[0].Resources.Requests
				// to prevent MBCAS from locking its own scaled-up requests as a permanent floor.
				if originalReq, ok := a.originalRequests[uid]; ok {
					workloadMinMilli = originalReq
				} else if len(pod.Spec.Containers) > 0 {
					if requestCPU, ok := pod.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]; ok {
						workloadMinMilli = requestCPU.MilliValue()
					}
				}
				// Fallback to baseline if no request
				if workloadMinMilli == 0 {
					workloadMinMilli = baselineMilli
				}
			case corev1.PodQOSBurstable:
				// Burstable: baseline minimum, lower during contention
				if highPriorityThrottled {
					workloadMinMilli = baselineMilli / 2 // Half baseline during contention
				} else {
					workloadMinMilli = baselineMilli
				}
			case corev1.PodQOSBestEffort:
				// BestEffort: very low minimum, even lower during contention
				if highPriorityThrottled {
					workloadMinMilli = baselineMilli / 4 // Quarter baseline during contention
				} else {
					workloadMinMilli = baselineMilli / 2 // Half baseline normally
				}
			default:
				// Unknown QoS: use baseline
				workloadMinMilli = baselineMilli
			}
		}

		// Override MinMilli with workload-specific minimum if it's higher
		oldMin := params.MinMilli
		if workloadMinMilli > 0 && workloadMinMilli > params.MinMilli {
			params.MinMilli = workloadMinMilli
			klog.InfoS("Enforcing workload-specific minimum floor",
				"pod", pod.Name,
				"namespace", pod.Namespace,
				"workloadMin", workloadMinMilli,
				"oldMin", oldMin,
				"newMin", params.MinMilli)
		}

		// Also try to use original request if available (backup)
		if originalRequest, hasOriginalRequest := a.originalRequests[uid]; hasOriginalRequest && originalRequest > 0 {
			if originalRequest > params.MinMilli {
				params.MinMilli = originalRequest
				klog.InfoS("Using original request for min (higher than workload floor)",
					"pod", pod.Name,
					"namespace", pod.Namespace,
					"originalRequest", originalRequest,
					"workloadMin", workloadMinMilli,
					"finalMin", params.MinMilli)
			}
		}

		// Ensure min is at least baseline
		if params.MinMilli < baselineMilli {
			params.MinMilli = baselineMilli
		}

		// Override MaxMilli with original limit if we have it stored
		// This prevents feedback loop where applied limits become new max
		if originalLimit, hasOriginalLimit := a.originalLimits[uid]; hasOriginalLimit && originalLimit > 0 {
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

	// Get shadow price from previous cycle (or 0 if first cycle)
	shadowPriceCPU := 0.0
	if a.shadowPrices != nil {
		shadowPriceCPU = a.shadowPrices.GetCPU()
	}

	// Agent-based modeling: get or create agent states and compute bids
	if a.config.EnableAgentBasedModeling && a.ql != nil {
		a.mu.Lock()
		// Ensure agent states exist for all pods
		for uid := range podParams {
			if _, exists := a.agentStates[uid]; !exists {
				a.agentStates[uid] = NewPodAgentState(uid, a.config.AgentMemorySize)
			}
		}
		a.mu.Unlock()

		// Let agents compute their own bids
		agentBids := make([]AgentBid, 0, len(podParams))
		for uid, params := range podParams {
			pod, exists := podMap[uid]
			if !exists {
				continue
			}

			agentState := a.agentStates[uid]
			throttling := demands[uid]
			if throttling < 0 {
				throttling = 0
			}
			if throttling > 1 {
				throttling = 1
			}

			// Create utility params for bid computation
			var p99Latency float64
			if a.latencyQuerier != nil {
				_, p99Latency, _ = a.latencyQuerier.QueryPodLatency(a.ctx, pod.Namespace, pod.Name)
			}
			targetLatencyMs := a.config.SLOTargetLatencyMs
			if pod.Annotations != nil {
				if targetLatencyStr, ok := pod.Annotations["mbcas.io/target-latency-ms"]; ok {
					if parsed, err := strconv.ParseFloat(targetLatencyStr, 64); err == nil {
						targetLatencyMs = parsed
					}
				}
			}

			utilParams := allocation.NewUtilityParamsFromPodParams(
				params,
				targetLatencyMs,
				p99Latency,
				shadowPriceCPU,
				throttling,
			)
			utilParams.SetAllocation(params.ActualUsageMilli)

			// Agent computes its bid
			currentAlloc := a.getCurrentAllocationMilli(uid)
			if currentAlloc == 0 {
				currentAlloc = params.MinMilli
			}
			bid := agentState.ComputeBid(shadowPriceCPU, utilParams, currentAlloc, throttling)
			agentBids = append(agentBids, bid)

			// CRITICAL FIX: DO NOT overwrite params.ActualUsageMilli here if it would
			// cause a feedback loop. AggregateBids will handle this if needed,
			// but we want to keep the "true" usage (or its stable estimate) for Nash.
			// params.ActualUsageMilli = bid.Demand
			// podParams[uid] = params
		}

		// Agents observe market conditions
		avgPrice := shadowPriceCPU
		if len(agentBids) > 0 {
			// Compute average price from bids (simplified)
			avgPrice = shadowPriceCPU
		}
		for _, bid := range agentBids {
			agentState := a.agentStates[bid.UID]
			priceHistory := []float64{shadowPriceCPU} // Simplified: just current price
			agentState.ObserveMarket(shadowPriceCPU, avgPrice, priceHistory)
		}
	}

	// Convert PodParams to UtilityParams
	utilityParams := make(map[types.UID]*allocation.UtilityParams)
	for uid, p := range podParams {
		pod, exists := podMap[uid]
		if !exists {
			continue
		}

		// Get latency from Prometheus (or 0 if unavailable)
		var p99Latency float64
		if a.latencyQuerier != nil {
			_, p99Latency, _ = a.latencyQuerier.QueryPodLatency(a.ctx, pod.Namespace, pod.Name)
		}

		// Get SLO target (from config or pod annotation)
		targetLatencyMs := a.config.SLOTargetLatencyMs
		if pod.Annotations != nil {
			if targetLatencyStr, ok := pod.Annotations["mbcas.io/target-latency-ms"]; ok {
				if parsed, err := strconv.ParseFloat(targetLatencyStr, 64); err == nil {
					targetLatencyMs = parsed
				}
			}
		}

		// Get throttling pressure from demand signal (normalized [0,1])
		throttlingPressure := demands[uid]
		if throttlingPressure < 0 {
			throttlingPressure = 0
		}
		if throttlingPressure > 1 {
			throttlingPressure = 1
		}

		// Convert to UtilityParams (now utility-driven instead of heuristic)
		util := allocation.NewUtilityParamsFromPodParams(
			p,
			targetLatencyMs,
			p99Latency,
			shadowPriceCPU,
			throttlingPressure,
		)
		utilityParams[uid] = util
	}

	// Coalition grouping: group pods by coalition annotation for joint optimization
	coalitionGroups := make(map[string][]types.UID)
	coalitionAnnotation := a.config.CoalitionGroupingAnnotation
	if coalitionAnnotation == "" {
		coalitionAnnotation = "mbcas.io/coalition"
	}

	for uid, pod := range podMap {
		coalitionID := "default" // Default coalition (no grouping)
		if pod.Annotations != nil {
			if val, ok := pod.Annotations[coalitionAnnotation]; ok && val != "" {
				coalitionID = val
			}
		}
		coalitionGroups[coalitionID] = append(coalitionGroups[coalitionID], uid)
	}

	// If all pods are in "default" coalition, skip coalition optimization
	useCoalitions := false
	if len(coalitionGroups) > 1 || (len(coalitionGroups) == 1 && coalitionGroups["default"] == nil) {
		useCoalitions = true
	}

	// Select allocation mechanism based on config
	var allocations map[types.UID]int64
	var needsMap map[types.UID]int64 // Track needs for mode determination
	mechanism := a.config.AllocationMechanism
	if mechanism == "" {
		mechanism = "nash" // Default
	}

	// If using coalitions, compute joint allocation per coalition
	if useCoalitions && mechanism == "nash" {
		allocations = make(map[types.UID]int64)
		needsMap = make(map[types.UID]int64)

		// Allocate capacity to each coalition, then distribute within coalition
		for _, coalitionPods := range coalitionGroups {
			if len(coalitionPods) == 0 {
				continue
			}

			// Build coalition-level PodParams (aggregate demands)
			coalitionParams := make(map[types.UID]allocation.PodParams)
			coalitionTotalNeed := int64(0)
			coalitionTotalMin := int64(0)

			for _, uid := range coalitionPods {
				if p, ok := podParams[uid]; ok {
					coalitionParams[uid] = p
					need := int64(float64(p.ActualUsageMilli) * (1.0 + a.config.NeedHeadroomFactor))
					if need < p.MinMilli {
						need = p.MinMilli
					}
					if need > p.MaxMilli {
						need = p.MaxMilli
					}
					coalitionTotalNeed += need
					coalitionTotalMin += p.MinMilli
				}
			}

			// Allocate capacity proportionally to coalition size/need
			// Simple approach: allocate based on total need ratio
			totalSystemNeed := int64(0)
			for _, p := range podParams {
				need := int64(float64(p.ActualUsageMilli) * (1.0 + a.config.NeedHeadroomFactor))
				if need < p.MinMilli {
					need = p.MinMilli
				}
				if need > p.MaxMilli {
					need = p.MaxMilli
				}
				totalSystemNeed += need
			}

			var coalitionCapacity int64
			if totalSystemNeed > 0 {
				coalitionCapacity = int64(float64(availableMilli) * float64(coalitionTotalNeed) / float64(totalSystemNeed))
			} else {
				coalitionCapacity = availableMilli / int64(len(coalitionGroups))
			}

			// Run Nash/Kalai-Smorodinsky on coalition
			coalitionAllocs := allocation.ClearMarketNash(coalitionCapacity, coalitionParams)

			// Distribute coalition allocation to pods
			for uid, alloc := range coalitionAllocs.Allocations {
				allocations[uid] = alloc
				if p, ok := podParams[uid]; ok {
					need := p.ActualUsageMilli + int64(float64(p.ActualUsageMilli)*0.15)
					if need < p.MinMilli {
						need = p.MinMilli
					}
					if need > p.MaxMilli {
						need = p.MaxMilli
					}
					needsMap[uid] = need
				}
			}
		}
	} else {
		// Normal per-pod allocation (no coalition grouping)
		if mechanism == "primal-dual" {
			// Use distributed primal-dual price-clearing
			// Initialize allocations for utility calculation
			for uid, util := range utilityParams {
				currentAlloc := a.getCurrentAllocationMilli(uid)
				if currentAlloc == 0 {
					currentAlloc = util.BaselineCPU
				}
				util.SetAllocation(currentAlloc)
			}

			// Convert to primal-dual agents
			primalDualAgents := allocation.ConvertUtilityParamsToPrimalDualAgents(utilityParams)

			// Create coordinator (reuse shadow price if available)
			initialLambda := shadowPriceCPU
			if initialLambda <= 0 {
				initialLambda = 0.0
			}
			coordinator := allocation.NewPrimalDualCoordinator(
				0.1, // eta: step size
				initialLambda,
				0.01, // tolerance
				50,   // max iterations (faster than Nash)
			)

			// Run primal-dual price-clearing
			result := allocation.PrimalDualPriceClearing(availableMilli, primalDualAgents, coordinator)
			allocations = result.Allocations

			// Compute needs from utility params (for mode determination)
			needsMap = make(map[types.UID]int64)
			for uid, util := range utilityParams {
				// Use actual usage + headroom as need estimate
				if podParam, ok := podParams[uid]; ok && (podParam.ActualUsageMilli > 0 || a.podSampled[uid]) {
					need := int64(float64(podParam.ActualUsageMilli) * (1.0 + a.config.NeedHeadroomFactor))
					if need < util.BaselineCPU {
						need = util.BaselineCPU
					}
					if need > util.MaxCPU {
						need = util.MaxCPU
					}
					needsMap[uid] = need
				} else {
					needsMap[uid] = util.BaselineCPU
				}
			}

			// Update shadow price from result
			if a.shadowPrices == nil {
				a.shadowPrices = price.NewShadowPrices()
			}
			a.shadowPrices.Update(result.ShadowPrice, 0, 0)

			klog.InfoS("Primal-dual allocation completed",
				"iterations", result.Iterations,
				"converged", result.Converged,
				"shadowPrice", result.ShadowPrice)
		} else {
			// Use Nash Bargaining (default)
			// Convert UtilityParams to NashBargainingParams
			nashAgents := make([]allocation.NashBargainingParams, 0, len(utilityParams))
			for uid, util := range utilityParams {
				// Use current allocation as demand estimate (or min if no current allocation)
				currentAlloc := a.getCurrentAllocationMilli(uid)
				if currentAlloc == 0 {
					currentAlloc = util.BaselineCPU
				}
				util.SetAllocation(currentAlloc)

				nashParams := util.ToNashBargainingParams(string(uid))
				// Update Demand field with actual need estimate from podParams
				if podParam, ok := podParams[uid]; ok && (podParam.ActualUsageMilli > 0 || a.podSampled[uid]) {
					// Use actual usage + headroom as demand estimate
					nashParams.Demand = int64(float64(podParam.ActualUsageMilli) * (1.0 + a.config.NeedHeadroomFactor))
					if nashParams.Demand < nashParams.Baseline {
						nashParams.Demand = nashParams.Baseline
					}
					if nashParams.Demand > nashParams.MaxAlloc {
						nashParams.Demand = nashParams.MaxAlloc
					}
				} else {
					nashParams.Demand = nashParams.Baseline
				}
				nashAgents = append(nashAgents, nashParams)
			}

			// Use formal Nash Bargaining Solution
			allocations = allocation.NashBargainingSolution(availableMilli, nashAgents)

			// Build needs map from nash agents
			needsMap = make(map[types.UID]int64)
			for _, agent := range nashAgents {
				needsMap[agent.UID] = agent.Demand
			}
		}
	}

	// Apply contention-aware adjustments before Lyapunov smoothing
	// Detect contention by aggregating throttling signals (not pod names)
	contentionAdjustedAllocations := make(map[types.UID]int64)
	totalThrottledDemand := 0.0
	highPriorityThrottled := false

	// Detect contention: if any high-priority pods (Guaranteed QoS or high PriorityClass) are throttled
	for uid, alloc := range allocations {
		pod, exists := podMap[uid]
		if !exists {
			contentionAdjustedAllocations[uid] = alloc
			continue
		}

		demand := demands[uid]
		if demand > 0.3 { // Throttling threshold
			totalThrottledDemand += demand

			// Check if high-priority pod is throttled (Guaranteed QoS = high priority)
			if pod.Status.QOSClass == corev1.PodQOSGuaranteed {
				highPriorityThrottled = true
			}
		}
		contentionAdjustedAllocations[uid] = alloc
	}

	// If high-priority pods are throttled, aggressively reduce reclaimable pods
	if highPriorityThrottled {
		klog.InfoS("Contention detected: high-priority pods throttled, reducing reclaimable allocations",
			"totalThrottledDemand", totalThrottledDemand)
		for uid, alloc := range contentionAdjustedAllocations {
			pod, exists := podMap[uid]
			if !exists {
				continue
			}

			// Reclaim from BestEffort/Burstable pods (QoS-based, not name-based)
			if pod.Status.QOSClass == corev1.PodQOSBestEffort || pod.Status.QOSClass == corev1.PodQOSBurstable {
				// Get minimum from annotation, or use default reclaimable minimum
				minCPU := getMinCPUFromAnnotation(pod, 100) // Default 100m for reclaimable pods
				if alloc > minCPU {
					contentionAdjustedAllocations[uid] = minCPU
					klog.InfoS("Contention: reducing reclaimable pod allocation",
						"pod", pod.Name,
						"namespace", pod.Namespace,
						"qos", pod.Status.QOSClass,
						"original", alloc,
						"reduced", minCPU)
				}
			}
		}
	}

	// Apply Lyapunov-bounded updates for stability with congestion-aware scaling
	stableAllocations := make(map[types.UID]int64)

	// Compute congestion factor from allocations and params
	// Convert UtilityParams to AllocationParams for congestion calculation
	congestionParams := make(map[types.UID]mbcastypes.AllocationParams)
	for uid, util := range utilityParams {
		congestionParams[uid] = mbcastypes.AllocationParams{
			UID:      uid,
			Baseline: util.BaselineCPU,
			MaxAlloc: util.MaxCPU,
			Weight:   util.SLOWeight, // Use SLOWeight as bargaining weight
			SLOGap:   0,              // SLOGap not directly available, use 0
		}
	}
	congestionFactor := stability.ComputeCongestionFactor(contentionAdjustedAllocations, congestionParams)

	for uid, desiredAlloc := range contentionAdjustedAllocations {
		currentAlloc := a.getCurrentAllocationMilli(uid)
		if currentAlloc == 0 {
			currentAlloc = utilityParams[uid].BaselineCPU
		}
		stableAlloc := a.lyapunovController.BoundedUpdateWithCongestion(currentAlloc, desiredAlloc, congestionFactor)
		stableAllocations[uid] = stableAlloc

		// Update UtilityParams with new allocation for metrics
		if util, ok := utilityParams[uid]; ok {
			util.SetAllocation(stableAlloc)
		}
	}

	// Check Lyapunov convergence
	allocationParams := make(map[types.UID]mbcastypes.AllocationParams)
	for uid, util := range utilityParams {
		// Compute SLO gap: positive if latency exceeds target
		sloGap := 0.0
		if util.TargetLatencyMs > 0 && util.CurrentLatencyMs > util.TargetLatencyMs {
			sloGap = util.CurrentLatencyMs - util.TargetLatencyMs
		}
		allocationParams[uid] = mbcastypes.AllocationParams{
			UID:      uid,
			Baseline: util.BaselineCPU,
			MaxAlloc: util.MaxCPU,
			Weight:   util.SLOWeight,
			SLOGap:   sloGap,
		}
	}
	potential := stability.ComputePotential(stableAllocations, allocationParams, 1.0, 0.5)
	converging := a.lyapunovController.CheckAndAdaptStepSize(potential)
	RecordLyapunovMetrics(potential, a.lyapunovController.GetStepSize(), converging)

	// Compute request allocations based on limit allocations (mode-aware)
	// Mode will be determined below, but we need to compute it first
	// For now, compute requests with mode-aware function after mode is determined
	requestAllocs := make(map[types.UID]int64)

	// Determine mode based on total allocation vs capacity
	totalAlloc := int64(0)
	totalNeed := int64(0)
	for _, alloc := range stableAllocations {
		totalAlloc += alloc
	}
	for _, need := range needsMap {
		totalNeed += need
	}

	mode := allocation.ModeUncongested
	if totalNeed > availableMilli {
		mode = allocation.ModeCongested
	}

	// Compute request allocations with mode-aware ratios
	requestAllocs = allocation.ComputeRequestAllocationsWithMode(stableAllocations, podParams, mode)

	// Convert to AllocationResult format
	result := allocation.AllocationResult{
		Allocations:        stableAllocations,
		RequestAllocations: requestAllocs,
		Mode:               mode,
		TotalNeed:          totalNeed,
		TotalAlloc:         totalAlloc,
		Needs:              needsMap,
		NashIterations:     1, // Track iterations separately for each mechanism
	}

	// Convert limits to string format ("${m}m")
	limitAllocations := make(map[types.UID]string)
	for uid, milli := range stableAllocations {
		limitAllocations[uid] = fmt.Sprintf("%dm", milli)
	}

	// Convert requests to string format ("${m}m")
	requestAllocations := make(map[types.UID]string)
	for uid, milli := range requestAllocs {
		requestAllocations[uid] = fmt.Sprintf("%dm", milli)
	}

	// Record utility and SLO metrics
	for uid, util := range utilityParams {
		pod, exists := podMap[uid]
		if exists {
			RecordUtility(pod.Namespace, pod.Name, util.Utility())
			RecordSLOScore(pod.Namespace, pod.Name, util.SLOScore())
		}
	}

	// Agent-based modeling: update agent states with outcomes and trigger learning
	if a.config.EnableAgentBasedModeling && a.ql != nil {
		// Compute average payoff for strategy evolution
		avgPayoff := ComputeAveragePayoff(a.agentStates)

		// Update each agent with outcome
		for uid, allocMilli := range stableAllocations {
			agentState, exists := a.agentStates[uid]
			if !exists {
				continue
			}

			// Get outcome metrics
			throttling := demands[uid]
			if throttling < 0 {
				throttling = 0
			}
			if throttling > 1 {
				throttling = 1
			}

			// Check SLO violation
			sloViolation := false
			if util, ok := utilityParams[uid]; ok {
				if util.TargetLatencyMs > 0 && util.CurrentLatencyMs > util.TargetLatencyMs {
					sloViolation = true
				}
			}

			// Get utility
			utility := 0.0
			if util, ok := utilityParams[uid]; ok {
				utility = util.Utility()
			}

			// Get strategy from agent state
			strategy := agentState.GetStrategyName()

			// Create outcome
			outcome := DecisionOutcome{
				Timestamp:    time.Now(),
				Allocation:   allocMilli,
				Demand:       podParams[uid].ActualUsageMilli,
				ShadowPrice:  shadowPriceCPU,
				Utility:      utility,
				SLOViolation: sloViolation,
				Throttling:   throttling,
				Strategy:     strategy,
			}

			// Record outcome
			agentState.RecordOutcome(outcome)

			// Compute reward for Q-learning
			reward := ComputeReward(outcome, 0.001) // costWeight = 0.001

			// Get state and action for Q-learning
			currentAlloc := a.getCurrentAllocationMilli(uid)
			if currentAlloc == 0 {
				currentAlloc = podParams[uid].MinMilli
			}
			stateStr := EncodeState(currentAlloc, throttling, shadowPriceCPU)
			nextStateStr := EncodeState(allocMilli, throttling, shadowPriceCPU)

			// Update Q-value
			a.ql.UpdateQValue(agentState, stateStr, strategy, reward, nextStateStr)

			// Evolve strategy based on performance
			ownPayoff := utility
			agentState.EvolveStrategy(ownPayoff, avgPayoff, a.config.AgentLearningRate)

			// Decay exploration rate over time
			if a.ql.GetExplorationRate() > 0.01 {
				a.ql.DecayExplorationRate(0.999) // Very slow decay
			}
		}
	}

	// Coalition formation and Shapley values (optional, disabled by default)
	// TODO: When tracing system is available:
	// 1. Query traces for request paths (e.g., from Jaeger/Zipkin/OpenTelemetry)
	// 2. Create coalitions via coalition.NewCoalitionFromPath() for each request path
	// 3. Check ε-core stability via coalition.IsInEpsilonCore()
	// 4. Adjust allocations based on coalition values if blocking coalitions exist
	// 5. Compute Shapley values via shapley.ComputeShapleyValue() and update credits
	// 6. Record coalition metrics via RecordCoalitionMetrics()
	// 7. Record Shapley credits via RecordShapleyCredit()
	if EnableCoalitionFormation {
		// Coalition logic will be implemented here when tracing is available
		// For now, record zero metrics to indicate feature is disabled
		RecordCoalitionMetrics(0, nil)
	} else {
		// Feature disabled - don't record misleading zero metrics
		// Just skip coalition metrics entirely
	}

	return result, limitAllocations, requestAllocations, podParams
}

// getCurrentAllocationMilli gets the current allocation in millicores from tracked state.
func (a *Agent) getCurrentAllocationMilli(uid types.UID) int64 {
	a.mu.RLock()
	defer a.mu.RUnlock()

	currentAllocStr, ok := a.allocations[uid]
	if !ok || currentAllocStr == "" {
		return 0
	}

	// Parse "request:limit" format or just "limit"
	parts := strings.Split(currentAllocStr, ":")
	limitStr := currentAllocStr
	if len(parts) == 2 {
		limitStr = parts[1]
	}

	qty, err := resource.ParseQuantity(limitStr)
	if err != nil {
		return 0
	}

	return qty.MilliValue()
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

	if time.Since(a.startTime) > a.config.StartupGracePeriod {
		a.gracePeriodDone = true
		klog.InfoS("Startup grace period ended", "duration", a.config.StartupGracePeriod)
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

// getMinCPUFromAnnotation extracts minimum CPU from pod annotation mbcas.io/min-cpu.
// Returns the parsed value in millicores, or defaultVal if annotation is missing/invalid.
func getMinCPUFromAnnotation(pod *corev1.Pod, defaultVal int64) int64 {
	annVal, ok := pod.Annotations["mbcas.io/min-cpu"]
	if !ok {
		return defaultVal
	}

	// Parse as Kubernetes quantity (supports "100m", "1", "0.5", etc.)
	qty, err := resource.ParseQuantity(annVal)
	if err != nil {
		klog.V(5).InfoS("Invalid mbcas.io/min-cpu annotation, using default",
			"pod", pod.Name,
			"namespace", pod.Namespace,
			"annotation", annVal,
			"error", err)
		return defaultVal
	}

	return qty.MilliValue()
}

// shouldWrite checks if the change is significant enough to write.
func shouldWrite(current, new string, minChangePercent float64) bool {
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
	return changePercent >= minChangePercent
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// convertPodParamsToAllocationParams converts allocation.PodParams to mbcastypes.AllocationParams
// for use in price computation.
func (a *Agent) convertPodParamsToAllocationParams(podParams map[types.UID]allocation.PodParams) map[types.UID]mbcastypes.AllocationParams {
	result := make(map[types.UID]mbcastypes.AllocationParams)
	for uid, p := range podParams {
		result[uid] = mbcastypes.AllocationParams{
			UID:      uid,
			Baseline: p.MinMilli,
			MaxAlloc: p.MaxMilli,
			Weight:   p.Weight,
			SLOGap:   0, // Not used in price computation
		}
	}
	return result
}
