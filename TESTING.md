# MBCAS Testing Guide

This document describes how to test the MBCAS (CoAllocator) system, including basic functionality verification, performance benchmarking, and comparison with baseline configurations.

## Overview

MBCAS is a game-theoretic CPU allocation system for Kubernetes that uses Nash Bargaining to fairly distribute CPU resources. Testing validates:

1. **Basic Functionality**: System deploys correctly and allocates CPU based on demand
2. **Performance**: Maintains good latency and lower resource usage than VPA
3. **Fairness**: Nash bargaining ensures fair allocation during contention

## Prerequisites

- Minikube installed and running
- Docker installed
- Go 1.21+ installed
- kubectl configured for Minikube
- PowerShell (for Windows) or Bash (for Linux/Mac)

## Quick Start

### Running E2E Tests From Scratch (Nothing Running)

If you have **nothing running** (no Minikube, no MBCAS, fresh start), here's the complete workflow:

#### Complete Workflow (2 Steps)

**Step 1**: Start Minikube
```powershell
.\scripts\start_minikube.ps1
```

**Step 2**: Run E2E Tests (handles everything else automatically)
```powershell
.\scripts\run_e2e_test.ps1
```

That's it! The E2E script will:
- ✅ Build eval-service image
- ✅ Remove any existing MBCAS (for clean baseline)
- ✅ Run baseline test
- ✅ Deploy MBCAS
- ✅ Run MBCAS test
- ✅ Generate comparison report

**Total time**: ~5-10 minutes

**Results**: Check `results/` directory for comparison reports

---

### Detailed Explanation

If you want to understand what each step does:

#### Step 1: Start Minikube with Feature Gates

```powershell
.\scripts\start_minikube.ps1
```

This starts Minikube with `InPlacePodVerticalScaling` feature gate enabled, which is required for in-place pod resource updates.

**What it does**:
- Checks if Minikube is running
- Stops any existing Minikube instance (if needed)
- Starts Minikube with required feature gates
- Enables metrics-server addon
- Verifies `pod/resize` subresource is available

**Expected output**: Minikube cluster is running and ready

#### Step 2: Run End-to-End Tests (All-in-One)

```powershell
.\scripts\run_e2e_test.ps1
```

This script handles **everything automatically**:
1. ✅ **Pre-flight checks** (verifies minikube, kubectl, docker, go are installed)
2. ✅ **Builds eval-service** image (required for test workloads)
3. ✅ **Removes MBCAS** (if deployed) to ensure clean baseline
4. ✅ **Runs baseline test** (static limits, no MBCAS)
5. ✅ **Deploys MBCAS** (builds images, deploys CRD, RBAC, agent, controller)
6. ✅ **Runs MBCAS test** (dynamic allocation with MBCAS)
7. ✅ **Generates comparison report** (JSON + Markdown)

**Note**: You don't need to manually build or deploy anything - the script does it all!

**Expected duration**: ~5-10 minutes (depending on build times and test duration)

**Results location**: `results/` directory in project root

### Manual Step-by-Step (Optional)

If you prefer to run steps manually or troubleshoot:

#### 1. Start Minikube

```powershell
.\scripts\start_minikube.ps1
```

#### 2. Build and Deploy MBCAS

```powershell
.\scripts\build_and_deploy.ps1
```

This builds the agent and controller Docker images, loads them into Minikube, and deploys all MBCAS components.

#### 3. Build Evaluation Service

```powershell
.\scripts\build_eval_service.ps1
```

This builds the eval-service image used by test workloads.

#### 4. Run End-to-End Tests

```powershell
.\scripts\run_e2e_test.ps1
```

**Note**: If you run steps 2-3 manually, the E2E script will still rebuild/redeploy to ensure everything is up-to-date.

## Test Scenarios

### Baseline Test (Mode A: Static)

- **Configuration**: `k8s/evaluation/mode-a-static/workloads.yaml`
- **Description**: Workloads run with static CPU limits (no dynamic allocation)
- **Purpose**: Establish baseline performance metrics for comparison

**Workloads**:
- Gateway: Routes requests (100m request, 200m limit)
- Worker A: Deterministic load (500m request, 1000m limit, 30ms CPU)
- Worker B: Bursty load (500m request, 1000m limit, 20% spike probability)
- Noise: Background load (2 replicas, 80% CPU intensity each)

### MBCAS Test (Mode C: CoAllocator)

- **Configuration**: `k8s/evaluation/mode-c-coallocator/workloads.yaml`
- **Description**: Workloads managed by MBCAS with dynamic CPU allocation
- **Purpose**: Validate MBCAS allocation behavior and performance

**Key Differences**:
- Pods have `mbcas.io/managed: "true"` label
- MBCAS agent senses demand and creates PodAllocations
- MBCAS controller applies CPU limits in-place (no restarts)
- Allocations adapt to throttling signals

## Test Flow

The E2E test script (`run_e2e_test.ps1`) orchestrates the following:

1. **Baseline Test**:
   - **Remove MBCAS** (if deployed) to ensure clean baseline
   - Deploy workloads (mode-a-static)
   - Wait for pods to be Running
   - **Automatically start monitoring**
   - Run load generation (warmup → saturation → contention → bursty)
   - Collect metrics
   - Save results to `results/baseline/`
   - Cleanup workloads

2. **MBCAS Test**:
   - **Deploy MBCAS** (agent + controller)
   - Wait for MBCAS to stabilize
   - Deploy workloads (mode-c-coallocator)
   - Wait for pods to be Running
   - Wait for PodAllocations to be created
   - **Automatically start monitoring**
   - Run load generation (same pattern as baseline)
   - Collect metrics
   - Save results to `results/mbcas/`
   - Cleanup workloads

3. **Comparison Report**:
   - Compare metrics from both scenarios
   - Generate JSON and Markdown reports
   - Highlight key differences

## Monitoring

Monitoring starts automatically after workloads are Running. It collects:

### Prometheus Metrics

- `mbcas_allocation_compute_duration_seconds` - Allocation computation time
- `mbcas_pod_discovery_duration_seconds` - Pod discovery time
- `mbcas_cgroup_read_duration_seconds` - Cgroup read time
- `mbcas_allocation_milli` - Current CPU allocations per pod
- `mbcas_demand_raw` / `mbcas_demand_smoothed` - Demand signals
- `mbcas_utility` - Pod satisfaction ratio
- `mbcas_allocation_mode` - Current mode (uncongested/congested/overloaded)

### Application Metrics

- Request latency (p50, p95, p99) from loadgen
- Request throughput (RPS)
- Error rates

### Kubernetes Metrics

- Pod CPU usage (from metrics-server)
- Pod CPU limits/requests
- Pod restart count (should be 0 for MBCAS)

### PodAllocation Status

- Desired vs applied CPU limits
- Allocation phase (Pending/Applied/Failed)
- Timestamps for allocation events

## Expected Behaviors

### Basic Functionality

1. **Agent Behavior**:
   - Reads cgroup throttling metrics every 1 second
   - Discovers pods on the node (using informer cache)
   - Calculates demand from throttling signals
   - Creates/updates PodAllocation CRDs every 15 seconds

2. **Controller Behavior**:
   - Watches PodAllocation CRDs
   - Patches pod CPU limits via `pods/resize` subresource
   - Updates PodAllocation status
   - No pod restarts occur during allocation changes

3. **Allocation Modes**:
   - **Uncongested**: Total need ≤ capacity → everyone gets what they need
   - **Congested**: Total need > capacity → Nash bargaining applied
   - **Overloaded**: Total baselines > capacity → emergency baseline scaling

### Performance Targets

- **Latency**: Application p95 latency < 100ms (or matches baseline)
- **Allocation Latency**: Time from demand signal to applied limit < 20 seconds
- **Resource Overhead**: MBCAS agent + controller CPU < 200m total
- **Resource Efficiency**: CPU utilization > 80% (less waste than static limits)

### MBCAS Advantages vs Baseline

- **Zero Restarts**: MBCAS updates CPU limits in-place (no pod restarts)
- **Faster Response**: Adapts to demand changes within 15-20 seconds
- **Fair Allocation**: Nash bargaining ensures fair distribution during contention
- **Better Utilization**: Allocates based on actual demand, reducing waste

## Load Test Phases

The load generator runs four phases:

1. **Warmup** (2 minutes, 10 RPS): Low load to establish baseline
2. **Saturation** (5 minutes, 50 RPS): Moderate load to test steady-state
3. **Contention** (5 minutes, 100 RPS): High load to trigger resource contention
4. **Bursty** (3 minutes, 50 RPS base, spikes to 100 RPS): Bursty load to test spike handling

Note: Default test duration is scaled to 10% (0.1x) for faster testing. Adjust `-scale` parameter in `run_e2e_test.ps1` to change.

## Results

Test results are saved in the `results/` directory:

```
results/
├── baseline/
│   ├── loadgen_results.json      # Load test metrics (latency, throughput)
│   ├── loadgen_results.csv       # CSV export of load test data
│   ├── metrics.json              # Kubernetes and Prometheus metrics
│   ├── metrics.csv               # CSV export of metrics
│   └── podallocations.json       # PodAllocation history (empty for baseline)
├── mbcas/
│   ├── loadgen_results.json
│   ├── loadgen_results.csv
│   ├── metrics.json
│   ├── metrics.csv
│   └── podallocations.json       # PodAllocation history
├── comparison_report.json        # JSON comparison report
└── comparison_report.md          # Markdown comparison report
```

### Understanding Results

**Load Test Results** (`loadgen_results.json`):
- Per-phase latency percentiles (p50, p95, p99)
- Throughput (RPS)
- Error rates
- Request counts

**Metrics** (`metrics.json`):
- Pod CPU usage over time
- Pod restart counts
- MBCAS component resource usage
- PodAllocation status history

**Comparison Report**:
- Side-by-side comparison of baseline vs MBCAS
- Latency differences
- Resource overhead analysis
- Pod restart comparison

## Troubleshooting

### Pods Not Starting

**Symptom**: Pods stuck in Pending or CrashLoopBackOff

**Solutions**:
- Check node resources: `kubectl describe node`
- Check pod events: `kubectl describe pod <pod-name> -n evaluation`
- Verify eval-service image exists: `docker images | grep eval-service`
- Rebuild eval-service: `.\scripts\build_eval_service.ps1`

### PodAllocations Not Created

**Symptom**: No PodAllocations appear in MBCAS test

**Solutions**:
- Check agent logs: `kubectl logs -n mbcas-system -l app.kubernetes.io/component=agent`
- Verify pods have `mbcas.io/managed: "true"` label
- Check agent can read cgroups (requires root/SYS_ADMIN)
- Verify agent is running: `kubectl get pods -n mbcas-system`

### High Latency

**Symptom**: Application latency is higher than expected

**Solutions**:
- Check if pods are being throttled: `kubectl top pods -n evaluation`
- Verify CPU limits are appropriate
- Check for resource contention: `kubectl describe node`
- Review allocation mode: Check `mbcas_allocation_mode` metric

### Monitoring Not Collecting Data

**Symptom**: Metrics files are empty or missing

**Solutions**:
- Verify monitoring job completed: Check PowerShell job status
- Check metrics-server is enabled: `minikube addons list | grep metrics-server`
- Verify port-forward connections (if used)
- Check output directory permissions

### Load Test Fails

**Symptom**: Loadgen returns errors or no requests succeed

**Solutions**:
- Verify gateway service is accessible
- Check service URL: `minikube service gateway -n evaluation --url`
- Test manually: `curl http://<gateway-url>/gateway`
- Check gateway pod logs: `kubectl logs -n evaluation -l app=gateway`

## Manual Testing

### Verify MBCAS Deployment

```powershell
# Check MBCAS components
kubectl get pods -n mbcas-system

# Check CRD
kubectl get crd podallocations.allocation.mbcas.io

# Check agent logs
kubectl logs -n mbcas-system -l app.kubernetes.io/component=agent --tail=50

# Check controller logs
kubectl logs -n mbcas-system -l app.kubernetes.io/component=controller --tail=50
```

### Deploy Test Workloads Manually

```powershell
# Baseline
kubectl apply -f k8s/evaluation/mode-a-static/workloads.yaml

# MBCAS
kubectl apply -f k8s/evaluation/mode-c-coallocator/workloads.yaml
```

### Monitor PodAllocations

```powershell
# Watch PodAllocations
kubectl get podallocations -n evaluation -w

# Get details
kubectl get podallocation <name> -n evaluation -o yaml
```

### Check Pod CPU Limits

```powershell
# Get current limits
kubectl get pod <pod-name> -n evaluation -o jsonpath='{.spec.containers[0].resources.limits.cpu}'

# Watch for changes
kubectl get pod <pod-name> -n evaluation -o jsonpath='{.spec.containers[0].resources.limits.cpu}' -w
```

## Success Criteria

### Basic Functionality

- [ ] MBCAS deploys successfully on Minikube
- [ ] Agent reads cgroup throttling metrics
- [ ] Agent creates PodAllocation CRDs for managed pods
- [ ] Controller applies CPU limits to pods via in-place scaling
- [ ] Pod CPU limits change based on demand (no restarts)
- [ ] Evaluation workloads (gateway, workers, noise) are managed correctly
- [ ] Allocation modes (uncongested/congested/overloaded) work as expected
- [ ] System handles bursty workloads (worker-b spikes)
- [ ] No critical errors in agent or controller logs

### Performance Targets

- [ ] **Latency**: Application p95 latency < 100ms (or matches baseline)
- [ ] **Allocation Latency**: Time from demand signal to applied limit < 20 seconds
- [ ] **Resource Overhead**: MBCAS agent + controller CPU < 200m total
- [ ] **Resource Efficiency**: CPU utilization > 80% (less waste than static limits)

### Comparison Targets

- [ ] **Latency**: MBCAS maintains equal or better application latency vs baseline
- [ ] **Zero Restarts**: MBCAS has zero pod restarts (baseline may have restarts)
- [ ] **Efficiency**: MBCAS achieves better CPU utilization than static limits

## Next Steps

After basic testing is complete:

1. **Performance Optimization**: Tune allocation parameters based on results
2. **VPA Comparison**: Add VPA (mode-b) to comparison tests
3. **Game Theory Validation**: Verify Nash bargaining fairness properties
4. **Multi-Node Testing**: Test with multiple nodes
5. **Stress Testing**: Test with 100+ pods
6. **Long-Running Tests**: Validate stability over extended periods

## Additional Resources

- **MBCAS Architecture**: See `README.md`
- **Implementation Plan**: See `PLAN.md`
- **Critical Improvements**: See `.cursor/plans/critical_and_major_improvements_3c2f958e.plan.md`

