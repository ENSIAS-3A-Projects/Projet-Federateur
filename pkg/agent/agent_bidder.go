package agent

import (
	"math"

	"k8s.io/apimachinery/pkg/types"

	"mbcas/pkg/allocation"
)

// AgentBid represents a bid submitted by an autonomous agent.
type AgentBid struct {
	UID             types.UID
	Demand          int64   // Requested CPU in millicores
	MarginalUtility float64 // Marginal utility at current allocation
	Weight          float64 // Bargaining weight
	Strategy        string  // Strategy used: "aggressive", "conservative", "cooperative"
	State           string  // Encoded state for Q-learning
}

// ComputeBid computes an agent's bid based on its state and strategy.
// This is the core autonomous decision-making function.
func (s *PodAgentState) ComputeBid(
	shadowPrice float64,
	utilityParams *allocation.UtilityParams,
	currentAllocationMilli int64,
	throttling float64,
) AgentBid {
	// Encode current state
	stateStr := EncodeState(currentAllocationMilli, throttling, shadowPrice)

	// Get base demand from utility parameters
	// Use actual usage as base, with headroom
	baseDemand := utilityParams.ActualUsageMilli
	if baseDemand == 0 {
		baseDemand = utilityParams.BaselineCPU
	}

	// Add headroom (15% for normal operation)
	baseDemand = int64(float64(baseDemand) * 1.15)

	// Clamp to bounds
	if baseDemand < utilityParams.BaselineCPU {
		baseDemand = utilityParams.BaselineCPU
	}
	if baseDemand > utilityParams.MaxCPU {
		baseDemand = utilityParams.MaxCPU
	}

	// Apply strategy adjustment
	strategyMultiplier := s.ComputeStrategyAdjustment()
	adjustedDemand := int64(float64(baseDemand) * strategyMultiplier)

	// Clamp again after strategy adjustment
	if adjustedDemand < utilityParams.BaselineCPU {
		adjustedDemand = utilityParams.BaselineCPU
	}
	if adjustedDemand > utilityParams.MaxCPU {
		adjustedDemand = utilityParams.MaxCPU
	}

	// Get marginal utility
	marginalUtil := utilityParams.MarginalUtilityCPU()

	// Get strategy name
	strategy := s.GetStrategyName()

	return AgentBid{
		UID:             s.UID,
		Demand:          adjustedDemand,
		MarginalUtility: marginalUtil,
		Weight:          utilityParams.SLOWeight,
		Strategy:        strategy,
		State:           stateStr,
	}
}

// AggregateBids aggregates agent bids into market parameters for allocation.
// Converts AgentBid slice to PodParams map for market clearing.
func AggregateBids(bids []AgentBid, podParams map[types.UID]allocation.PodParams) map[types.UID]allocation.PodParams {
	result := make(map[types.UID]allocation.PodParams)

	for _, bid := range bids {
		// Get base params for this pod
		params, exists := podParams[bid.UID]
		if !exists {
			continue
		}

		// Update demand based on bid
		// Use bid demand as the "need" for allocation
		params.ActualUsageMilli = bid.Demand

		// Update weight if provided (agents can signal importance)
		if bid.Weight > 0 {
			params.Weight = bid.Weight
		}

		result[bid.UID] = params
	}

	return result
}

// ComputeAveragePayoff computes the average payoff across all agents.
// Used for replicator dynamics (strategy evolution).
func ComputeAveragePayoff(agentStates map[types.UID]*PodAgentState) float64 {
	if len(agentStates) == 0 {
		return 0.0
	}

	totalPayoff := 0.0
	count := 0

	for _, state := range agentStates {
		avgUtil, _, _ := state.GetPerformanceStats()
		totalPayoff += avgUtil
		count++
	}

	if count == 0 {
		return 0.0
	}

	return totalPayoff / float64(count)
}

// SelectBestBid selects the best bid from multiple candidate bids.
// Used when agent needs to choose between different bidding strategies.
func SelectBestBid(bids []AgentBid, shadowPrice float64) AgentBid {
	if len(bids) == 0 {
		return AgentBid{}
	}

	bestBid := bids[0]
	bestValue := bids[0].MarginalUtility - shadowPrice*float64(bids[0].Demand)

	for _, bid := range bids[1:] {
		// Value = marginal utility - cost
		value := bid.MarginalUtility - shadowPrice*float64(bid.Demand)
		if value > bestValue {
			bestValue = value
			bestBid = bid
		}
	}

	return bestBid
}

// ComputeBidFromHistory computes a bid based on historical outcomes.
// Uses past performance to inform current bid.
func (s *PodAgentState) ComputeBidFromHistory(
	utilityParams *allocation.UtilityParams,
	shadowPrice float64,
) AgentBid {
	// Get recent outcomes
	recentOutcomes := s.GetRecentOutcomes(5)

	if len(recentOutcomes) == 0 {
		// No history, use standard bid computation
		return s.ComputeBid(shadowPrice, utilityParams, utilityParams.BaselineCPU, 0.0)
	}

	// Analyze recent outcomes
	avgAllocation := 0.0
	avgThrottling := 0.0
	successCount := 0

	for _, outcome := range recentOutcomes {
		avgAllocation += float64(outcome.Allocation)
		avgThrottling += outcome.Throttling
		if !outcome.SLOViolation && outcome.Throttling < 0.2 {
			successCount++
		}
	}

	avgAllocation /= float64(len(recentOutcomes))
	avgThrottling /= float64(len(recentOutcomes))
	successRate := float64(successCount) / float64(len(recentOutcomes))

	// Adjust strategy based on history
	// If recent outcomes were successful, maintain or increase aggressiveness
	// If recent outcomes were poor, reduce aggressiveness
	if successRate > 0.7 {
		// Successful, can be more aggressive
		s.SetAggressiveness(math.Min(1.0, s.GetAggressiveness()+0.1))
	} else if successRate < 0.3 {
		// Poor performance, be more conservative
		s.SetAggressiveness(math.Max(0.0, s.GetAggressiveness()-0.1))
	}

	// Use average allocation as baseline
	baseAllocation := int64(avgAllocation)
	if baseAllocation == 0 {
		baseAllocation = utilityParams.BaselineCPU
	}

	return s.ComputeBid(shadowPrice, utilityParams, baseAllocation, avgThrottling)
}




