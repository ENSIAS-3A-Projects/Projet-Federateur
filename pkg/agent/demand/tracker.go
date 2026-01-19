package demand

// Package demand implements demand signal tracking and smoothing.

import (
	"math"
	"time"
)

// Tracker tracks and smooths demand signals using exponential moving average.
// Implements "fast up, slow down" behavior to avoid oscillations.
type Tracker struct {
	// Smoothed demand value
	smoothed float64
	// Track consecutive zero readings for faster decay
	zeroCount int

	// Failure tracking
	consecutiveFailures int
	lastFailureTime     time.Time
	totalFailures       int64

	// Smoothing parameters
	AlphaIncrease float64
	AlphaDecrease float64
}

const (
	// MaxConsecutiveFailures before demand is forced to zero
	MaxConsecutiveFailures = 3
)

// NewTracker creates a new demand tracker with default values.
func NewTracker() *Tracker {
	return &Tracker{
		smoothed:      0.0,
		AlphaIncrease: 0.6, // Default: Fast Up
		AlphaDecrease: 0.2, // Default: Slow Down
	}
}

// Update updates the tracker with a new raw demand value and returns the smoothed value.
// Uses exponential moving average with configurable rates.
func (t *Tracker) Update(rawDemand float64) float64 {
	const (
		fastDecayThreshold     = 5   // After 5 consecutive zeros, use fast decay
		fastDecayMultiplier    = 2.5 // Multiplier for decay alpha when zero count high
		rapidIncreaseThreshold = 0.3 // If increase > 30% of current, use rapid alpha
	)

	var alpha float64
	if rawDemand > t.smoothed {
		// Increasing
		alpha = t.AlphaIncrease

		// Detect rapid increases (rate of change > threshold)
		// Only apply rapid increase logic if we are NOT in cost efficiency mode (default behavior)
		// In cost efficiency mode, we strictly follow AlphaIncrease (which will be low)
		increaseRate := (rawDemand - t.smoothed) / math.Max(t.smoothed, 0.01)
		if increaseRate > rapidIncreaseThreshold && t.AlphaIncrease > 0.5 {
			// Boost alpha for rapid response only if base alpha is already high (aggressive mode)
			alpha = math.Min(1.0, t.AlphaIncrease*1.5)
		}

		t.zeroCount = 0
	} else if rawDemand == 0 {
		// Zero reading
		t.zeroCount++
		if t.zeroCount >= fastDecayThreshold {
			// Accelerated decay for sustained zeros
			alpha = math.Min(1.0, t.AlphaDecrease*fastDecayMultiplier)
		} else {
			alpha = t.AlphaDecrease
		}
	} else {
		// Decreasing (non-zero)
		alpha = t.AlphaDecrease
		t.zeroCount = 0
	}

	t.smoothed = alpha*rawDemand + (1-alpha)*t.smoothed

	// Clamp to [0, 1]
	if t.smoothed < 0 {
		t.smoothed = 0
	} else if t.smoothed > 1 {
		t.smoothed = 1
	}

	// FIXED: Floor very small values to zero to avoid stuck micro-allocations
	if t.smoothed < 0.01 {
		t.smoothed = 0
	}

	return t.smoothed
}

// Current returns the current smoothed demand value.
func (t *Tracker) Current() float64 {
	return t.smoothed
}

// Reset resets the tracker to zero.
func (t *Tracker) Reset() {
	t.smoothed = 0.0
}

// SetSmoothedDemand manually sets the smoothed demand value.
// Used for coordinating control loops (e.g., Fast Guardrail locking in demand).
func (t *Tracker) SetSmoothedDemand(value float64) {
	if value < 0 {
		value = 0
	} else if value > 1 {
		value = 1
	}
	t.smoothed = value
}

// RecordFailure records a cgroup read failure.
// Returns true if demand should be treated as zero (sustained failure).
func (t *Tracker) RecordFailure() bool {
	t.consecutiveFailures++
	t.totalFailures++
	t.lastFailureTime = time.Now()

	return t.consecutiveFailures >= MaxConsecutiveFailures
}

// RecordSuccess resets the failure counter on successful read.
func (t *Tracker) RecordSuccess() {
	t.consecutiveFailures = 0
}

// FailureStats returns failure statistics for observability.
func (t *Tracker) FailureStats() (consecutive int, total int64) {
	return t.consecutiveFailures, t.totalFailures
}
