package agent

import (
	"math"
)

// EvolveStrategy updates strategy parameters using replicator dynamics.
// Successful strategies increase in frequency, unsuccessful ones decrease.
func (s *PodAgentState) EvolveStrategy(ownPayoff float64, avgPayoff float64, learningRate float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Replicator dynamics: successful strategies grow
	// If own payoff > average, increase aggressiveness
	// If own payoff < average, decrease aggressiveness
	payoffRatio := ownPayoff / (avgPayoff + 0.001) // Add small epsilon to avoid division by zero

	// Adjust aggressiveness based on performance
	// If performing well, can be more aggressive
	// If performing poorly, should be more conservative
	adjustment := (payoffRatio - 1.0) * learningRate

	newAggressiveness := s.Aggressiveness + adjustment
	if newAggressiveness < 0 {
		newAggressiveness = 0
	}
	if newAggressiveness > 1 {
		newAggressiveness = 1
	}
	s.Aggressiveness = newAggressiveness

	// Adjust cooperation level inversely to aggressiveness
	// More aggressive agents cooperate less, more conservative agents cooperate more
	s.CooperationLevel = 1.0 - s.Aggressiveness
}

// GetStrategyName returns a human-readable strategy name based on parameters.
func (s *PodAgentState) GetStrategyName() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.Aggressiveness > 0.7 {
		return "aggressive"
	} else if s.Aggressiveness < 0.3 {
		return "conservative"
	}
	return "cooperative"
}

// ComputeStrategyAdjustment computes how to adjust demand based on strategy.
// Returns a multiplier to apply to base demand.
func (s *PodAgentState) ComputeStrategyAdjustment() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Inline strategy check to avoid nested lock acquisition
	var strategy string
	if s.Aggressiveness > 0.7 {
		strategy = "aggressive"
	} else if s.Aggressiveness < 0.3 {
		strategy = "conservative"
	} else {
		strategy = "cooperative"
	}

	switch strategy {
	case "aggressive":
		// Aggressive: bid 20% more than base demand
		return 1.2
	case "conservative":
		// Conservative: bid 10% less than base demand
		return 0.9
	case "cooperative":
		// Cooperative: bid at base demand (no adjustment)
		return 1.0
	default:
		return 1.0
	}
}

// AdaptToMarketConditions adjusts strategy based on market signals.
// High prices indicate competition, low prices indicate abundance.
func (s *PodAgentState) AdaptToMarketConditions(shadowPrice float64, avgPrice float64, adaptationRate float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// If price is much higher than average, market is competitive
	// Consider backing off (reduce aggressiveness)
	if avgPrice > 0 && shadowPrice > avgPrice*1.2 {
		// Market is competitive, reduce aggressiveness
		s.Aggressiveness = math.Max(0, s.Aggressiveness-adaptationRate*0.1)
		s.CooperationLevel = math.Min(1, s.CooperationLevel+adaptationRate*0.1)
	} else if avgPrice > 0 && shadowPrice < avgPrice*0.8 {
		// Market is relaxed, can be more aggressive
		s.Aggressiveness = math.Min(1, s.Aggressiveness+adaptationRate*0.05)
		s.CooperationLevel = math.Max(0, s.CooperationLevel-adaptationRate*0.05)
	}
}

// ResetStrategy resets strategy parameters to default values.
// Useful for testing or when agent needs to start fresh.
func (s *PodAgentState) ResetStrategy() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Aggressiveness = 0.5
	s.CooperationLevel = 0.5
}

// GetStrategyParams returns current strategy parameters.
func (s *PodAgentState) GetStrategyParams() (aggressiveness float64, cooperationLevel float64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Aggressiveness, s.CooperationLevel
}




