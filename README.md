# Market-Based Collaborative Auto-Scaler (MBCAS)

**Algorithmic Mechanism Design for Kubernetes Resource Allocation.**  
A Kubernetes custom controller that treats each node as a **resource market** and allocates **CPU** (and optionally **memory**) using **proportional fairness / Nash Social Welfare** ideas inspired by Fisher markets.

> **Core idea:** Instead of reactive thresholds (HPA) or slow iterative learning, MBCAS runs a deterministic **market-clearing** step every control cycle. Microservices run **local agents** that compute *demand signals* from local metrics; the controller converts those signals + budgets into **enforced bids**, clears the market per node, and applies allocations via **in-place pod resize** (`pods/resize`).

---

## Why this exists

Standard autoscaling treats workloads as isolated optimizers. In shared-node microservice clusters this often leads to:

- **Noisy neighbors**: one workload’s spike degrades others
- **Thrashing**: oscillating limits/requests from competing controllers
- **Unclear priority**: “important” services don’t reliably win contention
- **Low utilization**: conservative limits waste capacity

MBCAS reframes resource allocation as **collaborative strategy optimization**: services express urgency (demand), the mechanism converts that into a fair allocation, and the system rebalances each cycle.

---

## Key design decision

### Who computes bids?
To remain production-plausible and avoid manipulation, **pods do not send raw bids**.

- **Distributed agents (pods) compute demand signals** from *local* state.
- **Central controller computes and enforces bids** using:
  - fixed **budgets** (business priority),
  - observed demand signals,
  - guardrails (caps/floors/smoothing).

This keeps the project **agent-based** (local decisions), while making the market **truthful-by-design** in practice (no “bid inflation” interface).

---

## Alignment with PF scope

**PF Scope:** implementation and optimization of collaborative strategies in microservices architectures using game theory and agent-based models to improve resource allocation, coordination, and system performance.

MBCAS fits directly:

- **Agent-based models:** each microservice runs a local agent that maps *local metrics → demand signal*.
- **Game theory / mechanism design:** the controller implements a Fisher-market-inspired allocation rule (proportional fairness / Nash Social Welfare).
- **Collaborative strategy optimization:** services “yield” resources implicitly as demand falls; the mechanism coordinates a stable, fair outcome under contention.
- **System performance:** reduces noisy-neighbor impact, stabilizes allocations, improves utilization.

---

## How it works

MBCAS runs a closed-loop control cycle (e.g., every 15 seconds):

### 1) Demand reporting (distributed agents)
Each participating pod runs a lightweight **Demand Agent** (sidecar or embedded library):

- **Inputs (local):** request rate, queue depth, latency, CPU throttling, etc.
- **Static config:** budget/weight (business priority)
- **Output:** a signed/identified **demand report** to the controller (not a bid)

> Agents remain autonomous: they decide how to interpret local state into urgency.

### 2) Market clearing (central mechanism, per node)
For each node independently:

- collect demand reports for pods scheduled on that node
- read node capacity (CPU allocatable)
- compute **effective bids** = `budget × demand_intensity` (with smoothing/caps)
- allocate CPU to maximize proportional fairness

A single-resource CPU formulation:

\[
\text{maximize } \sum_i \beta_i \log(x_i)\quad \text{s.t. } \sum_i x_i \le C,\ x_i \ge x_{\min}
\]

- \(\beta_i\): enforced effective bid (controller-derived)
- \(x_i\): CPU allocation (limit or request target)
- \(C\): node CPU capacity (allocatable)
- \(x_{\min}\): floor to avoid starvation

### 3) Actuation (distributed execution)
The controller applies the resulting allocations using the Kubernetes `pods/resize` subresource.

- **Zero downtime** when `resizePolicy` allows `NotRequired`
- **Guardrails:** cooldowns, change budgets, floors/caps

---

## Multi-resource stance (explicit)

### v1 (recommended): CPU-first, memory conservative
- **CPU:** actively allocated via market clearing.
- **Memory:**  
  - **increases allowed** when needed (safe),  
  - **decreases optional** and only with conservative decay (e.g., ≤ 5% per cycle) to reduce OOM risk.

### v2 (extension): true two-resource market
A real CPU+memory “bundle” requires an explicit multi-good model or coupling policy. This is an extension, not assumed in v1.

---

## QoS note (requests vs limits)

Kubernetes QoS class depends on requests/limits. Changing them can alter:

- scheduling/eviction priority,
- throttling behavior,
- whether a pod is **Guaranteed / Burstable / BestEffort**.

**Current actuator implementation patches both `requests` and `limits` together** (same value), which can preserve certain QoS properties but may still affect QoS depending on which resources/containers are specified.

**MBCAS policy** will explicitly define whether it:

- adjusts **limits only** (preferred for many clusters), or
- adjusts **requests + limits** together (stronger enforcement, different tradeoffs).

---

## CLI Usage

The `k8s-pod-tool` CLI provides a Phase 1 implementation for in-place pod vertical scaling.

### Update Command

Resize pod resources using the `pods/resize` subresource:

```bash
k8s-pod-tool update -namespace <namespace> -pod <pod-name> [options]
```

#### Required Flags

- `-pod`: Pod name (required)

#### Resource Flags

- `-cpu`: New CPU value (e.g., `250m`, `1`, `0.5`)
- `-memory`: New memory value (e.g., `512Mi`, `1Gi`)

At least one of `-cpu` or `-memory` must be specified.

#### Policy Flags

- `-policy`: Resize policy (default: `both`)
  - `both`: Set both requests and limits to the same value (default, backward compatible)
  - `limits`: Set only limits, leaving requests unchanged
  - `requests`: Set only requests, leaving limits unchanged

#### Wait Flags

- `-wait`: Wait for resize to complete before returning (default: `false`)
- `-wait-timeout`: Maximum time to wait for resize completion (default: `30s`)
- `-poll`: Poll interval when waiting for resize (default: `500ms`)

#### Other Flags

- `-namespace`: Pod namespace (default: `default`)
- `-container`: Container name (optional, defaults to first container)
- `-dry-run`: Show planned changes without applying them

### Examples

**Basic resize (both requests and limits):**
```bash
k8s-pod-tool update -namespace default -pod my-pod -cpu 500m -memory 256Mi
```

**Resize limits only:**
```bash
k8s-pod-tool update -namespace default -pod my-pod -cpu 500m -policy limits
```

**Dry-run to preview changes:**
```bash
k8s-pod-tool update -namespace default -pod my-pod -cpu 500m -dry-run
```

**Wait for resize to complete:**
```bash
k8s-pod-tool update -namespace default -pod my-pod -cpu 500m -wait -wait-timeout 60s
```

**Resize specific container:**
```bash
k8s-pod-tool update -namespace default -pod my-pod -container app -cpu 500m
```

### Error Handling

The CLI provides enhanced error messages with actionable hints:

- **RBAC errors**: "check RBAC: you need 'patch' permission on 'pods/resize' subresource"
- **Feature gate missing**: "enable InPlacePodVerticalScaling feature gate on your cluster"
- **Invalid quantities**: "verify quantities/policy and runtime support for in-place resize"
- **Container not found**: Clear error message with container name

### Prerequisites

- Kubernetes cluster with `InPlacePodVerticalScaling` feature gate enabled
- Kubernetes version 1.27 or later
- RBAC permissions to patch `pods/resize` subresource

---

## System architecture

```text
   ┌───────────────────────────────┐
   │     Demand Agents (Pods)      │
   │  local metrics → demand report│
   └──────────────┬────────────────┘
                  │ demand reports
                  ▼
   ┌─────────────────────────────────────────────┐
   │            Market Controller (Go)           │
   │                                             │
   │  1) Discover budgets (annotations/CRD)      │
   │  2) Aggregate demand per node               │
   │  3) Compute effective bids (enforced)       │
   │  4) Clear per-node CPU markets              │
   │  5) Apply guardrails (floors/caps/cooldowns)│
   │  6) Actuate via pods/resize                 │
   └──────────────┬──────────────────────────────┘
                  │ resize patches
                  ▼
   ┌─────────────────────────────────────────────┐
   │           Kubernetes API Server             │
   │             (pods/resize)                   │
   └──────────────┬──────────────────────────────┘
                  ▼
   ┌─────────────────────────────────────────────┐
   │               Worker Nodes                  │
   │           kubelet applies resize            │
   └─────────────────────────────────────────────┘
