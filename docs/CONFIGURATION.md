# MBCAS Configuration

## Configuration Sources

MBCAS loads configuration from:

1. **ConfigMap** (`mbcas-agent-config` in `mbcas-system` namespace)
2. **Environment variables** (fallback if ConfigMap missing)
3. **Default values** (if neither source provides a value)

## Agent Configuration

### Core Settings

| Variable | ConfigMap Key | Default | Description |
|----------|---------------|---------|-------------|
| `MBCAS_SAMPLING_INTERVAL` | `samplingInterval` | `1s` | Interval between cgroup samples |
| `MBCAS_WRITE_INTERVAL` | `writeInterval` | `5s` | Interval between PodAllocation writes |
| `MBCAS_MIN_CHANGE_PERCENT` | `minChangePercent` | `2.0` | Minimum change % to trigger update |
| `MBCAS_SYSTEM_RESERVE_PERCENT` | `systemReservePercent` | `10.0` | System reserve % of node capacity |
| `MBCAS_BASELINE_CPU_PER_POD` | `baselineCPUPerPod` | `100m` | Default baseline CPU per pod |
| `MBCAS_STARTUP_GRACE_PERIOD` | `startupGracePeriod` | `90s` | Grace period after agent startup |

### SLO Protection

| Variable | ConfigMap Key | Default | Description |
|----------|---------------|---------|-------------|
| `MBCAS_SLO_TARGET_LATENCY_MS` | `sloTargetLatencyMs` | `0` | Target p99 latency in ms (0 = disabled) |
| `MBCAS_PROMETHEUS_URL` | `prometheusURL` | `""` | Prometheus URL for latency queries |
| `MBCAS_FAST_LOOP_INTERVAL` | `fastLoopInterval` | `2s` | Fast guardrail check interval |
| `MBCAS_SLOW_LOOP_INTERVAL` | `slowLoopInterval` | `10s` | Normal allocation loop interval |
| `MBCAS_P99_THRESHOLD_MULTIPLIER` | `p99ThresholdMultiplier` | `1.2` | Multiplier for SLO violation threshold |
| `MBCAS_THROTTLING_THRESHOLD` | `throttlingThreshold` | `0.1` | Throttling ratio threshold for fast-up |
| `MBCAS_FAST_STEP_SIZE_MIN` | `fastStepSizeMin` | `0.20` | Minimum fast-up step (20%) |
| `MBCAS_FAST_STEP_SIZE_MAX` | `fastStepSizeMax` | `0.40` | Maximum fast-up step (40%) |

### Allocation Mechanism

| Variable | ConfigMap Key | Default | Description |
|----------|---------------|---------|-------------|
| `MBCAS_ALLOCATION_MECHANISM` | `allocationMechanism` | `nash` | `nash` or `primal-dual` |
| `MBCAS_ENABLE_KALMAN_PREDICTION` | `enableKalmanPrediction` | `true` | Enable Kalman filter for demand prediction |
| `MBCAS_ENABLE_BATCH_RECONCILIATION` | `enableBatchReconciliation` | `true` | Enable batch reconciliation |

### Agent-Based Modeling

| Variable | ConfigMap Key | Default | Description |
|----------|---------------|---------|-------------|
| `MBCAS_ENABLE_AGENT_BASED_MODELING` | `enableAgentBasedModeling` | `true` | Enable per-pod agent learning |
| `MBCAS_AGENT_LEARNING_RATE` | `agentLearningRate` | `0.1` | Learning rate for strategy evolution |
| `MBCAS_AGENT_MEMORY_SIZE` | `agentMemorySize` | `20` | Number of decisions to remember |
| `MBCAS_AGENT_EXPLORATION_RATE` | `agentExplorationRate` | `0.2` | Q-learning exploration rate |
| `MBCAS_AGENT_DISCOUNT_FACTOR` | `agentDiscountFactor` | `0.9` | Q-learning discount factor |

### Advanced Tuning

| Variable | ConfigMap Key | Default | Description |
|----------|---------------|---------|-------------|
| `MBCAS_MAX_COALITION_SIZE` | `maxCoalitionSize` | `8` | Maximum coalition members |
| `MBCAS_MAX_HISTORY_SIZE` | `maxHistorySize` | `1000` | Maximum decision history per pod |
| `MBCAS_MIN_USAGE_MICROSECONDS` | `minUsageMicroseconds` | `1000` | Minimum usage for valid samples |
| `MBCAS_ABSOLUTE_MIN_ALLOCATION` | `absoluteMinAllocation` | `10` | Minimum allocation in millicores |
| `MBCAS_NEED_HEADROOM_FACTOR` | `needHeadroomFactor` | `0.15` | Conservative headroom (15%) |
| `MBCAS_WANT_HEADROOM_FACTOR` | `wantHeadroomFactor` | `0.10` | Base headroom (10%) |
| `MBCAS_MAX_DEMAND_MULTIPLIER` | `maxDemandMultiplier` | `4.0` | Maximum demand scaling factor |

### Price Response

| Variable | ConfigMap Key | Default | Description |
|----------|---------------|---------|-------------|
| `MBCAS_ENABLE_PRICE_RESPONSE` | `enablePriceResponse` | `true` | Enable price-responsive demand adjustment |
| `MBCAS_COALITION_GROUPING_ANNOTATION` | `coalitionGroupingAnnotation` | `mbcas.io/coalition` | Annotation key for coalition grouping |

## Controller Configuration

The controller uses standard controller-runtime flags:

- `--metrics-bind-address`: Metrics endpoint (default: `:8080`)
- `--health-probe-bind-address`: Health probe endpoint (default: `:8081`)
- `--leader-elect`: Enable leader election (default: `false`)

## Pod Annotations

Pods can be configured via annotations:

- `mbcas.io/managed`: Set to `"true"` to enable MBCAS management (default: managed if label present)
- `mbcas.io/target-latency-ms`: Override SLO target latency for this pod
- `mbcas.io/coalition`: Group pods into coalitions for joint optimization

## Example ConfigMap

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: mbcas-agent-config
  namespace: mbcas-system
data:
  samplingInterval: "1s"
  writeInterval: "5s"
  sloTargetLatencyMs: "100"
  prometheusURL: "http://prometheus:9090"
  allocationMechanism: "nash"
  enableKalmanPrediction: "true"
```

## Validation

Configuration is validated at startup. Invalid values will cause the agent to fail fast with an error message.
