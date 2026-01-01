# MBCAS Game-Theoretic Model

This document defines the formal game-theoretic model implemented by MBCAS. Each concept maps to specific code locations.

## Overview

MBCAS implements a resource allocation mechanism based on the Nash Bargaining Solution. The mechanism allocates CPU resources fairly among competing pods based on observed demand signals.

## Formal Definitions

### Agents

Each pod is an agent. Agents do not make strategic choices; their behavior is observed through kernel measurements.

Code: `pkg/agent/agent.go` – `discoverPods()`

### State (Demand Signal)

Each agent has an observable state: the demand signal d_i in [0,1]. Derived from cgroup throttling:
- d_i = 0: not throttled
- d_i = 1: severely throttled

Signals are smoothed via EMA to reduce noise.

Code: `pkg/agent/cgroup/reader.go: ReadPodDemand`, `pkg/agent/demand/tracker.go: Update`

### Need Estimation

Need N_i is derived from demand:

N_i = min( M_i + (L_i - M_i) * d_i * (1 + h(d_i)), L_i )

- M_i: baseline (request or configured minimum)
- L_i: max (limit or node cap)
- d_i: demand signal
- h(d_i): 0.10 + 0.15 * d_i (adaptive headroom)

Code: `pkg/allocation/market.go: computeNeed`

### Utility Function

Utility is allocation satisfaction: U_i = min(A_i, N_i) / N_i. Utility reaches 1 when allocation meets need.

### Social Welfare

Maximize Nash Social Welfare (product of surpluses): NSW = product over i of (A_i - M_i). Equivalent to maximizing sum log(A_i - M_i), which yields proportional fairness.

Code: `pkg/allocation/market.go: nashReduce`

### Constraint

Capacity constraint: sum over i of A_i <= C, where C is available node CPU after reserve.

Code: `pkg/allocation/market.go: ClearMarketWithMetadata`

## Allocation Modes

### Uncongested
If sum needs <= C: A_i = N_i for all i.

### Congested
If sum needs > C but sum baselines <= C: surplus (N_i - M_i) is reduced proportionally. Ratio r = (C - sum M_i) / sum (N_i - M_i). Allocation: A_i = M_i + r * (N_i - M_i).

### Overloaded
If sum baselines > C: baselines are scaled proportionally: A_i = M_i * (C / sum M_i).

## Equilibrium Properties

- Existence: deterministic allocation always exists.
- Uniqueness: unique for given inputs (no randomness).
- Pareto efficiency: no reallocation can improve one agent without harming another.
- Proportional fairness: under congestion, surplus is reduced at the same percentage for all.
- Truthfulness: demand is measured (kernel), not declared; pods cannot misreport.

## Convergence

Stability comes from:
1) EMA smoothing (alpha up 0.3, down 0.2)
2) Write interval (15s)
3) Controller cooldown (10s)
4) Hysteresis threshold (2% minimum change)

## Code-to-Theory Mapping

| Concept | Code Location |
| --- | --- |
| Agent identification | `pkg/agent/agent.go: discoverPods` |
| Demand measurement | `pkg/agent/cgroup/reader.go: ReadPodDemand` |
| Demand smoothing | `pkg/agent/demand/tracker.go: Update` |
| Need estimation | `pkg/allocation/market.go: computeNeed` |
| Capacity constraint | `pkg/allocation/market.go: ClearMarketWithMetadata` |
| Nash bargaining | `pkg/allocation/market.go: nashReduce` |
| Baseline scaling | `pkg/allocation/market.go: scaleBaselines` |
| Mode detection | `pkg/allocation/market.go: ClearMarketWithMetadata` |
