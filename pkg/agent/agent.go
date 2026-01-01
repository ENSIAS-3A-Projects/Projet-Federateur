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
)

// Agent is the main node agent that senses kernel demand and writes allocations.
type Agent struct {
	k8sClient kubernetes.Interface
	nodeName  string
	ctx       context.Context
	cancel    context.CancelFunc

	// State
	podDemands  map[types.UID]*demand.Tracker
	allocations map[types.UID]string // pod UID -> current allocation
	mu          sync.RWMutex

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

		// Log at info level so demand visibility is always on during demo
		klog.InfoS("Pod demand sample", "pod", pod.Name, "namespace", pod.Namespace,
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

	// Compute allocations using market-clearing (Phase 4)
	allocations := a.computeAllocations(demands, availableCPU, podMap, nodeCapacity)

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
) map[types.UID]string {
	// Parse baseline once (not in hot loop)
	baselineQty, _ := resource.ParseQuantity(BaselineCPUPerPod)
	baselineMilli := baselineQty.MilliValue()

	// Convert capacities to millicores
	availableMilli := int64(availableCPU * 1000)
	nodeCapMilli := int64(nodeCapacity * 1000)

	// Build PodParams for each pod
	podParams := make(map[types.UID]allocation.PodParams)
	for uid, demand := range demands {
		pod, exists := podMap[uid]
		if !exists {
			continue
		}

		params := a.demandCalc.ParamsForPod(pod, demand, baselineMilli, nodeCapMilli)
		podParams[uid] = params
	}

	// Clear market using Phase 4 solver
	allocationsMilli := allocation.ClearMarket(availableMilli, podParams)

	// Convert to string format ("${m}m")
	allocations := make(map[types.UID]string)
	for uid, milli := range allocationsMilli {
		allocations[uid] = fmt.Sprintf("%dm", milli)
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
