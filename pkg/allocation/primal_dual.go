package allocation

import (
	"fmt"
	"math"

	"k8s.io/apimachinery/pkg/types"
)

// PrimalDualCoordinator coordinates the distributed primal-dual price-clearing mechanism.
// Updates shadow price based on capacity constraint violation.
type PrimalDualCoordinator struct {
	// Step size for price updates (learning rate)
	Eta float64

	// Current shadow price
	Lambda float64

	// Convergence tolerance
	Tolerance float64

	// Maximum iterations
	MaxIterations int
}

// PrimalDualAgent represents an agent in the primal-dual mechanism.
// Each agent computes best-response demand given the shadow price.
type PrimalDualAgent struct {
	UID          types.UID
	UtilityParams *UtilityParams
	MinMilli     int64
	MaxMilli     int64
}

// PrimalDualResult contains the allocation result from primal-dual mechanism.
type PrimalDualResult struct {
	Allocations map[types.UID]int64
	ShadowPrice float64
	Iterations  int
	Converged   bool
}

// NewPrimalDualCoordinator creates a new primal-dual coordinator.
func NewPrimalDualCoordinator(eta, initialLambda, tolerance float64, maxIterations int) *PrimalDualCoordinator {
	return &PrimalDualCoordinator{
		Eta:           eta,
		Lambda:        initialLambda,
		Tolerance:     tolerance,
		MaxIterations: maxIterations,
	}
}

// PrimalDualPriceClearing performs distributed primal-dual price-clearing.
//
// Algorithm:
//  1. Coordinator broadcasts shadow price λ
//  2. Each agent computes best-response: x_i = argmax_{x} U_i(x) - λx
//  3. Coordinator updates price: λ_{t+1} = [λ_t + η(Σx_i - C)]_+
//  4. Repeat until convergence or max iterations
//
// This implements a market where price rises when overloaded, causing agents
// to naturally back off unless SLO penalty dominates.
func PrimalDualPriceClearing(
	capacityMilli int64,
	agents []PrimalDualAgent,
	coordinator *PrimalDualCoordinator,
) PrimalDualResult {
	if len(agents) == 0 {
		return PrimalDualResult{
			Allocations: make(map[types.UID]int64),
			ShadowPrice: 0,
			Iterations:  0,
			Converged:   true,
		}
	}

	// Initialize coordinator if not provided
	if coordinator == nil {
		coordinator = NewPrimalDualCoordinator(
			0.1,   // eta: step size
			0.0,   // initial lambda
			0.01,  // tolerance
			100,   // max iterations
		)
	}

	allocations := make(map[types.UID]int64)
	converged := false

	iteration := 0
	for ; iteration < coordinator.MaxIterations; iteration++ {
		// Step 1: Each agent computes best-response given current price
		totalDemand := int64(0)
		for _, agent := range agents {
			// Set shadow price in utility params
			agent.UtilityParams.LambdaCPU = coordinator.Lambda

			// Find allocation that maximizes U_i(x) - λx
			bestAlloc := computeBestResponse(agent, coordinator.Lambda)
			allocations[agent.UID] = bestAlloc
			totalDemand += bestAlloc
		}

		// Step 2: Coordinator updates shadow price
		// λ_{t+1} = [λ_t + η(Σx_i - C)]_+
		excess := float64(totalDemand - capacityMilli)
		oldLambda := coordinator.Lambda
		coordinator.Lambda = math.Max(0, coordinator.Lambda+coordinator.Eta*excess)

		// Step 3: Check convergence
		// Converged if price change is small and constraint is satisfied (within tolerance)
		lambdaChange := math.Abs(coordinator.Lambda - oldLambda)
		constraintViolation := math.Abs(excess) / float64(capacityMilli)

		if lambdaChange < coordinator.Tolerance && constraintViolation < coordinator.Tolerance {
			converged = true
			break
		}

		// If price is very high and still violating, we may need to scale down
		// This handles the case where all agents are at their minimums
		if coordinator.Lambda > 1000 && excess > 0 {
			// Emergency: scale all allocations proportionally
			scale := float64(capacityMilli) / float64(totalDemand)
			for uid := range allocations {
				allocations[uid] = int64(float64(allocations[uid]) * scale)
			}
			converged = true
			break
		}
	}

	return PrimalDualResult{
		Allocations: allocations,
		ShadowPrice: coordinator.Lambda,
		Iterations:  iteration, // Track actual iterations
		Converged:   converged,
	}
}

// computeBestResponse computes the allocation that maximizes U_i(x) - λx.
// Uses binary search or gradient ascent to find optimal allocation.
func computeBestResponse(agent PrimalDualAgent, lambda float64) int64 {
	minAlloc := agent.MinMilli
	maxAlloc := agent.MaxMilli

	// If min == max, return immediately
	if minAlloc == maxAlloc {
		return minAlloc
	}

	// Binary search for optimal allocation
	// We want to find x that maximizes U(x) - λx
	// This is equivalent to finding where marginal utility = λ
	bestAlloc := minAlloc
	bestValue := math.Inf(-1)

	// Try a few candidate allocations
	candidates := []int64{
		minAlloc,
		maxAlloc,
		(minAlloc + maxAlloc) / 2,
		minAlloc + (maxAlloc-minAlloc)/4,
		minAlloc + 3*(maxAlloc-minAlloc)/4,
	}

	for _, candidate := range candidates {
		if candidate < minAlloc {
			candidate = minAlloc
		}
		if candidate > maxAlloc {
			candidate = maxAlloc
		}

		// Set allocation in utility params
		agent.UtilityParams.SetAllocation(candidate)

		// Compute utility - cost
		utility := agent.UtilityParams.Utility()
		cost := lambda * float64(candidate)
		netValue := utility - cost

		if netValue > bestValue {
			bestValue = netValue
			bestAlloc = candidate
		}
	}

	// Refine using gradient information
	// If marginal utility > λ, we should increase allocation
	// If marginal utility < λ, we should decrease allocation
	agent.UtilityParams.SetAllocation(bestAlloc)
	marginalUtil := agent.UtilityParams.MarginalUtilityCPU()

	// Adjust based on marginal utility vs price
	if marginalUtil > lambda && bestAlloc < maxAlloc {
		// Marginal utility exceeds price, increase allocation
		step := (maxAlloc - bestAlloc) / 10
		if step < 1 {
			step = 1
		}
		newAlloc := bestAlloc + step
		if newAlloc > maxAlloc {
			newAlloc = maxAlloc
		}

		agent.UtilityParams.SetAllocation(newAlloc)
		newUtility := agent.UtilityParams.Utility()
		newCost := lambda * float64(newAlloc)
		newNetValue := newUtility - newCost

		if newNetValue > bestValue {
			bestAlloc = newAlloc
		}
	} else if marginalUtil < lambda && bestAlloc > minAlloc {
		// Marginal utility below price, decrease allocation
		step := (bestAlloc - minAlloc) / 10
		if step < 1 {
			step = 1
		}
		newAlloc := bestAlloc - step
		if newAlloc < minAlloc {
			newAlloc = minAlloc
		}

		agent.UtilityParams.SetAllocation(newAlloc)
		newUtility := agent.UtilityParams.Utility()
		newCost := lambda * float64(newAlloc)
		newNetValue := newUtility - newCost

		if newNetValue > bestValue {
			bestAlloc = newAlloc
		}
	}

	return bestAlloc
}

// ConvertUtilityParamsToPrimalDualAgents converts UtilityParams to PrimalDualAgent slice.
func ConvertUtilityParamsToPrimalDualAgents(
	utilityParams map[types.UID]*UtilityParams,
) []PrimalDualAgent {
	agents := make([]PrimalDualAgent, 0, len(utilityParams))
	for uid, util := range utilityParams {
		agents = append(agents, PrimalDualAgent{
			UID:          uid,
			UtilityParams: util,
			MinMilli:     util.BaselineCPU,
			MaxMilli:     util.MaxCPU,
		})
	}
	return agents
}

// ValidatePrimalDualResult validates that the primal-dual result satisfies constraints.
func ValidatePrimalDualResult(result PrimalDualResult, capacityMilli int64, agents []PrimalDualAgent) error {
	totalAlloc := int64(0)
	for _, agent := range agents {
		alloc, ok := result.Allocations[agent.UID]
		if !ok {
			return fmt.Errorf("missing allocation for agent %s", agent.UID)
		}
		if alloc < agent.MinMilli {
			return fmt.Errorf("allocation %d < min %d for agent %s", alloc, agent.MinMilli, agent.UID)
		}
		if alloc > agent.MaxMilli {
			return fmt.Errorf("allocation %d > max %d for agent %s", alloc, agent.MaxMilli, agent.UID)
		}
		totalAlloc += alloc
	}

	if totalAlloc > capacityMilli*101/100 { // Allow 1% tolerance for rounding
		return fmt.Errorf("total allocation %d exceeds capacity %d", totalAlloc, capacityMilli)
	}

	return nil
}






