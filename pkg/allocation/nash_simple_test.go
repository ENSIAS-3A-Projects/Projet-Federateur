package allocation

import (
	"testing"

	"k8s.io/apimachinery/pkg/types"
)

func TestNashBargain_MultipleCappedAgents(t *testing.T) {
	bids := []Bid{
		{UID: types.UID("pod1"), Demand: 1000, Weight: 1.0, Min: 100, Max: 300},
		{UID: types.UID("pod2"), Demand: 1000, Weight: 1.0, Min: 100, Max: 300},
		{UID: types.UID("pod3"), Demand: 1000, Weight: 1.0, Min: 100, Max: 1000},
	}
	capacity := int64(1000)

	result := NashBargain(capacity, bids)

	if result[types.UID("pod1")] != 300 {
		t.Errorf("pod1 should be capped at 300, got %d", result[types.UID("pod1")])
	}
	if result[types.UID("pod2")] != 300 {
		t.Errorf("pod2 should be capped at 300, got %d", result[types.UID("pod2")])
	}

	expectedPod3 := capacity - 300 - 300
	if result[types.UID("pod3")] != expectedPod3 {
		t.Errorf("pod3 should get remaining %d, got %d", expectedPod3, result[types.UID("pod3")])
	}

	total := result[types.UID("pod1")] + result[types.UID("pod2")] + result[types.UID("pod3")]
	if total != capacity {
		t.Errorf("Total allocation %d should equal capacity %d", total, capacity)
	}
}

func TestNashBargain_AllCapped(t *testing.T) {
	bids := []Bid{
		{UID: types.UID("pod1"), Demand: 1000, Weight: 1.0, Min: 100, Max: 200},
		{UID: types.UID("pod2"), Demand: 1000, Weight: 1.0, Min: 100, Max: 200},
		{UID: types.UID("pod3"), Demand: 1000, Weight: 1.0, Min: 100, Max: 200},
	}
	capacity := int64(1000)

	result := NashBargain(capacity, bids)

	for uid, alloc := range result {
		if alloc != 200 {
			t.Errorf("%s should be capped at 200, got %d", uid, alloc)
		}
	}

	total := result[types.UID("pod1")] + result[types.UID("pod2")] + result[types.UID("pod3")]
	if total != 600 {
		t.Errorf("Total should be 600 (all capped), got %d", total)
	}
}

func TestNashBargain_CapacityFullyUsed(t *testing.T) {
	bids := []Bid{
		{UID: types.UID("pod1"), Demand: 500, Weight: 1.0, Min: 100, Max: 10000},
		{UID: types.UID("pod2"), Demand: 500, Weight: 1.0, Min: 100, Max: 10000},
	}
	capacity := int64(1000)

	result := NashBargain(capacity, bids)

	total := result[types.UID("pod1")] + result[types.UID("pod2")]
	if total != capacity {
		t.Errorf("Total allocation %d should equal capacity %d", total, capacity)
	}
}
