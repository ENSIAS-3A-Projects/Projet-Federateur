package demand

// Package demand implements demand signal tracking and market parameter extraction.
// Phase 4: Calculator extracts market parameters from pods using Kubernetes-native primitives.

import (
	corev1 "k8s.io/api/core/v1"

	"mbcas/pkg/allocation"
)

// Calculator handles demand calculation and market parameter extraction.
type Calculator struct{}

// NewCalculator creates a new demand calculator.
func NewCalculator() *Calculator {
	return &Calculator{}
}

// ParamsForPod extracts market parameters from a pod using Kubernetes-native primitives.
// Uses container[0] to match controller behavior.
//
// Parameters:
//   - pod: The pod to extract parameters from
//   - demand: Normalized demand [0,1] from Tracker
//   - baselineMilli: Baseline CPU in millicores (prevents starvation)
//   - nodeCapMilli: Node capacity in millicores (fallback for max)
//
// Returns:
//   - PodParams with weight, bid, min, max computed from K8s primitives
func (c *Calculator) ParamsForPod(
	pod *corev1.Pod,
	demand float64,
	baselineMilli int64,
	nodeCapMilli int64,
) allocation.PodParams {
	// Clamp demand defensively to [0,1]
	if demand < 0 {
		demand = 0
	}
	if demand > 1 {
		demand = 1
	}

	// P0 Fix: Support multi-container pods by aggregating resources
	// across all non-init, non-ephemeral containers.
	containers := filterNormalContainers(pod)
	if len(containers) == 0 {
		// Fallback for pods with no normal containers
		return allocation.PodParams{
			Demand:   demand,
			Bid:      1.0 * demand, // Default weight = 1
			MinMilli: baselineMilli,
			MaxMilli: nodeCapMilli,
			Weight:   1.0,
		}
	}

	// Sum request and limit CPU across all normal containers
	requestMilli := int64(0)
	limitMilli := int64(0)
	for _, container := range containers {
		if requestCPU, ok := container.Resources.Requests[corev1.ResourceCPU]; ok {
			requestMilli += requestCPU.MilliValue()
		}
		if limitCPU, ok := container.Resources.Limits[corev1.ResourceCPU]; ok {
			limitMilli += limitCPU.MilliValue()
		}
	}

	// Compute weight: max(1, requestCPU_milli)
	// This uses existing K8s requests as budget/importance signal
	weight := float64(requestMilli)
	if weight < 1.0 {
		weight = 1.0
	}

	// Optional: Multiply by PriorityClass factor (future enhancement)
	// For now, use weight as-is

	// Compute effective bid: weight × demand
	bid := weight * demand

	// Compute min: max(baselineMilli, requestCPU_milli)
	// Ensures limit >= request (K8s constraint)
	minMilli := baselineMilli
	if requestMilli > minMilli {
		minMilli = requestMilli
	}

	// Compute max: limitCPU_milli (or nodeCapMilli if no limit)
	maxMilli := limitMilli
	if maxMilli == 0 {
		maxMilli = nodeCapMilli
	}

	// Optional: Per-pod max cap to prevent one pod from consuming entire node
	// Use 90% of node capacity as a reasonable upper bound
	perPodMaxCap := int64(float64(nodeCapMilli) * 0.9)
	if maxMilli > perPodMaxCap {
		maxMilli = perPodMaxCap
	}

	// Ensure max >= min (sanity check)
	if maxMilli < minMilli {
		maxMilli = minMilli
	}

	return allocation.PodParams{
		Demand:   demand,
		Bid:      bid,
		MinMilli: minMilli,
		MaxMilli: maxMilli,
		Weight:   weight,
	}
}

// filterNormalContainers returns only non-init, non-ephemeral containers.
// P1 Fix: Explicitly filter out init and ephemeral containers.
func filterNormalContainers(pod *corev1.Pod) []corev1.Container {
	return pod.Spec.Containers // Spec.Containers only contains normal containers
	// Note: InitContainers are in pod.Spec.InitContainers
	// EphemeralContainers are in pod.Spec.EphemeralContainers
	// Neither are included in pod.Spec.Containers by Kubernetes design.
}
