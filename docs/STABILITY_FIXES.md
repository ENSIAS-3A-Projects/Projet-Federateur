# MBCAS Stability Fixes - Critical Oscillation Issues

## Problem Analysis

Based on benchmark metrics and code review, the allocator exhibited severe instability:

### Symptoms Observed:
1. **Massive oscillations**: Allocations jumping from hundreds of mCPU to tens of cores and back
2. **Runaway growth**: Desired allocations growing uncontrollably (e.g., bursty pod: final_limit=4500m, avg_usage=756m)
3. **Positive feedback loop**: Throttling signals causing exponential demand amplification
4. **Slow actuation**: Controller lag causing reactions to stale state
5. **Only idle workloads stable**: Confirms algorithmic issue, not infrastructure

### Root Causes Identified:

#### 1. **Unbounded MaxBid (CRITICAL)**
```go
// BEFORE (pod_agent.go:310-311)
if pa.Throttling > 0.05 {
    maxBid = 1000000  // 1000 cores! Completely unbounded
}
```
**Impact**: Nash bargaining could allocate up to 1000 cores per pod when throttling detected.

#### 2. **Unbounded Throttling Amplification**
```go
// BEFORE (pod_agent.go:284-286)
if pa.Throttling > 0.05 {
    demand = int64(float64(demand) * (1.0 + pa.Throttling*2))
    // With 50% throttling: demand *= 2.0x
    // With 100% throttling: demand *= 3.0x (unbounded)
}
```
**Impact**: Positive feedback loop - more throttling → higher demand → more allocation → potentially more throttling.

#### 3. **No Smoothing on Allocations**
```go
// BEFORE (agent.go:apply)
a.lastAllocations[pod.UID] = allocMilli  // Direct assignment, no smoothing
```
**Impact**: Every Nash bargaining result immediately written, causing oscillations.

#### 4. **No Bounds Checking**
No absolute maximum cap on allocations before writing to PodAllocation CR.

## Fixes Implemented

### Fix 1: Cap MaxBid to Reasonable Values
**File**: `pkg/agent/pod_agent.go:309-327`

```go
// AFTER
if pa.Throttling > 0.05 {
    // Cap maxBid to prevent runaway growth
    usageBasedMax := int64(float64(pa.Usage) * 10.0)  // 10x usage
    absoluteMax := int64(10000)  // 10 cores = 10000m
    maxBid = usageBasedMax
    if maxBid > absoluteMax {
        maxBid = absoluteMax
    }
    if maxBid < minBid+100 {
        maxBid = minBid + 100
    }
} else {
    // ... existing logic ...
    // Also cap non-throttled maxBid
    absoluteMax := int64(10000)
    if maxBid > absoluteMax {
        maxBid = absoluteMax
    }
}
```

**Result**: Max allocation per pod capped at 10 cores (10000m), preventing runaway growth.

### Fix 2: Cap Throttling Amplification
**File**: `pkg/agent/pod_agent.go:283-293`

```go
// AFTER
if pa.Throttling > 0.05 {
    // Cap throttling amplification to prevent runaway growth
    throttlingMultiplier := 1.0 + pa.Throttling*2.0
    if throttlingMultiplier > 3.0 {  // Max 3x amplification
        throttlingMultiplier = 3.0
    }
    demand = int64(float64(demand) * throttlingMultiplier)
    // Additional cap: never exceed 10x base usage
    maxDemand := baseDemand * 10
    if demand > maxDemand {
        demand = maxDemand
    }
}
```

**Result**: Throttling amplification capped at 3x, with additional 10x usage cap.

### Fix 3: Exponential Smoothing on Allocations
**File**: `pkg/agent/agent.go:374-432`

```go
// AFTER
// Exponential smoothing to prevent oscillations
lastSmoothed := a.smoothedAllocations[pod.UID]
if lastSmoothed == 0 {
    lastSmoothed = allocMilli  // Initialize
}

var smoothedAlloc int64
if allocMilli < lastSmoothed {
    // Going down: fast smoothing (alpha = 0.7)
    smoothedAlloc = int64(0.7*float64(allocMilli) + 0.3*float64(lastSmoothed))
} else {
    // Going up: slow smoothing (alpha = 0.2) to prevent overshoot
    smoothedAlloc = int64(0.2*float64(allocMilli) + 0.8*float64(lastSmoothed))
}

a.smoothedAllocations[pod.UID] = smoothedAlloc
// Use smoothedAlloc instead of allocMilli for writing
```

**Result**: Asymmetric smoothing (fast down, slow up) prevents oscillations and overshoot.

### Fix 4: Absolute Bounds Checking
**File**: `pkg/agent/agent.go:381-386`

```go
// AFTER
// CRITICAL FIX: Cap allocation to prevent runaway growth
const absoluteMaxAlloc = int64(10000)  // 10 cores
if allocMilli > absoluteMaxAlloc {
    klog.V(2).InfoS("Capping allocation to absolute max", 
        "pod", pod.Name, "requested", allocMilli, "capped", absoluteMaxAlloc)
    allocMilli = absoluteMaxAlloc
}
```

**Result**: Final safety net - no allocation can exceed 10 cores per pod.

## Expected Improvements

1. **Stability**: Allocations should stabilize within reasonable bounds
2. **No Runaway Growth**: Maximum allocation capped at 10 cores per pod
3. **Reduced Oscillations**: Exponential smoothing dampens rapid changes
4. **Better Responsiveness**: Asymmetric smoothing allows fast downscaling while preventing overshoot on upscaling

## Testing Recommendations

1. **Re-run benchmarks** with fixed code
2. **Monitor allocation_history** in metrics - should show smooth transitions
3. **Check for oscillations** - allocations should converge, not oscillate
4. **Verify caps** - no allocation should exceed 10 cores per pod
5. **Compare efficiency** - should maintain or improve CPU efficiency while stabilizing

## Remaining Considerations

### Future Improvements (Not Critical):
1. **Use Applied Allocation**: Currently using `lastAllocations` (desired), should read from PodAllocation status to get actual applied allocation
2. **Adaptive Smoothing**: Adjust smoothing factors based on workload characteristics
3. **Predictive Bounds**: Use usage patterns to set per-pod maximums dynamically

## Metrics to Monitor

- **Allocation Stability**: Standard deviation of allocation changes
- **Oscillation Frequency**: Number of direction reversals per hour
- **Overshoot Ratio**: (Max allocation - Usage) / Usage
- **Convergence Time**: Time to reach stable allocation after workload change
