# Phase 4: Market-Based Allocation Logic — Implementation Plan

## Objective

Replace Phase 3's simple proportional allocation with a **market-clearing allocation** that maximizes Nash Social Welfare using Fisher-market / proportional fairness, with proper weights/budgets and bounded redistribution.

## Key Insight

Phase 3 is already a form of proportional fairness (when mins are equal and no max caps bind). Phase 4 completes the mechanism by adding:
- **Weights/budgets** (wᵢ) from Kubernetes primitives
- **Hard max caps with correct redistribution** (water-filling)
- **Correct handling of degenerate cases** (zero bids, capacity constraints)

## Architecture

```
Node Agent (Phase 3)
  ↓
1. Sample demand (cgroup) → d_i ∈ [0,1]
2. Smooth demand (Tracker) → smoothed d_i
3. Compute market params (Calculator.ParamsForPod) → PodParams
   - Extract: request, limit, priority from pod
   - Compute: weight, bid, min, max
4. Clear market (allocation.ClearMarket) → allocations in millicores
5. Write PodAllocation CRDs
  ↓
Controller (Phase 2)
  - Reads PodAllocation
  - Applies with guardrails (cooldown, max-step)
```

## Implementation Structure

```
pkg/
  allocation/
    market.go          # ClearMarket() + helpers (all in one file)
    market_test.go     # Comprehensive tests
  
  agent/
    demand/
      calculator.go    # ParamsForPod() returns allocation.PodParams
```

## Market Parameters (Kubernetes-Native)

**For each pod (container[0]):**

1. **Demand** `d_i ∈ [0,1]`
   - From Phase 3: `Tracker.Current()`

2. **Weight/Budget** `w_i`
   - Default: `w_i = max(1, requestCPU_milli)`
   - Optional: multiply by PriorityClass factor
   - Uses existing K8s primitives (no annotations)

3. **Min Bound** `min_i`
   - `min_i = max(baselineMilli, requestCPU_milli)`
   - Ensures `limit >= request` (K8s constraint)

4. **Max Bound** `max_i`
   - `max_i = currentLimitCPU_milli` (if exists)
   - Else: `max_i = nodeCapacityMilli` (or per-pod cap)

## Market-Clearing Algorithm

**Input:**
- `capacityMilli int64` - Available CPU in millicores
- `pods map[UID]PodParams` - Market parameters per pod

**Algorithm (deterministic water-filling):**

```
Step 1: Handle minimums exceeding capacity
  totalMin = Σ min_i
  if totalMin > capacityMilli:
    scaleFactor = capacityMilli / totalMin
    for each pod i:
      min_i = min_i * scaleFactor  (scale down proportionally)
    remaining = 0
  else:
    for each pod i:
      alloc_i = min_i
    remaining = capacityMilli - totalMin

Step 2: Compute effective bids
  for each pod i:
    bid_i = w_i * d_i  (float64)

Step 3: Proportional distribution (in float64)
  totalBid = Σ bid_i
  
  if totalBid > 0:
    for each pod i:
      targetFloat_i = min_i + (bid_i / totalBid) * remaining
  else:
    // Zero-bid case: distribute remaining by weights (or equally)
    totalWeight = Σ w_i
    if totalWeight > 0:
      for each pod i:
        targetFloat_i = min_i + (w_i / totalWeight) * remaining
    else:
      // All weights zero: distribute equally
      for each pod i:
        targetFloat_i = min_i + (remaining / N)

Step 4: Enforce maximum bounds (water-filling)
  while any pod exceeds max:
    excess = 0
    for each pod i where targetFloat_i > max_i:
      excess += targetFloat_i - max_i
      targetFloat_i = max_i
    
    // Redistribute excess proportionally to uncapped pods
    uncappedBid = Σ bid_j for j where targetFloat_j < max_j
    if uncappedBid > 0:
      for each pod i where targetFloat_i < max_i:
        targetFloat_i += (bid_i / uncappedBid) * excess
    else:
      break  // All pods capped, excess remains unused

Step 5: Deterministic rounding to int64 millicores
  // Round fractional millicores by largest remainder
  // Use UID as stable tie-breaker for determinism
  for each pod i:
    alloc_i = round(targetFloat_i)  // int64 millicores
```

## Implementation Details

### 1. `pkg/allocation/market.go`

```go
package allocation

import (
    "sort"
    "k8s.io/apimachinery/pkg/types"
)

// PodParams represents market parameters for a pod.
type PodParams struct {
    Demand   float64  // Normalized demand [0,1]
    Bid      float64  // Effective bid (weight × demand)
    MinMilli int64    // Minimum CPU in millicores
    MaxMilli int64    // Maximum CPU in millicores
    Weight   float64  // Budget/weight (for zero-bid fallback)
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
func ClearMarket(capacityMilli int64, pods map[types.UID]PodParams) map[types.UID]int64 {
    // Implementation: all steps in one function with internal helpers
}
```

**Key implementation notes:**
- Bids computed in `float64` (for precision)
- Target allocations computed in `float64` (before capping)
- Rounding happens at the very end (after all capping/redistribution)
- Deterministic ordering: sort by UID for stable redistribution
- Largest-remainder rounding with UID tie-break

### 2. `pkg/agent/demand/calculator.go`

```go
package demand

import (
    "mbcas/pkg/allocation"
    corev1 "k8s.io/api/core/v1"
    "k8s.io/apimachinery/pkg/api/resource"
)

// ParamsForPod extracts market parameters from a pod using Kubernetes-native primitives.
// Uses container[0] to match controller behavior.
//
// Parameters:
//   - pod: The pod to extract parameters from
//   - demand: Normalized demand [0,1] from Tracker
//   - baselineMilli: Baseline CPU in millicores (prevents starvation)
//   - nodeCapMilli: Node capacity in millicores (fallback for max)
//
// Returns:
//   - PodParams with weight, bid, min, max computed from K8s primitives
func (c *Calculator) ParamsForPod(
    pod *corev1.Pod,
    demand float64,
    baselineMilli int64,
    nodeCapMilli int64,
) allocation.PodParams {
    // Extract container[0] resources
    // Compute weight = max(1, requestCPU_milli)
    // Compute bid = weight × demand
    // Compute minMilli = max(baselineMilli, requestCPU_milli)
    // Compute maxMilli = limitCPU_milli (or nodeCapMilli if no limit)
    // Return allocation.PodParams
}
```

### 3. Replace `computeAllocations()` in agent

**In `pkg/agent/agent.go`:**

```go
// Replace computeAllocations() with:
func (a *Agent) computeAllocations(demands map[types.UID]float64, availableCPU float64, podCount int) map[types.UID]string {
    // Convert availableCPU to millicores
    availableMilli := int64(availableCPU * 1000)
    
    // Build PodParams for each pod
    podParams := make(map[types.UID]allocation.PodParams)
    for uid, demand := range demands {
        pod := podMap[uid]  // Get pod from context
        params := a.demandCalc.ParamsForPod(pod, demand, baselineMilli, nodeCapMilli)
        podParams[uid] = params
    }
    
    // Clear market
    allocationsMilli := allocation.ClearMarket(availableMilli, podParams)
    
    // Convert to string format
    allocations := make(map[types.UID]string)
    for uid, milli := range allocationsMilli {
        allocations[uid] = fmt.Sprintf("%dm", milli)
    }
    
    return allocations
}
```

## Edge Cases & Correctness

### 1. Minimums exceed capacity
- **Policy**: Scale all minimums down proportionally by factor `capacityMilli / Σ min_i`
- **Result**: All pods get scaled-down minimum, no surplus
- **Deterministic**: Same scaling factor for all, UID ordering preserved

### 2. Zero bids (all demand = 0)
- **Policy**: Distribute remaining proportionally to weights (or equally if all weights equal)
- **Result**: Fair distribution even when idle, no wasted CPU
- **Rationale**: Preserves utilization and fairness

### 3. All pods hit maximum
- **Policy**: Cap all at max, excess remains unused
- **Result**: No redistribution possible, system at capacity
- **Note**: This is correct behavior (can't allocate more than max)

### 4. Some pods hit maximum
- **Policy**: Water-filling loop redistributes excess to uncapped pods
- **Result**: Efficient use of capacity, fairness maintained
- **Convergence**: Guaranteed (excess decreases each iteration)

### 5. Deterministic rounding
- **Method**: Largest remainder with UID tie-break
- **Result**: Reproducible allocations, no float drift
- **Precision**: 1 millicore resolution

## Testing Strategy

**Unit tests (`market_test.go`):**

1. **Invariants:**
   - `Σ allocations ≤ capacityMilli`
   - `min_i ≤ alloc_i ≤ max_i` for all pods
   - Allocations are non-negative integers

2. **Proportional fairness:**
   - For uncapped pods: `alloc_i / bid_i` is constant
   - Verify Nash Social Welfare property

3. **Edge cases:**
   - All zero demand → distribute by weights
   - Minimums exceed capacity → scaled down correctly
   - All pods hit max → all capped, excess unused
   - Some pods hit max → redistribution works
   - Single pod → gets min + remaining (if within max)

4. **Determinism:**
   - Same inputs → same outputs
   - UID ordering doesn't affect fairness
   - Rounding is stable

5. **Performance:**
   - Water-filling converges quickly (typically 1-2 iterations)
   - O(n²) worst case, O(n log n) typical

## Integration Points

**No changes needed:**
- Controller (Phase 2): Already has guardrails
- Agent sampling loop: Unchanged
- Demand smoothing: Unchanged

**Changes:**
- `demand.Calculator`: Implement `ParamsForPod()`
- `agent.computeAllocations()`: Replace with market clearing
- New: `allocation.ClearMarket()`

## Success Criteria

✅ Allocations maximize Nash Social Welfare (proportional fairness)  
✅ Uses only K8s-native primitives (requests, limits, PriorityClass)  
✅ Deterministic and reproducible (int64 millicores, stable ordering)  
✅ Fast (O(n²) worst case, typically O(n log n))  
✅ Handles all edge cases correctly  
✅ Drop-in replacement for Phase 3 allocation  
✅ No float drift in final allocations  
✅ Correct redistribution when caps bind  

## Implementation Order

1. Implement `allocation.PodParams` type
2. Implement `allocation.ClearMarket()` with all steps
3. Implement `demand.Calculator.ParamsForPod()`
4. Write comprehensive tests
5. Replace `computeAllocations()` in agent
6. Verify integration and behavior

