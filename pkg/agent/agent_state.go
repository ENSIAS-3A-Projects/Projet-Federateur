package agent

import (
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

// DecisionOutcome records the result of an allocation decision.
type DecisionOutcome struct {
	Timestamp      time.Time
	Allocation     int64 // CPU allocation in millicores
	Demand         int64 // Requested demand
	ShadowPrice    float64
	Utility        float64
	SLOViolation   bool
	Throttling     float64 // Throttling ratio [0,1]
	Strategy       string  // Strategy used: "aggressive", "conservative", "cooperative"
}

// PodAgentState represents the state of an autonomous pod agent.
// Each pod maintains its own state for learning and adaptation.
type PodAgentState struct {
	UID types.UID
	mu  sync.RWMutex

	// Memory: Last N decisions and outcomes
	History []DecisionOutcome
	maxHistorySize int

	// Strategy parameters (evolve over time)
	Aggressiveness  float64 // [0,1] - bidding aggressiveness
	CooperationLevel float64 // [0,1] - willingness to share

	// Performance tracking
	SLOViolations    int
	ThrottlingEvents int
	AvgUtility       float64
	TotalDecisions   int

	// Learning state
	QValues map[string]float64 // State-action values for Q-learning
}

// NewPodAgentState creates a new agent state for a pod.
func NewPodAgentState(uid types.UID, maxHistorySize int) *PodAgentState {
	return &PodAgentState{
		UID:             uid,
		maxHistorySize:  maxHistorySize,
		History:         make([]DecisionOutcome, 0, maxHistorySize),
		Aggressiveness:  0.5, // Start with moderate aggressiveness
		CooperationLevel: 0.5, // Start with moderate cooperation
		QValues:         make(map[string]float64),
	}
}

// RecordOutcome records a decision outcome in the agent's history.
func (s *PodAgentState) RecordOutcome(outcome DecisionOutcome) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Add to history
	s.History = append(s.History, outcome)
	if len(s.History) > s.maxHistorySize {
		// Remove oldest entry
		s.History = s.History[1:]
	}

	// Update performance metrics
	s.TotalDecisions++
	if outcome.SLOViolation {
		s.SLOViolations++
	}
	if outcome.Throttling > 0.1 {
		s.ThrottlingEvents++
	}

	// Update average utility (exponential moving average)
	if s.TotalDecisions == 1 {
		s.AvgUtility = outcome.Utility
	} else {
		alpha := 0.1 // Smoothing factor
		s.AvgUtility = alpha*outcome.Utility + (1-alpha)*s.AvgUtility
	}
}

// GetRecentOutcomes returns the last N outcomes.
func (s *PodAgentState) GetRecentOutcomes(n int) []DecisionOutcome {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if n > len(s.History) {
		n = len(s.History)
	}
	if n == 0 {
		return nil
	}

	start := len(s.History) - n
	return s.History[start:]
}

// GetAggressiveness returns the current aggressiveness level.
func (s *PodAgentState) GetAggressiveness() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Aggressiveness
}

// GetCooperationLevel returns the current cooperation level.
func (s *PodAgentState) GetCooperationLevel() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.CooperationLevel
}

// SetAggressiveness sets the aggressiveness level (clamped to [0,1]).
func (s *PodAgentState) SetAggressiveness(value float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value < 0 {
		value = 0
	}
	if value > 1 {
		value = 1
	}
	s.Aggressiveness = value
}

// SetCooperationLevel sets the cooperation level (clamped to [0,1]).
func (s *PodAgentState) SetCooperationLevel(value float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value < 0 {
		value = 0
	}
	if value > 1 {
		value = 1
	}
	s.CooperationLevel = value
}

// GetQValue returns the Q-value for a state-action pair.
func (s *PodAgentState) GetQValue(state, action string) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key := state + ":" + action
	if val, ok := s.QValues[key]; ok {
		return val
	}
	return 0.0 // Default Q-value
}

// SetQValue sets the Q-value for a state-action pair.
func (s *PodAgentState) SetQValue(state, action string, value float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := state + ":" + action
	s.QValues[key] = value
}

// GetMaxQValue returns the maximum Q-value for a given state across all actions.
func (s *PodAgentState) GetMaxQValue(state string) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	maxQ := 0.0
	actions := []string{"aggressive", "conservative", "cooperative"}
	for _, action := range actions {
		key := state + ":" + action
		if val, ok := s.QValues[key]; ok && val > maxQ {
			maxQ = val
		}
	}
	return maxQ
}

// GetPerformanceStats returns current performance statistics.
func (s *PodAgentState) GetPerformanceStats() (avgUtility float64, sloViolationRate float64, throttlingRate float64) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	avgUtility = s.AvgUtility
	if s.TotalDecisions > 0 {
		sloViolationRate = float64(s.SLOViolations) / float64(s.TotalDecisions)
		throttlingRate = float64(s.ThrottlingEvents) / float64(s.TotalDecisions)
	}
	return
}

// CleanupOldQValues removes old Q-values to prevent unbounded growth.
// Keeps only the most recently used entries (simplified: clears if over limit).
func (s *PodAgentState) CleanupOldQValues(maxEntries int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.QValues) <= maxEntries {
		return
	}

	// Simplified cleanup: clear all if over limit
	// In a production system, you might track access times and keep LRU entries
	s.QValues = make(map[string]float64)
}




