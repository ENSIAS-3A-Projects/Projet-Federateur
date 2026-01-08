package stability

import (
	"math"
	"sync"

	"k8s.io/apimachinery/pkg/types"

	mbcastypes "mbcas/pkg/types"
)

// MaxHistorySize limits the number of potential values stored in history.
// This prevents unbounded memory growth over long runtimes.
const MaxHistorySize = 1000

// BaselineViolationPenalty is the heavy penalty added when allocation < baseline.
// A high value ensures the optimizer strongly avoids baseline violations.
const BaselineViolationPenalty = 1e6

// LyapunovController ensures stable convergence of the allocation dynamics.
// Uses a potential function V that must be non-increasing across epochs.
type LyapunovController struct {
	mu          sync.RWMutex
	potential   float64   // Current V
	history     []float64 // V over time (circular buffer, max MaxHistorySize)
	stepSize    float64   // Current step size for updates
	minStepSize float64   // Minimum step size
	maxStepSize float64   // Maximum step size
}

// AllocationParams is an alias to the shared type for backward compatibility.
type AllocationParams = mbcastypes.AllocationParams

// NewLyapunovController creates a controller with initial step size.
func NewLyapunovController(initialStepSize, minStep, maxStep float64) *LyapunovController {
	return &LyapunovController{
		potential:   math.Inf(1),
		history:     make([]float64, 0, MaxHistorySize),
		stepSize:    initialStepSize,
		minStepSize: minStep,
		maxStepSize: maxStep,
	}
}

// ComputePotential calculates the Lyapunov function value.
//
// V = -Σ log(surplus_i) + α·Σ(SLO_violation_i)² + β·Var(surplus)
//
// Components:
// - Nash product (negative for minimization)
// - SLO violation penalty
// - Fairness term (variance of surpluses)
//
// Decreasing V means improving toward optimal allocation.
func ComputePotential(
	allocations map[types.UID]int64,
	params map[types.UID]AllocationParams,
	alpha, beta float64,
) float64 {
	// Component 1: Negative Nash product (for minimization)
	nashTerm := 0.0
	surpluses := make([]float64, 0, len(allocations))

	for uid, alloc := range allocations {
		p := params[uid]
		surplus := float64(alloc - p.Baseline)
		if surplus > 0 {
			nashTerm -= math.Log(surplus)
			surpluses = append(surpluses, surplus)
		} else {
			nashTerm += BaselineViolationPenalty
		}
	}

	// Component 2: SLO violation penalty
	sloTerm := 0.0
	for uid := range allocations {
		p := params[uid]
		if p.SLOGap > 0 {
			sloTerm += p.SLOGap * p.SLOGap
		}
	}

	// Component 3: Fairness (variance of surpluses)
	fairnessTerm := 0.0
	if len(surpluses) > 1 {
		mean := 0.0
		for _, s := range surpluses {
			mean += s
		}
		mean /= float64(len(surpluses))

		variance := 0.0
		for _, s := range surpluses {
			diff := s - mean
			variance += diff * diff
		}
		variance /= float64(len(surpluses))
		fairnessTerm = variance
	}

	return nashTerm + alpha*sloTerm + beta*fairnessTerm
}

// CheckAndAdaptStepSize verifies Lyapunov decrease and adjusts step size.
// Returns true if update should proceed, false if step size too small.
func (lc *LyapunovController) CheckAndAdaptStepSize(newPotential float64) bool {
	lc.mu.Lock()
	defer lc.mu.Unlock()

	// First epoch: just record
	if math.IsInf(lc.potential, 1) {
		lc.potential = newPotential
		lc.appendHistory(newPotential)
		return true
	}

	// Check if potential decreased (good) or increased (bad)
	delta := newPotential - lc.potential

	if delta <= 0 {
		// Good: potential decreased, can increase step size
		lc.stepSize *= 1.1
		if lc.stepSize > lc.maxStepSize {
			lc.stepSize = lc.maxStepSize
		}
		lc.potential = newPotential
		lc.appendHistory(newPotential)
		return true
	}

	// Bad: potential increased, reduce step size
	lc.stepSize *= 0.5
	if lc.stepSize < lc.minStepSize {
		lc.stepSize = lc.minStepSize
		// Still proceed but with minimum step
	}

	// Don't update potential (keep old value as target)
	lc.appendHistory(newPotential) // Record for monitoring
	return lc.stepSize >= lc.minStepSize
}

// appendHistory adds a value to history, trimming if over MaxHistorySize.
func (lc *LyapunovController) appendHistory(val float64) {
	lc.history = append(lc.history, val)
	// Trim to MaxHistorySize (keep most recent values)
	if len(lc.history) > MaxHistorySize {
		lc.history = lc.history[len(lc.history)-MaxHistorySize:]
	}
}

// GetStepSize returns current step size for allocation updates.
func (lc *LyapunovController) GetStepSize() float64 {
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	return lc.stepSize
}

// GetPotential returns the current potential value.
func (lc *LyapunovController) GetPotential() float64 {
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	return lc.potential
}

// GetHistory returns the history of potential values.
func (lc *LyapunovController) GetHistory() []float64 {
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	result := make([]float64, len(lc.history))
	copy(result, lc.history)
	return result
}

// BoundedUpdate applies an allocation change with Lyapunov step size.
// new_alloc = old_alloc + stepSize * (desired - old_alloc)
func (lc *LyapunovController) BoundedUpdate(current, desired int64) int64 {
	return lc.BoundedUpdateWithCongestion(current, desired, 1.0)
}

// BoundedUpdateWithCongestion applies an allocation change with congestion-aware step scaling.
// congestionFactor: 0.0 (no congestion) to 1.0 (high congestion)
// Step is scaled between 0.5x and 1.0x based on congestion.
func (lc *LyapunovController) BoundedUpdateWithCongestion(current, desired int64, congestionFactor float64) int64 {
	step := lc.GetStepSize()
	
	// Scale step by congestion: more congestion = larger step (faster response)
	// congestionFactor 0.0 -> 0.5x step, 1.0 -> 1.0x step
	adaptiveStep := step * (0.5 + 0.5*congestionFactor)
	
	// Clamp adaptive step to valid range
	if adaptiveStep < lc.minStepSize {
		adaptiveStep = lc.minStepSize
	}
	if adaptiveStep > lc.maxStepSize {
		adaptiveStep = lc.maxStepSize
	}
	
	delta := float64(desired - current)
	boundedDelta := int64(delta * adaptiveStep)

	return current + boundedDelta
}

// ComputeCongestionFactor computes congestion level from allocations and params.
// Returns value in [0.0, 1.0] where 1.0 = high congestion (many pods above baseline).
func ComputeCongestionFactor(
	allocations map[types.UID]int64,
	params map[types.UID]AllocationParams,
) float64 {
	if len(allocations) == 0 {
		return 0.0
	}
	
	totalResidual := 0.0
	totalBaseline := 0.0
	
	for uid, alloc := range allocations {
		p, ok := params[uid]
		if !ok {
			continue
		}
		baseline := float64(p.Baseline)
		totalBaseline += baseline
		if alloc > p.Baseline {
			residual := float64(alloc - p.Baseline)
			totalResidual += residual
		}
	}
	
	if totalBaseline == 0 {
		return 0.0
	}
	
	// Congestion factor = ratio of residual demand to baseline
	// Normalize to [0, 1] with saturation at 2.0x baseline
	congestion := totalResidual / totalBaseline
	if congestion > 2.0 {
		congestion = 2.0
	}
	return congestion / 2.0 // Normalize to [0, 1]
}

// IsConverging returns true if the potential has been decreasing.
func (lc *LyapunovController) IsConverging() bool {
	lc.mu.RLock()
	defer lc.mu.RUnlock()

	if len(lc.history) < 3 {
		return true // Too early to tell
	}

	// Check if last 3 values are decreasing
	n := len(lc.history)
	return lc.history[n-1] <= lc.history[n-2] && lc.history[n-2] <= lc.history[n-3]
}
