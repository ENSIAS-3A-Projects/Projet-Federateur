package allocation

import (
	"k8s.io/apimachinery/pkg/types"
)

// KalaiSmorodinskyParams holds parameters for Kalai-Smorodinsky bargaining.
// Similar to Nash but includes ideal point (utopia point).
type KalaiSmorodinskyParams struct {
	UID      types.UID
	Weight   float64 // w_i: bargaining power
	Baseline int64   // d_i: disagreement point (minimum)
	Ideal    int64   // u_i: ideal point (utopia) - maximum reasonable allocation
	MaxAlloc int64   // Hard maximum (from K8s limit)
	Demand   int64   // Requested allocation
}

// KalaiSmorodinskySolution computes the Kalai-Smorodinsky bargaining solution.
//
// Objective: max min_i (x_i - d_i) / (u_i - d_i)
// Then scale proportionally to preserve ratios while satisfying capacity constraint.
//
// The solution ensures proportional gains: each agent gets the same fraction
// of their gain range (ideal - baseline), which converges faster than Nash.
func KalaiSmorodinskySolution(
	capacity int64,
	agents []KalaiSmorodinskyParams,
) map[types.UID]int64 {
	if len(agents) == 0 {
		return make(map[types.UID]int64)
	}

	// Step 1: Compute total baseline and check feasibility
	totalBaseline := int64(0)
	for _, a := range agents {
		totalBaseline += a.Baseline
	}

	availableSurplus := capacity - totalBaseline
	if availableSurplus < 0 {
		// Overloaded: scale baselines proportionally (same as Nash)
		return scaleBaselinesKalai(agents, capacity)
	}

	// Step 2: Compute ideal points (utopia)
	// Ideal point = min(Demand, MaxAlloc) for each agent
	ideals := make(map[types.UID]int64)
	totalIdealGain := 0.0
	for _, a := range agents {
		ideal := a.Demand
		if ideal > a.MaxAlloc {
			ideal = a.MaxAlloc
		}
		if ideal < a.Baseline {
			ideal = a.Baseline
		}
		ideals[a.UID] = ideal
		gainRange := float64(ideal - a.Baseline)
		if gainRange > 0 {
			totalIdealGain += gainRange * a.Weight
		}
	}

	// Step 3: Find the maximum proportional gain λ such that:
	// x_i = d_i + λ * (u_i - d_i) for all i
	// and Σ x_i ≤ capacity
	//
	// We solve: Σ (d_i + λ * (u_i - d_i)) ≤ capacity
	// => λ * Σ (u_i - d_i) ≤ capacity - Σ d_i
	// => λ ≤ (capacity - Σ d_i) / Σ (u_i - d_i)
	
	if totalIdealGain == 0 {
		// No gain possible: everyone at baseline
		allocs := make(map[types.UID]int64)
		for _, a := range agents {
			allocs[a.UID] = a.Baseline
		}
		return allocs
	}

	// Compute maximum λ that satisfies capacity constraint
	maxLambda := float64(availableSurplus) / totalIdealGain
	
	// Step 4: Allocate proportionally to gain ranges
	allocations := make(map[types.UID]int64)
	totalAllocated := int64(0)
	
	for _, a := range agents {
		ideal := ideals[a.UID]
		gainRange := float64(ideal - a.Baseline)
		
		if gainRange <= 0 {
			// No gain range: just baseline
			allocations[a.UID] = a.Baseline
			totalAllocated += a.Baseline
			continue
		}
		
		// Weighted proportional gain
		weightedGain := gainRange * a.Weight
		lambda := maxLambda * (weightedGain / totalIdealGain)
		
		allocation := a.Baseline + int64(lambda*gainRange)
		
		// Clamp to max
		if allocation > a.MaxAlloc {
			allocation = a.MaxAlloc
		}
		
		allocations[a.UID] = allocation
		totalAllocated += allocation
	}

	// Step 5: Redistribute any remaining surplus (if some agents capped)
	remaining := capacity - totalAllocated
	if remaining > 0 {
		redistributeKalaiSurplus(allocations, agents, ideals, remaining, totalIdealGain)
	}

	return allocations
}

// scaleBaselinesKalai scales baselines proportionally when overloaded.
func scaleBaselinesKalai(agents []KalaiSmorodinskyParams, capacity int64) map[types.UID]int64 {
	allocations := make(map[types.UID]int64)
	
	weightedBaseline := 0.0
	for _, a := range agents {
		weightedBaseline += float64(a.Baseline) * a.Weight
	}
	
	if weightedBaseline == 0 {
		// All baselines zero: equal division
		share := capacity / int64(len(agents))
		for _, a := range agents {
			allocations[a.UID] = share
		}
		return allocations
	}
	
	// Scale proportionally to weighted baselines
	for _, a := range agents {
		share := float64(capacity) * (float64(a.Baseline) * a.Weight / weightedBaseline)
		allocations[a.UID] = int64(share)
	}
	
	return allocations
}

// redistributeKalaiSurplus redistributes excess when some agents hit their max.
func redistributeKalaiSurplus(
	allocations map[types.UID]int64,
	agents []KalaiSmorodinskyParams,
	ideals map[types.UID]int64,
	remaining int64,
	totalIdealGain float64,
) {
	// Find uncapped agents (those below their ideal)
	uncapped := make([]KalaiSmorodinskyParams, 0)
	uncappedWeight := 0.0
	
	for _, a := range agents {
		ideal := ideals[a.UID]
		if allocations[a.UID] < ideal {
			uncapped = append(uncapped, a)
			gainRange := float64(ideal - a.Baseline)
			if gainRange > 0 {
				uncappedWeight += gainRange * a.Weight
			}
		}
	}
	
	if uncappedWeight == 0 || remaining <= 0 {
		return
	}
	
	// Redistribute proportional to remaining gain ranges
	for _, a := range uncapped {
		ideal := ideals[a.UID]
		gainRange := float64(ideal - a.Baseline)
		if gainRange <= 0 {
			continue
		}
		
		remainingGain := float64(ideal - allocations[a.UID])
		if remainingGain <= 0 {
			continue
		}
		
		weightedGain := remainingGain * a.Weight
		extra := float64(remaining) * (weightedGain / uncappedWeight)
		
		newAlloc := allocations[a.UID] + int64(extra)
		if newAlloc > ideal {
			newAlloc = ideal
		}
		if newAlloc > a.MaxAlloc {
			newAlloc = a.MaxAlloc
		}
		
		allocations[a.UID] = newAlloc
	}
}

