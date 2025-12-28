package allocation

// Package allocation implements Phase 4: Market-based CPU allocation using
// Fisher-market / proportional fairness (Nash Social Welfare maximization).

import (
	"sort"

	"k8s.io/apimachinery/pkg/types"
)

// PodParams represents market parameters for a pod.
type PodParams struct {
	Demand   float64 // Normalized demand [0,1]
	Bid      float64 // Effective bid (weight × demand)
	MinMilli int64   // Minimum CPU in millicores
	MaxMilli int64   // Maximum CPU in millicores
	Weight   float64 // Budget/weight (for zero-bid fallback)
}

// ClearMarket performs market-clearing allocation using proportional fairness.
// Returns allocations in millicores (int64) for deterministic, high-resolution results.
//
// Algorithm:
//   1. Allocate minimums (scale down if they exceed capacity)
//   2. Compute effective bids (weight × demand)
//   3. Distribute remaining proportionally to bids (or weights if zero bids)
//   4. Enforce maximum bounds with water-filling redistribution
//   5. Round to int64 millicores deterministically
//
// This maximizes Nash Social Welfare: Σ log(cpu_i) subject to capacity and bounds.
func ClearMarket(capacityMilli int64, pods map[types.UID]PodParams) map[types.UID]int64 {
	if len(pods) == 0 {
		return make(map[types.UID]int64)
	}

	// Step 1: Handle minimums exceeding capacity
	totalMin := int64(0)
	for _, params := range pods {
		totalMin += params.MinMilli
	}

	var scaleFactor float64
	var remaining float64
	targets := make(map[types.UID]float64)

	if totalMin > capacityMilli {
		// Scale down minimums proportionally
		scaleFactor = float64(capacityMilli) / float64(totalMin)
		remaining = 0
		// Create adjusted PodParams with scaled minimums for rounding
		adjustedPods := make(map[types.UID]PodParams)
		for uid, params := range pods {
			scaledMin := float64(params.MinMilli) * scaleFactor
			targets[uid] = scaledMin
			// Create adjusted params with scaled minimum for rounding
			adjustedPods[uid] = PodParams{
				Demand:   params.Demand,
				Bid:      params.Bid,
				MinMilli: int64(scaledMin), // Use scaled minimum
				MaxMilli: params.MaxMilli,
				Weight:   params.Weight,
			}
		}
		// Use adjusted pods for rounding
		return roundDeterministic(targets, adjustedPods)
	} else {
		// Allocate minimums, compute remaining
		remaining = float64(capacityMilli - totalMin)
		for uid, params := range pods {
			targets[uid] = float64(params.MinMilli)
		}
	}

	// Step 2: Compute effective bids and determine redistribution basis
	totalBid := 0.0
	totalWeight := 0.0
	for _, params := range pods {
		totalBid += params.Bid
		totalWeight += params.Weight
	}

	// Determine redistribution basis (used in both Step 3 and Step 4)
	type redistributionKey struct {
		value float64
		uid   types.UID
	}
	redistKeys := make(map[types.UID]float64)
	var totalRedistKey float64

	if totalBid > 0 {
		// Use bids as redistribution key
		for uid, params := range pods {
			redistKeys[uid] = params.Bid
			totalRedistKey += params.Bid
		}
	} else if totalWeight > 0 {
		// Use weights as redistribution key
		for uid, params := range pods {
			redistKeys[uid] = params.Weight
			totalRedistKey += params.Weight
		}
	} else {
		// Equal share: use 1.0 for all
		for uid := range pods {
			redistKeys[uid] = 1.0
			totalRedistKey += 1.0
		}
	}

	// Step 3: Proportional distribution using the chosen basis
	if totalRedistKey > 0 {
		for uid := range pods {
			share := (redistKeys[uid] / totalRedistKey) * remaining
			targets[uid] += share
		}
	}

	// Step 4: Enforce maximum bounds (water-filling)
	for {
		excess := 0.0
		uncappedKey := 0.0
		uncappedUIDs := make([]types.UID, 0)

		for uid, params := range pods {
			if targets[uid] > float64(params.MaxMilli) {
				excess += targets[uid] - float64(params.MaxMilli)
				targets[uid] = float64(params.MaxMilli)
			} else if targets[uid] < float64(params.MaxMilli) {
				// Use the same redistribution key as Step 3
				uncappedKey += redistKeys[uid]
				uncappedUIDs = append(uncappedUIDs, uid)
			}
		}

		if excess <= 0.0001 { // Small epsilon for float comparison
			break
		}

		if uncappedKey > 0 {
			// Redistribute excess proportionally to uncapped pods using the same basis
			for _, uid := range uncappedUIDs {
				share := (redistKeys[uid] / uncappedKey) * excess
				targets[uid] += share
			}
		} else {
			// All pods capped, excess remains unused
			break
		}
	}

	// Step 5: Deterministic rounding to int64 millicores
	// Use largest remainder method with UID tie-break for determinism
	return roundDeterministic(targets, pods)
}

// roundDeterministic rounds float targets to int64 millicores using largest remainder method.
// Uses UID as stable tie-breaker for determinism.
// Bound-aware: never exceeds MaxMilli or goes below MinMilli.
func roundDeterministic(targets map[types.UID]float64, pods map[types.UID]PodParams) map[types.UID]int64 {
	allocations := make(map[types.UID]int64)
	remainders := make([]struct {
		uid       types.UID
		floor     int64
		remainder float64
		minMilli  int64
		maxMilli  int64
	}, 0, len(targets))

	// Compute floors and remainders, clamping to bounds
	totalFloor := int64(0)
	for uid, target := range targets {
		params := pods[uid]
		// Floor with small epsilon to handle float precision
		floor := int64(target + 0.0001)

		// Clamp to bounds immediately
		if floor < params.MinMilli {
			floor = params.MinMilli
		}
		if floor > params.MaxMilli {
			floor = params.MaxMilli
		}

		remainder := target - float64(floor)
		remainders = append(remainders, struct {
			uid       types.UID
			floor     int64
			remainder float64
			minMilli  int64
			maxMilli  int64
		}{uid, floor, remainder, params.MinMilli, params.MaxMilli})
		allocations[uid] = floor
		totalFloor += floor
	}

	// Calculate total target (sum of all targets)
	totalTarget := 0.0
	for _, target := range targets {
		totalTarget += target
	}

	// Calculate leftover millicores to distribute
	totalTargetMilli := int64(totalTarget + 0.5) // Round to nearest
	leftover := totalTargetMilli - totalFloor

	if leftover == 0 {
		// Final bound check (defensive)
		for uid, params := range pods {
			if allocations[uid] < params.MinMilli {
				allocations[uid] = params.MinMilli
			}
			if allocations[uid] > params.MaxMilli {
				allocations[uid] = params.MaxMilli
			}
		}
		return allocations
	}

	// Sort by remainder (descending), then by UID for tie-break
	// Only consider pods that can accept more (below max)
	sort.Slice(remainders, func(i, j int) bool {
		if remainders[i].remainder != remainders[j].remainder {
			return remainders[i].remainder > remainders[j].remainder
		}
		return string(remainders[i].uid) < string(remainders[j].uid)
	})

	// Distribute leftover to pods with largest remainders (bound-aware)
	if leftover > 0 {
		for i := int64(0); i < leftover && i < int64(len(remainders)); i++ {
			r := remainders[i]
			// Only increment if below max
			if allocations[r.uid] < r.maxMilli {
				allocations[r.uid]++
			}
		}
	} else if leftover < 0 {
		// If we over-allocated (shouldn't happen, but handle gracefully)
		// Sort ascending and reduce from smallest remainders (bound-aware)
		sort.Slice(remainders, func(i, j int) bool {
			if remainders[i].remainder != remainders[j].remainder {
				return remainders[i].remainder < remainders[j].remainder
			}
			return string(remainders[i].uid) < string(remainders[j].uid)
		})
		for i := int64(0); i < -leftover && i < int64(len(remainders)); i++ {
			r := remainders[i]
			// Only decrement if above min
			if allocations[r.uid] > r.minMilli {
				allocations[r.uid]--
			}
		}
	}

	// Final bound check (defensive)
	for uid, params := range pods {
		if allocations[uid] < params.MinMilli {
			allocations[uid] = params.MinMilli
		}
		if allocations[uid] > params.MaxMilli {
			allocations[uid] = params.MaxMilli
		}
	}

	return allocations
}

