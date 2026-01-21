package agent

import (
	"context"
	"fmt"
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

	// State
	mu        sync.RWMutex
	podAgents map[types.UID]*PodAgent
	config    *AgentConfig

	// Tracking for learning
	lastAllocations map[types.UID]int64
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

	return &Agent{
		k8sClient:       k8sClient,
		nodeName:        nodeName,
		ctx:             ctx,
		cancel:          cancel,
		cgroupReader:    cgroupReader,
		writer:          writer,
		podInformer:     podInformer,
		podAgents:       make(map[types.UID]*PodAgent),
		config:          config,
		lastAllocations: make(map[types.UID]int64),
	}, nil
}

// Run starts the agent loop.
func (a *Agent) Run() error {
	klog.InfoS("Starting unified node agent", "node", a.nodeName)

	// Cadence defined by WriteInterval
	ticker := time.NewTicker(a.config.WriteInterval)
	defer ticker.Stop()

	for {
		select {
		case <-a.ctx.Done():
			return a.ctx.Err()
		case <-ticker.C:
			a.Step()
		}
	}
}

// Step performs one complete cycle of the MBCAS ABM-Game Theory pipeline.
func (a *Agent) Step() {
	start := time.Now()

	// 1. Discover Pods on this node
	pods, err := a.podInformer.ListPods()
	if err != nil || len(pods) == 0 {
		klog.V(4).InfoS("No pods to manage on this node or error listing pods", "err", err)
		return
	}

	// 2. Sync Agents (create new, remove dead)
	a.syncAgents(pods)

	// 3. Bid Phase (Agents observe their state and Bid)
	bids := a.collectBids(pods)

	// 4. Bargain Phase (Resolve conflict using Nash Bargaining)
	capacity := a.getNodeCapacity()
	// Reserve CPU for system components
	reserve := int64(float64(capacity) * (a.config.SystemReservePercent / 100.0))
	available := capacity - reserve

	results := allocation.NashBargain(available, bids)

	// 5. Act Phase (Write allocations)
	a.apply(pods, results)

	klog.InfoS("MBCAS Pipeline Step",
		"duration", time.Since(start),
		"pods", len(pods),
		"availableCapacity", available,
		"totalDemand", a.sumDemand(bids))
}

func (a *Agent) syncAgents(pods []*corev1.Pod) {
	a.mu.Lock()
	defer a.mu.Unlock()

	activeUIDs := make(map[types.UID]bool)
	for _, pod := range pods {
		activeUIDs[pod.UID] = true
		if _, exists := a.podAgents[pod.UID]; !exists {
			a.podAgents[pod.UID] = NewPodAgent(pod.UID, 0.0)
		}
	}

	for uid := range a.podAgents {
		if !activeUIDs[uid] {
			delete(a.podAgents, uid)
			delete(a.lastAllocations, uid)
		}
	}
}

func (a *Agent) collectBids(pods []*corev1.Pod) []allocation.Bid {
	a.mu.RLock()
	defer a.mu.RUnlock()

	var bids []allocation.Bid
	for _, pod := range pods {
		agent := a.podAgents[pod.UID]
		if agent == nil {
			continue
		}

		// Read cgroup metrics
		metrics, err := a.cgroupReader.ReadPodMetrics(pod, a.config.WriteInterval.Seconds())
		if err != nil {
			klog.V(5).InfoS("Skipping pod: cgroup read failed", "pod", pod.Name, "err", err)
			continue
		}

		// Update agent state & reward from last decision
		lastAlloc := a.lastAllocations[pod.UID]
		if lastAlloc == 0 {
			lastAlloc = 100
		}

		agent.UpdateUsage(metrics.ActualUsageMilli)
		agent.Update(lastAlloc, metrics.Demand, false) // Note: SLO not queried here for simplicity

		// Compute Bid
		b := agent.ComputeBid()
		bids = append(bids, allocation.Bid{
			UID:    b.UID,
			Demand: b.Demand,
			Weight: b.Weight,
			Min:    b.Min,
			Max:    b.Max,
		})
	}
	return bids
}

func (a *Agent) apply(pods []*corev1.Pod, results map[types.UID]int64) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for _, pod := range pods {
		allocMilli, ok := results[pod.UID]
		if !ok {
			continue
		}

		a.lastAllocations[pod.UID] = allocMilli

		// Configurable request ratio
		reqMilli := int64(float64(allocMilli) * 0.9)
		if reqMilli < 10 {
			reqMilli = 10
		}

		limitStr := fmt.Sprintf("%dm", allocMilli)
		requestStr := fmt.Sprintf("%dm", reqMilli)

		_ = a.writer.WritePodAllocation(a.ctx, pod, requestStr, limitStr, 0.0)
	}
}

func (a *Agent) getNodeCapacity() int64 {
	node, err := a.k8sClient.CoreV1().Nodes().Get(a.ctx, a.nodeName, metav1.GetOptions{})
	if err != nil {
		return 4000
	}
	// Correctly call MilliValue on the quantity value
	q := node.Status.Capacity[corev1.ResourceCPU]
	return q.MilliValue()
}

func (a *Agent) sumDemand(bids []allocation.Bid) int64 {
	var total int64
	for _, b := range bids {
		total += b.Demand
	}
	return total
}

func (a *Agent) Stop() {
	a.cancel()
}
