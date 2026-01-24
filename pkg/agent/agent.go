package agent

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"mbcas/pkg/agent/cgroup"
	"mbcas/pkg/allocation"
)

// ManagedLabel is the label used to opt-out of MBCAS management.
const ManagedLabel = "mbcas.io/managed"

// ExcludedNamespaces are namespaces ignored by the agent.
var ExcludedNamespaces = map[string]bool{
	"kube-system":  true,
	"mbcas-system": true,
}

// Agent is the unified node agent.
// It uses autonomous PodAgents (ABM) for learning and NashBargain (Game Theory) for allocation.
type Agent struct {
	k8sClient kubernetes.Interface
	nodeName  string
	ctx       context.Context
	cancel    context.CancelFunc

	// Components
	cgroupReader *cgroup.Reader
	writer       *Writer
	podInformer  *PodInformer
	sloChecker   *SLOChecker
	qPersister   *QTablePersister

	// State
	mu        sync.RWMutex
	podAgents map[types.UID]*PodAgent
	config    *AgentConfig

	// Tracking for learning
	lastAllocations     map[types.UID]int64     // Last desired allocation
	smoothedAllocations map[types.UID]int64     // Exponentially smoothed allocations to prevent oscillations
	lastWriteTime       map[types.UID]time.Time // Last allocation write time for cooldown
	uidToName           map[types.UID]podInfo   // For cleanup tracking
}

type podInfo struct {
	namespace string
	name      string
}

// NewAgent creates a new unified node agent.
func NewAgent(k8sClient kubernetes.Interface, restConfig *rest.Config, nodeName string) (*Agent, error) {
	ctx, cancel := context.WithCancel(context.Background())

	config, err := LoadConfig(ctx, k8sClient)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to load config: %v", err)
	}

	cgroupReader, err := cgroup.NewReader()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create cgroup reader: %v", err)
	}

	writer, err := NewWriter(restConfig)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create writer: %v", err)
	}

	podInformer, err := NewPodInformer(ctx, k8sClient, nodeName)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create pod informer: %v", err)
	}

	var sloChecker *SLOChecker
	if config.PrometheusURL != "" {
		sloChecker = NewSLOChecker(config.PrometheusURL)
	}

	qPersister, err := NewQTablePersister(restConfig, nodeName)
	if err != nil {
		klog.Warningf("Failed to create Q-table persister: %v", err)
	}

	agent := &Agent{
		k8sClient:           k8sClient,
		nodeName:            nodeName,
		ctx:                 ctx,
		cancel:              cancel,
		cgroupReader:        cgroupReader,
		writer:              writer,
		podInformer:         podInformer,
		sloChecker:          sloChecker,
		qPersister:          qPersister,
		podAgents:           make(map[types.UID]*PodAgent),
		config:              config,
		lastAllocations:     make(map[types.UID]int64),
		smoothedAllocations: make(map[types.UID]int64),
		lastWriteTime:       make(map[types.UID]time.Time),
		uidToName:           make(map[types.UID]podInfo),
	}

	// Start health server
	agent.startHealthServer()

	return agent, nil
}

// Run starts the dual-loop agent.
func (a *Agent) Run() error {
	klog.InfoS("Starting dual-loop agent", "node", a.nodeName)

	fastTicker := time.NewTicker(a.config.FastLoopInterval)
	slowTicker := time.NewTicker(a.config.SlowLoopInterval)
	defer fastTicker.Stop()
	defer slowTicker.Stop()

	// Periodic Q-table persistence
	persistTicker := time.NewTicker(30 * time.Second)
	defer persistTicker.Stop()

	for {
		select {
		case <-a.ctx.Done():
			return a.ctx.Err()
		case <-fastTicker.C:
			a.FastStep()
		case <-slowTicker.C:
			a.SlowStep()
		case <-persistTicker.C:
			if a.qPersister != nil {
				a.mu.RLock()
				agentsCopy := make(map[types.UID]*PodAgent)
				for k, v := range a.podAgents {
					agentsCopy[k] = v
				}
				a.mu.RUnlock()
				if err := a.qPersister.Save(a.ctx, agentsCopy); err != nil {
					klog.V(2).InfoS("Failed to persist Q-tables", "error", err)
				}
			}
		}
	}
}

// Step performs one complete cycle of the MBCAS ABM-Game Theory pipeline.
func (a *Agent) Step() {
	start := time.Now()

	// 1. Discover Pods on this node
	pods, err := a.podInformer.ListPods()
	if err != nil {
		klog.V(4).InfoS("Failed to list pods", "err", err)
		return
	}

	// 2. Filter out terminating pods
	var activePods []*corev1.Pod
	for _, p := range pods {
		if p.DeletionTimestamp != nil || p.Status.Reason == "Evicted" {
			continue
		}
		activePods = append(activePods, p)
	}
	pods = activePods

	// 3. Bid Phase (Agents observe their state and Bid)
	bids, shadowPrice := a.collectBids(pods)

	// 4. Bargain Phase (Resolve conflict using Nash Bargaining)
	capacity := a.config.TotalClusterCPUCapacityMilli
	unmanagedUsage := a.getUnmanagedPodsCPU()
	available := capacity - unmanagedUsage
	reserve := int64(float64(available) * (a.config.SystemReservePercent / 100.0))
	available = available - reserve

	if available < 0 {
		available = 0
	}

	resultWithPrice := allocation.NashBargainWithPrice(available, bids)
	results := resultWithPrice.Allocations
	shadowPrice = resultWithPrice.ShadowPrice

	// 5. Act Phase (Write allocations with shadow price feedback)
	a.apply(pods, results, shadowPrice)

	// 6. Sync Agents (create new, remove dead)
	a.syncAgents(pods)

	// Cleanup stale cgroup samples
	existingPods := make(map[string]bool)
	for _, pod := range pods {
		existingPods[string(pod.UID)] = true
	}
	a.cgroupReader.Cleanup(existingPods)

	klog.InfoS("MBCAS Step",
		"duration", time.Since(start),
		"pods", len(pods),
		"managedPodsCapacity", capacity,
		"unmanagedUsage", unmanagedUsage,
		"available", available,
		"totalDemand", a.sumDemand(bids))
}

// FastStep handles fast loop: React to SLO violations and high throttling
// Only increases allocations, never decreases
func (a *Agent) FastStep() {
	pods, err := a.podInformer.ListPods()
	if err != nil || len(pods) == 0 {
		return
	}

	for _, pod := range pods {
		// Skip terminating or evicted pods
		if pod.DeletionTimestamp != nil || pod.Status.Reason == "Evicted" {
			continue
		}

		a.mu.RLock()
		agent := a.podAgents[pod.UID]
		a.mu.RUnlock()
		if agent == nil {
			continue
		}

		metrics, err := a.cgroupReader.ReadPodMetrics(pod, a.config.FastLoopInterval.Seconds())
		if err != nil {
			continue
		}

		needsBoost := false

		// Check throttling threshold
		if metrics.Demand > a.config.ThrottlingThreshold {
			needsBoost = true
		}

		// Check SLO
		if a.sloChecker != nil && agent.SLOTarget > 0 {
			violation, _ := a.sloChecker.CheckViolation(a.ctx, pod, agent.SLOTarget*a.config.P99ThresholdMultiplier)
			if violation {
				needsBoost = true
			}
		}

		if needsBoost {
			a.mu.Lock()

			// FIX #2: Add cooldown check for FastLoop
			if lastWrite, exists := a.lastWriteTime[pod.UID]; exists {
				fastCooldown := 5 * time.Second // Shorter than SlowLoop's 30s
				if time.Since(lastWrite) < fastCooldown {
					klog.V(3).InfoS("FastStep cooldown active",
						"pod", pod.Name,
						"sinceLastWrite", time.Since(lastWrite))
					a.mu.Unlock()
					continue
				}
			}

			currentAlloc := a.lastAllocations[pod.UID]
			if currentAlloc == 0 {
				currentAlloc = 100
			}

			// Fast step up
			stepSize := a.config.FastStepSizeMin +
				(a.config.FastStepSizeMax-a.config.FastStepSizeMin)*metrics.Demand

			rawAlloc := float64(currentAlloc) * (1.0 + stepSize)
			var newAlloc int64
			if rawAlloc > float64(math.MaxInt64) {
				newAlloc = math.MaxInt64
			} else {
				newAlloc = int64(rawAlloc)
			}

			// FIX #1: Cap at node capacity
			nodeCapacity := a.config.TotalClusterCPUCapacityMilli
			if newAlloc > nodeCapacity {
				klog.V(2).InfoS("FastStep capped at node capacity",
					"pod", pod.Name,
					"requested", newAlloc,
					"capped", nodeCapacity)
				newAlloc = nodeCapacity
			}

			// Also cap at pod's manifest limit
			podLimit := a.getPodCPULimit(pod)
			if newAlloc > podLimit {
				klog.V(2).InfoS("FastStep capped at pod limit",
					"pod", pod.Name,
					"requested", newAlloc,
					"capped", podLimit)
				newAlloc = podLimit
			}

			a.lastAllocations[pod.UID] = newAlloc
			a.lastWriteTime[pod.UID] = time.Now() // Update write time
			a.mu.Unlock()

			reqMilli := int64(float64(newAlloc) * 0.9)
			limitStr := fmt.Sprintf("%dm", newAlloc)
			requestStr := fmt.Sprintf("%dm", reqMilli)

			_ = a.writer.WritePodAllocation(a.ctx, pod, requestStr, limitStr, 0.0)
			klog.V(2).InfoS("Fast loop boost", "pod", pod.Name, "from", currentAlloc, "to", newAlloc)
		}
	}
}

// SlowStep handles slow loop: Full Nash bargaining optimization
func (a *Agent) SlowStep() {
	a.Step()
}

func (a *Agent) syncAgents(pods []*corev1.Pod) {
	a.mu.Lock()
	defer a.mu.Unlock()

	activeUIDs := make(map[types.UID]bool)
	for _, pod := range pods {
		activeUIDs[pod.UID] = true
		a.uidToName[pod.UID] = podInfo{
			namespace: pod.Namespace,
			name:      pod.Name,
		}
		if _, exists := a.podAgents[pod.UID]; !exists {
			sloTarget := extractSLOTarget(pod)
			podStartTime := time.Now()
			if pod.Status.StartTime != nil {
				podStartTime = pod.Status.StartTime.Time
			}
			agent := NewPodAgentWithConfig(pod.UID, sloTarget, a.config, podStartTime)
			// Load Q-table if available
			if a.qPersister != nil {
				if qtable, err := a.qPersister.Load(a.ctx, pod.UID); err == nil && qtable != nil {
					agent.mu.Lock()
					agent.QTable = qtable
					agent.mu.Unlock()
				}
			}
			a.podAgents[pod.UID] = agent
		}
	}

	for uid := range a.podAgents {
		if !activeUIDs[uid] {
			// Find the pod info before deleting
			if info, ok := a.uidToName[uid]; ok {
				_ = a.writer.DeletePodAllocation(a.ctx, info.namespace, info.name)
			}
			delete(a.podAgents, uid)
			delete(a.lastAllocations, uid)
			delete(a.smoothedAllocations, uid)
			delete(a.lastWriteTime, uid)
			delete(a.uidToName, uid)
		}
	}
}

func extractSLOTarget(pod *corev1.Pod) float64 {
	if val, ok := pod.Annotations["mbcas.io/target-latency-ms"]; ok {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f
		}
	}
	return 0.0
}

func (a *Agent) collectBids(pods []*corev1.Pod) ([]allocation.Bid, float64) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	// First pass: collect initial bids (without shadow price)
	initialBids := make([]allocation.Bid, 0, len(pods))
	for _, pod := range pods {
		agent := a.podAgents[pod.UID]
		if agent == nil {
			continue
		}

		metrics, err := a.cgroupReader.ReadPodMetrics(pod, a.config.WriteInterval.Seconds())
		if err != nil {
			klog.V(3).InfoS("Failed to read pod metrics, skipping bid",
				"pod", pod.Name, "namespace", pod.Namespace, "error", err)
			continue
		}

		// Use actual applied allocation from PodAllocation CR status, not desired
		lastAlloc := a.writer.GetActualAllocation(a.ctx, pod)
		if lastAlloc == 0 {
			// Fallback to desired allocation or pod's current limit
			lastAlloc = a.lastAllocations[pod.UID]
			if lastAlloc == 0 {
				// Try to read from pod's current resources
				if len(pod.Spec.Containers) > 0 {
					if limit, ok := pod.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU]; ok {
						lastAlloc = limit.MilliValue()
					}
				}
				if lastAlloc == 0 {
					lastAlloc = 100 // Final fallback
				}
			}
		}

		// Check SLO violation
		sloViolation := false
		if a.sloChecker != nil && agent.SLOTarget > 0 {
			sloViolation, _ = a.sloChecker.CheckViolation(a.ctx, pod, agent.SLOTarget)
		}

		agent.UpdateUsage(metrics.ActualUsageMilli)
		agent.Update(lastAlloc, metrics.Demand, sloViolation)

		// Collect initial bid (without shadow price)
		var initialBid Bid
		if a.config.CostEfficiencyMode {
			initialBid = agent.ComputeBidWithEfficiency(a.config)
		} else {
			initialBid = agent.ComputeBid(a.config)
		}
		initialBids = append(initialBids, allocation.Bid{
			UID:    initialBid.UID,
			Demand: initialBid.Demand,
			Weight: initialBid.Weight,
			Min:    initialBid.Min,
			Max:    initialBid.Max,
		})
	}

	// Compute shadow price from initial bids using fixed capacity from config
	capacity := a.config.TotalClusterCPUCapacityMilli
	unmanagedUsage := a.getUnmanagedPodsCPU()
	available := capacity - unmanagedUsage
	reserve := int64(float64(available) * (a.config.SystemReservePercent / 100.0))
	available = available - reserve
	if available < 0 {
		available = 0
	}
	previewResult := allocation.NashBargainWithPrice(available, initialBids)
	shadowPrice := previewResult.ShadowPrice

	// Second pass: collect final bids with shadow price feedback
	finalBids := make([]allocation.Bid, 0, len(pods))
	for _, pod := range pods {
		agent := a.podAgents[pod.UID]
		if agent == nil {
			continue
		}

		// Compute bid with shadow price feedback
		var b Bid
		if a.config.CostEfficiencyMode {
			b = agent.ComputeBidWithEfficiencyAndPrice(a.config, shadowPrice)
		} else {
			b = agent.ComputeBidWithShadowPrice(a.config, shadowPrice)
		}
		finalBids = append(finalBids, allocation.Bid{
			UID:    b.UID,
			Demand: b.Demand,
			Weight: b.Weight,
			Min:    b.Min,
			Max:    b.Max,
		})
	}

	return finalBids, shadowPrice
}

func (a *Agent) apply(pods []*corev1.Pod, results map[types.UID]int64, shadowPrice float64) {
	a.mu.Lock()
	defer a.mu.Unlock()

	var writeErrors int
	for _, pod := range pods {
		allocMilli, ok := results[pod.UID]
		if !ok {
			continue
		}

		// IMPROVEMENT #4: Cooldown after allocation changes
		// Prevent double-allocating by waiting for controller to apply
		if lastWrite, exists := a.lastWriteTime[pod.UID]; exists {
			// FIX #3: Add jitter to breakdown synchronization with workload spikes
			cooldownPeriod := a.getCooldownWithJitter()
			if time.Since(lastWrite) < cooldownPeriod {
				klog.V(3).InfoS("Skipping allocation write due to cooldown",
					"pod", pod.Name, "remaining", cooldownPeriod-time.Since(lastWrite))
				continue
			}
		}

		// CRITICAL FIX: Cap allocation to prevent runaway growth
		// Absolute maximum: 10 cores (10000m) per pod
		const absoluteMaxAlloc = int64(10000)
		if allocMilli > absoluteMaxAlloc {
			klog.V(2).InfoS("Capping allocation to absolute max",
				"pod", pod.Name, "requested", allocMilli, "capped", absoluteMaxAlloc)
			allocMilli = absoluteMaxAlloc
		}

		// CRITICAL FIX: Exponential smoothing to prevent oscillations
		// Use asymmetric smoothing: fast down, slow up
		lastSmoothed := a.smoothedAllocations[pod.UID]
		if lastSmoothed == 0 {
			// Initialize from pod's current limit to prevent initial spikes
			if len(pod.Spec.Containers) > 0 {
				if limit, ok := pod.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU]; ok {
					lastSmoothed = limit.MilliValue()
				}
			}
			if lastSmoothed == 0 {
				lastSmoothed = allocMilli // Fallback
			}
		}

		var smoothedAlloc int64
		if allocMilli < lastSmoothed {
			// Going down: fast smoothing (alpha = 0.7)
			smoothedAlloc = int64(0.7*float64(allocMilli) + 0.3*float64(lastSmoothed))
		} else {
			// Going up: very slow smoothing (alpha = 0.1) to prevent overshoot and reduce churn
			smoothedAlloc = int64(0.1*float64(allocMilli) + 0.9*float64(lastSmoothed))
		}

		// Ensure smoothed allocation never goes below absolute minimum
		if smoothedAlloc < a.config.AbsoluteMinAllocation {
			smoothedAlloc = a.config.AbsoluteMinAllocation
		}

		lastAlloc := a.lastAllocations[pod.UID]

		// Hysteresis: skip if change is below threshold
		if lastAlloc > 0 {
			changePct := math.Abs(float64(smoothedAlloc-lastAlloc)) / float64(lastAlloc) * 100
			if changePct < a.config.MinChangePercent {
				continue
			}
		}

		// CRITICAL FIX: Cap allocation to preserve node capacity for new pods
		// Never allow a single pod to take more than 75% of node capacity
		maxPerPod := int64(float64(a.config.TotalClusterCPUCapacityMilli) * 0.75)
		if smoothedAlloc > maxPerPod {
			klog.V(2).InfoS("Capping allocation to per-pod node share",
				"pod", pod.Name, "requested", smoothedAlloc, "capped", maxPerPod)
			smoothedAlloc = maxPerPod
		}

		a.lastAllocations[pod.UID] = smoothedAlloc
		a.smoothedAllocations[pod.UID] = smoothedAlloc

		reqMilli := int64(float64(smoothedAlloc) * 0.9)
		if reqMilli >= smoothedAlloc {
			reqMilli = smoothedAlloc - 5
		}
		if reqMilli < 10 {
			reqMilli = 10
		}

		limitStr := fmt.Sprintf("%dm", smoothedAlloc)
		requestStr := fmt.Sprintf("%dm", reqMilli)

		if err := a.writer.WritePodAllocation(a.ctx, pod, requestStr, limitStr, shadowPrice); err != nil {
			writeErrors++
			klog.ErrorS(err, "Failed to write allocation", "pod", pod.Name)
		} else {
			// Track write time for cooldown
			a.lastWriteTime[pod.UID] = time.Now()
		}
	}

	// If too many write errors, back off
	if writeErrors > len(pods)/2 {
		klog.Warning("High write error rate, backing off")
		time.Sleep(10 * time.Second)
	}
}

func (a *Agent) getUnmanagedPodsCPU() int64 {
	allPods, err := a.k8sClient.CoreV1().Pods("").List(a.ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + a.nodeName,
	})
	if err != nil {
		return 0
	}

	var total int64
	for _, pod := range allPods.Items {
		if ExcludedNamespaces[pod.Namespace] {
			for _, container := range pod.Spec.Containers {
				if req, ok := container.Resources.Requests[corev1.ResourceCPU]; ok {
					total += req.MilliValue()
				}
			}
		}
	}
	return total
}

func (a *Agent) sumDemand(bids []allocation.Bid) int64 {
	var total int64
	for _, b := range bids {
		total += b.Demand
	}
	return total
}

func (a *Agent) getPodCPULimit(pod *corev1.Pod) int64 {
	if len(pod.Spec.Containers) > 0 {
		if limit, ok := pod.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU]; ok {
			return limit.MilliValue()
		}
	}
	// Fallback to node capacity if no limit is set
	return a.config.TotalClusterCPUCapacityMilli
}

func (a *Agent) getCooldownWithJitter() time.Duration {
	baseCooldown := 30 * time.Second
	jitterRange := 5 * time.Second
	// rand.Float64() returns [0.0, 1.0)
	jitter := time.Duration(rand.Float64()*float64(jitterRange*2)) - jitterRange
	return baseCooldown + jitter // 25s - 35s
}

func (a *Agent) Stop() {
	a.cancel()
}

// startHealthServer starts the HTTP health server
func (a *Agent) startHealthServer() {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if !a.podInformer.HasSynced() {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("informer not synced"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		a.mu.RLock()
		podCount := len(a.podAgents)
		a.mu.RUnlock()

		fmt.Fprintf(w, "# HELP mbcas_managed_pods Number of pods managed by this agent\n")
		fmt.Fprintf(w, "# TYPE mbcas_managed_pods gauge\n")
		fmt.Fprintf(w, "mbcas_managed_pods %d\n", podCount)

		// Prometheus metrics handler would go here if using promhttp
		// For now, we just expose basic metrics
	})

	server := &http.Server{
		Addr:    ":8082",
		Handler: mux,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			klog.ErrorS(err, "Health server error")
		}
	}()
}
