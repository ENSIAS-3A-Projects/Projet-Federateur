package agent

import (
	"math"
)

// ObserveMarket allows agents to observe market conditions and adjust strategies.
// This enables implicit agent-to-agent communication through price signals.
func (s *PodAgentState) ObserveMarket(shadowPrice float64, avgPrice float64, priceHistory []float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// If price is rising, other agents are bidding aggressively
	// Consider backing off to avoid competition
	if avgPrice > 0 && shadowPrice > avgPrice*1.2 {
		// Market is competitive, increase cooperation
		s.CooperationLevel = math.Min(1.0, s.CooperationLevel+0.1)
		s.Aggressiveness = math.Max(0.0, s.Aggressiveness-0.05)
	} else if avgPrice > 0 && shadowPrice < avgPrice*0.8 {
		// Market is relaxed, can be more aggressive
		s.Aggressiveness = math.Min(1.0, s.Aggressiveness+0.05)
		s.CooperationLevel = math.Max(0.0, s.CooperationLevel-0.05)
	}

	// Analyze price trends from history
	if len(priceHistory) >= 3 {
		// Check if prices are trending up or down
		recent := priceHistory[len(priceHistory)-3:]
		trend := (recent[2] - recent[0]) / (recent[0] + 0.001) // Percentage change

		if trend > 0.1 {
			// Prices trending up: market getting competitive
			s.CooperationLevel = math.Min(1.0, s.CooperationLevel+0.05)
		} else if trend < -0.1 {
			// Prices trending down: market relaxing
			s.Aggressiveness = math.Min(1.0, s.Aggressiveness+0.05)
		}
	}
}

// ComputeMarketSignal computes a market signal from price and allocation data.
// Returns a value indicating market conditions: -1 (competitive) to +1 (relaxed).
func ComputeMarketSignal(shadowPrice float64, avgPrice float64, capacityUtilization float64) float64 {
	// Normalize price signal
	priceSignal := 0.0
	if avgPrice > 0 {
		priceSignal = (avgPrice - shadowPrice) / (avgPrice + 0.001)
	}

	// Normalize utilization signal
	utilizationSignal := 1.0 - capacityUtilization // High utilization = competitive

	// Combine signals (weighted average)
	marketSignal := 0.6*priceSignal + 0.4*utilizationSignal

	// Clamp to [-1, 1]
	if marketSignal > 1.0 {
		marketSignal = 1.0
	}
	if marketSignal < -1.0 {
		marketSignal = -1.0
	}

	return marketSignal
}

// FormImplicitCoalition detects if agents should form an implicit coalition.
// Agents with similar strategies and complementary needs can cooperate.
func FormImplicitCoalition(agent1, agent2 *PodAgentState, need1, need2 int64) bool {
	// Agents with similar cooperation levels are more likely to form coalitions
	coop1 := agent1.GetCooperationLevel()
	coop2 := agent2.GetCooperationLevel()
	coopSimilarity := 1.0 - math.Abs(coop1-coop2)

	// Agents with complementary needs (one high, one low) can help each other
	needComplementarity := 0.0
	if need1 > 0 && need2 > 0 {
		ratio := math.Min(float64(need1), float64(need2)) / math.Max(float64(need1), float64(need2))
		needComplementarity = 1.0 - ratio // Higher when needs are different
	}

	// Form coalition if similarity and complementarity are high enough
	coalitionScore := 0.6*coopSimilarity + 0.4*needComplementarity
	return coalitionScore > 0.6
}

// ShareInformation allows agents to share information about market conditions.
// This is implicit sharing through observing each other's behavior.
func (s *PodAgentState) ShareInformation(otherStates []*PodAgentState) {
	if len(otherStates) == 0 {
		return
	}

	// Compute average strategy of other agents
	avgAggressiveness := 0.0
	avgCooperation := 0.0
	for _, other := range otherStates {
		agg, coop := other.GetStrategyParams()
		avgAggressiveness += agg
		avgCooperation += coop
	}
	avgAggressiveness /= float64(len(otherStates))
	avgCooperation /= float64(len(otherStates))

	// Adjust own strategy to be closer to average (conformity)
	// This creates emergent coordination
	ownAgg, ownCoop := s.GetStrategyParams()
	conformityRate := 0.1 // How much to conform to group

	newAgg := ownAgg + conformityRate*(avgAggressiveness-ownAgg)
	newCoop := ownCoop + conformityRate*(avgCooperation-ownCoop)

	s.SetAggressiveness(newAgg)
	s.SetCooperationLevel(newCoop)
}

// DetectCompetition detects if agents are competing for resources.
// Returns true if competition is detected (similar needs, similar strategies).
func DetectCompetition(agent1, agent2 *PodAgentState, need1, need2 int64) bool {
	// Competition occurs when:
	// 1. Both agents have similar needs (competing for same resources)
	// 2. Both agents are aggressive (not willing to back down)

	needSimilarity := 0.0
	if need1 > 0 && need2 > 0 {
		ratio := math.Min(float64(need1), float64(need2)) / math.Max(float64(need1), float64(need2))
		needSimilarity = ratio // Higher when needs are similar
	}

	agg1 := agent1.GetAggressiveness()
	agg2 := agent2.GetAggressiveness()
	bothAggressive := (agg1 > 0.7 && agg2 > 0.7)

	// Competition if needs are similar and both are aggressive
	return needSimilarity > 0.8 && bothAggressive
}




