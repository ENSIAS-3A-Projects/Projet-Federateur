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

	totalBaseline := int64(0)
	totalWeight := 0.0
	for _, b := range bids {
		totalBaseline += b.Min
		totalWeight += b.Weight
	}

	if totalBaseline > capacity {
		return scaleBaselinesSimple(capacity, bids, totalWeight)
	}

	allocations := make(map[types.UID]int64)
	for _, b := range bids {
		allocations[b.UID] = b.Min
	}

	remaining := capacity - totalBaseline
	active := make([]Bid, len(bids))
	copy(active, bids)

	// Iterate until no more redistribution possible
	for remaining > 0 && len(active) > 0 {
		activeWeight := 0.0
		for _, b := range active {
			activeWeight += b.Weight
		}

		if activeWeight == 0 {
			// Fallback: equal distribution if no weights are set
			share := remaining / int64(len(active))
			for _, b := range active {
				allocations[b.UID] += share
			}
			break
		}

		newActive := make([]Bid, 0, len(active))
		distributed := int64(0)

		for _, b := range active {
			share := int64(float64(remaining) * (b.Weight / activeWeight))
			newAlloc := allocations[b.UID] + share

			if newAlloc >= b.Max {
				// Capped
				added := b.Max - allocations[b.UID]
				allocations[b.UID] = b.Max
				distributed += added
			} else {
				allocations[b.UID] = newAlloc
				distributed += share
				newActive = append(newActive, b)
			}
		}

		remaining -= distributed
		active = newActive

		// Safety: if nothing distributed, break to avoid infinite loop
		if distributed == 0 {
			break
		}
	}

	return allocations
}

// AllocationResultWithPrice contains allocations and shadow price
type AllocationResultWithPrice struct {
	Allocations map[types.UID]int64
	ShadowPrice float64
}

// NashBargainWithPrice computes Nash Bargaining Solution with shadow price
func NashBargainWithPrice(capacity int64, bids []Bid) AllocationResultWithPrice {
	allocations := NashBargain(capacity, bids)

	// Shadow price is the marginal value of additional capacity
	totalDemand := int64(0)
	totalWeight := 0.0
	for _, b := range bids {
		totalDemand += b.Demand
		totalWeight += b.Weight
	}

	var shadowPrice float64
	if totalDemand > capacity && totalWeight > 0 {
		// Congested: shadow price is positive
		congestionRatio := float64(totalDemand-capacity) / float64(capacity)
		shadowPrice = congestionRatio * (totalWeight / float64(len(bids)))
	} else {
		// Uncongested: shadow price is zero
		shadowPrice = 0.0
	}

	return AllocationResultWithPrice{
		Allocations: allocations,
		ShadowPrice: shadowPrice,
	}
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
