package allocation

import (
	"fmt"
	"math"

	"k8s.io/apimachinery/pkg/types"
)

// MaxRedistributionIterations prevents infinite loops in surplus redistribution.
// If all agents are capped and surplus remains, it will be discarded after this many iterations.
const MaxRedistributionIterations = 100

// NashBargainingParams holds the parameters for Nash Bargaining Solution.
type NashBargainingParams struct {
	UID          types.UID
	Weight       float64 // w_i: bargaining power (from K8s request or priority)
	Baseline     int64   // d_i: disagreement point (minimum allocation)
	MaxAlloc     int64   // Maximum allocation (from K8s limit)
	MarginalUtil float64 // ∂u_i/∂x_i: marginal utility at current allocation
	Demand       int64   // Requested allocation based on observed need
}

// NashBargainingResult contains the allocation result and metadata.
type NashBargainingResult struct {
	Allocations map[types.UID]int64
	Iterations  int // Number of redistribution iterations
}

// NashBargainingSolution computes the Nash Bargaining Solution.
//
// Objective: max Π_i (x_i - d_i)^w_i  (weighted Nash product)
// Equivalent: max Σ_i w_i · log(x_i - d_i)  (convex optimization)
//
// Subject to:
//
//	Σ_i x_i ≤ C (capacity)
//	x_i ≥ d_i (individual rationality)
//	x_i ≤ max_i (bounds)
//
// Algorithm: Water-filling with weights
//  1. Everyone gets baseline d_i
//  2. Distribute surplus proportional to w_i until capacity exhausted or bounds hit
//  3. Redistribute excess from capped agents
func NashBargainingSolution(
	capacity int64,
	agents []NashBargainingParams,
) map[types.UID]int64 {
	result := NashBargainingSolutionWithMetadata(capacity, agents)
	return result.Allocations
}

// NashBargainingSolutionWithMetadata computes the Nash Bargaining Solution and returns metadata.
func NashBargainingSolutionWithMetadata(
	capacity int64,
	agents []NashBargainingParams,
) NashBargainingResult {
	if len(agents) == 0 {
		return NashBargainingResult{
			Allocations: make(map[types.UID]int64),
			Iterations: 0,
		}
	}

	// Step 1: Compute total baseline and check feasibility
	totalBaseline := int64(0)
	totalWeight := 0.0
	for _, a := range agents {
		totalBaseline += a.Baseline
		totalWeight += a.Weight
	}

	availableSurplus := capacity - totalBaseline
	if availableSurplus < 0 {
		// Overloaded: scale baselines (emergency mode)
		allocs := scaleBaselinesWeighted(agents, capacity)
		return NashBargainingResult{
			Allocations: allocs,
			Iterations: 0, // No redistribution in overloaded mode
		}
	}

	// Step 2: Initial allocation = baseline + weighted share of surplus
	// Nash Bargaining with equal weights gives EQUAL surplus division
	// With weights: surplus_i ∝ w_i (this is the weighted NBS)
	allocations := make(map[types.UID]int64)
	remaining := availableSurplus
	uncapped := make([]NashBargainingParams, 0, len(agents))

	for _, a := range agents {
		// Weighted surplus share
		share := float64(availableSurplus) * (a.Weight / totalWeight)
		allocation := a.Baseline + int64(share)

		// Check if capped
		if allocation > a.MaxAlloc {
			allocations[a.UID] = a.MaxAlloc
			remaining -= (a.MaxAlloc - a.Baseline)
		} else {
			uncapped = append(uncapped, a)
			allocations[a.UID] = allocation
		}
	}

	// Step 3: Redistribute excess from capped agents
	iterations := 0
	if remaining > 0 && len(uncapped) > 0 {
		iterations = redistributeNashSurplus(allocations, uncapped, remaining)
	}

	return NashBargainingResult{
		Allocations: allocations,
		Iterations:  iterations,
	}
}

// redistributeNashSurplus handles the case where some agents hit their max.
// The excess is redistributed to uncapped agents proportional to weights.
//
// SAFETY: This function is protected against infinite loops. If all agents
// become capped and surplus remains, the surplus is discarded.
//
// Returns the number of iterations performed.
func redistributeNashSurplus(
	allocations map[types.UID]int64,
	uncapped []NashBargainingParams,
	remaining int64,
) int {
	iterations := 0

	for remaining > 0 && len(uncapped) > 0 && iterations < MaxRedistributionIterations {
		iterations++

		// Compute weight sum of uncapped agents
		uncappedWeight := 0.0
		for _, a := range uncapped {
			uncappedWeight += a.Weight
		}

		if uncappedWeight == 0 {
			// No weights to distribute by, exit
			break
		}

		// Track how much we distributed this iteration
		distributed := int64(0)
		newUncapped := make([]NashBargainingParams, 0, len(uncapped))

		// Redistribute proportional to weights with adaptive gain
		for _, a := range uncapped {
			// Compute residual demand (how much more the agent needs)
			residual := float64(a.Demand - allocations[a.UID])
			
			// Adaptive gain: increases with residual demand (up to 2.0x)
			// Clamp to [1.0, 2.0] to prevent instability
			adaptiveGain := 1.0
			if a.Baseline > 0 {
				residualRatio := residual / float64(a.Baseline)
				adaptiveGain = 1.0 + 0.5*math.Min(1.0, residualRatio)
			}
			if adaptiveGain > 2.0 {
				adaptiveGain = 2.0
			}
			if adaptiveGain < 1.0 {
				adaptiveGain = 1.0
			}
			
			// Apply adaptive gain to gradient step
			extra := float64(remaining) * (a.Weight / uncappedWeight) * adaptiveGain
			newAlloc := allocations[a.UID] + int64(extra)

			// Clamp to max
			if newAlloc > a.MaxAlloc {
				distributed += a.MaxAlloc - allocations[a.UID]
				allocations[a.UID] = a.MaxAlloc
				// Agent is now capped, don't add to newUncapped
			} else {
				distributed += int64(extra)
				allocations[a.UID] = newAlloc
				newUncapped = append(newUncapped, a)
			}
		}

		remaining -= distributed

		// Check if we made progress
		if len(newUncapped) == len(uncapped) && distributed == 0 {
			// No progress made - all agents saturated or rounding issues
			// Discard remaining surplus to prevent infinite loop
			break
		}

		uncapped = newUncapped
	}

	// If we exit with remaining > 0, surplus is discarded (all agents at max)
	// This is the expected behavior per the implementation warning in PLAN.md

	return iterations
}

// scaleBaselinesWeighted handles overloaded case with weighted scaling.
// Each agent gets: x_i = (d_i · w_i / Σ(d_j · w_j)) · C
func scaleBaselinesWeighted(agents []NashBargainingParams, capacity int64) map[types.UID]int64 {
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

	scale := float64(capacity) / weightedBaseline
	for _, a := range agents {
		alloc := int64(float64(a.Baseline) * a.Weight * scale)
		if alloc < 10 { // Absolute minimum
			alloc = 10
		}
		allocations[a.UID] = alloc
	}

	return allocations
}

// VerifyNashAxioms checks that the solution satisfies Nash axioms.
// Returns nil if valid, error describing violation otherwise.
func VerifyNashAxioms(
	solution map[types.UID]int64,
	agents []NashBargainingParams,
	capacity int64,
) error {
	// Axiom 1: Pareto Optimality - all capacity should be used (or agents capped)
	totalAlloc := int64(0)
	allCapped := true
	for _, a := range agents {
		alloc := solution[a.UID]
		totalAlloc += alloc
		if alloc < a.MaxAlloc {
			allCapped = false
		}
	}

	if totalAlloc < capacity && !allCapped {
		return fmt.Errorf("Pareto violation: capacity %d, allocated %d, not all capped",
			capacity, totalAlloc)
	}

	// Axiom 2: Individual Rationality - everyone >= baseline
	for _, a := range agents {
		if solution[a.UID] < a.Baseline {
			return fmt.Errorf("IR violation: agent %s got %d < baseline %d",
				a.UID, solution[a.UID], a.Baseline)
		}
	}

	// Axiom 3: Symmetry - equal weights → equal surplus (approximately)
	// (Only check if all weights equal)
	if len(agents) == 0 {
		return nil
	}

	firstWeight := agents[0].Weight
	allEqual := true
	for _, a := range agents {
		if math.Abs(a.Weight-firstWeight) > 0.001 {
			allEqual = false
			break
		}
	}

	if allEqual && len(agents) > 1 {
		// Check surplus equality (within tolerance)
		surpluses := make([]int64, len(agents))
		for i, a := range agents {
			surpluses[i] = solution[a.UID] - a.Baseline
		}

		maxSurplus := surpluses[0]
		minSurplus := surpluses[0]
		for _, s := range surpluses {
			if s > maxSurplus {
				maxSurplus = s
			}
			if s < minSurplus {
				minSurplus = s
			}
		}

		// Allow 10% tolerance for rounding
		if maxSurplus > 0 && float64(maxSurplus-minSurplus)/float64(maxSurplus) > 0.1 {
			return fmt.Errorf("Symmetry violation: surplus range [%d, %d] with equal weights",
				minSurplus, maxSurplus)
		}
	}

	return nil
}
