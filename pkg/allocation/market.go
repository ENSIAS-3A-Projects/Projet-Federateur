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
	Allocations        map[types.UID]int64 // CPU limit allocations in millicores
	RequestAllocations map[types.UID]int64 // CPU request allocations in millicores
	Mode               AllocationMode
	TotalNeed          int64
	TotalAlloc         int64
	Needs              map[types.UID]int64
}

// PodParams represents market parameters for a pod.
type PodParams struct {
	Demand           float64 // Normalized demand [0,1] from throttling signal
	Bid              float64 // Effective bid (weight × demand) - kept for compatibility
	MinMilli         int64   // Minimum CPU in millicores (baseline)
	MaxMilli         int64   // Maximum CPU in millicores (ceiling)
	Weight           float64 // Budget/weight (from request CPU)
	ActualUsageMilli int64   // Actual CPU usage in millicores (from cgroup metrics)
}

// ClearMarket performs demand-capped allocation.
//
// This replaces the original fair-share algorithm with:
//  1. Compute "need" for each pod based on demand signal
//  2. If total need <= capacity: give everyone their need (uncongested)
//  3. If total need > capacity: apply Nash bargaining reduction (congested)
//  4. If total baselines > capacity: scale baselines (overloaded)
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
			Allocations:        make(map[types.UID]int64),
			RequestAllocations: make(map[types.UID]int64),
			Mode:               ModeUncongested,
			Needs:              make(map[types.UID]int64),
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

	var allocs map[types.UID]int64
	var mode AllocationMode
	var totalAlloc int64

	// Step 2: Check overloaded condition (baselines exceed capacity)
	if totalMin > capacityMilli {
		allocs = scaleBaselines(pods, capacityMilli)
		mode = ModeOverloaded
		for _, a := range allocs {
			totalAlloc += a
		}
	} else if totalNeed > capacityMilli {
		// Step 3: Check congested condition (needs exceed capacity)
		allocs = nashReduce(needs, pods, capacityMilli)
		mode = ModeCongested
		for _, a := range allocs {
			totalAlloc += a
		}
	} else {
		// Step 4: Uncongested - give everyone what they need
		allocs = clampToBounds(needs, pods)
		mode = ModeUncongested
		totalAlloc = totalNeed
	}

	// Step 5: Compute request allocations based on limit allocations
	// Requests should be slightly below limits to allow burst headroom
	// Formula: request = limit * 0.9 (90% of limit), but clamped to actual usage
	requestAllocs := computeRequestAllocations(allocs, pods)

	return AllocationResult{
		Allocations:        allocs,
		RequestAllocations: requestAllocs,
		Mode:               mode,
		TotalNeed:          totalNeed,
		TotalAlloc:         totalAlloc,
		Needs:              needs,
	}
}

// computeRequestAllocations calculates optimal CPU requests based on limit allocations.
// Strategy:
//   - Request = max(actualUsage * 1.05, limit * 0.85)
//   - This ensures request is above usage (for scheduling) but below limit (for burst)
//   - Minimum request is 10m to avoid scheduling issues
func computeRequestAllocations(limits map[types.UID]int64, pods map[types.UID]PodParams) map[types.UID]int64 {
	requests := make(map[types.UID]int64)

	for uid, limit := range limits {
		p := pods[uid]

		var request int64

		if p.ActualUsageMilli > 0 {
			// Usage-based request: actual usage + 5% headroom
			usageBasedRequest := int64(float64(p.ActualUsageMilli) * 1.05)

			// Limit-based request: 85% of limit (ensures room for bursting)
			limitBasedRequest := int64(float64(limit) * 0.85)

			// Use the higher of the two to ensure scheduling works
			request = usageBasedRequest
			if limitBasedRequest > request {
				request = limitBasedRequest
			}
		} else {
			// No usage data: use 90% of limit
			request = int64(float64(limit) * 0.90)
		}

		// Minimum request of 10m to avoid scheduling edge cases
		if request < 10 {
			request = 10
		}

		// Request cannot exceed limit (K8s constraint)
		if request > limit {
			request = limit
		}

		requests[uid] = request
	}

	return requests
}

// computeNeed estimates CPU required based on actual usage.
// The goal is to set limits close to actual usage for ~100% utilization efficiency.
// We add a small headroom (5-10%) to prevent throttling during usage spikes.
// computeNeed estimates CPU required based on actual usage.
func computeNeed(p PodParams) int64 {
	// If we have actual usage data, use it directly
	if p.ActualUsageMilli > 0 {
		// Base headroom (10%)
		headroomFactor := 0.10
		
		// AGGRESSIVE SCALING FIX:
		// If throttling is detected, we must scale aggressively to escape the "low limit trap".
		// Instead of just 15%, we allow up to 400% (4x) growth if demand is high.
		demandFactor := 0.0
		if p.Demand > 0 {
			// p.Demand is [0,1].
			// If Demand is 0.5 (50% throttled), factor adds ~2.0x extra.
			// Formula: Allow rapid doubling/tripling when heavily throttled.
			demandFactor = p.Demand * 4.0 
		}

		// Calculate need
		// Example: Usage=100, Demand=0.5 -> Need = 100 * (1.0 + 0.1 + 2.0) = 310m
		need := int64(float64(p.ActualUsageMilli) * (1.0 + headroomFactor + demandFactor))

		// Clamp to pod bounds
		if need < p.MinMilli {
			need = p.MinMilli
		}
		if need > p.MaxMilli {
			need = p.MaxMilli
		}

		return need
	}

	// Fallback to demand-based calculation if no actual usage data
	// (Keep existing fallback logic...)
	demand := p.Demand
	if demand < 0 { demand = 0 }
	if demand > 1 { demand = 1 }
	
	baseNeed := p.MinMilli
	demandRange := p.MaxMilli - p.MinMilli
	if demandRange < 0 { demandRange = 0 }
	
	demandComponent := int64(float64(demandRange) * demand)
	rawNeed := baseNeed + demandComponent
	
	headroom := int64(float64(rawNeed) * 0.05)
	need := rawNeed + headroom

	if need < p.MinMilli { need = p.MinMilli }
	if need > p.MaxMilli { need = p.MaxMilli }

	return need
}

// nashReduce applies Nash Bargaining Solution when total need > capacity.
// See THEORY.md section "Congested Mode" and "Nash bargaining" mapping.
// Surplus is reduced proportionally to maximize the Nash product.
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
// See THEORY.md section "Overloaded Mode" for formal definition.
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
