# Demand-Capped Allocator Specification

## Overview

This document specifies a **minimal modification** to MBCAS that eliminates the
"allocate full capacity" behavior while preserving the existing architecture.

The change is localized to **one function**: `ClearMarket()` in `pkg/allocation/market.go`.

---

## Design Principles

1. **Demand-capped by default**: Allocate `need + headroom`, never more.
2. **Fairness only under contention**: Game theory kicks in only when `Σ need > capacity`.
3. **Reactive correctness**: Throttling signal remains authoritative; prediction is optional.
4. **Incremental adoption**: No new dependencies, no eBPF, no external metrics.

---

## Allocator Contract

### Inputs

```go
type AllocationInput struct {
    CapacityMilli int64                    // Node allocatable CPU (after system reserve)
    Pods          map[types.UID]PodParams  // Per-pod parameters
}

type PodParams struct {
    // From existing MBCAS
    Demand   float64  // Smoothed throttling signal [0,1]
    MinMilli int64    // Floor (baseline or request)
    MaxMilli int64    // Ceiling (limit or node cap)
    Weight   float64  // Budget/priority (from request CPU)
    
    // NEW: Estimated need (computed from demand signal)
    NeedMilli int64   // Estimated CPU to eliminate throttling
}
```

### Outputs

```go
type AllocationOutput struct {
    Allocations map[types.UID]int64  // CPU in millicores per pod
    Mode        AllocationMode       // Which path was taken
}

type AllocationMode string

const (
    ModeUncongested AllocationMode = "uncongested"  // Σ need <= capacity
    ModeCongested   AllocationMode = "congested"    // Σ need > capacity, fairness applied
    ModeOverloaded  AllocationMode = "overloaded"   // Σ min > capacity, baselines scaled
)
```

### Invariants (Must Always Hold)

```
INV-1: ∀ pod: MinMilli <= Allocation[pod] <= MaxMilli
INV-2: Σ Allocation <= CapacityMilli
INV-3: If Mode == Uncongested: Allocation[pod] == NeedMilli (clamped to bounds)
INV-4: If Mode == Congested: Allocation satisfies Nash proportional reduction
INV-5: Deterministic: Same inputs → same outputs (no randomness)
```

---

## State Machine

```
                    ┌─────────────────────────────────────────┐
                    │            COMPUTE NEED                 │
                    │  For each pod:                          │
                    │    NeedMilli = computeNeed(demand)      │
                    └─────────────────┬───────────────────────┘
                                      │
                                      ▼
                    ┌─────────────────────────────────────────┐
                    │          CHECK TOTAL MINIMUMS           │
                    │  totalMin = Σ MinMilli                  │
                    └─────────────────┬───────────────────────┘
                                      │
                         ┌────────────┴────────────┐
                         │                         │
                    totalMin > capacity       totalMin <= capacity
                         │                         │
                         ▼                         ▼
              ┌──────────────────┐    ┌─────────────────────────────┐
              │    OVERLOADED    │    │      CHECK TOTAL NEED       │
              │ Scale baselines  │    │  totalNeed = Σ NeedMilli    │
              │ proportionally   │    └──────────────┬──────────────┘
              └──────────────────┘                   │
                                        ┌───────────┴───────────┐
                                        │                       │
                                   totalNeed <= capacity   totalNeed > capacity
                                        │                       │
                                        ▼                       ▼
                             ┌──────────────────┐    ┌──────────────────┐
                             │   UNCONGESTED    │    │    CONGESTED     │
                             │ Allocate need    │    │ Nash reduction   │
                             │ (clamped)        │    │ of surplus       │
                             └──────────────────┘    └──────────────────┘
```

---

## Algorithm: `ClearMarket()` v2

```go
// ClearMarket allocates CPU using demand-capped allocation.
//
// Behavior:
//   - Uncongested (common): Each pod gets NeedMilli (clamped to bounds)
//   - Congested (rare): Nash bargaining reduces surplus proportionally
//   - Overloaded (emergency): Baselines scaled down to fit capacity
func ClearMarket(capacityMilli int64, pods map[types.UID]PodParams) map[types.UID]int64 {
    if len(pods) == 0 {
        return make(map[types.UID]int64)
    }

    // Step 1: Compute need for each pod
    needs := make(map[types.UID]int64)
    for uid, p := range pods {
        needs[uid] = computeNeed(p)
    }

    // Step 2: Check if baselines exceed capacity (overloaded)
    totalMin := int64(0)
    for _, p := range pods {
        totalMin += p.MinMilli
    }
    
    if totalMin > capacityMilli {
        return scaleBaselines(pods, capacityMilli)
    }

    // Step 3: Check if needs exceed capacity (congested)
    totalNeed := int64(0)
    for _, n := range needs {
        totalNeed += n
    }

    if totalNeed <= capacityMilli {
        // UNCONGESTED: Give everyone what they need
        return clampToBouns(needs, pods)
    }

    // CONGESTED: Apply Nash bargaining reduction
    return nashReduce(needs, pods, capacityMilli)
}
```

---

## Need Estimation

### Core Formula

```go
// computeNeed estimates CPU required to eliminate throttling.
//
// Logic:
//   demand = 0.0 → pod is not throttling → need = current usage + small buffer
//   demand = 1.0 → pod is severely throttling → need = current limit (or higher)
//
// The key insight: demand signal tells us "how much MORE does this pod want?"
// We translate that into an absolute CPU number.
func computeNeed(p PodParams) int64 {
    // Base need: at minimum, pod needs its baseline
    baseNeed := p.MinMilli
    
    // Demand-driven component: how much above baseline?
    // demand=0 → no additional need
    // demand=1 → wants up to MaxMilli
    demandRange := p.MaxMilli - p.MinMilli
    demandComponent := int64(float64(demandRange) * p.Demand)
    
    rawNeed := baseNeed + demandComponent
    
    // Add headroom based on demand volatility
    // Higher demand → more headroom (pod is under stress)
    headroomFactor := 0.10 + (p.Demand * 0.15)  // 10-25% headroom
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
```

### Why This Works

| Demand | Interpretation | Need Calculation |
|--------|---------------|------------------|
| 0.0 | Not throttling at all | `MinMilli + 10% headroom` |
| 0.5 | Moderate throttling | `MinMilli + 50% of range + 17.5% headroom` |
| 1.0 | Severe throttling | `MaxMilli` (give everything available) |

This maps the existing demand signal directly to a "need" value without requiring new metrics.

---

## Nash Bargaining Reduction

When `Σ need > capacity`, we reduce each pod's surplus proportionally.

```go
// nashReduce applies Nash Bargaining Solution for fair reduction.
//
// Algorithm:
//   1. Everyone keeps their baseline (MinMilli)
//   2. Surplus demand (need - baseline) is reduced proportionally
//   3. Reduction ratio = available_surplus / total_surplus_demand
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
    
    // Available for surplus distribution
    availableSurplus := capacity - totalBaseline
    if availableSurplus < 0 {
        // Should not happen (checked earlier), but handle gracefully
        return scaleBaselines(pods, capacity)
    }
    
    // Reduction ratio
    var ratio float64
    if totalSurplus > 0 {
        ratio = float64(availableSurplus) / float64(totalSurplus)
        if ratio > 1.0 {
            ratio = 1.0  // Don't inflate
        }
    } else {
        ratio = 0.0
    }
    
    // Allocate: baseline + reduced surplus
    for uid, p := range pods {
        allocation := p.MinMilli + int64(float64(surpluses[uid])*ratio)
        
        // Clamp to bounds (defensive)
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
```

---

## Baseline Scaling (Emergency Mode)

When even baselines exceed capacity, scale them proportionally.

```go
// scaleBaselines scales down baselines when Σ MinMilli > capacity.
// This is a last-resort mode indicating severe over-commitment.
func scaleBaselines(pods map[types.UID]PodParams, capacity int64) map[types.UID]int64 {
    allocations := make(map[types.UID]int64)
    
    totalMin := int64(0)
    for _, p := range pods {
        totalMin += p.MinMilli
    }
    
    scale := float64(capacity) / float64(totalMin)
    
    for uid, p := range pods {
        allocation := int64(float64(p.MinMilli) * scale)
        
        // Enforce absolute minimum (prevent starvation)
        absoluteMin := int64(10)  // 10m = survival threshold
        if allocation < absoluteMin {
            allocation = absoluteMin
        }
        
        allocations[uid] = allocation
    }
    
    return allocations
}
```

---

## Helper: Clamp to Bounds

```go
// clampToBounds ensures all allocations respect pod min/max.
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
```

---

## Integration with Existing MBCAS

### Changes Required

| File | Change |
|------|--------|
| `pkg/allocation/market.go` | Replace `ClearMarket()` with v2 above |
| `pkg/agent/agent.go` | No changes (already computes demand, calls ClearMarket) |
| `pkg/controller/*` | No changes (already applies allocations) |

### Migration Path

1. **Week 1**: Implement `computeNeed()` and `nashReduce()` functions
2. **Week 2**: Replace `ClearMarket()`, add tests
3. **Week 3**: Deploy, observe, tune headroom factors

---

## Observability

### New Metrics

```prometheus
# Which allocation mode was used
apex_allocation_mode{node, mode} 1  # mode: uncongested, congested, overloaded

# Need vs allocation (efficiency indicator)
apex_need_milli{pod, namespace}      # What pod needs
apex_allocation_milli{pod, namespace} # What pod got

# Surplus reduction under contention
apex_nash_reduction_ratio{pod, namespace}  # (need - allocation) / (need - baseline)

# Headroom utilization (is headroom being used or wasted?)
apex_headroom_utilization{pod, namespace}  # actual_usage / allocation
```

### Expected Behavior

| Scenario | Mode | Observation |
|----------|------|-------------|
| Light load | Uncongested | Most pods at MinMilli + small headroom |
| Normal load | Uncongested | Pods at demand-driven need |
| High load | Congested | Nash reduction visible, all pods reduced proportionally |
| Overcommitted | Overloaded | Baselines scaled, alarms should fire |

---

## Failure Modes and Mitigations

### Failure 1: Demand signal is zero when pod is actually throttling

**Cause**: Cgroup detection failed (the bug we fixed earlier)
**Detection**: `apex_need_milli` stuck at `MinMilli` while pod CPU usage is high
**Mitigation**: Fall back to usage-based estimation

```go
func computeNeed(p PodParams) int64 {
    // If demand signal seems broken, use usage as fallback
    if p.Demand == 0 && p.CurrentUsageMilli > p.MinMilli*0.9 {
        // Pod is using 90%+ of baseline but showing no demand
        // This suggests broken demand signal; use usage + buffer
        return int64(float64(p.CurrentUsageMilli) * 1.3)
    }
    
    // Normal calculation...
}
```

### Failure 2: Headroom too small, pod still throttles

**Cause**: Demand volatility higher than expected
**Detection**: `demand > 0.3` sustained after allocation
**Mitigation**: Adaptive headroom

```go
// Track headroom misses per pod
if postAllocationDemand > 0.3 {
    pod.HeadroomMisses++
}

// Increase headroom for pods that consistently miss
baseHeadroom := 0.10
adaptiveHeadroom := baseHeadroom + float64(pod.HeadroomMisses)*0.05
// Cap at 50%
if adaptiveHeadroom > 0.50 {
    adaptiveHeadroom = 0.50
}
```

### Failure 3: Oscillation between congested/uncongested modes

**Cause**: Total need hovering around capacity
**Detection**: Mode flapping in logs
**Mitigation**: Hysteresis band

```go
const hysteresisBand = 0.05  // 5%

if totalNeed <= capacityMilli*(1-hysteresisBand) {
    return modeUncongested
} else if totalNeed >= capacityMilli*(1+hysteresisBand) {
    return modeCongested
} else {
    // In hysteresis band: keep previous mode
    return previousMode
}
```

---

## Testing Strategy

### Unit Tests

```go
func TestClearMarket_Uncongested(t *testing.T) {
    // 2 pods, total need < capacity
    // Verify: each gets exactly need (clamped)
}

func TestClearMarket_Congested(t *testing.T) {
    // 2 pods, total need > capacity
    // Verify: Nash reduction applied, baselines preserved
}

func TestClearMarket_Overloaded(t *testing.T) {
    // 2 pods, total baselines > capacity
    // Verify: baselines scaled, no pod gets zero
}

func TestComputeNeed_DemandMapping(t *testing.T) {
    // Verify demand=0 → min+headroom
    // Verify demand=1 → max
    // Verify demand=0.5 → midpoint with headroom
}

func TestNashReduce_Proportionality(t *testing.T) {
    // Verify equal surplus → equal reduction
    // Verify unequal surplus → proportional reduction
}
```

### Integration Tests

```go
func TestEndToEnd_LoadSpike(t *testing.T) {
    // 1. Start with low-demand pods
    // 2. Verify: uncongested mode, minimal allocation
    // 3. Inject load, demand increases
    // 4. Verify: allocation increases to meet need
    // 5. Remove load
    // 6. Verify: allocation decreases back
}

func TestEndToEnd_Contention(t *testing.T) {
    // 1. Create pods with total need > node capacity
    // 2. Verify: congested mode, Nash reduction
    // 3. Remove one pod
    // 4. Verify: mode switches to uncongested
}
```

---

## Summary

This spec provides:

1. **Contract**: Clear inputs, outputs, and invariants
2. **State Machine**: Three modes (uncongested, congested, overloaded)
3. **Algorithm**: `computeNeed()` + `nashReduce()` + `scaleBaselines()`
4. **Integration**: Drop-in replacement for existing `ClearMarket()`
5. **Observability**: Metrics to verify behavior
6. **Failure Handling**: Fallbacks for broken signals, adaptive headroom
7. **Testing**: Unit and integration test strategy

No new dependencies. No eBPF. No external metrics. Just a smarter allocator.