package agent

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// Demand signal metrics
	metricDemandRaw = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "mbcas",
			Name:      "demand_raw",
			Help:      "Raw demand signal from cgroup throttling [0,1]",
		},
		[]string{"namespace", "pod"},
	)

	metricDemandSmoothed = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "mbcas",
			Name:      "demand_smoothed",
			Help:      "Smoothed demand signal after EMA [0,1]",
		},
		[]string{"namespace", "pod"},
	)

	// Allocation metrics
	metricAllocationMilli = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "mbcas",
			Name:      "allocation_milli",
			Help:      "CPU allocation in millicores",
		},
		[]string{"namespace", "pod"},
	)

	metricNeedMilli = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "mbcas",
			Name:      "need_milli",
			Help:      "Estimated CPU need in millicores",
		},
		[]string{"namespace", "pod"},
	)

	// Utility metric (legacy - allocation/need ratio)
	metricUtility = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "mbcas",
			Name:      "utility",
			Help:      "Pod utility (satisfaction ratio) [0,1]",
		},
		[]string{"namespace", "pod"},
	)

	// Game-theoretic utility metric
	metricGameUtility = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "mbcas",
			Name:      "game_utility",
			Help:      "Game-theoretic utility value (from UtilityParams)",
		},
		[]string{"namespace", "pod"},
	)

	// SLO score metric
	metricSLOScore = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "mbcas",
			Name:      "slo_score",
			Help:      "SLO satisfaction score [0,1] from sigmoid function",
		},
		[]string{"namespace", "pod"},
	)

	// System-wide metrics
	metricAllocationMode = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "mbcas",
			Name:      "allocation_mode",
			Help:      "Current allocation mode: 0=uncongested, 1=congested, 2=overloaded",
		},
		[]string{"node"},
	)

	metricNashProduct = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "mbcas",
			Name:      "nash_product_log",
			Help:      "Log of Nash product (sum of log(allocation - baseline))",
		},
	)

	metricTotalNeed = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "mbcas",
			Name:      "total_need_milli",
			Help:      "Total CPU need across all pods in millicores",
		},
	)

	metricTotalAllocation = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "mbcas",
			Name:      "total_allocation_milli",
			Help:      "Total CPU allocation across all pods in millicores",
		},
	)

	metricCapacity = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "mbcas",
			Name:      "capacity_milli",
			Help:      "Available CPU capacity in millicores",
		},
	)

	// Coalition and Shapley metrics (Phase 2)
	metricCoalitionCount = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "mbcas",
			Name:      "coalition_count",
			Help:      "Number of active coalitions",
		},
	)

	metricCoalitionSize = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "mbcas",
			Name:      "coalition_size",
			Help:      "Number of members in each coalition",
		},
		[]string{"coalition_id"},
	)

	metricShapleyCredit = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "mbcas",
			Name:      "shapley_credit",
			Help:      "Accumulated Shapley credit per pod",
		},
		[]string{"namespace", "pod"},
	)

	metricCoreStable = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "mbcas",
			Name:      "core_stable",
			Help:      "1 if allocation is in epsilon-core, 0 otherwise",
		},
	)

	// Lyapunov stability metrics (Phase 3)
	metricLyapunovPotential = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "mbcas",
			Name:      "lyapunov_potential",
			Help:      "Current Lyapunov potential function value",
		},
	)

	metricLyapunovStepSize = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "mbcas",
			Name:      "lyapunov_step_size",
			Help:      "Current Lyapunov controller step size",
		},
	)

	metricLyapunovConverging = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "mbcas",
			Name:      "lyapunov_converging",
			Help:      "1 if Lyapunov potential is decreasing (converging), 0 otherwise",
		},
	)

	// Shadow price metrics (Phase 3)
	metricShadowPriceCPU = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "mbcas",
			Name:      "shadow_price_cpu",
			Help:      "Current CPU shadow price (Lagrange multiplier)",
		},
	)

	// Timing metrics for performance monitoring
	metricAllocationComputeDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: "mbcas",
			Name:      "allocation_compute_duration_seconds",
			Help:      "Time taken to compute allocations in seconds",
			Buckets:   prometheus.ExponentialBuckets(0.001, 2, 10), // 1ms to ~1s
		},
	)

	metricNashSolverIterations = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "mbcas",
			Name:      "nash_solver_iterations",
			Help:      "Number of redistribution iterations in Nash solver",
		},
	)

	metricPodDiscoveryDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: "mbcas",
			Name:      "pod_discovery_duration_seconds",
			Help:      "Time taken to discover pods in seconds",
			Buckets:   prometheus.ExponentialBuckets(0.0001, 2, 10), // 0.1ms to ~100ms
		},
	)

	metricCgroupReadDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "mbcas",
			Name:      "cgroup_read_duration_seconds",
			Help:      "Time taken to read cgroup metrics in seconds",
			Buckets:   prometheus.ExponentialBuckets(0.0001, 2, 10), // 0.1ms to ~100ms
		},
		[]string{"namespace", "pod"},
	)
)

// RecordDemand records demand metrics for a pod.
func RecordDemand(namespace, pod string, raw, smoothed float64) {
	metricDemandRaw.WithLabelValues(namespace, pod).Set(raw)
	metricDemandSmoothed.WithLabelValues(namespace, pod).Set(smoothed)
}

// RecordAllocation records allocation metrics for a pod.
func RecordAllocation(namespace, pod string, allocationMilli, needMilli int64) {
	metricAllocationMilli.WithLabelValues(namespace, pod).Set(float64(allocationMilli))
	metricNeedMilli.WithLabelValues(namespace, pod).Set(float64(needMilli))

	if needMilli > 0 {
		utility := float64(allocationMilli) / float64(needMilli)
		if utility > 1.0 {
			utility = 1.0
		}
		metricUtility.WithLabelValues(namespace, pod).Set(utility)
	}
}

// RecordSystemMetrics records system-wide allocation metrics.
func RecordSystemMetrics(node string, mode int, nashProductLog float64, totalNeed, totalAlloc, capacity int64) {
	metricAllocationMode.WithLabelValues(node).Set(float64(mode))
	metricNashProduct.Set(nashProductLog)
	metricTotalNeed.Set(float64(totalNeed))
	metricTotalAllocation.Set(float64(totalAlloc))
	metricCapacity.Set(float64(capacity))
}

// ClearPodMetrics removes metrics for a pod that no longer exists.
func ClearPodMetrics(namespace, pod string) {
	metricDemandRaw.DeleteLabelValues(namespace, pod)
	metricDemandSmoothed.DeleteLabelValues(namespace, pod)
	metricAllocationMilli.DeleteLabelValues(namespace, pod)
	metricNeedMilli.DeleteLabelValues(namespace, pod)
	metricUtility.DeleteLabelValues(namespace, pod)
	metricShapleyCredit.DeleteLabelValues(namespace, pod)
}

// RecordCoalitionMetrics records coalition-related metrics.
func RecordCoalitionMetrics(coalitionCount int, coalitionSizes map[string]int) {
	metricCoalitionCount.Set(float64(coalitionCount))
	for id, size := range coalitionSizes {
		metricCoalitionSize.WithLabelValues(id).Set(float64(size))
	}
}

// RecordShapleyCredit records the Shapley credit for a pod.
func RecordShapleyCredit(namespace, pod string, credit float64) {
	metricShapleyCredit.WithLabelValues(namespace, pod).Set(credit)
}

// RecordCoreStability records whether the allocation is in the epsilon-core.
func RecordCoreStability(stable bool) {
	if stable {
		metricCoreStable.Set(1)
	} else {
		metricCoreStable.Set(0)
	}
}

// RecordLyapunovMetrics records Lyapunov stability controller metrics.
func RecordLyapunovMetrics(potential, stepSize float64, converging bool) {
	metricLyapunovPotential.Set(potential)
	metricLyapunovStepSize.Set(stepSize)
	if converging {
		metricLyapunovConverging.Set(1)
	} else {
		metricLyapunovConverging.Set(0)
	}
}

// RecordUtility records the game-theoretic utility value for a pod.
func RecordUtility(namespace, pod string, utility float64) {
	metricGameUtility.WithLabelValues(namespace, pod).Set(utility)
}

// RecordSLOScore records the SLO satisfaction score for a pod.
func RecordSLOScore(namespace, pod string, sloScore float64) {
	metricSLOScore.WithLabelValues(namespace, pod).Set(sloScore)
}

// RecordShadowPrice records the current CPU shadow price.
func RecordShadowPrice(cpuPrice float64) {
	metricShadowPriceCPU.Set(cpuPrice)
}

// RecordAllocationComputeDuration records the time taken to compute allocations.
func RecordAllocationComputeDuration(duration time.Duration) {
	metricAllocationComputeDuration.Observe(duration.Seconds())
}

// RecordNashSolverIterations records the number of redistribution iterations.
func RecordNashSolverIterations(iterations int) {
	metricNashSolverIterations.Set(float64(iterations))
}

// RecordPodDiscoveryDuration records the time taken to discover pods.
func RecordPodDiscoveryDuration(duration time.Duration) {
	metricPodDiscoveryDuration.Observe(duration.Seconds())
}

// RecordCgroupReadDuration records the time taken to read cgroup metrics for a pod.
func RecordCgroupReadDuration(namespace, pod string, duration time.Duration) {
	metricCgroupReadDuration.WithLabelValues(namespace, pod).Observe(duration.Seconds())
}
