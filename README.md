# CoAllocator

**Coalition-Based CPU Allocation System for Kubernetes**

A game-theoretic resource allocation operator that uses Nash Bargaining, Shapley Values, and Coalition Formation to fairly and efficiently distribute CPU resources across microservices.

## Authors

Created by:
- **ALAOUI SOSSE Saad**
- **BOUAZZA Chaymae**
- **BENABBOU Imane**
- **TAQI Mohamed Chadi**

---

## Overview

CoAllocator treats each pod as an **autonomous agent** that participates in a **cooperative game** for CPU allocation. Unlike traditional schedulers that use static limits or reactive autoscaling, CoAllocator:

- **Senses demand** from cgroup throttling signals
- **Computes fair allocations** using Nash Bargaining Solution
- **Tracks contributions** via Shapley values
- **Ensures stability** through ε-core checks

```
┌─────────────────────────────────────────────────────────────┐
│                     CoAllocator Architecture                │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│   ┌─────────────┐    PodAllocation CRD    ┌─────────────┐  │
│   │  Node Agent │ ───────────────────────► │ Controller  │  │
│   │   (per pod) │                          │  (cluster)  │  │
│   └──────┬──────┘                          └──────┬──────┘  │
│          │                                        │         │
│          ▼                                        ▼         │
│   ┌─────────────┐                          ┌─────────────┐  │
│   │   cgroup    │                          │  Pod Patch  │  │
│   │  throttling │                          │ (CPU limit) │  │
│   └─────────────┘                          └─────────────┘  │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

---

## Game-Theoretic Foundation

| Component | Theory | Purpose |
|-----------|--------|---------|
| **Nash Bargaining** | Maximize Π(xᵢ - dᵢ)^wᵢ | Fair surplus distribution |
| **Shapley Value** | φᵢ = Σ marginal contributions | Credit attribution |
| **Coalition Formation** | Services on shared paths | Collaborative allocation |
| **ε-Core Stability** | No blocking coalition | System stability |
| **Shadow Prices** | Lagrange multipliers | Implicit coordination |
| **Lyapunov Controller** | V must decrease | Convergence guarantee |

---

## Package Structure

```
pkg/
├── agent/          # Node agent: demand sensing, cgroup reading
├── allocation/     # Nash bargaining, utility functions, market clearing
│   ├── utility.go            # SLO-based utility with sigmoid
│   ├── nash_bargaining.go    # Nash Bargaining Solution solver
│   └── market.go             # Market clearing with Nash/heuristic
├── coalition/      # Coalition formation, Shapley values
│   ├── coalition.go          # Coalition game, ε-core checks
│   └── shapley.go            # Monte Carlo Shapley computation
├── price/          # Shadow price computation, demand response
│   └── price_signal.go       # Price headers for coordination
├── stability/      # Convergence guarantees
│   └── lyapunov.go           # Lyapunov potential function
├── controller/     # Kubernetes controller
└── types/          # Shared type definitions
```

---

## How It Works

### 1. Demand Sensing
The node agent reads cgroup throttling metrics every second to detect when pods need more CPU.

### 2. Allocation Computation
Every 15 seconds, the agent computes allocations:

```
if Σ baselines > capacity:
    → Overloaded mode: scale baselines proportionally
elif Σ needs > capacity:
    → Congested mode: Nash Bargaining Solution
else:
    → Uncongested mode: give everyone what they need
```

### 3. Coalition Cooperation
Services on the same request path (A → B → C) form coalitions and cooperate to minimize end-to-end latency.

### 4. Credit Tracking
Shapley values track which services contribute most. Services with positive credits get priority during contention.

---

## Key Features

- **Provably Fair**: Nash Bargaining satisfies Pareto optimality, symmetry, and individual rationality
- **Stable**: ε-core ensures no coalition can improve by defecting
- **Adaptive**: Shadow prices enable implicit coordination without central control
- **Kubernetes-Native**: Uses CRDs, works with existing K8s primitives

---

## Quick Start

```bash
# Build
go build ./...

# Run agent (on each node)
./agent --node-name=<node>

# Run controller (cluster-wide)
./controller
```

---

## Configuration

| Constant | Default | Description |
|----------|---------|-------------|
| `MaxCoalitionSize` | 8 | Max members per coalition (prevents O(2^n)) |
| `MaxRedistributionIterations` | 100 | Loop protection in Nash redistribution |
| `DefaultSensitivity` | 0.1 | SLO sigmoid steepness |
| `MaxHistorySize` | 1000 | Lyapunov history buffer size |

---

## License

MIT License

---

## Acknowledgments

This project was developed as part of a Federated Project exploring Game Theory and Agent-Based Models for distributed resource allocation in cloud-native environments.
