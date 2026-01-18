package agent

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// FastGuardrail implements the fast SLO protection loop.
// It responds quickly (1-2s) to SLO violations and throttling pressure
// by applying bounded fast-up steps, bypassing normal smoothing/hysteresis.
type FastGuardrail struct {
	config        *AgentConfig
	k8sClient     kubernetes.Interface
	writer        *Writer
	latencyQuerier *LatencyQuerier
	ctx           context.Context

	// State tracking
	mu                sync.RWMutex
	podStates         map[types.UID]*podGuardrailState
	podDemands        map[types.UID]float64 // throttling demand from sampling
	podUsages         map[types.UID]int64   // actual CPU usage
	currentAllocations map[types.UID]string // current allocation string "request:limit"
}

type podGuardrailState struct {
	lastP99Latency    float64
	lastThrottling    float64
	lastFastUpTime    int64 // Unix timestamp of last fast-up
	consecutiveViolations int
}

// NewFastGuardrail creates a new fast guardrail instance.
func NewFastGuardrail(
	config *AgentConfig,
	k8sClient kubernetes.Interface,
	writer *Writer,
	latencyQuerier *LatencyQuerier,
	ctx context.Context,
) *FastGuardrail {
	return &FastGuardrail{
		config:            config,
		k8sClient:         k8sClient,
		writer:            writer,
		latencyQuerier:    latencyQuerier,
		ctx:               ctx,
		podStates:         make(map[types.UID]*podGuardrailState),
		podDemands:        make(map[types.UID]float64),
		podUsages:         make(map[types.UID]int64),
		currentAllocations: make(map[types.UID]string),
	}
}

// UpdatePodState updates the guardrail state for a pod.
// Called from the sampling loop to track throttling and usage.
func (fg *FastGuardrail) UpdatePodState(uid types.UID, throttlingDemand float64, actualUsageMilli int64) {
	fg.mu.Lock()
	defer fg.mu.Unlock()

	fg.podDemands[uid] = throttlingDemand
	fg.podUsages[uid] = actualUsageMilli

	if _, exists := fg.podStates[uid]; !exists {
		fg.podStates[uid] = &podGuardrailState{}
	}
}

// UpdateAllocation tracks the current allocation for a pod.
func (fg *FastGuardrail) UpdateAllocation(uid types.UID, allocation string) {
	fg.mu.Lock()
	defer fg.mu.Unlock()
	fg.currentAllocations[uid] = allocation
}

// CheckAndApplyFastUp checks if a pod needs fast-up protection and applies it.
// Returns true if a fast-up was applied, false otherwise.
func (fg *FastGuardrail) CheckAndApplyFastUp(pod *corev1.Pod) (bool, error) {
	uid := pod.UID

	// Get current state
	fg.mu.RLock()
	state, hasState := fg.podStates[uid]
	throttlingDemand := fg.podDemands[uid]
	currentAllocStr := fg.currentAllocations[uid]
	fg.mu.RUnlock()

	if !hasState {
		state = &podGuardrailState{}
	}

	// Get p99 latency
	var p99Latency float64
	if fg.latencyQuerier != nil {
		_, p99Latency, _ = fg.latencyQuerier.QueryPodLatency(fg.ctx, pod.Namespace, pod.Name)
	}

	// Get SLO target
	targetLatencyMs := fg.config.SLOTargetLatencyMs
	if pod.Annotations != nil {
		if targetLatencyStr, ok := pod.Annotations["mbcas.io/target-latency-ms"]; ok {
			if parsed, err := parseFloat(targetLatencyStr); err == nil {
				targetLatencyMs = parsed
			}
		}
	}

	// Check triggers: p99 > target * multiplier OR throttling > threshold
	p99Violation := false
	if targetLatencyMs > 0 && p99Latency > 0 {
		threshold := targetLatencyMs * fg.config.P99ThresholdMultiplier
		if p99Latency > threshold {
			p99Violation = true
		}
	}

	throttlingViolation := throttlingDemand > fg.config.ThrottlingThreshold

	// Fast-up trigger: either condition
	shouldFastUp := p99Violation || throttlingViolation

	if !shouldFastUp {
		// Reset consecutive violations if no violation
		fg.mu.Lock()
		if state.consecutiveViolations > 0 {
			state.consecutiveViolations = 0
		}
		fg.mu.Unlock()
		return false, nil
	}

	// Increment consecutive violations
	fg.mu.Lock()
	state.consecutiveViolations++
	state.lastP99Latency = p99Latency
	state.lastThrottling = throttlingDemand
	fg.mu.Unlock()

	// Parse current allocation
	currentLimitMilli := int64(0)
	if currentAllocStr != "" {
		parts := splitAllocationString(currentAllocStr)
		if len(parts) >= 2 {
			limitStr := parts[1]
			if qty, err := resource.ParseQuantity(limitStr); err == nil {
				currentLimitMilli = qty.MilliValue()
			}
		}
	}

	// If no current allocation, try to get from pod spec
	if currentLimitMilli == 0 {
		if len(pod.Spec.Containers) > 0 {
			if limit, ok := pod.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU]; ok {
				currentLimitMilli = limit.MilliValue()
			}
		}
	}

	// Get max allocation (from original limits or pod spec)
	maxMilli := int64(0)
	if len(pod.Spec.Containers) > 0 {
		if limit, ok := pod.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU]; ok {
			maxMilli = limit.MilliValue()
		}
	}
	// Cap at 90% of node capacity as fallback
	if maxMilli == 0 {
		node, err := fg.k8sClient.CoreV1().Nodes().Get(fg.ctx, pod.Spec.NodeName, metav1.GetOptions{})
		if err == nil {
			if cpuStr, ok := node.Status.Capacity[corev1.ResourceCPU]; ok {
				nodeCapMilli := cpuStr.MilliValue()
				maxMilli = int64(float64(nodeCapMilli) * 0.9)
			}
		}
	}

	// Calculate fast-up step size (between min and max)
	// Use larger step for more severe violations
	// High-priority pods (PriorityClass or Priority value) get more aggressive fast-up
	stepSize := fg.config.FastStepSizeMin
	
	// Check if pod is high priority using PriorityClass or Priority value
	// Note: Controller skips Guaranteed QoS, so we check PriorityClass instead
	isHighPriority := false
	if pod.Spec.PriorityClassName != "" {
		// Pod has explicit priority class - treat as high priority
		isHighPriority = true
	} else if pod.Spec.Priority != nil && *pod.Spec.Priority > 0 {
		// Pod has explicit priority value - treat as high priority
		isHighPriority = true
	}
	
	if isHighPriority {
		// High-priority pods: use max step size immediately for faster protection
		stepSize = fg.config.FastStepSizeMax
	} else if state.consecutiveViolations > 1 {
		// Escalate step size for consecutive violations
		stepSize = fg.config.FastStepSizeMin + (fg.config.FastStepSizeMax-fg.config.FastStepSizeMin)*
			math.Min(1.0, float64(state.consecutiveViolations-1)/3.0)
	}

	// Calculate new limit: current + stepSize * current (bounded by max)
	newLimitMilli := currentLimitMilli + int64(float64(currentLimitMilli)*stepSize)
	if newLimitMilli > maxMilli && maxMilli > 0 {
		newLimitMilli = maxMilli
	}

	// Ensure minimum allocation
	baselineQty, _ := resource.ParseQuantity(fg.config.BaselineCPUPerPod)
	baselineMilli := baselineQty.MilliValue()
	if newLimitMilli < baselineMilli {
		newLimitMilli = baselineMilli
	}

	// Only apply if there's a meaningful increase
	if newLimitMilli <= currentLimitMilli {
		return false, nil
	}

	// Calculate request (use 0.95 ratio for fast-up to protect shares)
	requestMilli := int64(float64(newLimitMilli) * 0.95)
	if requestMilli < baselineMilli {
		requestMilli = baselineMilli
	}

	// Format allocations
	newLimitStr := fmt.Sprintf("%dm", newLimitMilli)
	newRequestStr := fmt.Sprintf("%dm", requestMilli)

	// Apply fast-up (bypass MinChangePercent hysteresis)
	klog.InfoS("Fast guardrail: applying fast-up",
		"pod", pod.Name,
		"namespace", pod.Namespace,
		"p99Latency", p99Latency,
		"targetLatency", targetLatencyMs,
		"throttlingDemand", throttlingDemand,
		"consecutiveViolations", state.consecutiveViolations,
		"currentLimit", currentLimitMilli,
		"newLimit", newLimitMilli,
		"stepSize", stepSize)

	// Fast guardrail doesn't use shadow prices (it's a quick response mechanism)
	// Pass 0.0 as shadow price - it will be updated in the next slow loop cycle
	if err := fg.writer.WritePodAllocation(fg.ctx, pod, newRequestStr, newLimitStr, 0.0); err != nil {
		return false, fmt.Errorf("write fast-up allocation: %w", err)
	}

	// Update tracked allocation
	fg.mu.Lock()
	fg.currentAllocations[uid] = newRequestStr + ":" + newLimitStr
	fg.mu.Unlock()

	return true, nil
}

// RemovePod removes a pod from tracking.
func (fg *FastGuardrail) RemovePod(uid types.UID) {
	fg.mu.Lock()
	defer fg.mu.Unlock()

	delete(fg.podStates, uid)
	delete(fg.podDemands, uid)
	delete(fg.podUsages, uid)
	delete(fg.currentAllocations, uid)
}

// Helper functions

func parseFloat(s string) (float64, error) {
	return strconv.ParseFloat(s, 64)
}

func splitAllocationString(s string) []string {
	parts := strings.Split(s, ":")
	return parts
}

