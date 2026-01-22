package agent

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	allocationChanges = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mbcas_allocation_changes_total",
			Help: "Total number of allocation changes",
		},
		[]string{"pod", "namespace", "direction"},
	)

	allocationLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "mbcas_allocation_latency_seconds",
			Help:    "Time from decision to applied",
			Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30},
		},
		[]string{"pod", "namespace"},
	)

	nashBargainMode = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mbcas_nash_mode_total",
			Help: "Nash bargaining mode selections",
		},
		[]string{"mode"},
	)

	qlearningReward = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "mbcas_qlearning_reward",
			Help:    "Q-learning reward distribution",
			Buckets: []float64{-100, -50, -20, -10, 0, 5, 10, 15, 20},
		},
		[]string{"pod", "namespace"},
	)

	throttlingRatio = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "mbcas_throttling_ratio",
			Help: "Current throttling ratio per pod",
		},
		[]string{"pod", "namespace"},
	)
)

func init() {
	prometheus.MustRegister(allocationChanges)
	prometheus.MustRegister(allocationLatency)
	prometheus.MustRegister(nashBargainMode)
	prometheus.MustRegister(qlearningReward)
	prometheus.MustRegister(throttlingRatio)
}
