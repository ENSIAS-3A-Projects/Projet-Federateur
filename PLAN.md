# PLAN.md — Market-Based Collaborative Auto-Scaler (MBCAS)

## Project framing

**MBCAS** is a Kubernetes custom controller that treats each node as a **resource market** and allocates resources using **mechanism design** ideas (proportional fairness / Nash Social Welfare). It is designed to stay within the PF scope:

> **PF Scope:** implementation and optimization of collaborative strategies in microservices architectures using game theory and agent-based models to improve resource allocation, coordination, and system performance.

### Design principles

1. **Agent-based sensing (distributed):** each microservice runs a lightweight agent that converts local signals (RPS/latency/queue depth/throttling) into a **demand intensity**.
2. **Centralized decision-making:** the controller computes **effective bids** (budget × demand) and clears a **per-node market** deterministically each control cycle.
3. **Distributed execution:** the controller applies the allocation using **in-place pod resize** (`pods/resize`) via the actuator library.
4. **CPU-first; memory conservative:** v1 focuses on CPU markets; memory is increase-only (or very slow decay) to avoid OOM risk.
5. **Single-writer safety:** MBCAS should not fight HPA/VPA. Run opt-in and/or clearly define knob ownership (requests vs limits).

---

## Phase 1: Infrastructure (The Actuator)

**Goal:** Provide a reusable Go library to apply in-place vertical scaling (`pods/resize`) safely and predictably.  
**Status:** ✅ **Complete**  
**Dependency:** Kubernetes cluster with `pods/resize` support.

### What’s included (Phase 1 hardening)
- Container existence validation (`resolveContainer`)
- CPU/memory quantity validation (`resource.ParseQuantity`)
- Resize policy control: `both | limits | requests`
- Optional wait for resize completion (poll + timeout)
- Dry-run that shows the planned patch
- Unit tests for patch generation, validation, retry, and support checks

### Deliverable
✅ `pkg/actuator` library + CLI wrapper that can resize pods in-place reliably.

### Definition of Done
- [x] Actuator applies `pods/resize` with retries on conflict.
- [x] CLI supports `--policy`, `--dry-run`, and `--wait`.
- [x] Unit tests validate patch structure across policies.
- [x] Manual test: resize a running pod without changing restart count.

---

## Phase 2: Agents (Demand Reporting)

**Goal:** Implement agent-based demand reporting inside microservices (sidecar or library) so each pod contributes **local state** to the collaborative allocation strategy.  
**Constraint:** Agents do **not** send free-form bids. They send **demand intensity** only.  
**Why:** avoids strategic bid inflation; controller enforces budgets and computes effective bids centrally.

### Agent Output (Demand Report)
Each agent produces a demand signal:

- `demand_intensity ∈ [0, 1]` (normalized urgency)
- optional breakdown signals (for debugging): latency pressure, queue pressure, throttling pressure

### Tasks

1. **Define the demand contract**
   - Create `pkg/agent/contracts.go` (or `docs/agent.md`) with:
     - demand definition
     - units and normalization
     - required labels: pod, namespace, container

2. **Implement a minimal Demand Agent (sidecar)**
   - Option A (recommended for Phase 2): agent exports Prometheus metrics:
     - `mbcas_demand_intensity{namespace,pod,container} 0.0..1.0`
   - Option B: agent exposes HTTP endpoint `/demand` returning JSON.

3. **Demand function v1 (simple + stable)**
   - Implement a conservative mapping using one or two signals:
     - queue depth (if available) and/or request rate
     - optional latency threshold breach
   - Normalize into `[0,1]` and apply smoothing in-agent (EWMA).

4. **Opt-in configuration**
   - Define annotations:
     - `mbcas.enabled: "true"`
     - `mbcas.budget: "<int>"`
     - optional bounds: `mbcas.minCPU`, `mbcas.maxCPU`

### Deliverable
- A `mbcas-demand-agent` image and a demo deployment that exports `mbcas_demand_intensity`.

### Definition of Done
- [ ] Under load, the agent metric increases and decreases predictably (no severe jitter).
- [ ] A pod without agent still functions (controller falls back to safe default demand).
- [ ] Demo YAML shows injection + scraping working end-to-end.

---

## Phase 3: Market Clearing Engine (Pure Logic)

**Goal:** Implement the market-clearing solver that converts (capacity, budgets, demand) into CPU allocations.  
**Constraint:** Pure logic package. No Kubernetes dependencies.  
**Focus:** CPU-only market in v1.

### Model (CPU-first)
For each node:

- Inputs:
  - `C`: allocatable CPU capacity (millicores)
  - for each pod `i` on node:
    - `budget_i` (business priority weight)
    - `demand_i ∈ [0,1]`
    - `min_i`, `max_i` bounds (optional)
- Controller computes effective bid:
  - `β_i = budget_i * f(demand_i)` (after smoothing & caps)

### Allocation rule (proportional fairness)
Start with a deterministic proportional solver that supports floors/caps:

1. Reserve floors: allocate `min_i` to each participant (if set)
2. Distribute remaining capacity proportionally:
   - `x_i = min_i + (β_i / Σβ) * remaining`
3. If any `x_i > max_i`, clamp and redistribute leftover (water-filling)

### Tasks

1. **Create `pkg/market`**
   - Types: `NodeMarketInput`, `Allocation`, `Reasoning`
   - Function: `SolveCPU(input) (allocations, debug, error)`

2. **Implement water-filling with caps**
   - Ensure stable behavior when some services hit max.

3. **Add guardrail hooks**
   - Max delta per cycle (CPU)
   - Cooldowns (enforced in controller later, but represented in solver debug)

4. **Unit tests**
   - proportional split baseline
   - floors only
   - caps only
   - floors + caps + redistribution
   - zero/empty demand handling

### Deliverable
- `pkg/market` solver that produces deterministic CPU allocations per node.

### Definition of Done
- [ ] `go test ./...` includes full solver test coverage.
- [ ] Given fixed input, allocations are stable and reproducible.
- [ ] Solver never exceeds capacity and never violates min/max bounds.

---

## Phase 4: Controller Integration (Observe → Clear → Actuate)

**Goal:** Build the in-cluster controller that runs the control loop every 15 seconds and applies allocations safely.  
**Role:** Central market maker + policy enforcer + single writer for resizing.

### Control Loop (every 15s)
1. **Discover participants**
   - pods with `mbcas.enabled=true`
   - read budgets and bounds from annotations/CRD
2. **Read demand**
   - from Prometheus (`mbcas_demand_intensity`) or HTTP endpoint
3. **Group by node**
   - per-node markets (topology constraint)
4. **Solve**
   - call `pkg/market.SolveCPU(...)`
5. **Apply guardrails**
   - smoothing of effective bids
   - max CPU delta per cycle
   - cooldowns
6. **Actuate**
   - call `pkg/actuator.ApplyScaling` with `policy=limits` by default

### Tasks

1. **Controller skeleton**
   - Use `controller-runtime` or a simple loop (Phase 4 can start as loop).
   - Reconcile period: 15s.

2. **Participant discovery**
   - Label/annotation selector for opt-in pods.
   - Resolve node placement.

3. **Demand ingestion**
   - Prometheus query integration:
     - query by pod label set
     - handle missing series safely

4. **Actuation strategy**
   - Default to **CPU limits-only** policy to reduce QoS surprises.
   - Add optional mode to adjust requests+limits for controlled experiments.

5. **Safety**
   - Always include floors and max caps.
   - Never downscale memory aggressively (memory increase-only in v1).
   - Abort changes if cluster lacks `pods/resize`.

### Deliverable
- `mbcas-controller` binary/container running in cluster and resizing a demo workload under changing load.

### Definition of Done
- [ ] Logs show full loop: Discover → Demand → Solve → Guardrail → Patch.
- [ ] Under load spike, high-budget services receive larger CPU share than low-budget.
- [ ] Allocations do not oscillate wildly (bounded by cooldown + max delta).
- [ ] No fights with other controllers in the demo namespace (single-writer setup).

---

## Phase 5: Observability & Evaluation (Proof of Collaboration)

**Goal:** Demonstrate that the collaborative mechanism improves coordination, fairness, and stability versus baseline autoscaling approaches.

### Metrics to expose
- `mbcas_alloc_cpu_mcores{pod}` (allocated CPU)
- `mbcas_effective_bid{pod}` (controller-derived β)
- `mbcas_demand_intensity{pod}` (agent-reported)
- `mbcas_resize_events_total{pod,action}`
- `mbcas_resize_delta_mcores{pod}`
- Optional evaluation metrics:
  - fairness ratio: `alloc_share / budget_share`
  - stability: average absolute delta per cycle
  - noisy-neighbor impact proxy: throttle time / latency spikes

### Tasks

1. **Controller exports Prometheus metrics**
   - allocations, bids, resize events, rejections (if any)

2. **Grafana dashboard**
   - Panel 1: CPU usage vs allocated CPU (efficiency)
   - Panel 2: allocations vs budgets (fairness)
   - Panel 3: resize deltas per cycle (stability)
   - Panel 4: demand intensity vs allocation (responsiveness)

3. **Evaluation experiments**
   - A/B: HPA-only vs MBCAS (CPU-only)
   - Stress test: noisy neighbor on same node
   - Outcome measures: latency p95, throttle time, stability deltas

### Deliverable
- Grafana dashboard JSON + demo runbook.

### Definition of Done
- [ ] Dashboard demonstrates reduced noisy-neighbor impact under contention.
- [ ] Allocation correlates with budgets under contention (priority works).
- [ ] Resize deltas remain bounded (no thrash).

---

## Optional extensions (post-PF / stretch goals)

1. **Memory policy v2**
   - increase-fast, decrease-slow with strict decay
   - runtime hints (optional advisory sidecar signals)

2. **True CPU+memory market**
   - multi-good model or explicit coupling rules
   - publish assumptions clearly

3. **Admission control / CRD policy**
   - centralized budget governance (FinOps)
   - namespace-level budgets and floors

