package allocation

import (
	"testing"

	"k8s.io/apimachinery/pkg/types"
)

func TestNashBargain_Uncongested(t *testing.T) {
	// Scenario: 2 pods, plenty of capacity
	bids := []Bid{
		{UID: "pod1", Demand: 500, Weight: 1.0, Min: 100, Max: 1000},
		{UID: "pod2", Demand: 300, Weight: 1.0, Min: 100, Max: 1000},
	}
	capacity := int64(2000) // More than enough

	result := AllocateWithMode(capacity, bids)

	// Should be uncongested - everyone gets what they want
	if result.Mode != ModeUncongested {
		t.Errorf("Expected uncongested mode, got %s", result.Mode)
	}

	// Check allocations are reasonable
	if result.Allocations["pod1"] < 500 {
		t.Errorf("Pod1 should get at least demand (500), got %d", result.Allocations["pod1"])
	}
	if result.Allocations["pod2"] < 300 {
		t.Errorf("Pod2 should get at least demand (300), got %d", result.Allocations["pod2"])
	}
}

func TestNashBargain_Congested(t *testing.T) {
	// Scenario: 2 pods, limited capacity
	bids := []Bid{
		{UID: "pod1", Demand: 800, Weight: 1.0, Min: 100, Max: 1000},
		{UID: "pod2", Demand: 800, Weight: 1.0, Min: 100, Max: 1000},
	}
	capacity := int64(1000) // Not enough for both demands

	result := AllocateWithMode(capacity, bids)

	// Should be congested - Nash bargaining applied
	if result.Mode != ModeCongested {
		t.Errorf("Expected congested mode, got %s", result.Mode)
	}

	// Check fairness: equal weights should get equal surplus
	alloc1 := result.Allocations["pod1"]
	alloc2 := result.Allocations["pod2"]

	surplus1 := alloc1 - 100 // Surplus above baseline
	surplus2 := alloc2 - 100

	// Should be approximately equal (within 10%)
	diff := float64(surplus1-surplus2) / float64(surplus1)
	if diff < -0.1 || diff > 0.1 {
		t.Errorf("Unequal surplus distribution: pod1=%d, pod2=%d", surplus1, surplus2)
	}

	// Total should not exceed capacity
	total := alloc1 + alloc2
	if total > capacity {
		t.Errorf("Total allocation %d exceeds capacity %d", total, capacity)
	}
}

func TestNashBargain_Overloaded(t *testing.T) {
	// Scenario: Baselines don't even fit
	bids := []Bid{
		{UID: "pod1", Demand: 800, Weight: 1.0, Min: 600, Max: 1000},
		{UID: "pod2", Demand: 800, Weight: 1.0, Min: 600, Max: 1000},
	}
	capacity := int64(1000) // Less than total baselines (1200)

	result := AllocateWithMode(capacity, bids)

	// Should be overloaded - scale baselines
	if result.Mode != ModeOverloaded {
		t.Errorf("Expected overloaded mode, got %s", result.Mode)
	}

	// Everyone should get at least something
	if result.Allocations["pod1"] < 10 {
		t.Error("Pod1 should get at least minimum allocation")
	}
	if result.Allocations["pod2"] < 10 {
		t.Error("Pod2 should get at least minimum allocation")
	}

	// Total should not exceed capacity
	total := result.Allocations["pod1"] + result.Allocations["pod2"]
	if total > capacity {
		t.Errorf("Total allocation %d exceeds capacity %d", total, capacity)
	}
}

func TestNashBargain_UnequalWeights(t *testing.T) {
	// Scenario: Different weights (priorities)
	bids := []Bid{
		{UID: "pod1", Demand: 800, Weight: 2.0, Min: 100, Max: 1000}, // Higher priority
		{UID: "pod2", Demand: 800, Weight: 1.0, Min: 100, Max: 1000}, // Lower priority
	}
	capacity := int64(1000)

	result := NashBargain(capacity, bids)

	// Pod1 should get more surplus than pod2
	surplus1 := result["pod1"] - 100
	surplus2 := result["pod2"] - 100

	if surplus1 <= surplus2 {
		t.Errorf("Higher weight pod should get more surplus: pod1=%d, pod2=%d", surplus1, surplus2)
	}

	// Ratio should be approximately 2:1
	ratio := float64(surplus1) / float64(surplus2)
	if ratio < 1.5 || ratio > 2.5 {
		t.Errorf("Surplus ratio should be ~2:1, got %.2f:1", ratio)
	}
}

func TestNashBargain_CappedAgent(t *testing.T) {
	// Scenario: One pod hits max limit
	bids := []Bid{
		{UID: "pod1", Demand: 800, Weight: 1.0, Min: 100, Max: 400}, // Capped
		{UID: "pod2", Demand: 800, Weight: 1.0, Min: 100, Max: 1000},
	}
	capacity := int64(1000)

	result := NashBargain(capacity, bids)

	// Pod1 should be at max
	if result["pod1"] != 400 {
		t.Errorf("Pod1 should be capped at max (400), got %d", result["pod1"])
	}

	// Pod2 should get the remaining capacity
	expected := capacity - 400
	if result["pod2"] < expected-10 || result["pod2"] > expected+10 {
		t.Errorf("Pod2 should get remaining capacity (~%d), got %d", expected, result["pod2"])
	}
}

func TestNashBargain_EmptyBids(t *testing.T) {
	bids := []Bid{}
	capacity := int64(1000)

	result := NashBargain(capacity, bids)

	if len(result) != 0 {
		t.Error("Empty bids should return empty allocations")
	}
}

func TestNashBargain_SinglePod(t *testing.T) {
	bids := []Bid{
		{UID: "pod1", Demand: 500, Weight: 1.0, Min: 100, Max: 1000},
	}
	capacity := int64(1000)

	result := NashBargain(capacity, bids)

	// Single pod should get min(demand, capacity, max)
	if result["pod1"] < 500 {
		t.Errorf("Single pod should get at least demand, got %d", result["pod1"])
	}
	if result["pod1"] > capacity {
		t.Errorf("Single pod should not exceed capacity, got %d", result["pod1"])
	}
}

// Benchmark Nash Bargaining performance
func BenchmarkNashBargain_10Pods(b *testing.B) {
	bids := make([]Bid, 10)
	for i := 0; i < 10; i++ {
		bids[i] = Bid{
			UID:    types.UID("pod" + string(rune(i))),
			Demand: 500,
			Weight: 1.0,
			Min:    100,
			Max:    1000,
		}
	}
	capacity := int64(5000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		NashBargain(capacity, bids)
	}
}

func BenchmarkNashBargain_50Pods(b *testing.B) {
	bids := make([]Bid, 50)
	for i := 0; i < 50; i++ {
		bids[i] = Bid{
			UID:    types.UID("pod" + string(rune(i))),
			Demand: 500,
			Weight: 1.0,
			Min:    100,
			Max:    1000,
		}
	}
	capacity := int64(25000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		NashBargain(capacity, bids)
	}
}

func BenchmarkNashBargain_100Pods(b *testing.B) {
	bids := make([]Bid, 100)
	for i := 0; i < 100; i++ {
		bids[i] = Bid{
			UID:    types.UID("pod" + string(rune(i))),
			Demand: 500,
			Weight: 1.0,
			Min:    100,
			Max:    1000,
		}
	}
	capacity := int64(50000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		NashBargain(capacity, bids)
	}
}
