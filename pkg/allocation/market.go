package allocation

// Package allocation implements demand-capped CPU allocation.
//
// This is a drop-in replacement for the original market.go that changes
// the allocation philosophy from "fair share of capacity" to "need + headroom".
//
// Behavior:
//   - Uncongested (Σ need <= capacity): Each pod gets exactly what it needs
//   - Congested (Σ need > capacity): Nash bargaining reduces surplus proportionally
//   - Overloaded (Σ min > capacity): Baselines scaled (emergency mode)

import (
	"sort"

	"k8s.io/apimachinery/pkg/types"
)

// AllocationMode indicates which allocation path was taken.
type AllocationMode string

const (
	// ModeUncongested means total need <= capacity; everyone gets what they need.
	ModeUncongested AllocationMode = "uncongested"
	// ModeCongested means total need > capacity; Nash reduction applied.
	ModeCongested AllocationMode = "congested"
	// ModeOverloaded means total baselines > capacity; emergency scaling.
	ModeOverloaded AllocationMode = "overloaded"
)

// AllocationResult contains allocations and metadata.
type AllocationResult struct {
	Allocations map[types.UID]int64
	Mode        AllocationMode
	TotalNeed   int64
	TotalAlloc  int64
}

// PodParams represents market parameters for a pod.
type PodParams struct {
	Demand   float64 // Normalized demand [0,1] from throttling signal
	Bid      float64 // Effective bid (weight × demand) - kept for compatibility
	MinMilli int64   // Minimum CPU in millicores (baseline)
	MaxMilli int64   // Maximum CPU in millicores (ceiling)
	Weight   float64 // Budget/weight (from request CPU)
}

// ClearMarket performs demand-capped allocation.
//
// This replaces the original fair-share algorithm with:
//   1. Compute "need" for each pod based on demand signal
//   2. If total need <= capacity: give everyone their need (uncongested)
//   3. If total need > capacity: apply Nash bargaining reduction (congested)
//   4. If total baselines > capacity: scale baselines (overloaded)
//
// Returns allocations in millicores (int64).
func ClearMarket(capacityMilli int64, pods map[types.UID]PodParams) map[types.UID]int64 {
	result := ClearMarketWithMetadata(capacityMilli, pods)
	return result.Allocations
}

// ClearMarketWithMetadata performs allocation and returns metadata for observability.
func ClearMarketWithMetadata(capacityMilli int64, pods map[types.UID]PodParams) AllocationResult {
	if len(pods) == 0 {
		return AllocationResult{
			Allocations: make(map[types.UID]int64),
			Mode:        ModeUncongested,
		}
	}

	// Step 1: Compute need for each pod
	needs := make(map[types.UID]int64)
	totalNeed := int64(0)
	totalMin := int64(0)

	for uid, p := range pods {
		need := computeNeed(p)
		needs[uid] = need
		totalNeed += need
		totalMin += p.MinMilli
	}

	// Step 2: Check overloaded condition (baselines exceed capacity)
	if totalMin > capacityMilli {
		allocs := scaleBaselines(pods, capacityMilli)
		totalAlloc := int64(0)
		for _, a := range allocs {
			totalAlloc += a
		}
		return AllocationResult{
			Allocations: allocs,
			Mode:        ModeOverloaded,
			TotalNeed:   totalNeed,
			TotalAlloc:  totalAlloc,
		}
	}

	// Step 3: Check congested condition (needs exceed capacity)
	if totalNeed > capacityMilli {
		allocs := nashReduce(needs, pods, capacityMilli)
		totalAlloc := int64(0)
		for _, a := range allocs {
			totalAlloc += a
		}
		return AllocationResult{
			Allocations: allocs,
			Mode:        ModeCongested,
			TotalNeed:   totalNeed,
			TotalAlloc:  totalAlloc,
		}
	}

	// Step 4: Uncongested - give everyone what they need
	allocs := clampToBounds(needs, pods)
	return AllocationResult{
		Allocations: allocs,
		Mode:        ModeUncongested,
		TotalNeed:   totalNeed,
		TotalAlloc:  totalNeed, // In uncongested mode, alloc = need
	}
}

// computeNeed estimates CPU required to eliminate throttling for a pod.
//
// The demand signal [0,1] maps to CPU need:
//   demand=0.0 → not throttling → need = baseline + small headroom
//   demand=0.5 → moderate throttling → need = midpoint of range + headroom
//   demand=1.0 → severe throttling → need = max (give everything)
//
// Headroom scales with demand: 10% at zero demand, up to 25% at full demand.
// This ensures pods under stress get extra buffer.
func computeNeed(p PodParams) int64 {
	// Clamp demand to [0,1] defensively
	demand := p.Demand
	if demand < 0 {
		demand = 0
	}
	if demand > 1 {
		demand = 1
	}

	// Base need starts at minimum (baseline)
	baseNeed := p.MinMilli

	// Demand-driven component: interpolate between min and max
	demandRange := p.MaxMilli - p.MinMilli
	if demandRange < 0 {
		demandRange = 0
	}
	demandComponent := int64(float64(demandRange) * demand)

	rawNeed := baseNeed + demandComponent

	// Adaptive headroom: higher demand → more headroom
	// 10% base + up to 15% additional based on demand
	headroomFactor := 0.10 + (demand * 0.15)
	headroom := int64(float64(rawNeed) * headroomFactor)

	need := rawNeed + headroom

	// Clamp to pod bounds
	if need < p.MinMilli {
		need = p.MinMilli
	}
	if need > p.MaxMilli {
		need = p.MaxMilli
	}

	return need
}

// nashReduce applies Nash Bargaining Solution when total need > capacity.
//
// Algorithm:
//   1. Everyone keeps their baseline (MinMilli) - the "disagreement point"
//   2. Surplus demand (need - baseline) is the negotiable portion
//   3. Available surplus = capacity - total baselines
//   4. Each pod gets: baseline + (surplus * reduction_ratio)
//
// This maximizes the Nash product: Π(allocation_i - baseline_i)
// which is equivalent to proportional reduction of surplus.
func nashReduce(needs map[types.UID]int64, pods map[types.UID]PodParams, capacity int64) map[types.UID]int64 {
	allocations := make(map[types.UID]int64)

	// Calculate baselines and surpluses
	totalBaseline := int64(0)
	surpluses := make(map[types.UID]int64)
	totalSurplus := int64(0)

	for uid, p := range pods {
		totalBaseline += p.MinMilli

		surplus := needs[uid] - p.MinMilli
		if surplus < 0 {
			surplus = 0
		}
		surpluses[uid] = surplus
		totalSurplus += surplus
	}

	// Available capacity for surplus distribution
	availableSurplus := capacity - totalBaseline
	if availableSurplus < 0 {
		// Should not happen (overloaded case handled earlier), but be safe
		return scaleBaselines(pods, capacity)
	}

	// Compute reduction ratio
	var ratio float64
	if totalSurplus > 0 {
		ratio = float64(availableSurplus) / float64(totalSurplus)
		if ratio > 1.0 {
			ratio = 1.0 // Don't inflate beyond need
		}
	} else {
		// No surplus demand: everyone gets baseline
		ratio = 0.0
	}

	// Allocate: baseline + reduced surplus
	for uid, p := range pods {
		reducedSurplus := int64(float64(surpluses[uid]) * ratio)
		allocation := p.MinMilli + reducedSurplus

		// Clamp to bounds (defensive)
		if allocation < p.MinMilli {
			allocation = p.MinMilli
		}
		if allocation > p.MaxMilli {
			allocation = p.MaxMilli
		}

		allocations[uid] = allocation
	}

	// Handle rounding: distribute any leftover to pods with room
	totalAlloc := int64(0)
	for _, a := range allocations {
		totalAlloc += a
	}
	leftover := capacity - totalAlloc

	if leftover > 0 {
		// Sort UIDs for determinism
		uids := make([]types.UID, 0, len(pods))
		for uid := range pods {
			uids = append(uids, uid)
		}
		sort.Slice(uids, func(i, j int) bool {
			return string(uids[i]) < string(uids[j])
		})

		// Distribute leftover to pods with headroom
		for _, uid := range uids {
			if leftover <= 0 {
				break
			}
			p := pods[uid]
			headroom := p.MaxMilli - allocations[uid]
			if headroom > 0 {
				add := leftover
				if add > headroom {
					add = headroom
				}
				allocations[uid] += add
				leftover -= add
			}
		}
	}

	return allocations
}

// scaleBaselines handles the overloaded case when total baselines > capacity.
// This is an emergency mode indicating severe over-commitment.
//
// All baselines are scaled proportionally to fit within capacity.
func scaleBaselines(pods map[types.UID]PodParams, capacity int64) map[types.UID]int64 {
	allocations := make(map[types.UID]int64)

	totalMin := int64(0)
	for _, p := range pods {
		totalMin += p.MinMilli
	}

	if totalMin == 0 {
		// Edge case: all pods have zero baseline
		// Give each pod equal share
		share := capacity / int64(len(pods))
		for uid := range pods {
			allocations[uid] = share
		}
		return allocations
	}

	scale := float64(capacity) / float64(totalMin)

	// Sort UIDs for deterministic rounding
	uids := make([]types.UID, 0, len(pods))
	for uid := range pods {
		uids = append(uids, uid)
	}
	sort.Slice(uids, func(i, j int) bool {
		return string(uids[i]) < string(uids[j])
	})

	// First pass: floor allocations
	totalAlloc := int64(0)
	remainders := make([]struct {
		uid       types.UID
		remainder float64
	}, 0, len(pods))

	for _, uid := range uids {
		p := pods[uid]
		scaled := float64(p.MinMilli) * scale
		floor := int64(scaled)

		// Enforce absolute minimum (prevent complete starvation)
		const absoluteMin = int64(10) // 10m
		if floor < absoluteMin && capacity >= absoluteMin*int64(len(pods)) {
			floor = absoluteMin
		}

		allocations[uid] = floor
		totalAlloc += floor
		remainders = append(remainders, struct {
			uid       types.UID
			remainder float64
		}{uid, scaled - float64(floor)})
	}

	// Second pass: distribute leftover by largest remainder
	leftover := capacity - totalAlloc
	if leftover > 0 {
		sort.Slice(remainders, func(i, j int) bool {
			if remainders[i].remainder != remainders[j].remainder {
				return remainders[i].remainder > remainders[j].remainder
			}
			return string(remainders[i].uid) < string(remainders[j].uid)
		})

		for i := int64(0); i < leftover && i < int64(len(remainders)); i++ {
			allocations[remainders[i].uid]++
		}
	}

	return allocations
}

// clampToBounds ensures allocations respect pod min/max bounds.
func clampToBounds(needs map[types.UID]int64, pods map[types.UID]PodParams) map[types.UID]int64 {
	allocations := make(map[types.UID]int64)

	for uid, need := range needs {
		p := pods[uid]
		allocation := need

		if allocation < p.MinMilli {
			allocation = p.MinMilli
		}
		if allocation > p.MaxMilli {
			allocation = p.MaxMilli
		}

		allocations[uid] = allocation
	}

	return allocations
}