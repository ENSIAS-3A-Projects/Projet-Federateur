package demand

// Package demand implements demand signal tracking and smoothing.

import (
	"math"
	"time"
)

// Tracker tracks and smooths demand signals using exponential moving average.
// Implements "fast up, slow down" behavior to avoid oscillations.
type Tracker struct {
	smoothed float64
	// Track consecutive zero readings for faster decay
	zeroCount int

	// Failure tracking
	consecutiveFailures int
	lastFailureTime     time.Time
	totalFailures       int64
}

const (
	// MaxConsecutiveFailures before demand is forced to zero
	MaxConsecutiveFailures = 3
)

// NewTracker creates a new demand tracker.
func NewTracker() *Tracker {
	return &Tracker{
		smoothed: 0.0,
	}
}

// Update updates the tracker with a new raw demand value and returns the smoothed value.
// Uses exponential moving average with different rates for increases vs decreases.
func (t *Tracker) Update(rawDemand float64) float64 {
	const (
		alphaIncrease      = 0.6 // Fast response to increases (increased from 0.3 for faster reaction)
		alphaDecrease      = 0.2 // Slow decay to avoid oscillations
		fastDecayThreshold = 5   // After 5 consecutive zeros, use fast decay
		fastDecayAlpha     = 0.5 // Fast decay rate
		rapidIncreaseAlpha = 0.8 // Very fast response for rapid increases
		rapidIncreaseThreshold = 0.3 // If increase > 30% of current, use rapid alpha
	)

	var alpha float64
	if rawDemand > t.smoothed {
		// Fast up: use higher alpha
		// Detect rapid increases (rate of change > threshold)
		increaseRate := (rawDemand - t.smoothed) / math.Max(t.smoothed, 0.01) // Avoid division by zero
		if increaseRate > rapidIncreaseThreshold {
			// Very rapid increase: use fastest alpha
			alpha = rapidIncreaseAlpha
		} else {
			// Normal increase: use standard fast alpha
			alpha = alphaIncrease
		}
		t.zeroCount = 0 // Reset zero counter on any increase
	} else if rawDemand == 0 {
		// Track consecutive zeros for accelerated decay
		t.zeroCount++
		if t.zeroCount >= fastDecayThreshold {
			// FIXED: Fast decay after sustained zero demand
			alpha = fastDecayAlpha
		} else {
			alpha = alphaDecrease
		}
	} else {
		// Slow down: use lower alpha
		alpha = alphaDecrease
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
