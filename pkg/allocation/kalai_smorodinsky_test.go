package allocation

import (
	"testing"

	"k8s.io/apimachinery/pkg/types"
)

func TestKalaiSmorodinskySolution_Basic(t *testing.T) {
	capacity := int64(1000) // 1 core
	agents := []KalaiSmorodinskyParams{
		{
			UID:      types.UID("pod1"),
			Weight:   1.0,
			Baseline: 200, // 200m
			Ideal:    500, // 500m
			MaxAlloc: 1000,
			Demand:   500,
		},
		{
			UID:      types.UID("pod2"),
			Weight:   1.0,
			Baseline: 200, // 200m
			Ideal:    500, // 500m
			MaxAlloc: 1000,
			Demand:   500,
		},
	}

	allocs := KalaiSmorodinskySolution(capacity, agents)

	// Both should get equal allocation (proportional to gain range)
	if allocs[types.UID("pod1")] == 0 || allocs[types.UID("pod2")] == 0 {
		t.Error("Allocations should be non-zero")
	}

	// Total should not exceed capacity
	total := allocs[types.UID("pod1")] + allocs[types.UID("pod2")]
	if total > capacity {
		t.Errorf("Total allocation %d exceeds capacity %d", total, capacity)
	}

	// Both should get at least baseline
	if allocs[types.UID("pod1")] < 200 || allocs[types.UID("pod2")] < 200 {
		t.Error("Allocations should be at least baseline")
	}
}

func TestKalaiSmorodinskySolution_Overloaded(t *testing.T) {
	capacity := int64(300) // Less than total baseline
	agents := []KalaiSmorodinskyParams{
		{
			UID:      types.UID("pod1"),
			Weight:   1.0,
			Baseline: 200,
			Ideal:    500,
			MaxAlloc: 1000,
			Demand:   500,
		},
		{
			UID:      types.UID("pod2"),
			Weight:   1.0,
			Baseline: 200,
			Ideal:    500,
			MaxAlloc: 1000,
			Demand:   500,
		},
	}

	allocs := KalaiSmorodinskySolution(capacity, agents)

	// Total should equal capacity (scaled baselines)
	total := allocs[types.UID("pod1")] + allocs[types.UID("pod2")]
	if total != capacity {
		t.Errorf("Total allocation %d should equal capacity %d in overloaded mode", total, capacity)
	}
}

func TestKalaiSmorodinskySolution_Empty(t *testing.T) {
	allocs := KalaiSmorodinskySolution(1000, []KalaiSmorodinskyParams{})
	if len(allocs) != 0 {
		t.Error("Empty agents should return empty allocations")
	}
}


