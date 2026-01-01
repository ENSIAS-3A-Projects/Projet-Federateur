package agent

import (
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

	// Utility metric
	metricUtility = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "mbcas",
			Name:      "utility",
			Help:      "Pod utility (satisfaction ratio) [0,1]",
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
}
