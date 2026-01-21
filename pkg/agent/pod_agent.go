package agent

import (
	"math/rand"
	"sync"

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
}

// Bid represents a pod's resource request to the Nash Bargaining solver
type Bid struct {
	UID    types.UID
	Demand int64   // Requested allocation (millicores)
	Weight float64 // Bargaining power (higher = more priority)
	Min    int64   // Minimum viable allocation (disagreement point)
	Max    int64   // Maximum useful allocation
}

// NewPodAgent creates a new agent for a pod
func NewPodAgent(uid types.UID, sloTarget float64) *PodAgent {
	return &PodAgent{
		UID:        uid,
		SLOTarget:  sloTarget,
		QTable:     make(map[string]map[string]float64),
		Alpha:      0.1, // 10% learning rate
		Gamma:      0.9, // Value future rewards
		Epsilon:    0.2, // 20% exploration
		Allocation: 100, // Start with baseline
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
	// Exploration: random action
	if rand.Float64() < pa.Epsilon {
		return Actions[rand.Intn(len(Actions))]
	}

	// Exploitation: best action based on Q-values
	return pa.bestAction(state)
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
func (pa *PodAgent) ComputeBid() Bid {
	pa.mu.Lock()
	defer pa.mu.Unlock()

	state := pa.stateInternal()
	action := pa.selectActionInternal(state)

	// Store for learning
	pa.PrevState = state
	pa.PrevAction = action

	// Action determines bid aggressiveness
	var demandMultiplier, weightMultiplier float64

	switch action {
	case "aggressive":
		// Request more, higher priority
		demandMultiplier = 1.5
		weightMultiplier = 1.2
	case "normal":
		// Balanced request
		demandMultiplier = 1.2
		weightMultiplier = 1.0
	case "conservative":
		// Request less, lower priority (save resources for others)
		demandMultiplier = 1.0
		weightMultiplier = 0.8
	}

	// Base demand on current usage + headroom
	baseDemand := pa.Usage
	if baseDemand < 100 {
		baseDemand = 100 // Minimum baseline
	}

	demand := int64(float64(baseDemand) * demandMultiplier)

	// If throttling, increase demand
	if pa.Throttling > 0.1 {
		demand = int64(float64(demand) * (1.0 + pa.Throttling))
	}

	return Bid{
		UID:    pa.UID,
		Demand: demand,
		Weight: weightMultiplier,
		Min:    100,                 // Minimum viable CPU
		Max:    max(demand*3, 2000), // Cap at 3x demand or 2 cores
	}
}

// Update learns from the outcome of the previous bid
func (pa *PodAgent) Update(newAllocation int64, newThrottling float64, sloViolation bool) {
	pa.mu.Lock()
	defer pa.mu.Unlock()

	// Update state
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
