# Migration Plan: MBCAS → MBCAS-PF

**From:** Pod-centric competitive allocation (feature/simplification)
**To:** Path-aware cooperative SLO optimization

---

## Current State Assessment

### What Exists (feature/simplification)

| Component | Location | Status |
|-----------|----------|--------|
| Cgroup throttling reader | `pkg/agent/cgroup/reader.go` | Working, single-signal |
| EMA demand tracker | `pkg/agent/demand/tracker.go` | Working |
| Pod parameter calculator | `pkg/agent/demand/calculator.go` | Working, pod-centric |
| "Market" solver | `pkg/allocation/market.go` | Working, mislabeled |
| Node agent orchestration | `pkg/agent/agent.go` | Working |
| PodAllocation CRD | `api/v1alpha1/` | Working |
| Controller/actuator | `pkg/controller/` | Working, single-container bug |
| UrbanMoveMS demo stack | `Software-Oriented-Application/` | Available |

### Known Defects to Fix During Migration

| Issue | Severity | Fix During |
|-------|----------|------------|
| Multi-container pods only resize first container | P0 | Phase 2 |
| Step-size safety bypass when current=0 | P0 | Phase 1 |
| PodAllocation lacks optimistic locking | P0 | Phase 2 |
| No dependency awareness | Architectural | Phase 3 |
| Misleading "Nash bargaining" naming | Documentation | Phase 1 |

---

## Migration Phases

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                            MIGRATION TIMELINE                                │
├──────────┬──────────┬──────────┬──────────┬──────────┬──────────┬──────────┤
│  Week 1  │  Week 2  │  Week 3  │  Week 4  │  Week 5  │  Week 6  │  Week 7  │
├──────────┴──────────┼──────────┴──────────┼──────────┴──────────┼──────────┤
│      PHASE 1        │      PHASE 2        │      PHASE 3        │  PHASE 4 │
│   Stabilization     │   Infrastructure    │   Path-Aware Core   │ Analysis │
│   + Cleanup         │   + Signals         │   + New Allocator   │  Layer   │
└─────────────────────┴─────────────────────┴─────────────────────┴──────────┘
```

---

## Phase 1: Stabilization and Honest Foundations

**Duration:** 1-2 weeks
**Branch:** `feature/pf-phase1-stabilization`
**Goal:** Fix critical bugs, rename misleading components, establish honest baseline

### 1.1 Fix P0 Safety Bugs

**File:** `pkg/controller/podallocation_controller.go`

**Current problem:** Step-size check bypassed when `current=0` or `desired=0`

**Change:**
- Remove the "allow if either is 0" branch
- Introduce minimum safe baseline (e.g., 50m) for factor calculation
- Add absolute delta cap (e.g., 2000m max change per cycle)
- Add test coverage for edge cases

### 1.2 Rename Misleading Components

**Rationale:** Intellectual honesty before building on top

| Current Name | New Name | Reason |
|--------------|----------|--------|
| `market.go` | `allocator.go` | Not a market mechanism |
| `nashReduce()` | `proportionalSurplusReduce()` | Not Nash bargaining |
| `ClearMarket()` | `ComputeAllocation()` | No market clearing |
| `PodParams.Bid` | `PodParams.EffectiveWeight` | Not a bid |
| `THEORY.md` references | Update all | Align with reality |

**New file header for `pkg/allocation/allocator.go`:**
```
// Package allocation implements CPU allocation using proportional
// surplus reduction. This is a heuristic allocator that guarantees
// baselines and distributes remaining capacity proportionally to
// estimated need. It is NOT a game-theoretic mechanism.
//
// For game-theoretic analysis, see pkg/analysis/ (Phase 4).
```

### 1.3 Document Current Behavior Accurately

**File:** `docs/ALLOCATION_ALGORITHM.md` (new)

Contents:
- Exact formulas currently implemented
- Explicit statement of what it optimizes (and doesn't)
- Known limitations
- Comparison to claimed vs actual behavior

### 1.4 Establish Baseline Metrics

**File:** `pkg/agent/metrics.go` (extend)

Add metrics for later comparison:
- `mbcas_allocation_decisions_total` (counter)
- `mbcas_slo_violation_seconds` (histogram) — placeholder, wired in Phase 3
- `mbcas_allocation_stability` (gauge) — coefficient of variation over window

### Phase 1 Deliverables

- [ ] Safety bugs fixed with tests
- [ ] All misleading names corrected
- [ ] Accurate documentation of current algorithm
- [ ] Baseline metrics instrumented
- [ ] All existing tests passing
- [ ] Clean merge to `main`

---

## Phase 2: Infrastructure for Path Awareness

**Duration:** 2 weeks
**Branch:** `feature/pf-phase2-infrastructure`
**Goal:** Build components needed for dependency-aware allocation without changing allocation logic yet

### 2.1 Service Dependency Graph

**New package:** `pkg/topology/`

```
pkg/topology/
├── graph.go          # Core graph structure
├── discovery.go      # Methods to populate graph
├── types.go          # ServiceNode, ServiceEdge, RequestPath
└── graph_test.go
```

**Core types:**

```
ServiceNode:
  - UID (matches pod UID for now, service UID later)
  - Name, Namespace
  - CurrentAllocation
  - ObservedLatency (P50, P95, P99)
  - ThrottlingRatio

ServiceEdge:
  - From, To ServiceNode
  - CallFrequency (requests/sec)
  - AvgLatency

RequestPath:
  - ID
  - Services []ServiceNode (ordered)
  - Priority (1-10)
  - SLOTarget (latency in ms)
  - CurrentP95
```

**Discovery methods (pluggable):**

| Method | Source | Accuracy | Implementation |
|--------|--------|----------|----------------|
| Annotation-based | Pod annotations | Manual | Phase 2 |
| Trace-based | OpenTelemetry spans | High | Phase 3 |
| Service mesh | Istio/Linkerd metrics | High | Future |

**Phase 2 implementation:** Annotation-based only

```yaml
# Example pod annotation
metadata:
  annotations:
    mbcas.io/dependencies: "ticket-service,bus-service"
    mbcas.io/paths: "user-booking:1:200ms,admin-query:2:500ms"
```

### 2.2 Fix Multi-Container Bug

**Files:** 
- `pkg/agent/demand/calculator.go`
- `pkg/controller/podallocation_controller.go`
- `pkg/actuator/resize.go`

**Change:**
- Calculator: Aggregate demand across all containers
- Controller: Apply allocation proportionally to all containers based on original limit ratios
- Actuator: Patch all containers, not just first

**New field in PodAllocation CRD:**
```yaml
spec:
  containerAllocations:
    - containerName: main
      desiredCPULimit: "500m"
    - containerName: sidecar
      desiredCPULimit: "100m"
```

### 2.3 Add Latency Signal Collection

**New package:** `pkg/signals/`

```
pkg/signals/
├── collector.go      # Interface for signal collection
├── throttling.go     # Existing cgroup signal (refactored from pkg/agent/cgroup)
├── latency.go        # New: latency from annotations or metrics
└── composite.go      # Combine multiple signals
```

**Latency sources (Phase 2):**
- Pod annotations (manual, for testing)
- Prometheus scrape (if metrics-server available)

**Latency sources (Phase 3):**
- OpenTelemetry collector integration

### 2.4 Optimistic Locking for CRD Updates

**File:** `pkg/agent/writer.go`

**Change:**
- Use Server-Side Apply with field manager `mbcas-agent`
- Or: Read-modify-write with resourceVersion, retry on conflict

### Phase 2 Deliverables

- [ ] Topology package with annotation-based discovery
- [ ] Multi-container allocation working
- [ ] Latency signal collection (annotation-based)
- [ ] CRD updates use optimistic locking
- [ ] Integration test: multi-container pod resize
- [ ] Integration test: topology graph construction
- [ ] Clean merge to `main`

---

## Phase 3: Path-Aware Allocation Core

**Duration:** 2 weeks
**Branch:** `feature/pf-phase3-path-allocation`
**Goal:** Replace pod-centric allocator with path-aware SLO-centric allocator

### 3.1 Path Criticality Calculator

**New file:** `pkg/allocation/criticality.go`

**Algorithm:**
```
For each service S:
  criticality[S] = 0
  For each path P containing S:
    path_weight = P.Priority × (1 + max(0, P.CurrentP95 - P.SLOTarget) / P.SLOTarget)
    criticality[S] += path_weight
  
  // Normalize
  criticality[S] /= max_criticality
```

**Properties:**
- Services on more paths get higher criticality
- Services on higher-priority paths get higher criticality
- Services on paths currently violating SLO get boosted criticality

### 3.2 Bottleneck Detection

**New file:** `pkg/allocation/bottleneck.go`

**Algorithm:**
```
For each path P:
  For each service S in P:
    bottleneck_score[S][P] = S.ThrottlingRatio × S.LatencyContribution
  
  bottleneck[P] = argmax_S(bottleneck_score[S][P])

Global bottleneck score:
  bottleneck_score[S] = Σ_P (bottleneck_score[S][P] × P.Priority)
```

**Output:** Ranked list of services by "how much they're blocking the system"

### 3.3 New Allocation Objective

**New file:** `pkg/allocation/slo_allocator.go`

**Objective function:**
```
Minimize: Σ_P Priority[P] × max(0, EstimatedLatency[P] - SLOTarget[P])

Subject to:
  Σ_S Allocation[S] ≤ Capacity
  MinCPU[S] ≤ Allocation[S] ≤ MaxCPU[S]
```

**Approximation (tractable):**
```
1. Allocate MinCPU to all services
2. Compute remaining capacity
3. Distribute remaining capacity weighted by:
   weight[S] = criticality[S] × (1 + bottleneck_score[S])
4. Respect MaxCPU bounds via water-filling
```

### 3.4 Integration with Agent

**File:** `pkg/agent/agent.go`

**Changes:**
- Inject topology graph (from Phase 2)
- Replace `computeAllocations()` call with new `ComputePathAwareAllocation()`
- Add fallback: if no topology available, use legacy allocator

**New agent initialization:**
```
agent := &Agent{
    // ... existing fields ...
    topology:     topology.NewGraph(),
    allocator:    allocation.NewSLOAllocator(),  // New
    legacyMode:   false,                          // Feature flag
}
```

### 3.5 SLO Configuration

**New CRD:** `SLOPolicy`

```yaml
apiVersion: mbcas.io/v1alpha1
kind: SLOPolicy
metadata:
  name: urbanmove-slos
  namespace: urbanmove
spec:
  paths:
    - name: user-booking
      services: [gateway, ticket-service, bus-service]
      priority: 1
      latencyTarget: 200ms
    - name: admin-dashboard
      services: [gateway, user-service]
      priority: 2
      latencyTarget: 500ms
  defaults:
    latencyTarget: 1000ms
    priority: 5
```

### Phase 3 Deliverables

- [ ] Criticality calculator with tests
- [ ] Bottleneck detector with tests
- [ ] SLO-aware allocator with tests
- [ ] SLOPolicy CRD and controller
- [ ] Agent integration with feature flag
- [ ] UrbanMoveMS demo with path annotations
- [ ] Comparison metrics: legacy vs path-aware allocation
- [ ] Clean merge to `main`

---

## Phase 4: Offline Analysis Layer

**Duration:** 1-2 weeks
**Branch:** `feature/pf-phase4-analysis`
**Goal:** Implement game-theoretic analysis tools for research and parameter tuning

### 4.1 Characteristic Function Estimator

**New package:** `pkg/analysis/`

```
pkg/analysis/
├── coalition.go         # Coalition enumeration utilities
├── characteristic.go    # v(S) estimation methods
├── shapley.go          # Shapley value computation
├── validation.go       # Compare Shapley to online heuristics
└── simulation/
    ├── simulator.go    # Trace replay simulator
    └── scenarios.go    # Test scenario definitions
```

**Characteristic function estimation methods:**

| Method | Input | Accuracy | Cost |
|--------|-------|----------|------|
| Trace replay | Historical traces | High | O(2^n) replays |
| Linear surrogate | Criticality scores | Medium | O(n) |
| Queueing model | Service parameters | Medium | O(n²) |

**Phase 4 implementation:** Linear surrogate + trace replay for validation

### 4.2 Shapley Calculator

**File:** `pkg/analysis/shapley.go`

**Methods:**
- `ExactShapley(services, valueFn)` — O(2^n), for small n
- `MonteCarloShapley(services, valueFn, samples)` — O(samples × n)
- `ApproximateShapley(services, criticalityScores)` — O(n), linear surrogate

**Output format:**
```
ShapleyResult:
  - ServiceValues map[ServiceID]float64
  - TotalValue float64
  - ComputationMethod string
  - ConfidenceInterval float64 (for Monte Carlo)
```

### 4.3 Validation Framework

**File:** `pkg/analysis/validation.go`

**Purpose:** Compare online heuristics to offline ground truth

**Metrics:**
- Rank correlation between criticality scores and Shapley values
- Allocation similarity (cosine distance)
- SLO attainment difference

**Output:** Validation report showing how well online approximates offline

### 4.4 Analysis CLI

**New command:** `cmd/mbcas-analyze/`

```bash
# Compute Shapley values from traces
mbcas-analyze shapley --traces ./traces/ --output shapley.json

# Compare online allocation to Shapley-optimal
mbcas-analyze validate --shapley shapley.json --allocation current.json

# Run simulation scenario
mbcas-analyze simulate --scenario bottleneck-shift --duration 10m
```

### 4.5 Parameter Bridge

**File:** `pkg/analysis/bridge.go`

**Purpose:** Generate online allocator parameters from offline analysis

**Outputs:**
- Criticality weight adjustments
- Bottleneck detection thresholds  
- Path priority recommendations

**Workflow:**
```
Traces → Shapley Analysis → Parameter Recommendations → ConfigMap → Agent reload
```

### Phase 4 Deliverables

- [ ] Characteristic function estimators
- [ ] Shapley calculator (exact + Monte Carlo)
- [ ] Validation framework
- [ ] Analysis CLI tool
- [ ] Parameter bridge
- [ ] Documentation: "How offline analysis informs online control"
- [ ] Research notebook: Shapley analysis on UrbanMoveMS traces
- [ ] Clean merge to `main`

---

## Phase 5: Integration and Validation

**Duration:** 1 week
**Branch:** `feature/pf-phase5-integration`
**Goal:** End-to-end validation on UrbanMoveMS

### 5.1 UrbanMoveMS Instrumentation

**Changes to UrbanMoveMS:**
- Add OpenTelemetry tracing to all services
- Add MBCAS annotations for dependencies and paths
- Create SLOPolicy for realistic scenarios
- Add load generator with realistic traffic patterns

### 5.2 Experiment Framework

**New directory:** `experiments/`

```
experiments/
├── scenarios/
│   ├── steady-state.yaml
│   ├── bottleneck-shift.yaml
│   ├── cascade-failure.yaml
│   └── burst-traffic.yaml
├── baselines/
│   ├── static-limits.yaml
│   ├── pod-fair-mbcas.yaml
│   └── vpa-recommender.yaml
├── run_experiment.py
└── analyze_results.py
```

### 5.3 Comparison Experiments

| Experiment | Question | Metrics |
|------------|----------|---------|
| Steady state | Does path-aware improve SLO? | P95 latency, SLO violations |
| Bottleneck shift | Does system adapt to moving bottleneck? | Time to recover, allocation changes |
| Burst traffic | Does system handle sudden load? | Max latency spike, recovery time |
| Resource pressure | Does system degrade gracefully? | SLO attainment vs available CPU |

### 5.4 Publication Artifacts

- Figures: Allocation comparison, SLO attainment curves
- Tables: Shapley vs criticality correlation
- Case study: UrbanMoveMS bottleneck scenario

### Phase 5 Deliverables

- [ ] Instrumented UrbanMoveMS
- [ ] Experiment framework
- [ ] Baseline comparisons completed
- [ ] Results analyzed and documented
- [ ] Publication-ready figures and tables
- [ ] Final merge to `main`

---

## Repository Structure (Final State)

```
mbcas/
├── api/
│   └── v1alpha1/
│       ├── podallocation_types.go
│       └── slopolicy_types.go          # NEW
├── cmd/
│   ├── agent/
│   ├── controller/
│   └── mbcas-analyze/                   # NEW
├── pkg/
│   ├── agent/
│   │   ├── agent.go                     # MODIFIED
│   │   └── ...
│   ├── allocation/
│   │   ├── allocator.go                 # RENAMED from market.go
│   │   ├── slo_allocator.go             # NEW
│   │   ├── criticality.go               # NEW
│   │   └── bottleneck.go                # NEW
│   ├── analysis/                         # NEW
│   │   ├── shapley.go
│   │   ├── characteristic.go
│   │   └── validation.go
│   ├── controller/
│   ├── signals/                          # NEW
│   │   ├── collector.go
│   │   ├── throttling.go
│   │   └── latency.go
│   └── topology/                         # NEW
│       ├── graph.go
│       └── discovery.go
├── config/
│   ├── crd/
│   │   └── bases/
│   │       ├── allocation.mbcas.io_podallocations.yaml
│   │       └── allocation.mbcas.io_slopolicies.yaml  # NEW
│   └── samples/
│       └── urbanmove-slo.yaml           # NEW
├── experiments/                          # NEW
├── docs/
│   ├── ALLOCATION_ALGORITHM.md          # NEW
│   ├── PATH_AWARE_DESIGN.md             # NEW
│   └── OFFLINE_ANALYSIS.md              # NEW
└── test/
    ├── integration/
    └── e2e/
```

---

## Migration Checklist

### Phase 1 (Week 1-2)
- [ ] Create branch `feature/pf-phase1-stabilization`
- [ ] Fix step-size safety bug
- [ ] Rename market.go → allocator.go
- [ ] Rename all misleading functions
- [ ] Update THEORY.md to be accurate
- [ ] Create ALLOCATION_ALGORITHM.md
- [ ] Add baseline metrics
- [ ] All tests passing
- [ ] PR review and merge

### Phase 2 (Week 3-4)
- [ ] Create branch `feature/pf-phase2-infrastructure`
- [ ] Implement pkg/topology/
- [ ] Implement annotation-based discovery
- [ ] Fix multi-container bug
- [ ] Extend PodAllocation CRD for multi-container
- [ ] Implement pkg/signals/
- [ ] Add optimistic locking to writer
- [ ] Integration tests
- [ ] PR review and merge

### Phase 3 (Week 5-6)
- [ ] Create branch `feature/pf-phase3-path-allocation`
- [ ] Implement criticality calculator
- [ ] Implement bottleneck detector
- [ ] Implement SLO-aware allocator
- [ ] Create SLOPolicy CRD
- [ ] Integrate with agent (feature flag)
- [ ] Demo on UrbanMoveMS
- [ ] Comparison metrics
- [ ] PR review and merge

### Phase 4 (Week 7-8)
- [ ] Create branch `feature/pf-phase4-analysis`
- [ ] Implement characteristic function estimators
- [ ] Implement Shapley calculator
- [ ] Implement validation framework
- [ ] Create analysis CLI
- [ ] Implement parameter bridge
- [ ] Documentation
- [ ] PR review and merge

### Phase 5 (Week 9)
- [ ] Create branch `feature/pf-phase5-integration`
- [ ] Instrument UrbanMoveMS
- [ ] Build experiment framework
- [ ] Run comparison experiments
- [ ] Analyze results
- [ ] Create publication artifacts
- [ ] Final merge to `main`

---

## Risk Mitigation

| Risk | Mitigation |
|------|------------|
| Phase 3 breaks existing functionality | Feature flag for legacy mode; extensive regression tests |
| Topology discovery too complex | Start with annotations only; trace-based is optional enhancement |
| Shapley computation too slow | Monte Carlo with configurable sample count; linear surrogate as fallback |
| UrbanMoveMS not representative | Document limitations; plan for additional case studies |
| Timeline slips | Phases 4-5 can be parallelized; Phase 4 can be descoped to Shapley-only |

---

## Success Criteria

**MVP Success (End of Phase 3):**
- Path-aware allocator deployed and functional
- Demonstrates measurable SLO improvement over pod-fair baseline
- Multi-container bug fixed
- Clean, accurate documentation

**Research Success (End of Phase 5):**
- Shapley analysis validates criticality heuristic (ρ ≥ 0.7)
- Experimental results show ≥10% SLO improvement
- Publication-ready artifacts complete
- Clear contribution to PF theme demonstrated