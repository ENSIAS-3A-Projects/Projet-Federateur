package demand

// Package demand implements demand signal tracking and smoothing.

// Tracker tracks and smooths demand signals using exponential moving average.
// Implements "fast up, slow down" behavior to avoid oscillations.
type Tracker struct {
	smoothed float64
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
		alphaIncrease = 0.3 // Fast response to increases
		alphaDecrease = 0.1 // Slow decay to avoid oscillations
	)

	if rawDemand > t.smoothed {
		// Fast up: use higher alpha
		t.smoothed = alphaIncrease*rawDemand + (1-alphaIncrease)*t.smoothed
	} else {
		// Slow down: use lower alpha
		t.smoothed = alphaDecrease*rawDemand + (1-alphaDecrease)*t.smoothed
	}

	// Clamp to [0, 1]
	if t.smoothed < 0 {
		t.smoothed = 0
	} else if t.smoothed > 1 {
		t.smoothed = 1
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

