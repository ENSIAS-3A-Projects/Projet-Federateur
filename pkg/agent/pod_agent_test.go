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

	bid := agent.ComputeBid()

	// Check bid structure
	if bid.UID != "test-pod" {
		t.Errorf("Expected UID test-pod, got %s", bid.UID)
	}

	// Demand should be based on usage with multiplier
	if bid.Demand < 500 {
		t.Errorf("Demand should be at least usage (500), got %d", bid.Demand)
	}

	// Min should be baseline
	if bid.Min != 100 {
		t.Errorf("Expected min 100, got %d", bid.Min)
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

	bid := agent.ComputeBid()

	// With throttling, demand should be higher
	// Base: 500, multiplier: ~1.2-1.5, throttling: +30%
	if bid.Demand < 600 {
		t.Errorf("Demand with throttling should be >600, got %d", bid.Demand)
	}
}

func TestPodAgent_Learning(t *testing.T) {
	agent := NewPodAgent("test-pod", 100.0)
	agent.Usage = 500
	agent.Throttling = 0.2
	agent.Allocation = 400

	// Generate initial bid (stores state/action)
	_ = agent.ComputeBid()

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
			agent.ComputeBid()
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
	for i := 0; i < b.N; i++ {
		agent.ComputeBid()
	}
}

// Benchmark learning update
func BenchmarkPodAgent_Update(b *testing.B) {
	agent := NewPodAgent("test-pod", 100.0)
	agent.Usage = 500
	agent.ComputeBid() // Initialize state

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		agent.Update(600, 0.1, false)
	}
}
