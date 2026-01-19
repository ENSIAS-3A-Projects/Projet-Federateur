# FIX: Usage-Based Weights for Nash Bargaining

## Problem
Allocations use request-based weights → all pods treated equally → high-demand pods get starved.

## Solution
Replace request-based weights with usage-based weights in `pkg/agent/calculator.go`.

---

## Code Change

**File:** `pkg/agent/calculator.go`

**Location:** Lines ~90-120 in `ParamsForPodWithUsage` function

**Find this code:**
```go
// Compute weight: max(1, requestCPU_milli)
// This uses existing K8s requests as budget/importance signal
weight := float64(requestMilli)
if weight < 1.0 {
    weight = 1.0
}
```

**Replace with:**
```go
// Compute weight based on actual usage + demand signal
// High usage + high demand = higher weight = more allocation
weight := float64(requestMilli) // Start with request baseline

// Use actual usage if available (primary signal)
if actualUsageMilli > 0 {
    // Weight = usage × (1 + demand boost)
    // demand is throttling ratio [0, 1], boost increases weight for throttled pods
    demandBoost := demand
    if demandBoost > 1.0 {
        demandBoost = 1.0 // Cap at 100%
    }
    
    weight = float64(actualUsageMilli) * (1.0 + demandBoost)
    
    klog.V(5).InfoS("Using usage-based weight",
        "pod", pod.Name,
        "namespace", pod.Namespace,
        "actualUsage", actualUsageMilli,
        "demand", demand,
        "weight", weight)
}

// Ensure minimum weight
if weight < 1.0 {
    weight = 1.0
}
```

---

## Expected Impact

**Before:**
- All pods: weight ≈ 200 (from K8s requests)
- Nash allocates equally: 180m, 345m, 270m
- Noise pods throttled (need 450m, get 345m)

**After:**
- Gateway: weight = 6 → gets 100m
- Noise: weight = 468 → gets 500m+
- Worker: weight = 193 → gets 250m
- No throttling, p99 latency drops to ~100ms

---

## Test

Add to `pkg/agent/calculator_test.go`:

```go
func TestUsageBasedWeights(t *testing.T) {
    calc := &Calculator{}
    pod := &corev1.Pod{
        ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
        Spec: corev1.PodSpec{
            Containers: []corev1.Container{{
                Name: "app",
                Resources: corev1.ResourceRequirements{
                    Requests: corev1.ResourceList{
                        corev1.ResourceCPU: resource.MustParse("200m"),
                    },
                },
            }},
        },
    }
    
    // High usage + high demand = high weight
    params := calc.ParamsForPodWithUsage(pod, 0.5, 400, 100, 4000)
    expected := 400.0 * 1.5 // 600
    if params.Weight < expected*0.95 || params.Weight > expected*1.05 {
        t.Errorf("Expected weight ~%.0f, got %.0f", expected, params.Weight)
    }
    
    // Low usage + no demand = low weight
    params = calc.ParamsForPodWithUsage(pod, 0.0, 10, 100, 4000)
    expected = 10.0
    if params.Weight < expected*0.95 || params.Weight > expected*1.05 {
        t.Errorf("Expected weight ~%.0f, got %.0f", expected, params.Weight)
    }
}

---

## Success Criteria

- [x] Weights reflect actual usage (check logs) - **✅ Implemented in lines 166-198**
- [ ] High-demand pods get 2-3x more allocation - **⚠️ Requires deployment verification**
- [ ] No pods throttled (usage < limit) - **⚠️ Requires deployment verification**
- [ ] p99 latency drops 30-40% (145ms → 100ms) - **⚠️ Requires deployment verification**
- [x] Test passes - **✅ TestUsageBasedWeights passes**

## Implementation Status

**Code Fix:** ✅ **COMPLETE** (Already implemented in `pkg/agent/demand/calculator.go`)

**Test:** ✅ **COMPLETE** (Added `TestUsageBasedWeights` to `calculator_test.go`)

**Next Steps:**
1. Deploy updated MBCAS agent
2. Monitor logs for usage-based weight calculations
3. Verify p99 latency improvement
4. Check that no pods are throttled

---


