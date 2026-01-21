package allocation

import (
	"k8s.io/apimachinery/pkg/types"
)

// Bid represents a pod's resource request
type Bid struct {
	UID    types.UID
	Demand int64   // Requested allocation (millicores)
	Weight float64 // Bargaining power (higher = more priority)
	Min    int64   // Minimum viable allocation (disagreement point)
	Max    int64   // Maximum useful allocation
}

// NashBargain computes Nash Bargaining Solution allocation
//
// Objective: maximize Π(x_i - d_i)^w_i
// Subject to: Σx_i ≤ C, x_i ≥ d_i, x_i ≤ max_i
//
// This is the ONLY allocation algorithm - simple, fast, provably fair.
func NashBargain(capacity int64, bids []Bid) map[types.UID]int64 {
	if len(bids) == 0 {
		return make(map[types.UID]int64)
	}

	// Step 1: Check feasibility - can we give everyone their minimum?
	totalBaseline := int64(0)
	totalWeight := 0.0
	for _, b := range bids {
		totalBaseline += b.Min
		totalWeight += b.Weight
	}

	// Overloaded: even baselines don't fit
	if totalBaseline > capacity {
		return scaleBaselinesSimple(capacity, bids, totalWeight)
	}

	// Step 2: Distribute surplus proportional to weights (Nash solution)
	surplus := capacity - totalBaseline
	allocations := make(map[types.UID]int64)
	uncapped := make([]Bid, 0, len(bids))
	usedCapacity := totalBaseline

	for _, b := range bids {
		// Weighted share of surplus
		share := float64(surplus) * (b.Weight / totalWeight)
		alloc := b.Min + int64(share)

		// Check if capped at max
		if alloc > b.Max {
			allocations[b.UID] = b.Max
			usedCapacity += (b.Max - b.Min)
		} else {
			allocations[b.UID] = alloc
			usedCapacity += int64(share)
			uncapped = append(uncapped, b)
		}
	}

	// Step 3: Redistribute unused capacity from capped agents
	remainingSurplus := capacity - usedCapacity
	if remainingSurplus > 0 && len(uncapped) > 0 {
		redistributeSurplus(allocations, uncapped, remainingSurplus, totalWeight)
	}

	return allocations
}

// scaleBaselinesSimple handles overloaded case where even baselines don't fit
// Scales baselines proportionally by weight
func scaleBaselinesSimple(capacity int64, bids []Bid, totalWeight float64) map[types.UID]int64 {
	allocations := make(map[types.UID]int64)

	// Weighted baseline allocation
	weightedBaseline := 0.0
	for _, b := range bids {
		weightedBaseline += float64(b.Min) * b.Weight
	}

	if weightedBaseline == 0 {
		// All baselines zero: equal division
		share := capacity / int64(len(bids))
		for _, b := range bids {
			allocations[b.UID] = share
		}
		return allocations
	}

	// Scale baselines by weight
	scale := float64(capacity) / weightedBaseline
	for _, b := range bids {
		alloc := int64(float64(b.Min) * b.Weight * scale)
		if alloc < 10 {
			alloc = 10 // Absolute minimum
		}
		allocations[b.UID] = alloc
	}

	return allocations
}

// redistributeSurplus redistributes excess capacity from capped agents to uncapped ones
func redistributeSurplus(allocations map[types.UID]int64, uncapped []Bid, surplus int64, totalWeight float64) {
	// Compute weight sum of uncapped agents
	uncappedWeight := 0.0
	for _, b := range uncapped {
		uncappedWeight += b.Weight
	}

	if uncappedWeight == 0 {
		return
	}

	// Redistribute proportional to weights
	for _, b := range uncapped {
		extra := float64(surplus) * (b.Weight / uncappedWeight)
		newAlloc := allocations[b.UID] + int64(extra)

		// Clamp to max
		if newAlloc > b.Max {
			newAlloc = b.Max
		}

		allocations[b.UID] = newAlloc
	}
}

// AllocationMode indicates which allocation path was taken
type AllocationMode string

const (
	ModeUncongested AllocationMode = "uncongested" // Total demand ≤ capacity
	ModeCongested   AllocationMode = "congested"   // Total demand > capacity
	ModeOverloaded  AllocationMode = "overloaded"  // Total baselines > capacity
)

// AllocationResult contains allocations and metadata for observability
type AllocationResult struct {
	Allocations map[types.UID]int64
	Mode        AllocationMode
	TotalDemand int64
	TotalAlloc  int64
	Capacity    int64
}

// AllocateWithMode performs allocation and returns mode information
func AllocateWithMode(capacity int64, bids []Bid) AllocationResult {
	// Compute total demand and baselines
	totalDemand := int64(0)
	totalBaseline := int64(0)
	for _, b := range bids {
		totalDemand += b.Demand
		totalBaseline += b.Min
	}

	// Determine mode
	var mode AllocationMode
	if totalDemand <= capacity {
		mode = ModeUncongested
	} else if totalBaseline <= capacity {
		mode = ModeCongested
	} else {
		mode = ModeOverloaded
	}

	// Allocate
	allocations := NashBargain(capacity, bids)

	// Compute total allocation
	totalAlloc := int64(0)
	for _, alloc := range allocations {
		totalAlloc += alloc
	}

	return AllocationResult{
		Allocations: allocations,
		Mode:        mode,
		TotalDemand: totalDemand,
		TotalAlloc:  totalAlloc,
		Capacity:    capacity,
	}
}
