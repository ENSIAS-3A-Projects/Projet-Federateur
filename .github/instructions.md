### GitHub Issue List (P0/P1 focus: actual flaws/bugs)

---

## 1) P0 — Multi-container pods are not supported (only `Containers[0]`)

**Labels:** `bug`, `correctness`, `kubernetes`
**Problem:** The system uses only `pod.Spec.Containers[0]` for parameter extraction and resize targeting; other containers in the same pod are ignored. 
**Impact:** Incorrect allocations and incorrect enforcement for real-world pods (sidecars, service mesh, exporters, etc.). Can lead to persistent throttling in ignored containers, or resizing the wrong container.
**Proposed fix (minimal viable):**

* Make the behavior explicit and correct by choosing one:

  1. **Pod-level allocation split across containers** (e.g., proportional to current `limits.cpu` or `requests.cpu`), and patch all containers; or
  2. Treat **each container as an agent** (larger change).
* Update both calculator and actuator paths to iterate `pod.Spec.Containers[]`.
  **Acceptance criteria:**
* A pod with 2+ containers receives deterministic CPU limit updates for all containers according to the chosen policy.
* Unit/integration test exists for multi-container pod resize behavior.
* Documentation states the chosen policy and rationale.

---

## 2) P0 — Step-size safety check can be bypassed when current or desired is 0

**Labels:** `bug`, `safety`, `correctness`
**Problem:** The controller’s step-size check explicitly “allows” when either value is 0. 
**Impact:** Large, abrupt CPU jumps can occur without triggering the step-factor limit, undermining the intended safety invariant (step factor ≤ 10×). 
**Proposed fix:**

* Remove the “allow if either is 0” branch.
* Define a safe baseline for zero cases (e.g., if current=0, substitute `max(requestMilli, minMilli, lastApplied)`; if desired=0 treat as invalid because allocations should be bounded by min).
* Add a second guard: **absolute delta cap** (e.g., max +N millicores per apply) to prevent extreme jumps even when factor is small.
  **Acceptance criteria:**
* Any resize where factor exceeds configured max is rejected even if current or desired is 0.
* Tests cover: current=0 → desired large; desired=0; both nonzero.
* Safety metrics/logging show when step check blocks an action.

---

## 3) P0 — PodAllocation CRD updates lack optimistic locking (risk of lost updates)

**Labels:** `bug`, `correctness`, `api-concurrency`
**Problem:** Agent updates do not use `resourceVersion` for optimistic locking (noted explicitly), meaning concurrent writes can overwrite each other. 
**Impact:** Lost updates / stale desired limits, non-deterministic behavior under contention, hard-to-debug flapping.
**Proposed fix (preferred):**

* Switch to **server-side apply** (SSA) for PodAllocation spec with field ownership, or
* Use typed client read–modify–update preserving `resourceVersion`, with retry on conflict.
  **Acceptance criteria:**
* Under concurrent updates, desiredCPULimit converges to the latest decision without silent loss.
* Add a stress test (or integration test) that simulates conflict and confirms retry/SSA behavior.

---

## 4) P1 — Demand calculation is unstable for small/zero `deltaUsage` and can misclassify demand

**Labels:** `bug`, `stability`, `metrics`
**Problem:** Raw demand is computed from `deltaThrottled/deltaUsage`, then `demand = throttlingRatio / 0.1`, clamped; if `deltaUsage == 0`, demand returns 0. 
**Impact:**

* If `deltaUsage` is tiny, ratio becomes numerically unstable (spikes).
* If `deltaUsage == 0` during brief windows, demand is forced to 0, causing incorrect downward pressure.
  This can induce avoidable oscillations, especially with short sampling intervals.
  **Proposed fix:**
* Add `MIN_USAGE_USEC` threshold: if `deltaUsage < MIN_USAGE_USEC`, treat sample as invalid and reuse prior smoothed demand (or skip update).
* Compute deltas with actual elapsed time (timestamp already exists in samples). 
* Emit metrics: dropped/invalid samples count.
  **Acceptance criteria:**
* Demand is stable under low-usage or idle windows; no spurious drop to zero due solely to `deltaUsage==0`.
* Tests cover: tiny usage, zero usage, burst throttling.

---

## 5) P1 — No verification that in-place resize was actually applied by kubelet

**Labels:** `bug`, `observability`, `correctness`
**Problem:** The system explicitly relies on the assumption that “kubelet respects patched limits monotonically” with “no explicit verification of applied state.” 
**Impact:** Controller may mark progress (or continue operating) even if resizes are not actually applied (feature gate off, RBAC issues, transient kubelet behavior, conflicts).
**Proposed fix:**

* After applying resize, re-fetch pod and verify `resources.limits.cpu` matches the intended value (or verify resize status if available).
* Record verification result in PodAllocation status (e.g., `Applied`, `Pending`, `Failed`, with reason).
  **Acceptance criteria:**
* Controller detects and reports failed or unapplied resizes.
* Status accurately reflects “desired vs observed”.
* Logs/metrics expose apply latency and failures.

---

## 6) P1 — Init and ephemeral containers are not explicitly handled (can cause misbehavior)

**Labels:** `bug`, `correctness`, `kubernetes`
**Problem:** Init containers are “not explicitly filtered” and ephemeral containers are “not considered.” 
**Impact:** Parameter extraction and/or cgroup reading may behave unpredictably if init/ephemeral containers have different resource configs or lifecycle.
**Proposed fix:**

* Clarify scope: allocation/enforcement only applies to normal containers.
* Ensure the control logic explicitly ignores init/ephemeral containers for sizing/enforcement, or documents the limitation and excludes such pods.
  **Acceptance criteria:**
* Pods with init containers do not break calculations or enforcement.
* If ephemeral containers exist, behavior is deterministic (ignore or handle explicitly).
* Tests cover presence of init containers.

---

# Optional (P2) — Not “bugs” but high-value hardening

## 7) P2 — Make control constants configurable (thresholds, alphas, cooldowns)

**Labels:** `enhancement`, `operability`
Justification: demand threshold (0.1) and EMA constants are hard-coded in the described flow.  This is acceptable for v1, but production robustness improves with config + safe defaults.

