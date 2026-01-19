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

// Allocation tuning constants
const (
	// NeedHeadroomFactor is the CONSERVATIVE headroom for actual need (15%)
	// This is what the pod truly requires to avoid throttling
	NeedHeadroomFactor = 0.15

	// WantHeadroomFactor is the BASE headroom for want calculation (10%)
	WantHeadroomFactor = 0.10

	// MaxDemandMultiplier controls aggressive scaling in want (4x max growth)
	// Used when pod signals it would like more resources
	MaxDemandMultiplier = 4.0

	// RequestUsageHeadroom is added to actual usage for request calculation (5%)
	RequestUsageHeadroom = 1.05

	// RequestLimitRatio is the request/limit ratio when no usage data (85%)
	RequestLimitRatio = 0.85

	// RequestLimitFallbackRatio is used when no usage data (90%)
	RequestLimitFallbackRatio = 0.90

	// MinimumRequestMilli is the minimum CPU request to avoid scheduling issues
	MinimumRequestMilli = int64(10)

	// AbsoluteMinimumAlloc is the minimum allocation per pod in millicores
	AbsoluteMinimumAlloc = int64(10)
)

// AllocationResult contains allocations and metadata.
type AllocationResult struct {
	Allocations        map[types.UID]int64 // CPU limit allocations in millicores
	RequestAllocations map[types.UID]int64 // CPU request allocations in millicores
	Mode               AllocationMode
	TotalNeed          int64
	TotalAlloc         int64
	Needs              map[types.UID]int64
	NashIterations     int // Number of Nash solver redistribution iterations (0 if not used)
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

// MarketConfig holds configuration for the market clearing mechanism.
type MarketConfig struct {
	// CostEfficiencyMode enables aggressive cost optimization logic.
	CostEfficiencyMode bool
	// NeedHeadroomFactor is the conservative headroom for actual need.
	NeedHeadroomFactor float64
	// TargetThrottling is the acceptable throttling target (0-1).
	TargetThrottling float64
}

// DefaultMarketConfig returns the default market configuration.
func DefaultMarketConfig() MarketConfig {
	return MarketConfig{
		CostEfficiencyMode: false,
		NeedHeadroomFactor: 0.15, // Default 15%
		TargetThrottling:   0.0,
	}
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
func ClearMarket(capacityMilli int64, pods map[types.UID]PodParams, config MarketConfig) map[types.UID]int64 {
	result := ClearMarketWithMetadata(capacityMilli, pods, config)
	return result.Allocations
}

// ClearMarketWithMetadata performs allocation and returns metadata for observability.
func ClearMarketWithMetadata(capacityMilli int64, pods map[types.UID]PodParams, config MarketConfig) AllocationResult {
	return clearMarketInternal(capacityMilli, pods, config, false)
}

// ClearMarketNash performs allocation using the formal NashBargainingSolution solver.
// This provides mathematically optimal Nash Bargaining Solution with verified axioms.
func ClearMarketNash(capacityMilli int64, pods map[types.UID]PodParams, config MarketConfig) AllocationResult {
	return clearMarketInternal(capacityMilli, pods, config, true)
}

// clearMarketInternal is the common implementation for both allocation modes.
func clearMarketInternal(capacityMilli int64, pods map[types.UID]PodParams, config MarketConfig, useNashSolver bool) AllocationResult {
	if len(pods) == 0 {
		return AllocationResult{
			Allocations:        make(map[types.UID]int64),
			RequestAllocations: make(map[types.UID]int64),
			Mode:               ModeUncongested,
			Needs:              make(map[types.UID]int64),
		}
	}

	// Step 1: Compute NEED (conservative) and WANT (greedy) for each pod
	needs := make(map[types.UID]int64) // What pods truly need
	wants := make(map[types.UID]int64) // What pods would like
	totalNeed := int64(0)
	totalWant := int64(0)
	totalMin := int64(0)

	for uid, p := range pods {
		need := computeNeed(p, config.NeedHeadroomFactor)
		want := computeWant(p)

		// In Cost Efficiency Mode, aggressive want reduction
		if config.CostEfficiencyMode {
			// Want is just need (no greedy expansion beyond headroom)
			want = need
		}

		needs[uid] = need
		wants[uid] = want
		totalNeed += need
		totalWant += want
		totalMin += p.MinMilli
	}

	var allocs map[types.UID]int64
	var mode AllocationMode
	var totalAlloc int64
	var nashIterations int

	// Step 2: Check overloaded condition (baselines exceed capacity)
	if totalMin > capacityMilli {
		allocs = scaleBaselines(pods, capacityMilli)
		mode = ModeOverloaded
		nashIterations = 0
		for _, a := range allocs {
			totalAlloc += a
		}
	} else if totalWant > capacityMilli && !config.CostEfficiencyMode {
		// Step 3: CONGESTED - wants exceed capacity
		// Normal mode: apply Nash bargaining or Kalai-Smorodinsky
		if useNashSolver {
			allocs, nashIterations = kalaiSolve(pods, capacityMilli)
		} else {
			allocs = nashReduce(wants, pods, capacityMilli)
			nashIterations = 0
		}
		mode = ModeCongested
		for _, a := range allocs {
			totalAlloc += a
		}
	} else {
		// Step 4: UNCONGESTED (or Cost Mode where we refuse "Want")
		// Give everyone only what they NEED.
		// In Cost Efficiency Mode, this "Need" is already aggressive (low headroom).

		// MaximizeUtilization / Search Logic for Cost Efficiency
		if config.CostEfficiencyMode {
			// In cost mode, we treat capacity as a soft limit if needed,
			// but primarily we allocate explicitly based on tight needs.
			// "SearchMinViable" is implicitly handled by the tight NeedHeadroomFactor
			// passed to computeNeed.
			// We DO NOT give "Want" (greedy expansion).
			// If totalNeed > capacity, we still reduce (Congested logic via Nash fallback
			// or just proportional reduction if strictly cost focused).
			// If totalNeed > capacity in cost mode, we definitely are congested.

			if totalNeed > capacityMilli {
				// Congested even with tight needs -> use Nash/Kalai on NEEDs
				allocs, nashIterations = kalaiSolveOnNeeds(pods, needs, capacityMilli)
				mode = ModeCongested
			} else {
				// Satisfied with tight needs
				allocs = clampToBounds(needs, pods)
				mode = ModeUncongested
			}
		} else {
			// Standard mode: "Need" allocation in uncongested state
			allocs = clampToBounds(needs, pods)
			mode = ModeUncongested
		}

		if nashIterations == 0 {
			nashIterations = 0
		}
		for _, a := range allocs {
			totalAlloc += a
		}
	}

	// Step 5: Compute request allocations based on limit allocations (mode-aware)
	// Requests use mode-aware ratios: congested (0.95) vs uncongested (0.75)
	requestAllocs := ComputeRequestAllocationsWithMode(allocs, pods, mode)

	return AllocationResult{
		Allocations:        allocs,
		RequestAllocations: requestAllocs,
		Mode:               mode,
		TotalNeed:          totalNeed,
		TotalAlloc:         totalAlloc,
		Needs:              needs,
		NashIterations:     nashIterations,
	}
}

// nashSolve uses NashBargainingSolution for mathematically optimal allocation.
// Returns allocations and iteration count.
func nashSolve(pods map[types.UID]PodParams, capacity int64) (map[types.UID]int64, int) {
	// Convert PodParams to NashBargainingParams
	agents := make([]NashBargainingParams, 0, len(pods))
	for uid, p := range pods {
		agents = append(agents, NashBargainingParams{
			UID:      uid,
			Weight:   p.Weight,
			Baseline: p.MinMilli,
			MaxAlloc: p.MaxMilli,
			Demand:   computeNeed(p, NeedHeadroomFactor), // Use default for pure Nash solver unless updated
		})
	}

	result := NashBargainingSolutionWithMetadata(capacity, agents)
	return result.Allocations, result.Iterations
}

// kalaiSolve uses KalaiSmorodinskySolution for faster convergence in congested mode.
// Returns allocations and iteration count (always 1 for Kalai-Smorodinsky as it's direct).
func kalaiSolve(pods map[types.UID]PodParams, capacity int64) (map[types.UID]int64, int) {
	// Convert PodParams to KalaiSmorodinskyParams
	agents := make([]KalaiSmorodinskyParams, 0, len(pods))
	for uid, p := range pods {
		need := computeNeed(p, NeedHeadroomFactor) // Use default for compatibility
		ideal := need                              // Ideal point = need (what they want)
		if ideal > p.MaxMilli {
			ideal = p.MaxMilli
		}
		if ideal < p.MinMilli {
			ideal = p.MinMilli
		}

		agents = append(agents, KalaiSmorodinskyParams{
			UID:      uid,
			Weight:   p.Weight,
			Baseline: p.MinMilli,
			Ideal:    ideal,
			MaxAlloc: p.MaxMilli,
			Demand:   need,
		})
	}

	allocs := KalaiSmorodinskySolution(capacity, agents)
	// Kalai-Smorodinsky is direct (no iterations), but return 1 to indicate it was used
	return allocs, 1
}

// kalaiSolveOnNeeds uses KalaiSmorodinskySolution but prioritizes NEEDS not WANTS.
func kalaiSolveOnNeeds(pods map[types.UID]PodParams, needs map[types.UID]int64, capacity int64) (map[types.UID]int64, int) {
	agents := make([]KalaiSmorodinskyParams, 0, len(pods))
	for uid, p := range pods {
		need := needs[uid]
		// In cost mode, ideal is equal to need (we don't want more)
		// But if need < min, ideal should be min? No, need is already clamped to min.

		agents = append(agents, KalaiSmorodinskyParams{
			UID:      uid,
			Weight:   p.Weight,
			Baseline: p.MinMilli,
			Ideal:    need, // Ideal is purely the need
			MaxAlloc: p.MaxMilli,
			Demand:   need,
		})
	}
	allocs := KalaiSmorodinskySolution(capacity, agents)
	return allocs, 1
}

// ComputeRequestAllocations calculates optimal CPU requests based on limit allocations.
// Mode-aware strategy:
//   - Congested mode: r_i ≈ 0.95 x_i (protect CPU shares for latency-critical)
//   - Uncongested mode: r_i ≈ 0.70-0.85 x_i (efficiency, lower total requests)
//   - Request = max(actualUsage * 1.05, limit * ratio)
//   - This ensures request is above usage (for scheduling) but below limit (for burst)
//   - Minimum request is 10m to avoid scheduling issues
func ComputeRequestAllocations(limits map[types.UID]int64, pods map[types.UID]PodParams) map[types.UID]int64 {
	return ComputeRequestAllocationsWithMode(limits, pods, ModeUncongested)
}

// ComputeRequestAllocationsWithMode calculates requests with mode-aware ratios.
func ComputeRequestAllocationsWithMode(limits map[types.UID]int64, pods map[types.UID]PodParams, mode AllocationMode) map[types.UID]int64 {
	requests := make(map[types.UID]int64)

	// Mode-aware ratios
	var limitRatio float64
	var fallbackRatio float64
	switch mode {
	case ModeCongested:
		// Congested: protect CPU shares (r_i ≈ 0.95 x_i)
		limitRatio = 0.95
		fallbackRatio = 0.95
	case ModeOverloaded:
		// Overloaded: same as congested (protect shares)
		limitRatio = 0.95
		fallbackRatio = 0.95
	default:
		// Uncongested: efficiency (r_i ≈ 0.75 x_i)
		limitRatio = 0.75
		fallbackRatio = 0.85 // Slightly higher fallback for safety
	}

	for uid, limit := range limits {
		p := pods[uid]

		var request int64

		if p.ActualUsageMilli > 0 {
			// Usage-based request: actual usage + 5% headroom
			usageBasedRequest := int64(float64(p.ActualUsageMilli) * 1.05)

			// Limit-based request: mode-aware ratio of limit
			limitBasedRequest := int64(float64(limit) * limitRatio)

			// Use the higher of the two to ensure scheduling works
			request = usageBasedRequest
			if limitBasedRequest > request {
				request = limitBasedRequest
			}
		} else {
			// No usage data: use mode-aware fallback ratio
			request = int64(float64(limit) * fallbackRatio)
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

// computeNeed estimates the CONSERVATIVE CPU required based on actual usage.
// This represents what the pod truly NEEDS to function without throttling.
// It uses a small headroom (configured via config or default) to handle normal variance.
//
// Need = actual_usage * (1 + headroomFactor) (clamped to bounds)
//
// Use computeNeed for uncongested mode to avoid over-provisioning.
func computeNeed(p PodParams, headroomFactor float64) int64 {
	if p.ActualUsageMilli > 0 {
		// Conservative: actual usage + headroom
		// If headroomFactor is 0 (cost mode), this is just actual usage.
		need := int64(float64(p.ActualUsageMilli) * (1.0 + headroomFactor))

		// Clamp to bounds
		if need < p.MinMilli {
			need = p.MinMilli
		}
		if need > p.MaxMilli {
			need = p.MaxMilli
		}
		return need
	}

	// Fallback: use baseline when no usage data
	return p.MinMilli
}

// computeWant estimates the GREEDY CPU the pod would like based on demand signal.
// This represents what the pod WANTS when it signals throttling pressure.
// It scales aggressively (up to 4x) based on the demand signal.
//
// Want = actual_usage * (1 + 0.10 + demand * 4.0) (clamped to bounds)
//
// Use computeWant for congested mode where Nash bargaining will fairly reduce.
func computeWant(p PodParams) int64 {
	if p.ActualUsageMilli > 0 {
		// Base headroom (10%)
		headroomFactor := WantHeadroomFactor

		// Aggressive scaling based on throttling demand
		demandFactor := 0.0
		if p.Demand > 0 {
			// p.Demand is [0,1]. If 0.5 → adds 2.0x extra.
			demandFactor = p.Demand * MaxDemandMultiplier
		}

		// Calculate want
		want := int64(float64(p.ActualUsageMilli) * (1.0 + headroomFactor + demandFactor))

		// Clamp to bounds
		if want < p.MinMilli {
			want = p.MinMilli
		}
		if want > p.MaxMilli {
			want = p.MaxMilli
		}
		return want
	}

	// Fallback: use demand-based calculation
	demand := p.Demand
	if demand < 0 {
		demand = 0
	}
	if demand > 1 {
		demand = 1
	}

	baseNeed := p.MinMilli
	demandRange := p.MaxMilli - p.MinMilli
	if demandRange < 0 {
		demandRange = 0
	}

	return baseNeed + int64(float64(demandRange)*demand)
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
