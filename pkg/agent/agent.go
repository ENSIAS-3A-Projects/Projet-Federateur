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
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"mbcas/pkg/agent/cgroup"
	"mbcas/pkg/agent/demand"
)

const (
	// SamplingInterval is how often we sample kernel signals.
	SamplingInterval = 1 * time.Second

	// WriteInterval is how often we write PodAllocation updates to the API.
	WriteInterval = 15 * time.Second

	// MinChangePercent is the minimum change required to write an update (hysteresis).
	MinChangePercent = 10.0

	// SystemReservePercent is the percentage of node CPU to reserve for system.
	SystemReservePercent = 10.0

	// BaselineCPUPerPod is the minimum CPU to allocate per pod.
	BaselineCPUPerPod = "100m"
)

// Agent is the main node agent that senses kernel demand and writes allocations.
type Agent struct {
	k8sClient    kubernetes.Interface
	nodeName     string
	ctx          context.Context
	cancel       context.CancelFunc

	// State
	podDemands   map[types.UID]*demand.Tracker
	allocations  map[types.UID]string // pod UID -> current allocation
	mu           sync.RWMutex

	// Components
	cgroupReader *cgroup.Reader
	demandCalc   *demand.Calculator
	writer       *Writer
	restConfig   *rest.Config
}

// NewAgent creates a new node agent.
func NewAgent(k8sClient kubernetes.Interface, restConfig *rest.Config, nodeName string) (*Agent, error) {
	ctx, cancel := context.WithCancel(context.Background())

	cgroupReader, err := cgroup.NewReader()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create cgroup reader: %w", err)
	}

	demandCalc := demand.NewCalculator()

	writer, err := NewWriter(restConfig)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create writer: %w", err)
	}

	return &Agent{
		k8sClient:    k8sClient,
		nodeName:     nodeName,
		ctx:          ctx,
		cancel:       cancel,
		podDemands:   make(map[types.UID]*demand.Tracker),
		allocations:  make(map[types.UID]string),
		cgroupReader: cgroupReader,
		demandCalc:   demandCalc,
		writer:       writer,
		restConfig:   restConfig,
	}, nil
}

// Run starts the agent's main loop.
func (a *Agent) Run() error {
	klog.InfoS("Starting node agent", "node", a.nodeName)

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

		// Read demand signal from cgroup
		rawDemand, err := a.cgroupReader.ReadPodDemand(pod)
		if err != nil {
			klog.V(4).InfoS("Failed to read pod demand", "pod", pod.Name, "namespace", pod.Namespace, "error", err)
			// Set demand to 0 if we can't read it
			rawDemand = 0.0
		}

		// Update demand tracker (applies smoothing)
		a.mu.Lock()
		tracker, exists := a.podDemands[pod.UID]
		if !exists {
			tracker = demand.NewTracker()
			a.podDemands[pod.UID] = tracker
		}
		smoothedDemand := tracker.Update(rawDemand)
		a.mu.Unlock()

		klog.V(5).InfoS("Pod demand sample", "pod", pod.Name, "namespace", pod.Namespace,
			"raw", rawDemand, "smoothed", smoothedDemand)
	}

	// Remove pods that are no longer present
	a.mu.Lock()
	for uid := range a.podDemands {
		if !seen[uid] {
			delete(a.podDemands, uid)
			delete(a.allocations, uid)
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

	// Compute allocations (baseline + proportional)
	allocations := a.computeAllocations(demands, availableCPU, len(demands))

	// Write allocations
	for uid, cpuAllocation := range allocations {
		pod, exists := podMap[uid]
		if !exists {
			klog.V(4).InfoS("Pod not found, skipping allocation", "podUID", uid)
			continue
		}

		// Check if change is significant
		a.mu.RLock()
		currentAlloc := a.allocations[uid]
		a.mu.RUnlock()

		if shouldWrite(currentAlloc, cpuAllocation) {
			if err := a.writer.WritePodAllocation(a.ctx, pod, cpuAllocation); err != nil {
				klog.ErrorS(err, "Failed to write PodAllocation", "pod", pod.Name, "namespace", pod.Namespace)
				continue
			}

			// Update tracked allocation
			a.mu.Lock()
			a.allocations[uid] = cpuAllocation
			a.mu.Unlock()

			klog.InfoS("Updated PodAllocation", "pod", pod.Name, "namespace", pod.Namespace,
				"cpu", cpuAllocation, "previous", currentAlloc)
		}
	}

	return nil
}

// discoverPods discovers all running pods on this node.
func (a *Agent) discoverPods() ([]*corev1.Pod, error) {
	// List all pods, filtered by nodeName
	podList, err := a.k8sClient.CoreV1().Pods("").List(a.ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("spec.nodeName=%s", a.nodeName),
	})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}

	// Filter to running pods only
	var pods []*corev1.Pod
	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.Status.Phase == corev1.PodRunning {
			// Skip system pods (optional, but recommended)
			if pod.Namespace == "kube-system" {
				continue
			}
			pods = append(pods, pod)
		}
	}

	return pods, nil
}

// computeAllocations computes CPU allocations using baseline + proportional model.
//
// Allocation model:
//   - Baseline: minimum CPU per pod (prevents starvation)
//   - Proportional: remaining CPU distributed by demand
//   - Allocation targets the pod as a unit (via PodAllocation)
//   - Multi-container pods: allocation applies to first container (controller handles this)
func (a *Agent) computeAllocations(demands map[types.UID]float64, availableCPU float64, podCount int) map[types.UID]string {
	// Parse baseline
	baselineQty, _ := resource.ParseQuantity(BaselineCPUPerPod)
	baselineMilli := baselineQty.MilliValue()
	baselineCPU := float64(baselineMilli) / 1000.0

	// Ensure baseline doesn't exceed capacity
	maxBaseline := availableCPU / float64(2*podCount)
	if baselineCPU > maxBaseline {
		baselineCPU = maxBaseline
	}

	// Compute total baseline
	totalBaseline := baselineCPU * float64(podCount)
	remaining := availableCPU - totalBaseline
	if remaining < 0 {
		remaining = 0
	}

	// Sum all demands
	var totalDemand float64
	for _, d := range demands {
		totalDemand += d
	}

	// Allocate: baseline + proportional share of remaining
	allocations := make(map[types.UID]string)
	for uid, demand := range demands {
		var cpu float64
		if totalDemand > 0 {
			proportional := (demand / totalDemand) * remaining
			cpu = baselineCPU + proportional
		} else {
			cpu = baselineCPU
		}

		// Convert to resource quantity string
		cpuMilli := int64(cpu * 1000)
		allocations[uid] = fmt.Sprintf("%dm", cpuMilli)
	}

	return allocations
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

