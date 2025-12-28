

# PLAN.md — MBCAS Project Plan

## Project Goal

Design and implement a **market-based, kernel-informed CPU allocation system for Kubernetes** that dynamically redistributes CPU among running pods using real execution pressure, without relying on user configuration, external metrics systems, or Kubernetes autoscalers such as HPA.

The system must:

* be Kubernetes-native
* maximize CPU utilization and fairness
* remain stable under real workloads
* reuse existing, proven tools wherever possible
* avoid unnecessary complexity

---

## Guiding Principles

1. **Reuse before build**
   If Kubernetes or Linux already provides it, use it.

2. **Kernel truth over declared intent**
   Real pressure beats configured metrics.

3. **One responsibility per component**
   Sensing, decision, enforcement are separate.

4. **Control-plane simplicity**
   No custom networking, no receivers, no streaming protocols.

5. **Stability over aggressiveness**
   Avoid oscillations even at the cost of slower convergence.

---

## Current Status (Baseline)

### Completed

* In-place pod resizing actuator using `pods/resize`
* CLI for manual inspection and testing

### Not Yet Implemented

* Node agent
* Demand sensing
* Allocation logic
* CRDs
* Controllers

This is intentional and correct.

---

## High-Level Architecture (Target)

```
Linux Kernel (cgroups, PSI)
        ↓
Node Agent (DaemonSet)
        ↓
PodAllocation (CRD)
        ↓
Controller
        ↓
Actuator (existing)
        ↓
pods/resize
```

Kubernetes Scheduler remains unchanged and continues to control placement.

---

## Phase 1 — Control Contract (CRD)

### Objective

Define a **single authoritative control object** for CPU allocation decisions.

### Tooling

* Kubernetes CRDs
* controller-runtime code generation

### Tasks

* Define `PodAllocation` CRD
* One object per pod (name = pod UID)
* Fields:

  * desired CPU limit
  * status (applied, timestamps, reason)
* No user-facing configuration
* No validation webhooks
* No multiple versions

### Rationale

CRDs provide:

* persistence
* observability
* reconciliation semantics
* auditability

No custom APIs required.

---

## Phase 2 — Actuation Controller

### Objective

Enforce CPU decisions declaratively.

### Tooling

* controller-runtime
* existing `pkg/actuator`

### Tasks

* Implement controller watching:

  * `PodAllocation`
  * `Pod`
* Compare desired vs actual CPU
* Apply changes using the actuator
* Update status and emit Kubernetes Events

### Safety Rules

* Mutate CPU limits only
* Rate-limit resizes per pod
* Maximum step size per resize

### Rationale

Controller-runtime provides:

* caching
* retries
* backoff
* leader election (optional)

No custom loops required.

---

## Phase 3 — Node Agent (Kernel Demand Sensing)

### Objective

Infer real CPU demand using kernel signals.

### Tooling

* Linux cgroups
* Linux PSI
* Kubernetes client-go
* DaemonSet

### Tasks

* Deploy lightweight agent per node
* Read:

  * CPU throttling (cgroups)
  * CPU PSI
* Aggregate per pod
* Normalize demand ∈ [0,1]
* Smooth signals (fast up, slow down)
* Write desired CPU into `PodAllocation`

### Explicit Non-Goals

* No gRPC
* No HTTP endpoints
* No Prometheus dependency
* No time-series storage

### Rationale

Kernel signals are:

* always available
* low overhead
* truthful by construction

---

## Phase 4 — Market-Based Allocation Logic

### Objective

Replace HPA-style scaling with a **market-clearing allocation rule**.

### Tooling

* Pure Go logic
* No Kubernetes dependencies in solver

### Allocation Model

* CPU is a finite divisible good
* Pods are agents
* Kernel pressure acts as implicit bids
* Allocation approximates proportional fairness

Formally:

* Maximize Nash Social Welfare under node capacity

### Tasks

* Compute baseline fair share per pod
* Distribute remaining CPU proportional to demand
* Enforce min/max CPU bounds
* Ignore insignificant changes (hysteresis)

### Rationale

This provides:

* fairness
* efficiency
* stability
* incentive compatibility

---

## Phase 5 — Stability and Safety Hardening

### Objective

Ensure correctness under real workloads.

### Tooling

* Simple control rules
* No advanced control frameworks

### Measures

* Demand smoothing
* Deadbands
* Cooldowns
* Warm-up period for new pods
* Node-level CPU reserve
* Guaranteed pod exclusion

### Rationale

These are standard control-theory safeguards implemented with minimal code.

---

## Phase 6 — Operational Visibility

### Objective

Maintain debuggability and predictability.

### Tooling

* Kubernetes Events
* CRD status fields
* Optional metrics endpoints (no dependency)

### Tasks

* Record reason for each allocation
* Expose last-applied CPU
* Emit resize events

### Rationale

CRDs act as the system’s observable state without external tooling.

---

## Phase 7 — Evaluation and Validation

### Objective

Demonstrate effectiveness and academic relevance.

### Metrics

* CPU utilization
* Throttling reduction
* PSI improvement
* Allocation stability
* Reaction time vs HPA

### Method

* Compare against HPA on identical workloads
* Stress-test under contention
* Observe convergence behavior

---

## Explicit Non-Goals

The project will **not**:

* Integrate with Prometheus
* Use HPA or VPA
* Require user annotations
* Modify the scheduler
* Store raw metrics in CRDs
* Implement multi-resource optimization (CPU only)

---

## Final Deliverables

* Kubernetes controller binary
* Node agent DaemonSet
* `PodAllocation` CRD
* Reusable actuator library
* Evaluation results and analysis

---

## Summary

MBCAS explores how **market-based, kernel-informed mechanisms** can replace configuration-heavy autoscaling in Kubernetes. By reusing existing platform primitives and grounding decisions in real execution pressure, the project aims to deliver a simpler, fairer, and more efficient approach to CPU allocation in microservices systems.

