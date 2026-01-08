package allocation

import (
	"testing"

	"k8s.io/apimachinery/pkg/types"
)

func TestNashBargainingSolution_Empty(t *testing.T) {
	result := NashBargainingSolution(1000, nil)
	if len(result) != 0 {
		t.Errorf("Expected empty result for nil agents, got %d entries", len(result))
	}
}

func TestNashBargainingSolution_SingleAgent(t *testing.T) {
	agents := []NashBargainingParams{
		{UID: types.UID("pod1"), Weight: 1.0, Baseline: 100, MaxAlloc: 500},
	}
	result := NashBargainingSolution(1000, agents)

	if result[types.UID("pod1")] != 500 {
		t.Errorf("Single agent should get MaxAlloc, got %d", result[types.UID("pod1")])
	}
}

func TestNashBargainingSolution_EqualWeights(t *testing.T) {
	agents := []NashBargainingParams{
		{UID: types.UID("pod1"), Weight: 1.0, Baseline: 100, MaxAlloc: 1000},
		{UID: types.UID("pod2"), Weight: 1.0, Baseline: 100, MaxAlloc: 1000},
	}
	result := NashBargainingSolution(600, agents)

	// With equal weights, surplus should be split equally
	// Total baseline = 200, surplus = 400, each should get 200 surplus
	// So each should get 300 (100 baseline + 200 surplus)
	if result[types.UID("pod1")] != result[types.UID("pod2")] {
		t.Errorf("Equal weights should give equal allocations: pod1=%d, pod2=%d",
			result[types.UID("pod1")], result[types.UID("pod2")])
	}
}

func TestNashBargainingSolution_WeightedDistribution(t *testing.T) {
	agents := []NashBargainingParams{
		{UID: types.UID("pod1"), Weight: 2.0, Baseline: 100, MaxAlloc: 1000},
		{UID: types.UID("pod2"), Weight: 1.0, Baseline: 100, MaxAlloc: 1000},
	}
	result := NashBargainingSolution(600, agents)

	// Higher weight should get more surplus
	if result[types.UID("pod1")] <= result[types.UID("pod2")] {
		t.Errorf("Higher weight should get more: pod1=%d, pod2=%d",
			result[types.UID("pod1")], result[types.UID("pod2")])
	}
}

func TestNashBargainingSolution_RespectsCaps(t *testing.T) {
	agents := []NashBargainingParams{
		{UID: types.UID("pod1"), Weight: 1.0, Baseline: 100, MaxAlloc: 200},
		{UID: types.UID("pod2"), Weight: 1.0, Baseline: 100, MaxAlloc: 1000},
	}
	result := NashBargainingSolution(1000, agents)

	// Pod1 should be at max, pod2 gets the rest
	if result[types.UID("pod1")] > 200 {
		t.Errorf("Pod1 should be capped at 200, got %d", result[types.UID("pod1")])
	}
}

func TestVerifyNashAxioms_IR(t *testing.T) {
	agents := []NashBargainingParams{
		{UID: types.UID("pod1"), Weight: 1.0, Baseline: 100, MaxAlloc: 500},
	}
	solution := map[types.UID]int64{types.UID("pod1"): 50} // Below baseline

	err := VerifyNashAxioms(solution, agents, 1000)
	if err == nil {
		t.Error("Expected IR violation error for allocation below baseline")
	}
}
