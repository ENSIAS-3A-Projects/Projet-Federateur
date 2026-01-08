package stability

import (
	"math"
	"testing"

	"k8s.io/apimachinery/pkg/types"

	mbcastypes "mbcas/pkg/types"
)

func TestBoundedUpdateWithCongestion(t *testing.T) {
	// Use initial step size >= min to avoid clamping issues
	lc := NewLyapunovController(0.5, 0.2, 1.0)

	// Test with no congestion (factor = 0.0)
	result := lc.BoundedUpdateWithCongestion(1000, 2000, 0.0)
	// Should use smaller step (0.5x of base step = 0.25, clamped to min 0.2)
	if result <= 1000 || result >= 2000 {
		t.Errorf("Expected result between current and desired, got %d", result)
	}

	// Test with high congestion (factor = 1.0)
	result2 := lc.BoundedUpdateWithCongestion(1000, 2000, 1.0)
	// Should use larger step (1.0x of base step = 0.5)
	// High congestion should result in larger change
	if result2 < result {
		t.Errorf("High congestion should use larger step, got %d vs %d", result2, result)
	}
}

func TestComputeCongestionFactor(t *testing.T) {
	allocations := map[types.UID]int64{
		types.UID("pod1"): 500,
		types.UID("pod2"): 300,
	}

	params := map[types.UID]mbcastypes.AllocationParams{
		types.UID("pod1"): {
			Baseline: 200,
		},
		types.UID("pod2"): {
			Baseline: 200,
		},
	}

	factor := ComputeCongestionFactor(allocations, params)
	if factor < 0.0 || factor > 1.0 {
		t.Errorf("Congestion factor should be in [0, 1], got %f", factor)
	}

	// With allocations above baseline, should have positive congestion
	if factor <= 0.0 {
		t.Error("Expected positive congestion factor with allocations above baseline")
	}
}

func TestComputeCongestionFactor_NoCongestion(t *testing.T) {
	allocations := map[types.UID]int64{
		types.UID("pod1"): 200,
		types.UID("pod2"): 200,
	}

	params := map[types.UID]mbcastypes.AllocationParams{
		types.UID("pod1"): {
			Baseline: 200,
		},
		types.UID("pod2"): {
			Baseline: 200,
		},
	}

	factor := ComputeCongestionFactor(allocations, params)
	// All at baseline, should have low congestion
	if factor < 0.0 || factor > 1.0 {
		t.Errorf("Congestion factor should be in [0, 1], got %f", factor)
	}
}

func TestBoundedUpdate_BackwardCompatibility(t *testing.T) {
	lc := NewLyapunovController(0.1, 0.2, 1.0)

	// BoundedUpdate should work (calls BoundedUpdateWithCongestion with factor 1.0)
	result1 := lc.BoundedUpdate(1000, 2000)
	result2 := lc.BoundedUpdateWithCongestion(1000, 2000, 1.0)

	// Should give same result (congestion factor 1.0 = no scaling)
	if math.Abs(float64(result1-result2)) > 1 {
		t.Errorf("BoundedUpdate should match BoundedUpdateWithCongestion(..., 1.0), got %d vs %d", result1, result2)
	}
}

