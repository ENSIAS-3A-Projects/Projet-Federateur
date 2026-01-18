package demand

// Package demand implements demand signal tracking and market parameter extraction.
// Phase 4: Calculator extracts market parameters from pods using Kubernetes-native primitives.

import (
	"context"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"mbcas/pkg/allocation"
	"mbcas/pkg/price"
)

// Calculator handles demand calculation and market parameter extraction.
type Calculator struct {
	k8sClient kubernetes.Interface
}

// NewCalculator creates a new demand calculator.
func NewCalculator(k8sClient kubernetes.Interface) *Calculator {
	return &Calculator{
		k8sClient: k8sClient,
	}
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

	// Apply PriorityClass-based priority multiplier (Kubernetes-native)
	// Note: We use context.Background() here since this is called from agent loops
	// In production, consider passing context through the call chain
	priorityMultiplier := c.getPriorityMultiplier(context.Background(), pod)
	
	// Apply annotation-based override if present (mbcas.io/priority-multiplier)
	if annMultiplier, ok := pod.Annotations["mbcas.io/priority-multiplier"]; ok {
		if parsed, err := strconv.ParseFloat(annMultiplier, 64); err == nil && parsed > 0 {
			priorityMultiplier = parsed
			klog.V(5).InfoS("Using annotation-based priority multiplier",
				"pod", pod.Name,
				"namespace", pod.Namespace,
				"multiplier", priorityMultiplier)
		}
	}

	weight = weight * priorityMultiplier

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

	params := allocation.PodParams{
		Demand:           demand,
		Bid:              bid,
		MinMilli:         minMilli,
		MaxMilli:         maxMilli,
		Weight:           weight,
		ActualUsageMilli: 0, // Legacy function, no actual usage
	}
	
	
	return params
}

// ParamsForPodWithUsage extracts market parameters including actual CPU usage.
// This is the preferred function for utilization-based allocation.
func (c *Calculator) ParamsForPodWithUsage(
	pod *corev1.Pod,
	demand float64,
	actualUsageMilli int64,
	baselineMilli int64,
	nodeCapMilli int64,
) allocation.PodParams {
	// Get base params from the existing function
	params := c.ParamsForPod(pod, demand, baselineMilli, nodeCapMilli)
	// Add actual usage
	params.ActualUsageMilli = actualUsageMilli
	return params
}

// ParamsForPodWithPrice extracts market parameters and applies price-responsive demand adjustment.
// This implements price-taking behavior: agents adjust demand based on shadow prices.
// shadowPrice: current CPU shadow price (0 if not available)
// enablePriceResponse: whether to apply price response (if false, behaves like ParamsForPodWithUsage)
// elasticity: how responsive to price changes [0, 1] (default 0.5 if <= 0)
func (c *Calculator) ParamsForPodWithPrice(
	pod *corev1.Pod,
	demand float64,
	actualUsageMilli int64,
	baselineMilli int64,
	nodeCapMilli int64,
	shadowPrice float64,
	enablePriceResponse bool,
	elasticity float64,
) allocation.PodParams {
	// Get base params with usage
	params := c.ParamsForPodWithUsage(pod, demand, actualUsageMilli, baselineMilli, nodeCapMilli)

	// Apply price response if enabled and price is available
	if enablePriceResponse && shadowPrice > 0 && actualUsageMilli > 0 {
		// Compute marginal utility for price response
		// Use a simplified marginal utility: weight * (1 + throttling_pressure) - price
		// This approximates the marginal utility without full utility computation
		throttlingPressure := demand
		if throttlingPressure < 0 {
			throttlingPressure = 0
		}
		if throttlingPressure > 1 {
			throttlingPressure = 1
		}
		marginalUtility := params.Weight * (1.0 + throttlingPressure)

		// Default elasticity if not provided
		if elasticity <= 0 {
			elasticity = 0.5 // Moderate responsiveness
		}

		// Apply price response: adjust demand based on price
		adjustedDemandMilli := price.DemandResponse(
			actualUsageMilli,
			shadowPrice,
			marginalUtility,
			elasticity,
		)

		// Convert adjusted demand back to normalized [0,1] if needed
		// For now, we'll use the adjusted demand in millicores directly
		// The allocation algorithm will use actualUsageMilli, so we update that
		// But we also want to preserve the demand signal for other uses
		// So we'll store the price-adjusted usage as a hint
		if adjustedDemandMilli != actualUsageMilli {
			klog.V(5).InfoS("Price response adjusted demand",
				"pod", pod.Name,
				"namespace", pod.Namespace,
				"originalUsage", actualUsageMilli,
				"adjustedDemand", adjustedDemandMilli,
				"shadowPrice", shadowPrice,
				"marginalUtility", marginalUtility)
		}

		// Update actual usage to reflect price-adjusted demand
		// This will be used in need/want calculations
		params.ActualUsageMilli = adjustedDemandMilli
	}

	return params
}

// filterNormalContainers returns only non-init, non-ephemeral containers.
// P1 Fix: Explicitly filter out init and ephemeral containers.
func filterNormalContainers(pod *corev1.Pod) []corev1.Container {
	return pod.Spec.Containers // Spec.Containers only contains normal containers
	// Note: InitContainers are in pod.Spec.InitContainers
	// EphemeralContainers are in pod.Spec.EphemeralContainers
	// Neither are included in pod.Spec.Containers by Kubernetes design.
}

// getPriorityMultiplier computes priority multiplier from Kubernetes PriorityClass.
// Returns 1.0 if no PriorityClass is set.
// Higher priority values result in higher multipliers (logarithmic scaling).
func (c *Calculator) getPriorityMultiplier(ctx context.Context, pod *corev1.Pod) float64 {
	if pod.Spec.PriorityClassName == "" {
		return 1.0 // Default: no priority boost
	}

	// Fetch PriorityClass
	pc, err := c.k8sClient.SchedulingV1().PriorityClasses().Get(ctx, pod.Spec.PriorityClassName, metav1.GetOptions{})
	if err != nil {
		// PriorityClass not found or error - log and return default
		klog.V(5).InfoS("Could not fetch PriorityClass, using default multiplier",
			"pod", pod.Name,
			"namespace", pod.Namespace,
			"priorityClass", pod.Spec.PriorityClassName,
			"error", err)
		return 1.0
	}

	// Convert priority value to multiplier
	// Priority values typically range from -1000 to 1000000
	// Use linear scaling: priority 1000 = 1.0x, priority 2000 = 1.5x, priority 10000 = 2.0x
	priorityValue := float64(pc.Value)
	if priorityValue <= 0 {
		return 1.0
	}

	// Linear scaling: priority 1000 = 1.0x, priority 10000 = 2.0x
	multiplier := 1.0 + (priorityValue/10000.0) * 1.0
	if multiplier < 0.1 {
		multiplier = 0.1 // Minimum 0.1x
	}
	if multiplier > 10.0 {
		multiplier = 10.0 // Maximum 10.0x
	}

	klog.V(5).InfoS("Computed priority multiplier from PriorityClass",
		"pod", pod.Name,
		"namespace", pod.Namespace,
		"priorityClass", pod.Spec.PriorityClassName,
		"priorityValue", pc.Value,
		"multiplier", multiplier)

	return multiplier
}

// getQoSClass returns the QoS class of the pod (Guaranteed, Burstable, or BestEffort).
func getQoSClass(pod *corev1.Pod) corev1.PodQOSClass {
	return pod.Status.QOSClass
}

// isReclaimable returns true if the pod's CPU can be aggressively reclaimed during contention.
// BestEffort and Burstable pods are reclaimable; Guaranteed pods are not.
func isReclaimable(pod *corev1.Pod) bool {
	qos := getQoSClass(pod)
	// BestEffort and Burstable pods can be reclaimed
	// Guaranteed pods have strict limits and should not be reduced below requests
	return qos == corev1.PodQOSBestEffort || qos == corev1.PodQOSBurstable
}
