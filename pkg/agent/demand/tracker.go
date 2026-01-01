package demand

// Package demand implements demand signal tracking and smoothing.

// Tracker tracks and smooths demand signals using exponential moving average.
// Implements "fast up, slow down" behavior to avoid oscillations.
type Tracker struct {
	smoothed float64
	// Track consecutive zero readings for faster decay
	zeroCount int
}

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
		alphaIncrease      = 0.3 // Fast response to increases
		alphaDecrease      = 0.2 // FIXED: Increased from 0.1 for faster scaling down
		fastDecayThreshold = 5   // After 5 consecutive zeros, use fast decay
		fastDecayAlpha     = 0.5 // Fast decay rate
	)

	var alpha float64
	if rawDemand > t.smoothed {
		// Fast up: use higher alpha
		alpha = alphaIncrease
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
