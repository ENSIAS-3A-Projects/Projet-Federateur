package agent

// QLearning implements Q-learning for agent strategy selection.
type QLearning struct {
	learningRate   float64 // α: how quickly to update Q-values
	discountFactor float64 // γ: importance of future rewards
	explorationRate float64 // ε: probability of exploration vs exploitation
}

// NewQLearning creates a new Q-learning instance.
func NewQLearning(learningRate, discountFactor, explorationRate float64) *QLearning {
	return &QLearning{
		learningRate:   learningRate,
		discountFactor: discountFactor,
		explorationRate: explorationRate,
	}
}

// UpdateQValue updates the Q-value using the Q-learning update rule.
// Q(s,a) = Q(s,a) + α[r + γ·max Q(s',a') - Q(s,a)]
func (q *QLearning) UpdateQValue(state *PodAgentState, stateStr, action string, reward float64, nextStateStr string) {
	currentQ := state.GetQValue(stateStr, action)
	maxNextQ := state.GetMaxQValue(nextStateStr)

	// Q-learning update: Q(s,a) = Q(s,a) + α[r + γ·max Q(s',a') - Q(s,a)]
	newQ := currentQ + q.learningRate*(reward+q.discountFactor*maxNextQ-currentQ)

	state.SetQValue(stateStr, action, newQ)
}

// SelectAction selects an action using ε-greedy policy.
// With probability ε, selects random action (exploration).
// Otherwise, selects action with highest Q-value (exploitation).
func (q *QLearning) SelectAction(state *PodAgentState, stateStr string, randomFloat func() float64) string {
	// Exploration: random action
	if randomFloat() < q.explorationRate {
		actions := []string{"aggressive", "conservative", "cooperative"}
		idx := int(randomFloat() * float64(len(actions)))
		return actions[idx]
	}

	// Exploitation: best action
	return q.selectBestAction(state, stateStr)
}

// selectBestAction selects the action with the highest Q-value for the given state.
func (q *QLearning) selectBestAction(state *PodAgentState, stateStr string) string {
	actions := []string{"aggressive", "conservative", "cooperative"}
	bestAction := actions[0]
	bestQ := state.GetQValue(stateStr, bestAction)

	for _, action := range actions[1:] {
		qVal := state.GetQValue(stateStr, action)
		if qVal > bestQ {
			bestQ = qVal
			bestAction = action
		}
	}

	return bestAction
}

// ComputeReward computes the reward for a decision outcome.
// Reward = utility - cost - SLO_penalty
func ComputeReward(outcome DecisionOutcome, costWeight float64) float64 {
	// Base reward from utility
	reward := outcome.Utility

	// Subtract cost (proportional to allocation and price)
	cost := costWeight * float64(outcome.Allocation) * outcome.ShadowPrice
	reward -= cost

	// Penalty for SLO violations
	if outcome.SLOViolation {
		reward -= 10.0 // Heavy penalty for SLO violations
	}

	// Small penalty for throttling (encourages avoiding contention)
	if outcome.Throttling > 0.3 {
		reward -= 2.0 * outcome.Throttling
	}

	return reward
}

// EncodeState encodes the current state into a string representation.
// State: (allocation_level, throttling_level, price_level)
func EncodeState(allocationMilli int64, throttling float64, shadowPrice float64) string {
	// Discretize allocation into levels: low, medium, high
	allocationLevel := "low"
	if allocationMilli > 1000 {
		allocationLevel = "high"
	} else if allocationMilli > 500 {
		allocationLevel = "medium"
	}

	// Discretize throttling: none, low, high
	throttlingLevel := "none"
	if throttling > 0.3 {
		throttlingLevel = "high"
	} else if throttling > 0.1 {
		throttlingLevel = "low"
	}

	// Discretize price: low, medium, high
	priceLevel := "low"
	if shadowPrice > 1.0 {
		priceLevel = "high"
	} else if shadowPrice > 0.5 {
		priceLevel = "medium"
	}

	return allocationLevel + ":" + throttlingLevel + ":" + priceLevel
}

// DecayExplorationRate reduces exploration rate over time (annealing).
// This allows agents to explore more initially and exploit more as they learn.
func (q *QLearning) DecayExplorationRate(decayFactor float64) {
	q.explorationRate *= decayFactor
	if q.explorationRate < 0.01 {
		q.explorationRate = 0.01 // Minimum exploration
	}
}

// GetExplorationRate returns the current exploration rate.
func (q *QLearning) GetExplorationRate() float64 {
	return q.explorationRate
}

