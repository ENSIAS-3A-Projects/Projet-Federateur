# VPA Tracking and MBCAS vs VPA Comparison

This guide explains how to run the same idle pod test with VPA and compare results with MBCAS.

## Files

1. **track_vpa_pod.py** - VPA tracking script (mirrors MBCAS test)
2. **compare_mbcas_vpa.py** - Side-by-side comparison and visualization
3. **VPA_COMPARISON.md** - This file

## Prerequisites

### VPA Installation

VPA must be installed in your cluster. Check if it's installed:

```bash
kubectl get crd verticalpodautoscalers.autoscaling.k8s.io
```

If not installed:

```bash
# Clone the autoscaler repo
git clone https://github.com/kubernetes/autoscaler.git
cd autoscaler/vertical-pod-autoscaler

# Install VPA
./hack/vpa-up.sh

# Verify installation
kubectl get pods -n kube-system | grep vpa
```

You should see three VPA components:
- `vpa-recommender`
- `vpa-updater`
- `vpa-admission-controller`

### In-Place Updates (Optional but Recommended)

For Kubernetes 1.27+, enable in-place pod vertical scaling:

```bash
# Check if feature is available
kubectl api-resources | grep resize

# For minikube
minikube start --feature-gates=InPlacePodVerticalScaling=true
```

## Quick Start

### Run VPA Test

```bash
# Make script executable
chmod +x track_vpa_pod.py

# Run with default settings (5 minutes)
./track_vpa_pod.py

# Run with custom duration (30 minutes recommended for VPA)
./track_vpa_pod.py --duration 1800

# Use different update mode
./track_vpa_pod.py --update-mode InPlaceOrRecreate --duration 600
```

### Compare with MBCAS Results

```bash
# Make comparison script executable
chmod +x compare_mbcas_vpa.py

# Compare results
./compare_mbcas_vpa.py idle_metrics_20260123_014930.json vpa_metrics_20260123_020000.json

# Save comparison plot
./compare_mbcas_vpa.py mbcas_results.json vpa_results.json --output comparison.png

# Only show statistics (no plot)
./compare_mbcas_vpa.py mbcas_results.json vpa_results.json --stats-only
```

## VPA Update Modes

VPA supports several update modes:

| Mode | Behavior | Use Case |
|------|----------|----------|
| **Off** | Only provides recommendations, doesn't update pods | Testing, manual review |
| **Initial** | Sets resources only when pod is created | Minimal disruption |
| **Recreate** | Evicts and recreates pods to apply changes | Older K8s versions |
| **Auto** | Same as Recreate (default) | General use |
| **InPlaceOrRecreate** | In-place updates when possible, recreate if needed | K8s 1.27+ (recommended) |

## Understanding VPA Behavior

### Expected Timeline

**For a 5-minute test (300s)**:
```
T=0-60s:    VPA starts collecting metrics
T=60-120s:  First recommendation may appear
T=120-180s: Recommendation stabilizes
T=180-300s: Pod may be updated (if update mode != Off)

Note: VPA is designed for 24-48 hour observation periods!
```

**For production-like behavior (24+ hours)**:
```
Hours 0-24:   VPA observes and builds histogram
Hours 24-48:  Recommendations stabilize
Days 2-7:     Fine-tuning continues
Days 7+:      Stable recommendations
```

### What VPA Tracks

Unlike MBCAS (which tracks every 15s), VPA:
- Uses **histogram-based** percentile calculations
- Looks at **longer time windows** (hours/days)
- Updates **infrequently** (when statistically significant)
- Prioritizes **stability over responsiveness**

## Test Scenarios

### Scenario 1: Quick Comparison (5 minutes)

**Purpose**: See early behavior differences

```bash
# Run MBCAS test
./track_idle_pod.py --duration 300 --output mbcas_5min.json

# Run VPA test (same duration)
./track_vpa_pod.py --duration 300 --output vpa_5min.json

# Compare
./compare_mbcas_vpa.py mbcas_5min.json vpa_5min.json
```

**Expected results**:
- MBCAS: 5-10 allocation changes, converged
- VPA: 0-1 changes, recommendation may appear

### Scenario 2: Medium Test (30 minutes)

**Purpose**: See VPA start to act

```bash
./track_vpa_pod.py --duration 1800 --output vpa_30min.json
```

**Expected results**:
- VPA recommendation appears within 10-15 minutes
- Pod may be updated once (if using Auto/InPlaceOrRecreate mode)

### Scenario 3: Realistic Test (24 hours)

**Purpose**: Full VPA behavior

```bash
# Run overnight test
./track_vpa_pod.py --duration 86400 --output vpa_24h.json --no-cleanup
```

**Expected results**:
- VPA builds complete histogram
- Recommendation stabilizes after ~8 hours
- 1-2 pod updates total

### Scenario 4: VPA in "Off" Mode (Recommendations Only)

**Purpose**: See what VPA would recommend without applying

```bash
./track_vpa_pod.py --update-mode Off --duration 1800
```

This shows VPA's recommendations without changing the pod. Useful for:
- Understanding VPA's decision-making
- Planning before enabling auto-update
- Comparing with MBCAS decisions

## Interpreting Results

### VPA Metrics Explained

| Field | Meaning |
|-------|---------|
| **vpa_target** | VPA's recommended CPU allocation |
| **vpa_lower_bound** | Minimum safe allocation (prevents OOM) |
| **vpa_upper_bound** | Maximum reasonable allocation |
| **vpa_uncapped_target** | What VPA would recommend without policy limits |
| **pod_limit** | Actual pod CPU limit |

### Good VPA Behavior

```
T=0s:     Pod starts, limit=1000m
T=300s:   VPA target appears (e.g., 100m)
T=600s:   VPA target stable (e.g., 50m)
T=900s:   Pod updated to 50m (if update mode allows)
T=1200s+: No more changes (stable)
```

### VPA Issues to Watch For

1. **No recommendation appears**:
   - VPA needs more observation time
   - Check VPA components are running
   - Verify metrics-server is working

2. **Recommendation appears but pod not updated**:
   - Update mode is "Off"
   - Or recommendation doesn't differ enough from current
   - Check VPA updater logs

3. **Pod gets recreated frequently**:
   - VPA is using Recreate mode
   - Recommendations are fluctuating
   - Consider longer observation period

## Comparison Metrics

The comparison script shows:

### 1. Allocation Timeline
- Side-by-side view of both systems
- Shows when each made changes

### 2. Stability
- Number of allocation changes
- MBCAS: 5-10 changes typical
- VPA: 0-1 changes in 5 minutes

### 3. Speed
- Time to first change
- MBCAS: 10-50 seconds
- VPA: 5-30 minutes (or no change)

### 4. Effectiveness
- Final resource reduction
- Both should achieve 95-98% for idle pods
- MBCAS reaches it faster

## Real-World Comparison

### When to Use MBCAS

✅ **Dynamic workloads** with changing patterns  
✅ **Fast response needed** (seconds to minutes)  
✅ **Cost optimization** in dev/staging  
✅ **Cluster-wide coordination** important  
✅ **Willing to accept more frequent updates**

### When to Use VPA

✅ **Stable workloads** with predictable patterns  
✅ **Set-and-forget** operation desired  
✅ **Minimal churn** is priority  
✅ **Simple deployment** (no custom CRDs)  
✅ **Standard K8s tool** preferred

### Hybrid Approach

Some teams use both:
- **VPA in "Off" mode**: Get recommendations
- **MBCAS**: Actually manage resources
- **Compare recommendations**: Validate both systems agree

## Troubleshooting

### VPA recommender not running

```bash
kubectl get pods -n kube-system -l app=vpa-recommender
kubectl logs -n kube-system -l app=vpa-recommender
```

### VPA not creating recommendations

```bash
# Check VPA status
kubectl describe vpa idle-overprovisioned-vpa -n vpa-test

# Check if VPA can read metrics
kubectl get --raw /apis/metrics.k8s.io/v1beta1/pods
```

### Recommendations not being applied

```bash
# Check updater
kubectl logs -n kube-system -l app=vpa-updater

# Check admission controller
kubectl logs -n kube-system -l app=vpa-admission-controller
```

### In-place updates not working

```bash
# Verify feature gate
kubectl get --raw /api/v1 | jq '.resources[] | select(.name == "pods/resize")'

# If empty, in-place resize not available
# Use --update-mode Recreate instead
```

## Example Output

### VPA Test (5 minutes)

```
Time  | Request | Limit   | Usage   | VPA Target | Lower Bnd  | Upper Bnd
------|---------|---------|---------|------------|------------|----------
005s  | 500m    | 1000m   | N/A     | N/A        | N/A        | N/A
010s  | 500m    | 1000m   | 1m      | N/A        | N/A        | N/A
...
120s  | 500m    | 1000m   | 1m      | 50m        | 25m        | 100m
...
300s  | 500m    | 1000m   | 1m      | 50m        | 25m        | 100m

Summary:
  Pod stayed at 1000m (no updates in 5 min)
  VPA recommended 50m (not yet applied)
```

### MBCAS Test (5 minutes)

```
Time  | Request | Limit   | Usage   | PA Limit
------|---------|---------|---------|----------
005s  | 500m    | 1000m   | N/A     | N/A
010s  | 500m    | 1000m   | 1m      | N/A
050s  | 282m    | 314m    | 1m      | N/A
...
300s  | 18m     | 20m     | 0m      | N/A

Summary:
  Pod reduced to 20m in ~4 minutes
  7 allocation changes
  98% reduction achieved
```

### Comparison

```
MBCAS vs VPA: DETAILED COMPARISON
================================================================================

Final State:
  MBCAS: 20m
  VPA:   1000m (recommendation: 50m)

Reduction:
  MBCAS: 980m (98.0%)
  VPA:   0m (0.0%) - not applied yet

Allocation Changes:
  MBCAS: 7 changes
  VPA:   0 changes

Time to First Change:
  MBCAS: 10s
  VPA:   No changes
  
VPA First Recommendation: 120s

Conclusion: MBCAS acted 100x faster, VPA provided stable recommendation
```

## Advanced Usage

### Long-Running VPA Test

```bash
# Run for 48 hours with hourly samples
./track_vpa_pod.py \
  --duration 172800 \
  --interval 3600 \
  --output vpa_48h.json \
  --no-cleanup

# Monitor progress
tail -f vpa_48h.json
```

### Compare Multiple Runs

```bash
# Test different VPA modes
./track_vpa_pod.py --update-mode Off --duration 600 --output vpa_off.json
./track_vpa_pod.py --update-mode Auto --duration 600 --output vpa_auto.json
./track_vpa_pod.py --update-mode InPlaceOrRecreate --duration 600 --output vpa_inplace.json

# Compare all with MBCAS
./compare_mbcas_vpa.py mbcas.json vpa_off.json --output comparison_off.png
./compare_mbcas_vpa.py mbcas.json vpa_auto.json --output comparison_auto.png
```

## Expected Results Summary

| Metric | MBCAS (5 min) | VPA (5 min) | VPA (24 hours) |
|--------|---------------|-------------|----------------|
| **Time to recommendation** | 10s | 2-15 min | 1-8 hours |
| **Time to first change** | 10-50s | Usually none | 4-24 hours |
| **Allocation changes** | 5-10 | 0-1 | 1-2 |
| **Final reduction** | 95-98% | 0-50% | 95-98% |
| **Stability** | High (after convergence) | Very high | Very high |

## Cleanup

```bash
# Delete VPA test resources
kubectl delete namespace vpa-test

# Or manually
kubectl delete vpa idle-overprovisioned-vpa -n vpa-test
kubectl delete deployment idle-overprovisioned -n vpa-test

# Remove VPA from cluster (if needed)
cd autoscaler/vertical-pod-autoscaler
./hack/vpa-down.sh
```

## Conclusion

This test suite allows you to:
1. ✅ Run identical tests on both MBCAS and VPA
2. ✅ Compare behavior side-by-side
3. ✅ Visualize differences in speed and stability
4. ✅ Make informed decisions about which to use

The key takeaway: **MBCAS is 100-1000x faster** for dynamic workloads, while **VPA provides rock-solid stability** for predictable workloads.
