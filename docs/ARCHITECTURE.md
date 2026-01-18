# MBCAS Architecture

## System Overview

MBCAS (Market-Based CPU Allocation System) is a Kubernetes operator that uses game-theoretic principles to allocate CPU resources fairly and efficiently across pods.

## Components

### 1. Node Agent (`cmd/agent`)

The node agent runs as a DaemonSet, one per node. It:

- **Samples cgroup metrics** every 1 second to detect throttling
- **Computes demand signals** from throttling ratios
- **Calculates allocations** using Nash Bargaining or Primal-Dual mechanisms
- **Writes PodAllocation CRDs** with desired CPU requests and limits
- **Monitors SLO violations** and triggers fast guardrail protection

**Key Files:**
- `pkg/agent/agent.go` - Main agent loop
- `pkg/agent/cgroup/reader.go` - Cgroup v2 statistics reading
- `pkg/agent/demand/calculator.go` - Demand calculation from throttling
- `pkg/agent/fast_guardrail.go` - Fast SLO protection loop

### 2. Controller (`cmd/controller`)

The controller runs as a Deployment (single instance with leader election). It:

- **Watches PodAllocation CRDs** via controller-runtime
- **Applies allocations** by patching pod resource requests/limits
- **Skips Guaranteed QoS pods** (they have fixed resources)
- **Updates status** with phase and shadow prices

**Key Files:**
- `pkg/controller/podallocation_controller.go` - Reconciliation logic
- `pkg/controller/pod_event_handler.go` - Pod event handling

### 3. Allocation Mechanisms

#### Nash Bargaining (`pkg/allocation/nash_bargaining.go`)

Maximizes the Nash product: `Π(xᵢ - dᵢ)^wᵢ`

- Fair surplus distribution
- Proportional to utility weights
- Used in congested mode

#### Primal-Dual (`pkg/allocation/primal_dual.go`)

Distributed price-clearing mechanism:

- Coordinator broadcasts shadow price λ
- Agents compute best-response: `argmax U(x) - λx`
- Price updates: `λ_{t+1} = [λ_t + η(Σx_i - C)]_+`
- Converges to market-clearing allocation

### 4. Fast Guardrail (`pkg/agent/fast_guardrail.go`)

Fast SLO protection loop (1-2 second response):

- Monitors p99 latency from Prometheus
- Detects throttling pressure from cgroup samples
- Applies bounded fast-up steps (20-40% increase)
- Bypasses normal smoothing/hysteresis for urgent cases

## Data Flow

```
1. Pod runs → cgroup throttling occurs
2. Agent samples cgroup → computes demand signal
3. Agent calculates allocation (Nash/Primal-Dual)
4. Agent writes PodAllocation CRD
5. Controller reconciles CRD
6. Controller patches pod resources
7. Pod gets new CPU allocation
```

## State Management

### Agent State

- `podDemands` - Demand trackers per pod (EMA smoothing)
- `podUsages` - Actual CPU usage in millicores
- `allocations` - Current allocation strings ("request:limit")
- `agentStates` - Per-pod agent learning state (Q-learning)

### Controller State

- Watches PodAllocation CRDs via informer cache
- Tracks reconciliation status in CRD status field
- No persistent state (stateless reconciliation)

## Coordination Mechanisms

### Shadow Prices

Shadow prices (Lagrange multipliers) coordinate agents:

- High price → high contention → agents back off
- Low price → abundance → agents can request more
- Stored in PodAllocation status for agent visibility

### Price Signals

Agents can adjust demand based on shadow prices when `EnablePriceResponse` is true.

## Stability Guarantees

### Lyapunov Controller (`pkg/stability/lyapunov.go`)

Ensures system converges:

- Lyapunov function: `V = Σ(alloc_i - target_i)²`
- Controller adjusts allocations to decrease V
- Guarantees convergence to stable allocation

## Performance Characteristics

- **Sampling interval**: 1 second (configurable)
- **Write interval**: 5 seconds (configurable)
- **Fast guardrail response**: 1-2 seconds
- **Market clearing**: < 100ms for typical workloads
