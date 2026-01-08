package coalition

import (
	"math/rand"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

func TestComputeShapleyValue_Empty(t *testing.T) {
	game := &CoalitionGame{Players: []types.UID{}}
	result := ComputeShapleyValue(game, 100, nil)
	if len(result) != 0 {
		t.Errorf("Expected empty result for empty game, got %d entries", len(result))
	}
}

func TestComputeShapleyValue_SinglePlayer(t *testing.T) {
	game := &CoalitionGame{
		Players: []types.UID{"p1"},
		Values:  map[string]float64{"p1": 10.0},
	}
	result := ComputeShapleyValue(game, 100, nil)

	// Single player should get full value
	if result["p1"] != 10.0 {
		t.Errorf("Single player expected 10.0, got %f", result["p1"])
	}
}

func TestComputeShapleyValue_Deterministic(t *testing.T) {
	game := &CoalitionGame{
		Players: []types.UID{"p1", "p2"},
		Values: map[string]float64{
			"p1":    5.0,
			"p2":    5.0,
			"p1,p2": 20.0,
		},
	}

	// Use same seed for reproducibility
	rng := rand.New(rand.NewSource(42))
	result1 := ComputeShapleyValue(game, 1000, rng)

	rng = rand.New(rand.NewSource(42))
	result2 := ComputeShapleyValue(game, 1000, rng)

	if result1["p1"] != result2["p1"] || result1["p2"] != result2["p2"] {
		t.Error("Same seed should produce same results")
	}
}

func TestShapleyCredits_UpdateCredits(t *testing.T) {
	sc := NewShapleyCredits()

	shapleyValues := map[types.UID]float64{"p1": 100.0, "p2": 50.0}
	consumption := map[types.UID]int64{"p1": 80, "p2": 60}

	sc.UpdateCredits(shapleyValues, consumption, time.Now())

	// p1: 100 - 80 = 20 credit
	// p2: 50 - 60 = -10 credit
	if sc.Credits["p1"] != 20.0 {
		t.Errorf("Expected p1 credit 20, got %f", sc.Credits["p1"])
	}
	if sc.Credits["p2"] != -10.0 {
		t.Errorf("Expected p2 credit -10, got %f", sc.Credits["p2"])
	}
}
