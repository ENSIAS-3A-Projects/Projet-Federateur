package agent

import (
	"math"
	"math/rand"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

// PodAgent represents an autonomous agent for a single pod.
// It uses Q-learning to learn optimal bidding strategies based on observed outcomes.
type PodAgent struct {
	mu sync.RWMutex

	// Identity
	UID types.UID

	// Current State
	Usage      int64   // Current CPU usage (millicores)
	Throttling float64 // Current throttling ratio [0, 1]
	Allocation int64   // Current CPU allocation (millicores)
	SLOTarget  float64 // Target latency (ms), 0 if no SLO

	// Learning Parameters
	QTable  map[string]map[string]float64 // Q(state, action)
	Alpha   float64                       // Learning rate (0.1 = 10% weight to new info)
	Gamma   float64                       // Discount factor (0.9 = value future rewards)
	Epsilon float64                       // Exploration rate (0.2 = 20% random actions)

	// History for learning
	PrevState  string
	PrevAction string
	PrevReward float64

	// Startup grace period
	StartTime time.Time

	// Cost efficiency mode
	SmoothedDemand int64 // EMA smoothed demand

	// IMPROVEMENT #2: Throttling trend for pattern learning (ABM)
	ThrottlingHistory []float64 // Last 3 throttling samples for trend analysis
	PrevAllocation    int64     // Previous allocation for oscillation detection
}

// Bid represents a pod's resource request to the Nash Bargaining solver
type Bid struct {
	UID    types.UID
	Demand int64   // Requested allocation (millicores)
	Weight float64 // Bargaining power (higher = more priority)
	Min    int64   // Minimum viable allocation (disagreement point)
	Max    int64   // Maximum useful allocation
}

// NewPodAgent creates a new agent for a pod (deprecated, use NewPodAgentWithConfig)
func NewPodAgent(uid types.UID, sloTarget float64) *PodAgent {
	return &PodAgent{
		UID:        uid,
		SLOTarget:  sloTarget,
		QTable:     make(map[string]map[string]float64),
		Alpha:      0.1, // 10% learning rate
		Gamma:      0.9, // Value future rewards
		Epsilon:    0.2, // 20% exploration
		Allocation: 100, // Start with baseline
		StartTime:  time.Now(),
	}
}

// NewPodAgentWithConfig creates a new agent with configuration
func NewPodAgentWithConfig(uid types.UID, sloTarget float64, config *AgentConfig) *PodAgent {
	return &PodAgent{
		UID:            uid,
		SLOTarget:      sloTarget,
		QTable:         make(map[string]map[string]float64),
		Alpha:          config.AgentLearningRate,
		Gamma:          config.AgentDiscountFactor,
		Epsilon:        config.AgentExplorationRate,
		Allocation:     100,
		StartTime:      time.Now(),
		SmoothedDemand: 100,
	}
}

// State encodes current state into a discrete string
// State space: (usage_level, throttle_level, allocation_level)
func (pa *PodAgent) State() string {
	pa.mu.RLock()
	defer pa.mu.RUnlock()
	return pa.stateInternal()
}

// stateInternal is the internal version that doesn't lock (for use when already locked)
func (pa *PodAgent) stateInternal() string {
	// Discretize usage
	usageLevel := "low"
	if pa.Usage > 1000 {
		usageLevel = "high"
	} else if pa.Usage > 500 {
		usageLevel = "medium"
	}

	// Discretize throttling
	throttleLevel := "none"
	if pa.Throttling > 0.3 {
		throttleLevel = "high"
	} else if pa.Throttling > 0.1 {
		throttleLevel = "some"
	}

	// Discretize allocation relative to usage
	allocLevel := "adequate"
	if pa.Allocation < pa.Usage {
		allocLevel = "low"
	} else if pa.Allocation > pa.Usage*2 {
		allocLevel = "excess"
	}

	return usageLevel + ":" + throttleLevel + ":" + allocLevel
}

// Actions available to the agent
var Actions = []string{"aggressive", "normal", "conservative"}

// SelectAction chooses an action using ε-greedy policy
func (pa *PodAgent) SelectAction(state string) string {
	pa.mu.Lock()
	defer pa.mu.Unlock()
	return pa.selectActionInternal(state)
}

// selectActionInternal is the internal version that doesn't lock
func (pa *PodAgent) selectActionInternal(state string) string {
	return pa.selectActionInternalWithPrice(state, 0.0)
}

// selectActionInternalWithPrice selects action with shadow price feedback (Game Theory + ABM)
func (pa *PodAgent) selectActionInternalWithPrice(state string, shadowPrice float64) string {
	// Exploration: random action
	if rand.Float64() < pa.Epsilon {
		return Actions[rand.Intn(len(Actions))]
	}

	// Exploitation: best action based on Q-values, adjusted by shadow price
	return pa.bestActionWithPrice(state, shadowPrice)
}

// bestActionWithPrice returns best action adjusted by shadow price
func (pa *PodAgent) bestActionWithPrice(state string, shadowPrice float64) string {
	if _, exists := pa.QTable[state]; !exists {
		pa.QTable[state] = make(map[string]float64)
	}

	// IMPROVEMENT #5: Adjust Q-values by shadow price (high price = be conservative)
	adjustedQ := make(map[string]float64)
	for _, action := range Actions {
		q := pa.QTable[state][action]
		if shadowPrice > 0.3 {
			// High shadow price: penalize aggressive, reward conservative
			if action == "aggressive" {
				q -= shadowPrice * 5.0 // Penalty for aggressive when resources scarce
			} else if action == "conservative" {
				q += shadowPrice * 2.0 // Bonus for conservative when resources scarce
			}
		}
		adjustedQ[action] = q
	}

	// Select best action from adjusted Q-values
	bestAction := Actions[0]
	bestQ := adjustedQ[bestAction]
	for _, action := range Actions[1:] {
		if adjustedQ[action] > bestQ {
			bestQ = adjustedQ[action]
			bestAction = action
		}
	}

	return bestAction
}

// bestAction returns the action with highest Q-value for given state
func (pa *PodAgent) bestAction(state string) string {
	if _, exists := pa.QTable[state]; !exists {
		pa.QTable[state] = make(map[string]float64)
	}

	bestAction := Actions[0]
	bestQ := pa.QTable[state][bestAction]

	for _, action := range Actions[1:] {
		q := pa.QTable[state][action]
		if q > bestQ {
			bestQ = q
			bestAction = action
		}
	}

	return bestAction
}

// ComputeBid generates a bid based on current state and learned policy
func (pa *PodAgent) ComputeBid(config *AgentConfig) Bid {
	return pa.ComputeBidWithShadowPrice(config, 0.0)
}

// ComputeBidWithShadowPrice generates a bid with shadow price feedback (game theory)
func (pa *PodAgent) ComputeBidWithShadowPrice(config *AgentConfig, shadowPrice float64) Bid {
	pa.mu.Lock()
	defer pa.mu.Unlock()

	// During startup grace period, only allow increases
	inGracePeriod := time.Since(pa.StartTime) < config.StartupGracePeriod

	state := pa.stateInternal()
	
	// IMPROVEMENT #5: Use shadow price to adjust action selection (Game Theory + ABM)
	action := pa.selectActionInternalWithPrice(state, shadowPrice)

	pa.PrevState = state
	pa.PrevAction = action

	demandMultiplier := 1.0
	weightMultiplier := 1.0

	switch action {
	case "aggressive":
		demandMultiplier = 1.5
		weightMultiplier = 1.2
	case "normal":
		demandMultiplier = 1.2
		weightMultiplier = 1.0
	case "conservative":
		demandMultiplier = 1.0
		weightMultiplier = 0.8
	}

	baseDemand := pa.Usage
	if baseDemand < config.AbsoluteMinAllocation {
		baseDemand = config.AbsoluteMinAllocation
	}

	demand := int64(float64(baseDemand) * demandMultiplier)

	// IMPROVEMENT #1: Use shadow price to dampen demand when resources are scarce (Game Theory)
	if shadowPrice > 0.3 {
		reductionFactor := 1.0 - (shadowPrice * 0.5) // Max 50% reduction
		if reductionFactor < 0.5 {
			reductionFactor = 0.5 // Minimum 50% of original demand
		}
		demand = int64(float64(demand) * reductionFactor)
	}

	// IMPROVEMENT #2: Use throttling trend instead of current value (ABM pattern learning)
	effectiveThrottling := pa.Throttling
	if len(pa.ThrottlingHistory) >= 3 {
		// Use average of last 3 samples to smooth out spikes
		avgThrottling := 0.0
		for _, t := range pa.ThrottlingHistory[len(pa.ThrottlingHistory)-3:] {
			avgThrottling += t
		}
		avgThrottling /= 3.0
		effectiveThrottling = avgThrottling
		// Only react if trend is increasing (last > first)
		if len(pa.ThrottlingHistory) >= 2 {
			trend := pa.ThrottlingHistory[len(pa.ThrottlingHistory)-1] - pa.ThrottlingHistory[len(pa.ThrottlingHistory)-3]
			if trend < 0 {
				// Decreasing trend, be more conservative
				effectiveThrottling *= 0.7
			}
		}
	}

	if effectiveThrottling > 0.05 {
		// CRITICAL FIX: Cap throttling amplification to prevent runaway growth
		throttlingMultiplier := 1.0 + effectiveThrottling*2.0
		if throttlingMultiplier > 3.0 {
			throttlingMultiplier = 3.0
		}
		demand = int64(float64(demand) * throttlingMultiplier)
		// Additional cap: never exceed 10x base usage
		maxDemand := baseDemand * 10
		if demand > maxDemand {
			demand = maxDemand
		}
	}

	minBid := int64(float64(pa.Usage) * (1.0 + config.NeedHeadroomFactor))
	if minBid < config.AbsoluteMinAllocation {
		minBid = config.AbsoluteMinAllocation
	}

	// During grace period, min cannot be below current allocation
	if inGracePeriod && minBid < pa.Allocation {
		minBid = pa.Allocation
	}

	var maxBid int64
	if pa.Throttling > 0.05 {
		// CRITICAL FIX: Cap maxBid to prevent runaway growth
		usageBasedMax := int64(float64(pa.Usage) * 10.0)
		absoluteMax := int64(10000) // 10 cores = 10000m
		maxBid = usageBasedMax
		if maxBid > absoluteMax {
			maxBid = absoluteMax
		}
		if maxBid < minBid+100 {
			maxBid = minBid + 100
		}
	} else if pa.Usage < config.AbsoluteMinAllocation*2 {
		maxBid = int64(float64(pa.Usage) * (1.0 + config.WantHeadroomFactor))
		if maxBid < minBid+10 {
			maxBid = minBid + 10
		}
	} else {
		maxBid = int64(float64(demand) * (1.0 + config.WantHeadroomFactor))
		// Cap maxBid to prevent excessive allocation
		absoluteMax := int64(10000) // 10 cores
		if maxBid > absoluteMax {
			maxBid = absoluteMax
		}
	}

	return Bid{
		UID:    pa.UID,
		Demand: demand,
		Weight: weightMultiplier,
		Min:    minBid,
		Max:    maxBid,
	}
}

// ComputeBidWithEfficiency generates a bid with cost efficiency mode enabled
func (pa *PodAgent) ComputeBidWithEfficiency(config *AgentConfig) Bid {
	return pa.ComputeBidWithEfficiencyAndPrice(config, 0.0)
}

// ComputeBidWithEfficiencyAndPrice generates a bid with cost efficiency and shadow price
func (pa *PodAgent) ComputeBidWithEfficiencyAndPrice(config *AgentConfig, shadowPrice float64) Bid {
	pa.mu.Lock()
	defer pa.mu.Unlock()

	rawDemand := pa.computeRawDemandWithPrice(config, shadowPrice)

	if config.CostEfficiencyMode {
		// Asymmetric smoothing: fast down, slow up
		if rawDemand < pa.SmoothedDemand {
			// Going down: fast (high alpha)
			pa.SmoothedDemand = int64(config.AlphaDown*float64(rawDemand) + 
				(1-config.AlphaDown)*float64(pa.SmoothedDemand))
		} else {
			// Going up: slow (low alpha)
			pa.SmoothedDemand = int64(config.AlphaUp*float64(rawDemand) + 
				(1-config.AlphaUp)*float64(pa.SmoothedDemand))
		}

		// Idle decay: if usage is very low, decay allocation
		if pa.Usage < config.AbsoluteMinAllocation && pa.Throttling < config.TargetThrottling {
			pa.SmoothedDemand = int64(float64(pa.SmoothedDemand) * (1.0 - config.IdleDecayRate))
		}

		// Target throttling: allow some throttling before increasing
		if pa.Throttling < config.TargetThrottling {
			// Below target, no urgency to increase
			rawDemand = pa.SmoothedDemand
		}
	} else {
		pa.SmoothedDemand = rawDemand
	}

	return pa.buildBid(rawDemand, config)
}

func (pa *PodAgent) computeRawDemand(config *AgentConfig) int64 {
	return pa.computeRawDemandWithPrice(config, 0.0)
}

func (pa *PodAgent) computeRawDemandWithPrice(config *AgentConfig, shadowPrice float64) int64 {
	state := pa.stateInternal()
	action := pa.selectActionInternalWithPrice(state, shadowPrice)
	pa.PrevState = state
	pa.PrevAction = action

	multiplier := map[string]float64{
		"aggressive":   1.5,
		"normal":       1.2,
		"conservative": 1.0,
	}[action]

	baseDemand := pa.Usage
	if baseDemand < config.AbsoluteMinAllocation {
		baseDemand = config.AbsoluteMinAllocation
	}

	demand := int64(float64(baseDemand) * multiplier)
	
	// IMPROVEMENT #1: Use shadow price to dampen demand (Game Theory)
	if shadowPrice > 0.3 {
		reductionFactor := 1.0 - (shadowPrice * 0.5)
		if reductionFactor < 0.5 {
			reductionFactor = 0.5
		}
		demand = int64(float64(demand) * reductionFactor)
	}

	// IMPROVEMENT #2: Use throttling trend (ABM)
	effectiveThrottling := pa.Throttling
	if len(pa.ThrottlingHistory) >= 3 {
		avgThrottling := 0.0
		for _, t := range pa.ThrottlingHistory[len(pa.ThrottlingHistory)-3:] {
			avgThrottling += t
		}
		avgThrottling /= 3.0
		effectiveThrottling = avgThrottling
		if len(pa.ThrottlingHistory) >= 2 {
			trend := pa.ThrottlingHistory[len(pa.ThrottlingHistory)-1] - pa.ThrottlingHistory[len(pa.ThrottlingHistory)-3]
			if trend < 0 {
				effectiveThrottling *= 0.7
			}
		}
	}

	if effectiveThrottling > 0.05 {
		// CRITICAL FIX: Cap throttling amplification to prevent runaway growth
		// Max amplification: 3x (when throttling = 1.0)
		throttlingMultiplier := 1.0 + effectiveThrottling*2.0
		if throttlingMultiplier > 3.0 {
			throttlingMultiplier = 3.0
		}
		demand = int64(float64(demand) * throttlingMultiplier)
		// Additional cap: never exceed 10x base usage
		maxDemand := baseDemand * 10
		if demand > maxDemand {
			demand = maxDemand
		}
	}

	return demand
}

func (pa *PodAgent) buildBid(demand int64, config *AgentConfig) Bid {
	inGracePeriod := time.Since(pa.StartTime) < config.StartupGracePeriod

	weightMultiplier := 1.0
	if pa.Throttling > 0.05 {
		weightMultiplier = 1.2
	}

	minBid := int64(float64(pa.Usage) * (1.0 + config.NeedHeadroomFactor))
	if minBid < config.AbsoluteMinAllocation {
		minBid = config.AbsoluteMinAllocation
	}

	// During grace period, min cannot be below current allocation
	if inGracePeriod && minBid < pa.Allocation {
		minBid = pa.Allocation
	}

	var maxBid int64
	if pa.Throttling > 0.05 {
		maxBid = 1000000
	} else if pa.Usage < config.AbsoluteMinAllocation*2 {
		maxBid = int64(float64(pa.Usage) * (1.0 + config.WantHeadroomFactor))
		if maxBid < minBid+10 {
			maxBid = minBid + 10
		}
	} else {
		maxBid = int64(float64(demand) * (1.0 + config.WantHeadroomFactor))
	}

	return Bid{
		UID:    pa.UID,
		Demand: demand,
		Weight: weightMultiplier,
		Min:    minBid,
		Max:    maxBid,
	}
}

// Update learns from the outcome of the previous bid
func (pa *PodAgent) Update(newAllocation int64, newThrottling float64, sloViolation bool) {
	pa.mu.Lock()
	defer pa.mu.Unlock()

	// IMPROVEMENT #2: Track throttling history for trend analysis
	pa.ThrottlingHistory = append(pa.ThrottlingHistory, newThrottling)
	if len(pa.ThrottlingHistory) > 3 {
		pa.ThrottlingHistory = pa.ThrottlingHistory[len(pa.ThrottlingHistory)-3:]
	}

	// Update state
	pa.PrevAllocation = pa.Allocation
	pa.Allocation = newAllocation
	pa.Throttling = newThrottling

	// Compute reward
	reward := pa.computeReward(newAllocation, newThrottling, sloViolation)

	// Q-learning update: Q(s,a) = Q(s,a) + α[r + γ·max Q(s',a') - Q(s,a)]
	if pa.PrevState != "" && pa.PrevAction != "" {
		currentState := pa.stateInternal()

		// Initialize Q-table entries if needed
		if _, exists := pa.QTable[pa.PrevState]; !exists {
			pa.QTable[pa.PrevState] = make(map[string]float64)
		}
		if _, exists := pa.QTable[currentState]; !exists {
			pa.QTable[currentState] = make(map[string]float64)
		}

		// Get current Q-value
		currentQ := pa.QTable[pa.PrevState][pa.PrevAction]

		// Get max Q-value for next state
		maxNextQ := 0.0
		for _, action := range Actions {
			q := pa.QTable[currentState][action]
			if q > maxNextQ {
				maxNextQ = q
			}
		}

		// Update Q-value
		newQ := currentQ + pa.Alpha*(reward+pa.Gamma*maxNextQ-currentQ)
		pa.QTable[pa.PrevState][pa.PrevAction] = newQ
	}

	// Decay exploration over time (anneal)
	pa.Epsilon *= 0.999
	if pa.Epsilon < 0.01 {
		pa.Epsilon = 0.01 // Minimum exploration
	}
}

// computeReward calculates reward based on allocation outcome
func (pa *PodAgent) computeReward(allocation int64, throttling float64, sloViolation bool) float64 {
	reward := 0.0

	// Positive reward for meeting demand
	if allocation >= pa.Usage {
		reward += 10.0
	} else {
		// Penalty for under-allocation
		shortfall := float64(pa.Usage-allocation) / float64(pa.Usage)
		reward -= shortfall * 20.0
	}

	// Penalty for throttling (indicates insufficient CPU)
	reward -= throttling * 30.0

	// Heavy penalty for SLO violations
	if sloViolation {
		reward -= 100.0
	}

	// Small penalty for over-requesting (encourage efficiency)
	if allocation > pa.Usage*2 {
		waste := float64(allocation-pa.Usage*2) / float64(pa.Usage)
		reward -= waste * 5.0
	}

	// Bonus for zero throttling (optimal state)
	if throttling < 0.01 {
		reward += 5.0
	}

	// IMPROVEMENT #3: Penalize oscillations in Q-learning reward (ABM)
	if pa.PrevAllocation > 0 {
		changeRatio := math.Abs(float64(allocation-pa.PrevAllocation)) / float64(pa.PrevAllocation)
		if changeRatio > 0.2 {
			// Large allocation change (>20%) - penalize oscillation
			oscillationPenalty := (changeRatio - 0.2) * 10.0 // Penalty increases with change size
			reward -= oscillationPenalty
		} else if changeRatio < 0.05 {
			// Small change (<5%) - bonus for stability
			reward += 2.0
		}
	}

	return reward
}

// UpdateUsage updates the agent's observed usage
func (pa *PodAgent) UpdateUsage(usage int64) {
	pa.mu.Lock()
	defer pa.mu.Unlock()
	pa.Usage = usage
}

// GetQValue returns the Q-value for a state-action pair (for debugging)
func (pa *PodAgent) GetQValue(state, action string) float64 {
	pa.mu.RLock()
	defer pa.mu.RUnlock()

	if stateMap, exists := pa.QTable[state]; exists {
		return stateMap[action]
	}
	return 0.0
}

// GetExplorationRate returns current exploration rate
func (pa *PodAgent) GetExplorationRate() float64 {
	pa.mu.RLock()
	defer pa.mu.RUnlock()
	return pa.Epsilon
}

func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
