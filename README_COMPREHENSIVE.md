# MBCAS (Market-Based CPU Allocation System) - Comprehensive Codebase Summary

**Project Name:** CoAllocator / MBCAS  
**Repository:** shady0503/Projet-Federateur  
**Authors:** ALAOUI SOSSE Saad, BOUAZZA Chaymae, BENABBOU Imane, TAQI Mohamed Chadi  
**Purpose:** Game-theoretic CPU resource allocation system for Kubernetes using Nash Bargaining Solution

---

## Executive Summary

MBCAS (Market-Based CPU Allocation System), also known as CoAllocator, is a Kubernetes-native resource allocation operator that applies **game theory** and **economic principles** to fairly and efficiently distribute CPU resources across microservices. Unlike traditional autoscalers (VPA, HPA) that use reactive heuristics, MBCAS treats each pod as an **autonomous agent** participating in a **cooperative game** for CPU allocation.

### Core Innovation

- **Game-Theoretic Foundation**: Uses Nash Bargaining Solution (NBS) for provably fair allocation
- **Demand-Driven**: Senses actual demand from kernel signals (cgroup throttling)
- **In-Place Scaling**: Updates CPU limits without pod restarts using Kubernetes resize API
- **Multi-Mode Allocation**: Adapts strategy based on resource contention (uncongested/congested/overloaded)
- **Zero-Downtime**: All allocations happen in-place with no service interruption

---

## Architecture Overview

### System Components

```
┌─────────────────────────────────────────────────────────────────┐
│                    MBCAS Architecture                           │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  ┌──────────────┐   PodAllocation CRD   ┌──────────────┐      │
│  │ Node Agent   │ ──────────────────────►│ Controller   │      │
│  │ (DaemonSet)  │                        │ (Deployment) │      │
│  └──────┬───────┘                        └──────┬───────┘      │
│         │                                       │              │
│         ▼                                       ▼              │
│  ┌──────────────┐                        ┌──────────────┐      │
│  │   cgroup     │                        │  Pod Resize  │      │
│  │  throttling  │                        │  (in-place)  │      │
│  └──────────────┘                        └──────────────┘      │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### Three-Phase Design

1. **Phase 1: Controller (Actuation)**
   - Watches `PodAllocation` CRDs
   - Applies CPU limit changes via Kubernetes resize API
   - Enforces safety constraints (cooldown, step size)
   - Location: `pkg/controller/`

2. **Phase 2: Agent (Demand Sensing)**
   - Reads cgroup throttling metrics every 1 second
   - Computes demand signals with exponential smoothing
   - Runs allocation algorithms every 15 seconds
   - Location: `pkg/agent/`

3. **Phase 3: Allocation (Game Theory)**
   - Nash Bargaining Solution for fair allocation
   - Kalai-Smorodinsky Solution for faster convergence
   - Market clearing with shadow prices
   - Location: `pkg/allocation/`

---

## Directory Structure

```
mbcas/
├── api/v1alpha1/              # Kubernetes CRD definitions
│   └── podallocation_types.go # PodAllocation resource schema
│
├── pkg/                       # Core implementation
│   ├── agent/                 # Node agent (demand sensing)
│   │   ├── agent.go           # Main agent loop (sampling, allocation)
│   │   ├── cgroup/            # Cgroup metrics reader
│   │   ├── demand/            # Demand tracking & prediction
│   │   ├── fast_guardrail.go  # SLO protection (1-2s loop)
│   │   ├── metrics.go         # Prometheus metrics
│   │   └── config.go          # Agent configuration
│   │
│   ├── allocation/            # Game-theoretic allocation
│   │   ├── market.go          # Market clearing algorithm
│   │   ├── nash_bargaining.go # Nash Bargaining Solution
│   │   ├── kalai_smorodinsky.go # Kalai-Smorodinsky Solution
│   │   ├── primal_dual.go     # Primal-dual optimizer
│   │   └── utility.go         # Utility functions
│   │
│   ├── coalition/             # Coalition formation (future)
│   │   ├── coalition.go       # Coalition game logic
│   │   └── shapley.go         # Shapley value computation
│   │
│   ├── controller/            # Kubernetes controller
│   │   ├── podallocation_controller.go # Main reconciliation loop
│   │   └── pod_event_handler.go        # Pod event handling
│   │
│   ├── actuator/              # Pod resize actuator
│   │   └── actuator.go        # In-place CPU limit updates
│   │
│   ├── price/                 # Shadow price coordination
│   │   └── price_signal.go    # Price signal computation
│   │
│   ├── stability/             # Convergence guarantees
│   │   └── lyapunov.go        # Lyapunov stability controller
│   │
│   └── types/                 # Shared type definitions
│       └── types.go           # Common data structures
│
├── cmd/                       # Entry points
│   ├── agent/main.go          # Agent binary
│   └── controller/main.go     # Controller binary
│
├── config/                    # Kubernetes manifests
│   ├── crd/                   # Custom Resource Definitions
│   ├── rbac/                  # RBAC permissions
│   ├── agent/                 # Agent DaemonSet
│   └── controller/            # Controller Deployment
│
├── k8s/                       # Test workloads & deployments
│   ├── mbcas/                 # MBCAS deployment manifests
│   ├── vpa-mbcas-test/        # VPA vs MBCAS comparison
│   └── policy/                # Resource policies
│
├── scripts/                   # Automation scripts
│   ├── setup-minikube-mbcas.ps1  # Minikube setup
│   ├── test-mbcas-metrics.ps1    # MBCAS testing
│   ├── test-vpa-metrics.ps1      # VPA testing
│   └── debug-mbcas.ps1           # Debugging utilities
│
├── apps/eval-service/         # Evaluation workload
│   └── main.go                # Test service for benchmarking
│
├── docs/                      # Documentation
│   └── ALLOCATION_ALGORITHM.md # Algorithm details
│
├── PLAN.md                    # Implementation roadmap
├── TESTING.md                 # Testing guide
└── README.md                  # Project overview
```

---

## Core Components Deep Dive

### 1. Node Agent (`pkg/agent/`)

**Purpose:** Sense demand from kernel signals and compute allocations

**Key Files:**
- `agent.go` - Main agent loop with dual-speed control
  - **Fast Loop (1-2s)**: SLO protection, throttling detection
  - **Slow Loop (5-15s)**: Market clearing, Nash bargaining
  - **Sampling Loop (1s)**: Cgroup metrics collection

- `cgroup/reader.go` - Reads cgroup v2 metrics
  - CPU throttling ratio: `cpu.stat` (throttled_usec / total_usec)
  - Actual CPU usage: `cpu.stat` (usage_usec)
  - PSI (Pressure Stall Information): Future integration

- `demand/calculator.go` - Demand signal processing
  - Exponential smoothing: `smoothed = α * raw + (1-α) * previous`
  - Asymmetric smoothing: Fast up (α=0.3), slow down (α=0.1)
  - Kalman filtering: Optional prediction for bursty workloads

- `fast_guardrail.go` - SLO protection mechanism
  - Detects latency violations via Prometheus queries
  - Applies emergency CPU increases (fast-up)
  - Prevents allocation decreases during SLO violations

**Agent Workflow:**
```
1. Sample cgroup metrics (1s interval)
   ├─ Read throttling ratio
   ├─ Read actual CPU usage
   └─ Update demand tracker

2. Fast guardrail check (1-2s interval)
   ├─ Query Prometheus for latency metrics
   ├─ Detect SLO violations
   └─ Apply emergency increases if needed

3. Slow optimizer (5-15s interval)
   ├─ Discover pods on node
   ├─ Compute allocations via market clearing
   ├─ Write PodAllocation CRDs
   └─ Update shadow prices
```

### 2. Allocation Engine (`pkg/allocation/`)

**Purpose:** Implement game-theoretic allocation algorithms

**Key Algorithms:**

#### Nash Bargaining Solution (`nash_bargaining.go`)
```
Objective: max Π_i (x_i - d_i)^w_i
Subject to:
  - Σ x_i ≤ C (capacity constraint)
  - x_i ≥ d_i (individual rationality)
  - x_i ≤ max_i (bounds)

Where:
  - x_i = allocation to pod i
  - d_i = disagreement point (baseline)
  - w_i = bargaining weight
  - C = available CPU capacity
```

**Properties:**
- **Pareto Optimal**: No allocation can improve one pod without hurting another
- **Symmetric**: Equal weights → equal surplus
- **Individual Rational**: Everyone gets at least their baseline
- **Independent of Irrelevant Alternatives**: Adding/removing pods doesn't affect others' relative shares

#### Kalai-Smorodinsky Solution (`kalai_smorodinsky.go`)
```
Objective: Maximize min_i (x_i - d_i) / (m_i - d_i)
Where:
  - m_i = maximum achievable for pod i (aspiration point)
  - Ensures proportional satisfaction across all pods
```

**Faster convergence than Nash, used in congested mode**

#### Market Clearing (`market.go`)

**Three Allocation Modes:**

1. **Uncongested Mode** (Σ needs ≤ capacity)
   ```
   Everyone gets what they need
   Allocation: x_i = need_i
   ```

2. **Congested Mode** (Σ needs > capacity)
   ```
   Nash Bargaining applied
   Allocation: Nash solution with weights
   ```

3. **Overloaded Mode** (Σ baselines > capacity)
   ```
   Emergency: scale baselines proportionally
   Allocation: x_i = (d_i * w_i / Σ(d_j * w_j)) * C
   ```

**Need Computation:**
```go
need_i = actual_usage_i * (1 + headroom_factor)
headroom_factor = 0.05 (5% safety margin)
```

### 3. Controller (`pkg/controller/`)

**Purpose:** Enforce allocation decisions via Kubernetes API

**Key Features:**

- **In-Place Resize**: Uses `pods/resize` subresource (no restarts)
- **Safety Constraints**:
  - Cooldown: 5 seconds between resizes
  - Step size: Max 2x change per resize
  - Minimum allocation: 10m CPU
  
- **Status Tracking**:
  - `Applied`: Resize successful
  - `Pending`: Waiting for cooldown
  - `Failed`: Temporary error (will retry)

**Reconciliation Loop:**
```
1. Watch PodAllocation CRD
2. Get current pod CPU limit
3. Compare with desired limit
4. Check safety constraints
5. Apply resize via actuator
6. Update PodAllocation status
```

### 4. Actuator (`pkg/actuator/`)

**Purpose:** Execute in-place pod resizes

**Implementation:**
```go
// Patch pod resources using resize subresource
PATCH /api/v1/namespaces/{ns}/pods/{name}/resize
{
  "spec": {
    "containers": [{
      "resources": {
        "requests": {"cpu": "500m"},
        "limits": {"cpu": "1000m"}
      }
    }]
  }
}
```

**Resize Policy:**
- Request = 90-95% of limit (mode-dependent)
- Ensures CPU share protection during contention

---

## Game Theory Implementation

### Nash Bargaining Solution

**Mathematical Formulation:**
```
max Π_i (x_i - d_i)^w_i
s.t. Σ x_i ≤ C, x_i ≥ d_i, x_i ≤ max_i

Equivalent convex form:
max Σ_i w_i * log(x_i - d_i)
```

**Water-Filling Algorithm:**
1. Allocate baselines: `x_i = d_i`
2. Distribute surplus proportional to weights: `surplus_i ∝ w_i`
3. Redistribute from capped agents to uncapped

**Axiom Verification:**
- Pareto optimality: All capacity used (or agents capped)
- Individual rationality: `x_i ≥ d_i` for all i
- Symmetry: Equal weights → equal surplus

### Kalai-Smorodinsky Solution

**Proportional Fairness:**
```
Maximize: min_i (x_i - d_i) / (m_i - d_i)

Where m_i = aspiration point (max achievable)
```

**Advantages:**
- Faster convergence (O(n) vs O(n²) for Nash)
- Better for dynamic workloads
- Monotonic in aspirations

### Coalition Formation (Future)

**Planned Features:**
- Request-path coalitions: Services on same trace path cooperate
- Shapley value attribution: Fair credit for contributions
- ε-core stability: Prevent blocking coalitions

**Current Status:** Disabled (`EnableCoalitionFormation = false`)
- Requires distributed tracing integration
- Will enable cross-service coordination

---

## Configuration & Tuning

### Agent Configuration (`config/agent/configmap.yaml`)

```yaml
# Sampling & Control Loops
samplingInterval: 1s          # Cgroup read frequency
fastLoopInterval: 2s          # SLO protection loop
slowLoopInterval: 15s         # Market clearing loop

# Allocation Parameters
baselineCPUPerPod: 100m       # Minimum CPU per pod
systemReservePercent: 10.0    # Reserve for system pods
minChangePercent: 2.0         # Hysteresis threshold

# Demand Smoothing
alphaUp: 0.3                  # Fast increase (30% weight to new)
alphaDown: 0.1                # Slow decrease (10% weight to new)

# Cost Efficiency Mode
costEfficiencyMode: false     # Enable aggressive cost optimization
needHeadroomFactor: 0.05      # 5% headroom above actual usage
targetThrottling: 0.01        # Target 1% throttling

# Fast Guardrail
enableFastGuardrail: true     # Enable SLO protection
fastUpThreshold: 0.05         # 5% throttling triggers fast-up
sloLatencyTarget: 100ms       # Target p99 latency
```

### Controller Configuration

```yaml
# Safety Constraints
resizeCooldown: 5s            # Min time between resizes
maxStepSizeFactor: 2.0        # Max 2x change per resize

# QoS Management
skipGuaranteed: false         # Manage Guaranteed QoS pods
```

---

## Metrics & Observability

### Prometheus Metrics

**Agent Metrics:**
```
# Demand signals
mbcas_demand_raw{namespace, pod}
mbcas_demand_smoothed{namespace, pod}

# Allocations
mbcas_allocation_milli{namespace, pod}
mbcas_allocation_mode{node}  # 0=uncongested, 1=congested, 2=overloaded

# Performance
mbcas_allocation_compute_duration_seconds
mbcas_cgroup_read_duration_seconds
mbcas_pod_discovery_duration_seconds

# Game Theory
mbcas_nash_product_log{node}
mbcas_nash_solver_iterations
mbcas_shadow_price_cpu{node}

# Utility
mbcas_utility{namespace, pod}  # Satisfaction ratio
```

**Controller Metrics:**
```
# Resize operations
mbcas_resize_total{namespace, pod, result}
mbcas_resize_duration_seconds
mbcas_cooldown_violations_total
mbcas_step_size_violations_total
```

### Logging

**Log Levels:**
- `klog.V(0)`: Errors and critical events
- `klog.V(2)`: Allocation decisions
- `klog.V(4)`: Detailed sampling data

**Key Log Messages:**
```
"Allocation decision" - Shows demand, need, allocation per pod
"Allocation mode changed" - Mode transitions
"Pod demand sample" - Raw and smoothed demand
"Fast guardrail triggered" - SLO protection activations
```

---

## Testing & Validation

### Test Scenarios

1. **Baseline Test** (`k8s/evaluation/mode-a-static/`)
   - Static CPU limits (no MBCAS)
   - Establishes performance baseline

2. **MBCAS Test** (`k8s/evaluation/mode-c-coallocator/`)
   - Dynamic allocation with MBCAS
   - Compares against baseline

3. **VPA Comparison** (`k8s/vpa-mbcas-test/`)
   - Side-by-side VPA vs MBCAS
   - Latency and resource efficiency comparison

### Load Test Phases

```
1. Warmup (2 min, 10 RPS)      - Establish baseline
2. Saturation (5 min, 50 RPS)  - Steady-state testing
3. Contention (5 min, 100 RPS) - Resource pressure
4. Bursty (3 min, spikes)      - Spike handling
```

### Test Workloads

- **Gateway**: Routes requests (100m-200m CPU)
- **Worker A**: Deterministic load (500m-1000m CPU, 30ms CPU time)
- **Worker B**: Bursty load (500m-1000m CPU, 20% spike probability)
- **Noise**: Background load (2 replicas, 80% CPU intensity)

### Success Criteria

**Functionality:**
- ✅ PodAllocations created for all managed pods
- ✅ CPU limits updated in-place (no restarts)
- ✅ Allocation modes work correctly
- ✅ Fast guardrail responds to SLO violations

**Performance:**
- ✅ p95 latency < 100ms (or matches baseline)
- ✅ Allocation latency < 20 seconds
- ✅ MBCAS overhead < 200m CPU
- ✅ CPU utilization > 80%

**Comparison vs VPA:**
- ✅ Zero restarts (VPA requires restarts)
- ✅ Faster response (15s vs minutes)
- ✅ Fair allocation (Nash bargaining)
- ✅ Better utilization

---

## Deployment

### Prerequisites

- Kubernetes 1.26+ with `InPlacePodVerticalScaling` feature gate
- Cgroup v2 enabled on nodes
- Metrics-server installed
- Prometheus (optional, for SLO monitoring)

### Quick Start

```bash
# 1. Start Minikube with feature gates
minikube start --feature-gates=InPlacePodVerticalScaling=true \
  --extra-config=kubelet.feature-gates=InPlacePodVerticalScaling=true

# 2. Enable metrics-server
minikube addons enable metrics-server

# 3. Deploy MBCAS
kubectl apply -f config/crd/
kubectl apply -f config/rbac/
kubectl apply -f config/agent/
kubectl apply -f config/controller/

# 4. Verify deployment
kubectl get pods -n mbcas-system
kubectl get crd podallocations.allocation.mbcas.io

# 5. Deploy test workloads
kubectl apply -f k8s/evaluation/mode-c-coallocator/
```

### PowerShell Automation

```powershell
# Complete setup and testing
.\scripts\setup-minikube-mbcas.ps1
.\scripts\test-mbcas-metrics.ps1

# Compare with VPA
.\scripts\test-vpa-metrics.ps1
```

---

## Performance Characteristics

### Latency

- **Allocation Computation**: 1-5ms (market clearing)
- **Cgroup Read**: 0.1-1ms per pod
- **Pod Discovery**: 5-10ms (informer cache)
- **End-to-End**: 15-20s (demand signal → applied limit)

### Scalability

- **Pods per Node**: Tested up to 50 pods
- **Allocation Frequency**: Every 15 seconds
- **Memory Footprint**: ~50MB per agent
- **CPU Overhead**: ~50m per agent

### Comparison with VPA

| Metric | MBCAS | VPA |
|--------|-------|-----|
| **Restart Required** | No | Yes |
| **Response Time** | 15-20s | 2-5 minutes |
| **Allocation Method** | Nash Bargaining | Historical percentile |
| **Fairness** | Provably fair | Heuristic |
| **SLO Awareness** | Yes (fast guardrail) | No |
| **CPU Overhead** | ~50m | ~30m |

---

## Advanced Features

### 1. Cost Efficiency Mode

**Purpose:** Minimize CPU allocation while maintaining SLOs

**Mechanisms:**
- **Asymmetric Smoothing**: Fast down (α=0.1), slow up (α=0.3)
- **Idle Decay**: Reduce allocations for idle pods
- **Target Throttling**: Allow 1% throttling for efficiency
- **Search-Based Allocation**: Find minimum feasible CPU

**Configuration:**
```yaml
costEfficiencyMode: true
alphaDown: 0.1
needHeadroomFactor: 0.05
targetThrottling: 0.01
```

### 2. Fast Guardrail (SLO Protection)

**Purpose:** Prevent latency violations during allocation changes

**Workflow:**
```
1. Query Prometheus for p99 latency
2. Compare with SLO target (100ms)
3. If violated:
   - Increase CPU by 20%
   - Block allocation decreases
   - Record violation event
4. Cooldown: 30 seconds
```

**Metrics:**
```
mbcas_fast_guardrail_triggers_total
mbcas_fast_guardrail_latency_ms
```

### 3. Kalman Filtering (Demand Prediction)

**Purpose:** Predict future demand for bursty workloads

**Model:**
```
State: [demand, velocity]
Prediction: demand(t+1) = demand(t) + velocity(t)
Update: Kalman gain based on measurement noise
```

**Configuration:**
```yaml
enableKalmanPrediction: true
```

### 4. Agent-Based Modeling (Q-Learning)

**Purpose:** Learn optimal allocation policies via reinforcement learning

**State Space:**
- Current allocation
- Demand signal
- Throttling ratio
- Latency

**Actions:**
- Increase CPU (+10%)
- Decrease CPU (-10%)
- Maintain

**Reward:**
```
reward = -latency_penalty - throttling_penalty - cost_penalty
```

**Configuration:**
```yaml
enableAgentBasedModeling: true
agentLearningRate: 0.1
agentDiscountFactor: 0.9
agentExplorationRate: 0.1
```

---

## Known Limitations & Future Work

### Current Limitations

1. **Single-Node Scope**: Agent operates per-node (no cross-node coordination)
2. **CPU Only**: Memory and other resources not yet supported
3. **No Coalition Formation**: Tracing integration required
4. **Cgroup v2 Required**: Not compatible with cgroup v1
5. **Minikube Focus**: Production hardening needed

### Planned Enhancements

1. **Multi-Resource Allocation**
   - Memory, network bandwidth, I/O
   - Joint optimization across resources

2. **Coalition Formation**
   - Integrate with Jaeger/OpenTelemetry
   - Request-path coalitions
   - Shapley value attribution

3. **Cross-Node Coordination**
   - Shadow price propagation
   - Global market clearing
   - Pod migration hints

4. **Advanced Game Theory**
   - Core stability checks
   - Auction mechanisms
   - Mechanism design

5. **Production Readiness**
   - Multi-cluster support
   - High availability
   - Disaster recovery

---

## Research Contributions

### Novel Aspects

1. **First Kubernetes-native Nash Bargaining implementation**
   - Provably fair CPU allocation
   - In-place scaling without restarts

2. **Dual-speed control architecture**
   - Fast loop (1-2s) for SLO protection
   - Slow loop (5-15s) for optimization

3. **Demand-driven allocation**
   - Kernel signal integration (cgroup throttling)
   - Exponential smoothing with asymmetric rates

4. **Safety-first design**
   - Cooldown and step size constraints
   - Grace period protection
   - QoS-aware minimums

### Academic Foundation

**Game Theory:**
- Nash Bargaining Solution (Nash, 1950)
- Kalai-Smorodinsky Solution (Kalai & Smorodinsky, 1975)
- Shapley Value (Shapley, 1953)
- Core Stability (Gillies, 1959)

**Economics:**
- Shadow Prices (Lagrange multipliers)
- Market Clearing (Walrasian equilibrium)
- Mechanism Design (Vickrey, 1961)

**Control Theory:**
- Lyapunov Stability (Lyapunov, 1892)
- Exponential Smoothing (Brown, 1956)
- Kalman Filtering (Kalman, 1960)

---

## References & Documentation

### Internal Documentation

- `README.md` - Project overview and quick start
- `PLAN.md` - Implementation roadmap (1068 lines)
- `TESTING.md` - Testing guide and procedures
- `docs/ALLOCATION_ALGORITHM.md` - Algorithm details

### Key Files

- `pkg/agent/agent.go` - Main agent implementation (1767 lines)
- `pkg/allocation/market.go` - Market clearing (649 lines)
- `pkg/controller/podallocation_controller.go` - Controller (704 lines)
- `pkg/allocation/nash_bargaining.go` - Nash solver
- `pkg/allocation/kalai_smorodinsky.go` - Kalai-Smorodinsky solver

### External Resources

- Kubernetes In-Place Pod Resize: [KEP-1287](https://github.com/kubernetes/enhancements/tree/master/keps/sig-node/1287-in-place-update-pod-resources)
- Cgroup v2: [kernel.org](https://www.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html)
- Nash Bargaining: [Wikipedia](https://en.wikipedia.org/wiki/Nash_bargaining_game)

---

## License

MIT License

---

## Acknowledgments

This project was developed as part of a **Federated Project** (Projet Fédérateur) exploring **Game Theory and Agent-Based Models** for distributed resource allocation in cloud-native environments.

**Academic Context:**
- Application of cooperative game theory to Kubernetes resource management
- Integration of economic principles (market clearing, shadow prices) with container orchestration
- Demonstration of provably fair allocation using Nash Bargaining Solution

**Technical Innovation:**
- First implementation of Nash Bargaining for Kubernetes CPU allocation
- Novel dual-speed control architecture for SLO protection
- In-place scaling without pod restarts using Kubernetes 1.27+ features

---

## Contact & Contribution

**Authors:**
- ALAOUI SOSSE Saad
- BOUAZZA Chaymae
- BENABBOU Imane
- TAQI Mohamed Chadi

**Repository:** shady0503/Projet-Federateur

For questions, issues, or contributions, please refer to the project repository.

---

**Last Updated:** January 2026  
**Version:** 1.0  
**Status:** Active Development
