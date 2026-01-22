package agent

import (
	"testing"
)

func TestPodAgent_StateEncoding(t *testing.T) {
	agent := NewPodAgent("test-pod", 100.0)

	// Test low usage, no throttling
	agent.Usage = 200
	agent.Throttling = 0.0
	agent.Allocation = 300

	state := agent.State()
	expected := "low:none:adequate"
	if state != expected {
		t.Errorf("Expected state %s, got %s", expected, state)
	}

	// Test high usage, high throttling
	agent.Usage = 1500
	agent.Throttling = 0.5
	agent.Allocation = 1000

	state = agent.State()
	expected = "high:high:low"
	if state != expected {
		t.Errorf("Expected state %s, got %s", expected, state)
	}
}

func TestPodAgent_BidComputation(t *testing.T) {
	agent := NewPodAgent("test-pod", 100.0)
	agent.Usage = 500
	agent.Throttling = 0.0
	agent.Allocation = 500
	agent.Epsilon = 0.0 // Disable exploration for deterministic test

	conf := DefaultConfig()
	bid := agent.ComputeBid(conf)

	// Check bid structure
	if bid.UID != "test-pod" {
		t.Errorf("Expected UID test-pod, got %s", bid.UID)
	}

	// Default action is "aggressive" (first in list) -> 1.5 multiplier
	// 500 * 1.5 = 750
	if bid.Demand != 750 {
		t.Errorf("Expected demand 750 (aggressive), got %d", bid.Demand)
	}

	// Min should be usage + NeedHeadroomFactor (15%)
	// 500 * 1.15 = 575
	expectedMin := int64(500 * 1.15)
	if bid.Min != expectedMin {
		t.Errorf("Expected min %d, got %d", expectedMin, bid.Min)
	}

	// Weight should be positive
	if bid.Weight <= 0 {
		t.Errorf("Weight should be positive, got %f", bid.Weight)
	}
}

func TestPodAgent_BidWithThrottling(t *testing.T) {
	agent := NewPodAgent("test-pod", 100.0)
	agent.Usage = 500
	agent.Throttling = 0.3 // Significant throttling
	agent.Allocation = 400
	agent.Epsilon = 0.0 // Disable exploration

	conf := DefaultConfig()
	bid := agent.ComputeBid(conf)

	// With throttling, demand should be higher
	// Base: 500
	// Action: Aggressive (1.5x)
	// Throttling Boost: (1 + 0.3*2) = 1.6
	// 500 * 1.5 * 1.6 = 1200
	if bid.Demand != 1200 {
		t.Errorf("Expected demand 1200 with throttling, got %d", bid.Demand)
	}

	// Max should be capped (10x usage or 10 cores max)
	// With usage=500, throttling=0.3: demand = 500 * 1.5 * (1 + 0.3*2) = 500 * 1.5 * 1.6 = 1200
	// Max should be capped at 10x usage (5000) or 10 cores (10000), whichever is lower
	expectedMax := int64(5000) // 10x usage
	if bid.Max != expectedMax {
		t.Errorf("Expected max %d for throttled pod (capped at 10x usage), got %d", expectedMax, bid.Max)
	}
}

func TestPodAgent_Learning(t *testing.T) {
	agent := NewPodAgent("test-pod", 100.0)
	agent.Usage = 500
	agent.Throttling = 0.2
	agent.Allocation = 400

	// Generate initial bid (stores state/action)
	conf := DefaultConfig()
	_ = agent.ComputeBid(conf)

	// Simulate outcome: got good allocation, no throttling
	agent.Update(600, 0.0, false)

	// Q-table should have been updated
	if len(agent.QTable) == 0 {
		t.Error("Q-table should have entries after learning")
	}

	// Exploration rate should decay
	initialEpsilon := 0.2
	if agent.Epsilon >= initialEpsilon {
		t.Error("Exploration rate should decay after update")
	}
}

func TestPodAgent_RewardComputation(t *testing.T) {
	agent := NewPodAgent("test-pod", 100.0)
	agent.Usage = 500

	// Good outcome: allocation meets demand, no throttling
	reward := agent.computeReward(600, 0.0, false)
	if reward <= 0 {
		t.Errorf("Good outcome should have positive reward, got %f", reward)
	}

	// Bad outcome: under-allocated, high throttling
	reward = agent.computeReward(300, 0.5, false)
	if reward >= 0 {
		t.Errorf("Bad outcome should have negative reward, got %f", reward)
	}

	// Terrible outcome: SLO violation
	reward = agent.computeReward(300, 0.5, true)
	if reward >= -50 {
		t.Errorf("SLO violation should have very negative reward, got %f", reward)
	}
}

func TestPodAgent_ExplorationDecay(t *testing.T) {
	agent := NewPodAgent("test-pod", 100.0)
	initialEpsilon := agent.Epsilon

	// Simulate many updates
	for i := 0; i < 1000; i++ {
		agent.Update(500, 0.0, false)
	}

	// Epsilon should have decayed significantly
	if agent.Epsilon >= initialEpsilon*0.5 {
		t.Errorf("Epsilon should decay significantly, started at %f, now %f", initialEpsilon, agent.Epsilon)
	}

	// But not below minimum
	if agent.Epsilon < 0.01 {
		t.Errorf("Epsilon should not go below 0.01, got %f", agent.Epsilon)
	}
}

func TestPodAgent_ActionSelection(t *testing.T) {
	agent := NewPodAgent("test-pod", 100.0)
	agent.Epsilon = 0.0 // No exploration, pure exploitation

	// Initialize Q-values to prefer "aggressive"
	state := "medium:some:adequate"
	agent.QTable[state] = map[string]float64{
		"aggressive":   10.0,
		"normal":       5.0,
		"conservative": 2.0,
	}

	// Should always select "aggressive"
	for i := 0; i < 10; i++ {
		action := agent.SelectAction(state)
		if action != "aggressive" {
			t.Errorf("Should select aggressive (highest Q), got %s", action)
		}
	}
}

func TestPodAgent_ConcurrentAccess(t *testing.T) {
	agent := NewPodAgent("test-pod", 100.0)

	// Simulate concurrent access (should not panic)
	done := make(chan bool, 2)

	go func() {
		for i := 0; i < 100; i++ {
			conf := DefaultConfig()
			agent.ComputeBid(conf)
		}
		done <- true
	}()

	go func() {
		for i := 0; i < 100; i++ {
			agent.Update(500, 0.1, false)
		}
		done <- true
	}()

	<-done
	<-done
}

// Benchmark bid computation
func BenchmarkPodAgent_ComputeBid(b *testing.B) {
	agent := NewPodAgent("test-pod", 100.0)
	agent.Usage = 500
	agent.Throttling = 0.2
	agent.Allocation = 400

	b.ResetTimer()
	conf := DefaultConfig()
	for i := 0; i < b.N; i++ {
		agent.ComputeBid(conf)
	}
}

// Benchmark learning update
func BenchmarkPodAgent_Update(b *testing.B) {
	agent := NewPodAgent("test-pod", 100.0)
	agent.Usage = 500
	conf := DefaultConfig()
	agent.ComputeBid(conf) // Initialize state

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		agent.Update(600, 0.1, false)
	}
}
