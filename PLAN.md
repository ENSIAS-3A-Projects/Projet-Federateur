## Phase 1: The Infrastructure (The Actuator)

**Goal:** Establish the mechanism to vertically scale Pods in-place without restarts.
**Status:** *Partially Implemented (CLI Tool exists).*
**Dependency:** Kubernetes Cluster (Minikube) with `InPlacePodVerticalScaling` feature gate.

### Tasks

1. **Refactor CLI to Library:** Convert the existing `update.go` logic into a reusable package `pkg/actuator`.
* **Function Signature:** `ApplyScaling(ctx, podName, ns, newCpu, newMem) error`


2. **Verify Feature Gates:** Add a startup check to ensure the cluster supports the `resize` subresource.
3. **Implement Retry Logic:** Kubernetes updates can fail on version conflicts. Implement a simple retry mechanism for the `Patch` operation.

### Deliverable

A Go library function that takes a target resource value and successfully patches the Pod spec in the running cluster.

### Definition of Done

* [ ] Unit tests pass for patch generation.
* [ ] Integration test: Calling `ApplyScaling` updates a running Pod's resources without changing its `restartCount`.

---

## Phase 2: The Simulation Engine (The Brain)

**Goal:** Implement the Game Theory logic to calculate optimal resource strategies.
**Constraint:** **Pure Logic Only.** No Kubernetes dependencies. This phase operates on abstract `Agent` structs.
**Theoretical Basis:** Regret Matching & Potential Games.

### Mathematical Specification

Implementation must strictly follow these formulas derived from the research:

1. **The Utility Function ():**
Each agent maximizes:


* *Performance:* Inverse latency or throughput.
* *Cost:* Resource usage  Price factor.
* *Congestion:* Penalty if Node Capacity is exceeded (Internalizing Externalities).


2. **The Algorithm (Regret Matching):**
* **Step A:** Track **Cumulative Regret ()**. For every possible action  (even those not chosen), calculate:


* **Step B:** Calculate Probabilities. Select the next action proportional to **Positive Regret**:


* **Step C:** Sample the next resource limit from this distribution. "Actions with high positive regret get selected more often."



### Tasks

1. **Define Agent Structure:** Create `pkg/gametheory/agent.go`.
* State: `CurrentUsage`, `CurrentLimit`, `RegretHistory` (Map of action  float).


2. **Implement Simulation Loop:** Create a function that runs  rounds of the Regret Matching algorithm to find the stable state (Nash Equilibrium) before applying it.
3. **Implement Global Potential Function ():**
* Calculate . This will serve as the **Lyapunov Function** to prove convergence later.



### Deliverable

A Go package `pkg/gametheory` that accepts a list of agent metrics and returns the next resource recommendations.

### Definition of Done

* [ ] Unit Test: A simulation of 50 rounds shows the agent's decisions converging (stabilizing) on a specific value.
* [ ] The logic correctly implements the **Regret Matching** algorithm described above.

---

## Phase 3: The Safety Layer (The Diplomat)

**Goal:** Prevent OOM Kills by enforcing "Individually Rational" constraints via a sidecar.
**Theoretical Basis:** A Pod must reject any resource cut that drops it below its "Punishment Level" (minimum survival resources).

### Tasks

1. **Build Sidecar Agent:** A lightweight HTTP server (`go` or `python`) to run alongside the application.
* **Endpoint:** `POST /negotiate`
* **Payload:** `{ "proposed_memory": "500Mi" }`


2. **Implement Memory Hooks:**
* **Golang Apps:** Call `debug.FreeOSMemory()` upon receiving a request.
* **Java Apps:** Trigger `System.gc()` or tune `G1GC` parameters dynamically.


3. **Implement The Veto:**
* **Check:** `if CurrentUsage > ProposedLimit`.
* **Action:** Return `409 Conflict`. This signals that the proposal is not **Individually Rational**—it violates the "Punishment Level" constraint.



### Deliverable

A Docker image `gtcas-sidecar:latest` that can be injected into any Deployment.

### Definition of Done

* [ ] `curl` request to the sidecar triggers a measurable drop in RAM usage (verified via `top` or metrics).
* [ ] Sidecar returns `409` if the request is aggressive/unsafe.

---

## Phase 4: Integration (The Controller)

**Goal:** Automate the loop and implement Correlated Equilibrium.
**Role:** The Controller acts as the "Traffic Light" (Correlation Device) coordinating the agents.

### Tasks

1. **Controller Skeleton:** Create the main loop (`main.go`) running every 15 seconds.
2. **Metric Ingestion:** Use Prometheus Client to fetch "Golden Signals" (Latency, CPU) for all Game-Aware Pods.
3. **Pipeline Construction:**
* **Step 1:** Fetch Metrics -> Convert to `Agent` structs.
* **Step 2:** Pass Agents to **Phase 2 Engine** -> Get Recommendations.
* **Step 3:** Send Recommendations to **Phase 3 Sidecar** -> Negotiate.
* **Step 4:** If Accepted, call **Phase 1 Actuator** -> Patch Kubernetes.



### Deliverable

A standalone binary running in the cluster that manages the lifecycle of resource resizing.

### Definition of Done

* [ ] The system automatically resizes a test deployment under load.
* [ ] Logs show the full cycle: Observe -> Calculate -> Negotiate -> Act.

---

## Phase 5: Observability (The Eyes)

**Goal:** Prove convergence and stability.
**Theoretical Basis:** Lyapunov Functions.

### Tasks

1. **Expose Custom Metrics:** The Controller must export the calculated **Global Potential Function** as a Prometheus metric (e.g., `gtcas_global_potential`).
2. **Grafana Dashboard:**
* **Panel 1:** CPU Usage vs. Limit (Efficiency).
* **Panel 2:** The Lyapunov Function ().
* **Success Criteria:** The Lyapunov line must strictly increase/decrease and then plateau, proving mathematical convergence.



### Deliverable

A Grafana Dashboard JSON file and Prometheus configuration.

### Definition of Done

* [ ] Visual proof that the system stops oscillating ("thrashing") after N iterations.