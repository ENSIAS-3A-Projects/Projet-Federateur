# MBCAS Idle Pod Metrics Tracker

This set of scripts allows you to launch an overprovisioned idle pod and track how MBCAS reduces its resource allocation over time.

## Files

1. **track_idle_pod.sh** - Bash script for basic tracking
2. **track_idle_pod.py** - Python script with enhanced features
3. **visualize_metrics.py** - Visualization script for analyzing results

## Prerequisites

### For Bash Script (track_idle_pod.sh)
- `kubectl` configured to access your cluster
- MBCAS installed and running in the cluster
- `jq` for JSON processing (optional, for viewing results)
- `bc` for calculations (optional, for showing reduction percentage)

### For Python Scripts
- Python 3.7+
- `kubectl` configured
- For visualization: `matplotlib` library

```bash
pip install matplotlib
```

## Quick Start

### Option 1: Using Bash Script

```bash
# Make script executable
chmod +x track_idle_pod.sh

# Run with default settings (5 minutes, 5s interval)
./track_idle_pod.sh
```

The script will:
1. Create a namespace `mbcas-test`
2. Deploy an idle pod with 500m request, 1000m limit
3. Sample metrics every 5 seconds for 5 minutes
4. Save results to `idle_pod_metrics_TIMESTAMP.json`

### Option 2: Using Python Script

```bash
# Make script executable
chmod +x track_idle_pod.py

# Run with default settings
./track_idle_pod.py

# Run with custom settings
./track_idle_pod.py \
  --namespace my-test \
  --pod-name my-idle-pod \
  --duration 600 \
  --interval 10 \
  --initial-request 1000m \
  --initial-limit 2000m \
  --output my_results.json
```

### Option 3: Keep Pod Running for Manual Inspection

```bash
# Keep the pod running after the test
./track_idle_pod.py --no-cleanup

# Then inspect it manually
kubectl get pod idle-overprovisioned -n mbcas-test
kubectl get podallocation idle-overprovisioned -n mbcas-test -o yaml
```

## Analyzing Results

### View Raw JSON Data

```bash
# Pretty-print JSON
cat idle_pod_metrics_*.json | jq .

# Extract specific metrics
cat idle_pod_metrics_*.json | jq '.samples[] | {time: .elapsed_seconds, limit: .limit_milli, usage: .usage_milli}'

# Show allocation changes
cat idle_pod_metrics_*.json | jq '.samples[] | select(.limit_milli != .request_milli)'
```

### Visualize Results

```bash
# Create plots and show statistics
./visualize_metrics.py idle_pod_metrics_20260123_120000.json

# Save plot to file instead of showing interactively
./visualize_metrics.py idle_pod_metrics_20260123_120000.json --output plot.png

# Only show statistics (no plot)
./visualize_metrics.py idle_pod_metrics_20260123_120000.json --stats-only
```

The visualization creates three plots:
1. **Resource Allocation Over Time** - Shows limit, request, and actual usage
2. **Allocation Adjustments** - Step chart showing when MBCAS made changes
3. **CPU Utilization** - Efficiency percentage (usage/limit)

## Expected Behavior

For an **idle overprovisioned pod** (1000m limit, ~1m actual usage), MBCAS should:

1. **Initial State** (T=0-15s):
   - Pod starts with 1000m limit
   - Very low CPU usage (~1-5m)
   
2. **First Allocation** (T=15-30s):
   - MBCAS detects low usage
   - Reduces allocation significantly (e.g., 1000m → 200m)
   
3. **Convergence** (T=30-120s):
   - Multiple adjustments downward
   - Approaches optimal allocation (~10-20m for truly idle)
   
4. **Stable State** (T=120s+):
   - Small oscillations around optimal value
   - Usually settles at 10-30m for idle workload

## Understanding the Metrics

### Pod Metrics
- **Request**: Kubernetes resource request (scheduling guarantee)
- **Limit**: Kubernetes resource limit (hard cap)
- **Usage**: Actual CPU usage from metrics-server

### PodAllocation Metrics
- **PA Limit**: MBCAS's desired CPU limit (from PodAllocation CR)
- **PA Status**: Applied, Pending, or Failed
- **Shadow Price**: Economic signal (0 = uncongested)

### Key Observations

**Good Behavior:**
- Limit decreases over time for idle pod
- Final limit close to actual usage (10-30m for idle)
- 80-95% resource reduction
- Converges in 60-120 seconds

**Potential Issues:**
- Limit stays at 1000m (MBCAS not running or pod not labeled)
- Frequent oscillations (need to tune minChangePercent)
- PA Status always "Pending" (controller issue)

## Troubleshooting

### Pod Not Tracked by MBCAS

**Symptom**: Limit never changes from initial 1000m

**Check:**
```bash
# Is MBCAS running?
kubectl get pods -n mbcas-system

# Is namespace labeled?
kubectl get namespace mbcas-test -o yaml | grep mbcas.io/managed

# Is pod labeled?
kubectl get pod idle-overprovisioned -n mbcas-test -o yaml | grep mbcas.io/managed

# Check MBCAS agent logs
kubectl logs -n mbcas-system -l app=mbcas-agent --tail=50
```

### Metrics-Server Not Available

**Symptom**: Usage shows "N/A"

**Solution:**
```bash
# Install metrics-server
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml

# For Minikube
minikube addons enable metrics-server
```

### PodAllocation Not Created

**Symptom**: PA Limit shows "N/A"

**Check:**
```bash
# List all PodAllocations
kubectl get podallocation -n mbcas-test

# Check agent logs
kubectl logs -n mbcas-system -l app=mbcas-agent | grep idle-overprovisioned
```

## Cleanup

```bash
# Delete the test pod
kubectl delete pod idle-overprovisioned -n mbcas-test

# Delete the entire test namespace
kubectl delete namespace mbcas-test

# Clean up output files
rm idle_pod_metrics_*.json
rm idle_pod_metrics_*_plot.png
```

## Advanced Usage

### Test Different Initial Allocations

```bash
# Test with very overprovisioned pod (10x over actual usage)
./track_idle_pod.py --initial-limit 5000m --duration 600

# Test with moderately overprovisioned pod
./track_idle_pod.py --initial-limit 500m --duration 300

# Test with barely overprovisioned pod
./track_idle_pod.py --initial-limit 100m --duration 180
```

### Compare with VPA

To compare MBCAS with VPA behavior, you can:

1. Run the test with MBCAS (as shown above)
2. Disable MBCAS for the namespace
3. Install and configure VPA
4. Run a similar test
5. Compare the results

```bash
# Disable MBCAS for namespace
kubectl label namespace mbcas-test mbcas.io/managed=false --overwrite

# Create VPA for the pod
kubectl apply -f - <<EOF
apiVersion: autoscaling.k8s.io/v1
kind: VerticalPodAutoscaler
metadata:
  name: idle-overprovisioned-vpa
  namespace: mbcas-test
spec:
  targetRef:
    apiVersion: v1
    kind: Pod
    name: idle-overprovisioned
  updatePolicy:
    updateMode: "Auto"
EOF
```

## Example Output

```
========================================
MBCAS Idle Pod Metrics Tracker
========================================
Namespace:       mbcas-test
Pod:             idle-overprovisioned
Duration:        300s (5.0 minutes)
Sample interval: 5s
Initial request: 500m
Initial limit:   1000m

Creating namespace mbcas-test...
✓ Namespace created and labeled
Creating pod idle-overprovisioned...
✓ Pod created
✓ Pod is ready!

Starting metrics collection...
Time  | Request | Limit   | Usage   | PA Limit | Shadow$ | Status
------|---------|---------|---------|----------|---------|-------
005s  | 500m    | 1000m   | 2m      | N/A      | N/A     | N/A
010s  | 500m    | 1000m   | 1m      | N/A      | N/A     | N/A
015s  | 500m    | 1000m   | 2m      | N/A      | N/A     | N/A
020s  | 450m    | 500m    | 1m      | 500m     | 0.0     | Applied
025s  | 225m    | 250m    | 2m      | 250m     | 0.0     | Applied
030s  | 112m    | 125m    | 1m      | 125m     | 0.0     | Applied
035s  | 56m     | 63m     | 2m      | 63m      | 0.0     | Applied
...
295s  | 18m     | 20m     | 1m      | 20m      | 0.0     | Applied
300s  | 18m     | 20m     | 2m      | 20m      | 0.0     | Applied

✓ Results saved to: idle_pod_metrics_20260123_120000.json

Summary:
  Initial Request: 500m (500m)
  Initial Limit:   1000m (1000m)
  Final Request:   18m (18m)
  Final Limit:     20m (20m)
  Final Usage:     2m (2m)

Resource Reduction:
  Absolute: 980m
  Percentage: 98.0%

Metrics:
  Total Samples: 60
  Allocation Changes: 8
  Time to First Change: 20s
  Average Usage: 1.5m
```

## Notes

- The first allocation change typically occurs after the first SlowLoopInterval (default 15s)
- Resource reduction for truly idle pods should be 95-99%
- Convergence time depends on SlowLoopInterval and smoothing factors
- Shadow price should be 0.0 for uncongested clusters

## Interpreting Results

### Healthy MBCAS Behavior

- **Fast initial response**: First change within 15-30s
- **Progressive reduction**: Multiple steps toward optimal
- **Good final efficiency**: 80-95% utilization (usage/limit)
- **Stability**: Minimal oscillations after convergence

### Potential Tuning Needed

- **Too aggressive**: Changes every cycle → increase minChangePercent
- **Too slow**: Takes >5 minutes → decrease SlowLoopInterval
- **Oscillating**: Bouncing between values → increase smoothing alpha
- **Underprovisioned**: Usage > Limit → check for throttling detection

## Related Files

For benchmark comparison with full workload mix, see:
- `test/benchmark/mbcas_benchmark.go`
- `test/benchmark/vpa_benchmark.go`
