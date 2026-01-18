# VPA vs MBCAS Comparison Test Suite

This test suite provides comprehensive comparison testing between VPA (Vertical Pod Autoscaler) and MBCAS (Multi-Bargaining CPU Allocation System).

## Test Structure

### Workloads

The test suite deploys three sets of identical workloads:

1. **Baseline** (`workloads-baseline.yaml`): Static CPU limits, no autoscaling
2. **VPA** (`workloads-vpa.yaml`): Managed by VPA with `updateMode: Auto`
3. **MBCAS** (`workloads-mbcas.yaml`): Managed by MBCAS with `mbcas.io/managed: "true"` label

Each set includes:
- **Gateway**: Routes requests to workers (latency-sensitive)
- **Worker A**: Steady-state deterministic load (30ms CPU per request)
- **Worker B**: Bursty load with 20% spike probability (3x multiplier)
- **Noise**: Background load generators (2 replicas, 80% CPU intensity)

### Test Scenarios

The stress generator runs four phases:

1. **Warmup** (2 minutes, 10 RPS): Low load to establish baseline
2. **Saturation** (5 minutes, 50 RPS): Moderate load to test steady-state
3. **Contention** (5 minutes, 100 RPS): High load to trigger resource contention
4. **Bursty** (3 minutes, 50 RPS base, spikes to 100 RPS): Variable load with spikes

Total test duration: ~15 minutes

## Key Metrics Collected

### Pod Metrics
- CPU requests and limits
- Memory requests and limits
- Actual CPU and memory usage
- Pod restart counts
- Pod phase (Running, Pending, etc.)

### VPA Metrics
- VPA recommendations (target, lowerBound, upperBound, uncappedTarget)
- VPA conditions (RecommendationProvided, etc.)
- Update mode and resource policies

### MBCAS Metrics
- PodAllocation resources (desired CPU, applied CPU)
- Allocation phase and reason
- Shadow prices (Lagrange multipliers)
- Allocation history over time

### System Metrics
- Node CPU and memory usage
- Overall cluster resource utilization

## Prerequisites

1. **Minikube** running with:
   - `InPlacePodVerticalScaling` feature gate enabled
   - `metrics-server` addon enabled
   - MBCAS deployed (agent + controller)

2. **Docker** for building images

3. **kubectl** configured for Minikube

4. **VPA** components installed (recommender, updater, admission-controller)

## Usage

### Quick Start

```powershell
# Run the comparison test (default 15 minutes)
.\scripts\run-vpa-mbcas-comparison.ps1

# Custom test duration (in minutes)
.\scripts\run-vpa-mbcas-comparison.ps1 -TestDurationMinutes 20

# Skip image build (use existing)
.\scripts\run-vpa-mbcas-comparison.ps1 -SkipBuild

# Keep resources after test (for manual inspection)
.\scripts\run-vpa-mbcas-comparison.ps1 -SkipCleanup
```

### Manual Deployment

If you prefer to deploy manually:

```powershell
# 1. Create namespace
kubectl apply -f k8s/vpa-mbcas-test/namespace.yaml

# 2. Build and load eval-service
cd apps/eval-service
docker build -t eval-service:latest .
minikube image load eval-service:latest
cd ../..

# 3. Deploy workloads
kubectl apply -f k8s/vpa-mbcas-test/workloads-baseline.yaml
kubectl apply -f k8s/vpa-mbcas-test/workloads-vpa.yaml
kubectl apply -f k8s/vpa-mbcas-test/workloads-mbcas.yaml

# 4. Deploy VPA configs
kubectl apply -f k8s/vpa-mbcas-test/vpa-configs.yaml

# 5. Deploy stress generator
kubectl apply -f k8s/vpa-mbcas-test/stress-generator.yaml

# 6. Monitor
kubectl get pods -n vpa-mbcas-test -w
kubectl get vpa -n vpa-mbcas-test
kubectl get podallocations -n vpa-mbcas-test
```

## Results

Test results are saved to `results/vpa-mbcas-comparison/<timestamp>/`:

```
results/vpa-mbcas-comparison/
└── 20240116-123456/
    ├── comparison-report.json          # Comprehensive comparison report
    ├── baseline-initial.json           # Baseline metrics at start
    ├── baseline-final.json             # Baseline metrics at end
    ├── baseline-collection-*.json      # Baseline metrics per collection
    ├── vpa-initial.json                 # VPA metrics at start
    ├── vpa-final.json                   # VPA metrics at end
    ├── vpa-collection-*.json            # VPA metrics per collection
    ├── vpa-recommendations-*.json       # VPA recommendations per collection
    ├── mbcas-initial.json               # MBCAS metrics at start
    ├── mbcas-final.json                 # MBCAS metrics at end
    ├── mbcas-collection-*.json          # MBCAS metrics per collection
    ├── mbcas-allocations-*.json         # MBCAS PodAllocations per collection
    └── system-*.json                    # System-wide metrics per collection
```

### Understanding Results

**comparison-report.json** contains:
- Test metadata (duration, intervals, timestamps)
- Summary statistics (pod counts, restart counts, VPA/MBCAS resource counts)
- All collection data over time
- Final state metrics

**Key Comparisons**:

1. **Response Time**: How quickly each system adapts to load changes
   - MBCAS: 1-2s (fast guardrail) to 5-15s (slow optimizer)
   - VPA: Minutes to hours (historical analysis)

2. **Pod Restarts**: 
   - MBCAS: Zero (in-place updates)
   - VPA: Restarts pods to apply new limits

3. **Fairness During Contention**:
   - MBCAS: Nash bargaining ensures fair allocation
   - VPA: Based on historical usage patterns

4. **Resource Efficiency**:
   - Compare CPU utilization across all three scenarios
   - Check for over-provisioning or under-provisioning

## Test Scenarios Highlights

### Scenario 1: Steady-State (Saturation Phase)
- **MBCAS Advantage**: Real-time demand sensing adapts quickly
- **VPA Advantage**: Conservative recommendations based on history

### Scenario 2: Bursty Load (Bursty Phase)
- **MBCAS Advantage**: Fast guardrail (1-2s) handles spikes immediately
- **VPA Limitation**: Slow to react to sudden spikes

### Scenario 3: Resource Contention (Contention Phase)
- **MBCAS Advantage**: Nash bargaining ensures fair distribution
- **VPA Limitation**: May favor pods with higher historical usage

### Scenario 4: Zero-Downtime Updates
- **MBCAS Advantage**: In-place updates, no pod restarts
- **VPA Limitation**: Pod restarts required for updates

## Troubleshooting

### Pods not starting
```powershell
kubectl get pods -n vpa-mbcas-test
kubectl describe pod <pod-name> -n vpa-mbcas-test
kubectl logs <pod-name> -n vpa-mbcas-test
```

### VPA not providing recommendations
```powershell
kubectl get vpa -n vpa-mbcas-test
kubectl describe vpa <vpa-name> -n vpa-mbcas-test
kubectl logs -n kube-system -l app=vpa-recommender
```

### MBCAS not creating PodAllocations
```powershell
kubectl get podallocations -n vpa-mbcas-test
kubectl logs -n mbcas-system -l app.kubernetes.io/component=agent
kubectl get pods -n vpa-mbcas-test -l mbcas.io/managed=true
```

### Metrics not available
```powershell
# Check metrics-server
kubectl get deployment metrics-server -n kube-system
kubectl top nodes
kubectl top pods -n vpa-mbcas-test
```

## Cleanup

```powershell
# Delete test namespace (removes all resources)
kubectl delete namespace vpa-mbcas-test

# Or use the script with cleanup
.\scripts\run-vpa-mbcas-comparison.ps1 -SkipCleanup:$false
```
