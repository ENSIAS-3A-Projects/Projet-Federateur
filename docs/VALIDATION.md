# MBCAS Validation Strategy

This document describes the comprehensive validation strategy for MBCAS, covering unit tests, integration tests, end-to-end scenarios, and metrics.

## Unit Tests

### Writer Unit Tests

Tests verify that the Writer correctly creates, updates, and deletes PodAllocation resources.

**Test Files:**
- `pkg/agent/writer_test.go`

**Coverage:**
- Creates PodAllocation when it doesn't exist
- Updates existing PodAllocation
- Skips update when values are unchanged
- Deletes PodAllocation correctly

### Nash Bargaining Tests

Tests verify the redistribution logic handles multiple capped agents correctly.

**Test Files:**
- `pkg/allocation/nash_simple_test.go`

**Coverage:**
- Multiple capped agents redistribute remaining capacity
- All agents capped leaves capacity unused
- Capacity fully utilized when possible

### Hysteresis Tests

Tests verify that small changes are suppressed to prevent oscillation.

**Test Files:**
- `pkg/agent/hysteresis_test.go`

**Coverage:**
- Small changes below MinChangePercent are suppressed
- Changes above threshold are written

### Startup Grace Period Tests

Tests verify that allocations only increase during grace period.

**Test Files:**
- `pkg/agent/startup_grace_period_test.go`

**Coverage:**
- Min bid cannot decrease below current allocation during grace period
- After grace period, allocations can decrease based on usage

### Capacity Model Tests

Tests verify that node capacity is used instead of sum of limits.

**Test Files:**
- `pkg/agent/capacity_test.go`

**Coverage:**
- Uses node allocatable CPU
- Subtracts unmanaged pods CPU usage

## Integration Tests

### Agent to Controller Integration

Tests verify that allocations written by the agent are correctly reconciled by the controller.

**Test Files:**
- `test/integration/agent_controller_test.go`

### Full Pipeline Integration

Tests the complete pipeline from cgroup metrics to PodAllocation creation.

**Test Files:**
- `test/integration/full_pipeline_test.go`

## End-to-End Tests

### E2E Test Framework

Provides utilities for creating workloads, waiting for states, and cleaning up.

**Test Files:**
- `test/e2e/e2e_test.go`

**Scenarios:**
- Single pod allocation
- Multi-pod contention
- Throttling response
- Pod deletion cleanup
- Load testing with many pods
- Rapid pod churn
- Agent restart resilience
- Controller restart resilience

## Metrics to Track

### Prometheus Queries

#### Allocation Effectiveness
```promql
# Allocation accuracy: percentage of pods where allocation is within 20% of usage
sum(
  (mbcas_pod_cpu_allocation_millicores > mbcas_pod_cpu_usage_millicores * 0.8) and
  (mbcas_pod_cpu_allocation_millicores < mbcas_pod_cpu_usage_millicores * 1.5)
) / count(mbcas_pod_cpu_allocation_millicores) * 100
```

#### Throttling Reduction
```promql
# Average throttling ratio across managed pods
avg(mbcas_throttling_ratio)

# Percentage of pods with zero throttling
sum(mbcas_throttling_ratio == 0) / count(mbcas_throttling_ratio) * 100

# Throttling ratio trend over time
avg_over_time(mbcas_throttling_ratio[5m])
```

#### Allocation Latency
```promql
# P99 allocation latency
histogram_quantile(0.99, sum(rate(mbcas_allocation_latency_seconds_bucket[5m])) by (le))

# Average allocation latency
sum(rate(mbcas_allocation_latency_seconds_sum[5m])) / sum(rate(mbcas_allocation_latency_seconds_count[5m]))
```

#### System Stability
```promql
# Allocation change rate per pod (should be low when stable)
sum(rate(mbcas_allocation_changes_total[5m])) by (pod)

# Detect oscillation: more than 10 changes per hour for any pod
count(sum(increase(mbcas_allocation_changes_total[1h])) by (pod) > 10)
```

#### Nash Bargaining Mode Distribution
```promql
# Distribution of Nash modes over time
sum(rate(mbcas_nash_mode_total[5m])) by (mode)

# Percentage of time in congested mode
sum(rate(mbcas_nash_mode_total{mode="congested"}[1h])) / sum(rate(mbcas_nash_mode_total[1h])) * 100
```

#### Q-Learning Convergence
```promql
# Average reward over time (should trend upward then stabilize)
avg(mbcas_qlearning_reward)

# Reward distribution
histogram_quantile(0.5, sum(rate(mbcas_qlearning_reward_bucket[5m])) by (le))
```

#### Resource Utilization Efficiency
```promql
# Total allocated vs total used
sum(mbcas_pod_cpu_allocation_millicores) / sum(mbcas_pod_cpu_usage_millicores)

# Waste ratio: allocated but unused
(sum(mbcas_pod_cpu_allocation_millicores) - sum(mbcas_pod_cpu_usage_millicores)) / sum(mbcas_pod_cpu_allocation_millicores) * 100
```

## Success Criteria

### Functional Criteria

1. **Writer creates PodAllocation CRs** for every managed pod
   - Verify: Count of PodAllocations equals count of managed pods minus excluded pods

2. **Controller applies allocations** within configured cooldown period
   - Verify: status.phase transitions to Applied within twice the cooldown interval

3. **Nash bargaining solver allocates exactly** the available capacity when congested
   - Verify: Sum of allocations equals capacity minus reserve

4. **Allocations respect min and max bounds** from bids
   - Verify: No allocation is below any bid's min or above any bid's max

5. **Pods deleted cause their PodAllocations to be deleted**
   - Verify: No orphaned PodAllocations after pod deletion

### Performance Criteria

1. **Allocation latency P99** is below 30 seconds
   - From cgroup sample to pod resize completion

2. **Agent CPU overhead** per node is below 50 millicores with 50 managed pods
   - Measure agent container CPU usage

3. **Controller CPU overhead** is below 100 millicores with 500 PodAllocations cluster-wide

4. **Memory usage** per agent is below 128 MiB with 100 managed pods

### Stability Criteria

1. **No pod experiences more than 12 allocation changes per hour** in steady state
   - Prevents oscillation

2. **Throttling ratio** for any pod does not exceed 0.3 for more than 5 consecutive minutes
   - Ensures responsiveness to load

3. **Q-learning rewards** trend positive over the first 100 iterations then stabilize
   - Indicates learning convergence

4. **After agent restart**, allocations resume within 2 minutes
   - Q-table persistence optional but state should recover

### Efficiency Criteria

1. **Average waste ratio** below 30 percent
   - Waste is allocated minus used divided by allocated

2. **During contention**, high-weight pods receive proportionally more allocation than low-weight pods
   - Verify ratio matches weight ratio within 10 percent

3. **Idle pods** (usage below 10 percent of allocation for 5 minutes) have allocations reduced in cost efficiency mode

## Validation Dashboard

Create a Grafana dashboard with these panels:

1. **Allocation Overview**: Total managed pods, total allocated CPU, total used CPU, waste percentage
2. **Throttling Heatmap**: Throttling ratio per pod over time
3. **Nash Mode Timeline**: Stacked area of uncongested, congested, and overloaded modes
4. **Allocation Latency Histogram**: P50, P90, P99 latencies
5. **Q-Learning Reward Trend**: Average reward per hour with trendline
6. **Allocation Changes Rate**: Changes per pod per hour with threshold line at 12
7. **Agent Health**: CPU and memory usage per agent pod
8. **Controller Reconcile Rate**: Reconciliations per second and error rate

## Automated Validation Script

Run `scripts/validate-mbcas.sh` to perform automated validation checks:

- Verifies all components are running
- Checks CRD exists
- Creates test workload
- Waits for PodAllocation creation
- Waits for allocation application
- Verifies pod was resized
- Cleans up test resources

## Running Tests

### Unit Tests
```bash
go test ./pkg/agent/... -v
go test ./pkg/allocation/... -v
```

### Integration Tests
```bash
go test ./test/integration/... -v
```

### E2E Tests
```bash
go test ./test/e2e/... -v -timeout 30m
```

### Validation Script
```bash
chmod +x scripts/validate-mbcas.sh
./scripts/validate-mbcas.sh
```
