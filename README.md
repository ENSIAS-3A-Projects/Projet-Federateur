# MBCAS - Market-Based CPU Allocation System

**Agent-Based Modeling + Game Theory for Kubernetes Resource Allocation**

A Kubernetes system that uses autonomous PodAgents (with Q-Learning) and Nash Bargaining to dynamically allocate CPU resources based on real-time demand.

## Authors

- **ALAOUI SOSSE Saad**
- **BOUAZZA Chaymae**
- **BENABBOU Imane**
- **TAQI Mohamed Chadi**

---

## What is MBCAS?

MBCAS treats CPU allocation as a **cooperative game** where:
- Each pod is an **autonomous agent** that learns optimal bidding strategies
- **Nash Bargaining** resolves conflicts fairly when resources are scarce
- Allocations update **in-place** (no pod restarts) based on real cgroup metrics

### The Pipeline

```
┌──────────┐   ┌─────────┐   ┌─────────┐   ┌──────────┐   ┌──────┐
│ Discover │ → │  Sync   │ → │   Bid   │ → │ Bargain  │ → │ Act  │
│  Pods    │   │ Agents  │   │  (ABM)  │   │  (Nash)  │   │Write │
└──────────┘   └─────────┘   └─────────┘   └──────────┘   └──────┘
```

1. **Discover**: Find all pods on the node
2. **Sync**: Create/remove PodAgents for each pod
3. **Bid**: Each agent observes its metrics and bids for CPU
4. **Bargain**: Nash solver allocates CPU fairly
5. **Act**: Write new allocations to Kubernetes

---

## Quick Start

### Prerequisites

- Kubernetes cluster with `InPlacePodVerticalScaling` feature gate
- Go 1.21+
- kubectl configured

### Build & Deploy

```bash
# Build binaries
make build

# Build container images
make docker-build

# Deploy to Kubernetes
kubectl apply -f config/crd/
kubectl apply -f config/namespace.yaml
kubectl apply -f config/rbac/
kubectl apply -f config/agent/
kubectl apply -f config/controller/
```

### Verify Installation

```bash
# Check agent is running on all nodes
kubectl get pods -n mbcas-system -l app.kubernetes.io/component=agent

# Check controller is running
kubectl get pods -n mbcas-system -l app.kubernetes.io/component=controller

# View PodAllocations
kubectl get podallocations -A
```

---

## How It Works

### Dual-Loop Architecture

MBCAS uses a **dual-loop architecture** for responsive and efficient resource allocation:

- **Fast Loop** (2s interval): Reacts quickly to SLO violations and high throttling
  - Only increases allocations (never decreases)
  - Responds to immediate pressure signals
  - Prevents performance degradation
  
- **Slow Loop** (10-15s interval): Full Nash Bargaining optimization
  - Complete market clearing with all pods
  - Q-Learning updates and strategy adaptation
  - Long-term efficiency optimization

### 1. Autonomous PodAgents (ABM)

Each pod has a dedicated agent that:
- **Observes**: CPU usage, throttling trends, current allocation
- **Learns**: Uses Q-Learning to find optimal bidding strategies
- **Adapts**: Adjusts behavior based on rewards and shadow prices
- **Persists**: Q-tables saved across restarts for continuous learning

**State Space**: `(usage_level, throttle_level, allocation_level)`  
**Action Space**: `{aggressive, normal, conservative}` bidding  
**Learning**: ε-greedy Q-Learning with exploration decay  
**Shadow Price Feedback**: Agents adjust bids based on market conditions (high price = conservative, low price = aggressive)

**Key Features**:
- **Throttling Trend Analysis**: Uses last 3 samples for pattern learning
- **Oscillation Detection**: Penalizes large allocation changes in reward function
- **Q-Table Persistence**: Learning state preserved across agent restarts
- **Startup Grace Period**: Allocations only increase during initial 45-90s

### 2. Nash Bargaining with Shadow Prices (Game Theory)

When total demand exceeds capacity:
- **Baseline**: Every pod gets a minimum viable allocation
- **Surplus**: Remaining CPU distributed to maximize Nash product
- **Fairness**: Weighted by pod priority/importance
- **Efficiency**: Pareto optimal (no waste)
- **Shadow Price**: Market-clearing price fed back to agents for demand adjustment

**Shadow Price Mechanism**:
- High shadow price (>0.3) → Resources scarce → Agents reduce demand
- Low shadow price → Resources abundant → Agents can increase demand
- Enables price-responsive bidding behavior

### 3. Real-Time Metrics

Reads from cgroup v2:
- `throttled_usec`: Time pod was throttled
- `usage_usec`: Actual CPU time used
- **Demand Signal**: `throttling_ratio / threshold` normalized to [0,1]
- **Actual Usage**: Computed in millicores from delta samples

---

## Configuration

Agent behavior is controlled via ConfigMap `mbcas-agent-config` in namespace `mbcas-system`:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: mbcas-agent-config
  namespace: mbcas-system
data:
  # Timing intervals
  samplingInterval: "1s"          # Cgroup metric sampling
  fastLoopInterval: "2s"          # Fast SLO guardrail loop
  slowLoopInterval: "15s"         # Slow optimization loop
  writeInterval: "10s"            # Allocation update frequency
  
  # Resource management
  systemReservePercent: "10.0"    # System CPU reservation
  totalClusterCPUCapacityMilli: "4000"  # Total CPU capacity (4 cores)
  baselineCPUPerPod: "100m"       # Minimum per pod
  
  # Stability controls
  minChangePercent: "5.0"         # Hysteresis threshold
  startupGracePeriod: "45s"       # Grace period for new pods
  
  # Q-Learning parameters
  agentLearningRate: "0.1"        # Learning rate (α)
  agentExplorationRate: "0.2"     # Exploration rate (ε)
  agentDiscountFactor: "0.9"      # Discount factor (γ)
  
  # Fast loop thresholds
  throttlingThreshold: "0.1"      # Throttling ratio trigger
  fastStepSizeMin: "0.20"         # Min fast step (20%)
  fastStepSizeMax: "0.40"         # Max fast step (40%)
  
  # Optional features
  prometheusURL: ""               # SLO checking (empty = disabled)
  costEfficiencyMode: "false"     # Aggressive cost optimization
```

See `config/agent/configmap.yaml` for all options.

---

## Pod Annotations

Control MBCAS behavior per-pod:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: my-app
  annotations:
    # Opt-out of MBCAS management
    mbcas.io/managed: "false"
    
    # Set minimum CPU (overrides QoS defaults)
    mbcas.io/min-cpu: "500m"
    
    # Set SLO target for learning
    mbcas.io/target-latency-ms: "100"
```

---

## Architecture

See [ARCHITECTURE.md](docs/ARCHITECTURE.md) for detailed system design.

Key components:
- **Agent** (`pkg/agent/`): Node-level daemon, runs dual-loop pipeline with PodAgents and Nash solver
- **Controller** (`pkg/controller/`): Cluster-level, applies PodAllocation decisions via in-place pod resizing
- **PodAgent** (`pkg/agent/pod_agent.go`): Per-pod autonomous learning agent with Q-Learning
- **Nash Solver** (`pkg/allocation/nash_simple.go`): Fair allocation algorithm with shadow price computation
- **Cgroup Reader** (`pkg/agent/cgroup/`): Reads throttling and usage metrics from cgroup v2
- **Writer** (`pkg/agent/writer.go`): Creates/updates PodAllocation CRDs
- **Q-Table Persister** (`pkg/agent/qtable_persister.go`): Saves/loads learning state
- **SLO Checker** (`pkg/agent/slo_checker.go`): Optional Prometheus integration for latency SLOs

---

## Game Theory Concepts

See [GAME_THEORY.md](GAME_THEORY.md) for detailed explanation of:
- Nash Bargaining Solution
- Pareto Efficiency
- Agent-Based Modeling
- Q-Learning for resource allocation

---

## Testing

```bash
# Run all tests
go test ./...

# Test specific packages
go test ./pkg/agent -v
go test ./pkg/allocation -v

# Build verification
go build ./...
```

---

## Project Structure

```
.
├── api/v1alpha1/          # CRD definitions (PodAllocation)
├── cmd/
│   ├── agent/             # Agent binary entrypoint
│   └── controller/        # Controller binary entrypoint
├── config/                # Kubernetes manifests
│   ├── agent/             # Agent DaemonSet
│   ├── controller/        # Controller Deployment
│   ├── crd/               # Custom Resource Definitions
│   └── rbac/              # RBAC policies
├── pkg/
│   ├── agent/             # Node agent implementation
│   │   ├── agent.go       # Main orchestrator (200 lines)
│   │   ├── pod_agent.go   # Autonomous agent with Q-Learning
│   │   └── cgroup/        # Cgroup metrics reader
│   ├── allocation/        # Allocation algorithms
│   │   └── nash_simple.go # Nash Bargaining solver
│   ├── controller/        # Controller implementation
│   └── actuator/          # Pod resize actuator
└── docs/
    ├── ARCHITECTURE.md    # System architecture
    └── GAME_THEORY.md     # Game theory concepts
```

---

## Performance

- **Response Time**: 
  - Fast loop: 2 seconds (SLO violations, throttling)
  - Slow loop: 10-15 seconds (full optimization)
- **Overhead**: <1% CPU per node (agent), <0.1 cores (controller)
- **Scalability**: O(n) complexity, tested with 100+ pods per node
- **Zero Downtime**: In-place updates, no pod restarts
- **Stability**: Exponential smoothing, hysteresis, and cooldown periods prevent oscillations
- **Learning Persistence**: Q-tables preserved across restarts for continuous improvement

---

## Limitations

- Requires Kubernetes 1.27+ with `InPlacePodVerticalScaling` feature gate
- Only manages CPU (memory support planned)
- Cgroup v2 required (most modern distributions)
- Best suited for latency-sensitive workloads with variable demand

---

## Contributing

This is an academic research project. For questions or collaboration:
- Open an issue on GitHub
- Contact the authors

---

## License

Academic research project - see institution policies.

---

## References

- Nash Bargaining: Nash, J. (1950). "The Bargaining Problem"
- Q-Learning: Watkins & Dayan (1992). "Q-learning"
- Kubernetes VPA: https://github.com/kubernetes/autoscaler/tree/master/vertical-pod-autoscaler
- Cgroup v2: https://www.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html
