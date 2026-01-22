# Benchmark Metrics Analysis - Pre-Fix Behavior

## Executive Summary

The benchmark metrics reveal **severe instability** in MBCAS, confirming the algorithmic issues identified. While MBCAS achieved higher CPU efficiency (70% vs 27%), this came at the cost of:
- **Massive oscillations** (300 allocation changes in 10 minutes)
- **Severe over-provisioning** (6.38x average)
- **Unstable allocations** that never converged
- **Runaway growth** in some workloads

## Key Metrics Breakdown

### 1. Allocation Instability

**MBCAS**: 300 allocation changes across 8 pods = **37.5 changes per pod** in 10 minutes
- **Rate**: ~3.75 changes per pod per minute = **1 change every 16 seconds**
- **Pattern**: Continuous oscillation, never stabilizing

**VPA**: 0 allocation changes
- **Reason**: VPA didn't apply recommendations (likely UpdateMode issue)
- **Impact**: Not a fair comparison, but shows MBCAS was hyperactive

**Analysis**: The 37.5 changes/pod indicates the system was in constant flux, reacting to every throttling signal and creating a feedback loop.

### 2. Over-Provisioning Analysis

**MBCAS Average**: 6.38x over-provisioning
**VPA Average**: 4.85x over-provisioning

**Workload-Specific Examples**:

| Workload | Final Limit | Avg Usage | Over-Provisioning | Status |
|----------|-------------|------------|-------------------|--------|
| **bursty** | 4500m | 756m | **5.95x** | Severe over-allocation |
| **steady-high** | 2029m | 1747m | **1.16x** | Reasonable |
| **throttle-prone** | 278m | 3594m | **0.08x** | SEVERELY UNDER-allocated! |
| **idle** | 28m | 0.97m | **28.9x** | Acceptable (tiny absolute values) |
| **spiky** | 855m | 684m | **1.25x** | Reasonable |
| **ramping** | 1029m | 903m | **1.14x** | Reasonable |

**Critical Finding**: The `throttle-prone` workload shows the **worst case**:
- Final limit: **278m**
- Average usage: **3594m** (12.9x higher!)
- Max usage: **17098m** (61x higher!)
- This pod was **severely throttled** throughout the test

**Root Cause**: The oscillation prevented the system from ever converging to the correct allocation. When throttling was detected, it would spike allocation, but then immediately drop it back down, creating a sawtooth pattern.

### 3. CPU Efficiency Paradox

**MBCAS**: 70.37% efficiency
**VPA**: 26.90% efficiency

**Why MBCAS appears better**:
- MBCAS was allocating **much more total CPU** (over-provisioning)
- Efficiency = Used / Allocated
- When you allocate 6.38x what's needed, even if you use it inefficiently, the ratio looks good
- **This is misleading** - it's like saying "I'm efficient because I bought 10 cars and use 7 of them"

**Reality Check**:
- MBCAS was wasting **29.6%** of allocated CPU (still significant)
- VPA was wasting **73.1%** (but wasn't actually allocating, so this metric is invalid)
- The real issue: MBCAS was allocating **way too much** to achieve that efficiency

### 4. Allocation History Patterns

Looking at `bursty` workload allocation history:
```
Time 00:17:08: limit=589m,  usage=176m  (3.3x)
Time 00:17:23: limit=3512m, usage=1053m (3.3x)  ← SPIKE
Time 00:17:28: limit=531m,  usage=159m  (3.3x)  ← DROP
Time 00:17:38: limit=1896m, usage=568m  (3.3x)  ← SPIKE
Time 00:17:48: limit=666m,  usage=999m  (0.67x) ← UNDER-allocated!
```

**Pattern Observed**:
1. **Oscillation**: Wild swings between high and low allocations
2. **Delayed Response**: Allocation changes lag behind usage spikes
3. **Overcorrection**: When throttling detected, massive spike, then immediate drop
4. **Never Converges**: No stable allocation reached

### 5. Throttling Metrics

**Both systems identical**:
- Avg throttling: 0.0625 (6.25%)
- Max throttling: 0.4444 (44.44%)
- Total throttling duration: 695 seconds
- Underprovisioning events: 139

**Analysis**: Despite MBCAS's hyperactive allocation changes, throttling was **not reduced**. This proves:
- The oscillations were **not solving the problem**
- The system was reacting to throttling but creating new problems
- The positive feedback loop was preventing effective allocation

### 6. Time to Stability

**MBCAS**:
- Avg time to first allocation: **22.8 seconds** (good responsiveness)
- Avg time to stable allocation: **77.6 seconds** (but never actually stabilized!)

**Analysis**: The "stable allocation" metric is misleading - allocations never truly stabilized, they just oscillated around a range.

## Root Cause Analysis

### The Positive Feedback Loop

1. **Throttling Detected** → Agent increases demand
2. **Unbounded MaxBid** → Nash bargaining allocates massive amount (up to 1000 cores!)
3. **Allocation Applied** → Pod gets huge allocation
4. **Usage Doesn't Match** → Pod uses less than allocated
5. **Next Cycle** → Agent sees low usage, reduces demand
6. **Overshoot Down** → Allocation drops too low
7. **Throttling Returns** → Back to step 1

### Why Idle Workloads Were Stable

- **Low usage** → No throttling signals
- **No throttling** → No amplification
- **No amplification** → No oscillation
- **Conclusion**: The instability was **throttling-driven**

### Why Throttle-Prone Failed Worst

- **Designed to exceed limits** → Constant throttling
- **Constant throttling** → Constant amplification
- **Constant amplification** → Runaway growth attempts
- **Controller safety limits** → Prevents extreme changes
- **Result**: Oscillation between controller's min/max bounds

## Comparison with VPA

**VPA Results** (though not fully functional):
- 0 allocation changes (didn't apply recommendations)
- Lower over-provisioning (4.85x vs 6.38x) - but this is misleading since it didn't change
- Higher system overhead (150m CPU, 384MiB memory vs 80m, 112MiB)

**Key Insight**: VPA's "stability" (0 changes) was actually a **failure to act**, not true stability. However, this does highlight that **doing nothing** can be better than **doing the wrong thing repeatedly**.

## What the Metrics Tell Us

### Positive Signs:
1. ✅ **Responsive**: 22.8s to first allocation (good)
2. ✅ **Low overhead**: 80m CPU, 112MiB memory (excellent)
3. ✅ **Some workloads reasonable**: steady-high, spiky, ramping had ~1.2x over-provisioning

### Critical Problems:
1. ❌ **Unstable**: 37.5 changes/pod indicates oscillation
2. ❌ **Over-provisioning**: 6.38x average is excessive
3. ❌ **Runaway growth**: bursty pod reached 4500m limit
4. ❌ **Severe under-allocation**: throttle-prone pod at 278m vs 3594m usage
5. ❌ **Never converges**: No stable allocation achieved

## Expected Improvements After Fixes

With the stability fixes applied:

1. **Reduced Oscillations**: Exponential smoothing should reduce changes from 37.5/pod to <10/pod
2. **Bounded Growth**: Max allocation capped at 10 cores should prevent runaway growth
3. **Better Convergence**: Asymmetric smoothing should allow stable allocations
4. **Reduced Over-provisioning**: Should drop from 6.38x to <3x average
5. **Fixed Under-allocation**: Throttle-prone should converge to ~4000m instead of oscillating

## Recommendations

1. **Re-run benchmarks** with fixes to validate improvements
2. **Monitor allocation_history** - should show smooth transitions, not sawtooth patterns
3. **Track convergence time** - allocations should stabilize within 2-3 minutes
4. **Verify caps** - no allocation should exceed 10 cores
5. **Compare efficiency** - should maintain or improve while stabilizing

## Conclusion

The metrics confirm the algorithmic instability issues:
- **The system was working** (responding to throttling, allocating resources)
- **But working incorrectly** (oscillating, over-allocating, never converging)
- **The fixes address root causes** (bounded growth, smoothing, caps)
- **Expected outcome**: Stable, efficient allocations that converge to optimal values

The benchmark was **valuable** - it revealed critical issues that would have caused production problems. The fixes should transform this from an unstable system into a production-ready allocator.
