## Game-Theoretic Collaborative Auto-Scaler (GTCAS)

**Centralize Policy, Decentralize Execution.**

An experimental Kubernetes Custom Controller that treats cluster auto-scaling as a **Potential Game**, enabling microservices to negotiate resources strategically rather than reacting chaotically.

> **What this is:** A Kubernetes controller that uses game theory (potential games, Nash equilibrium, no-regret learning) to coordinate vertical pod scaling across services.
>
> **What you get:** Less thrashing, fewer noisy neighbors, and safer in-place scaling for CPU and memory.

---

## Quick View

- **Language:** Go
- **Target:** Kubernetes (Minikube/dev cluster)
- **Scaling Mode:** In-place vertical scaling (no restarts)
- **Core Ideas:** Potential games, Nash equilibrium, no-regret learning, Lyapunov stability
- **Status:** Experimental / research-grade

---

## Code Layout

- **`main.go`**  
  Thin CLI entrypoint. Parses the first argument (`list`, `usage`, `update`) and delegates to the `podtool` package.

- **`pkg/podtool/`** – **CLI layer (tools for humans)**
  - `app.go` – Builds the Kubernetes client (`App`), handles kubeconfig/in-cluster config, and provides global CLI usage text.
  - `list.go` – Implements `k8s-pod-tool list` for listing non-system pods.
  - `usage.go` – Implements `k8s-pod-tool usage` for showing CPU/memory requests & limits for a pod.
  - `update.go` – Implements `k8s-pod-tool update` as a thin wrapper around the actuator library.

- **`pkg/actuator/`** – **Phase 1 library (the in-place scaler)**
  - `actuator.go` – `ApplyScaling` (core in-place scaling API) and `CheckResizeSupport` (verifies `pods/resize` is available).

- **Docs & examples**
  - `README.md` – High-level overview, architecture, and usage.
  - `PLAN.md` – Phase-by-phase roadmap (Actuator → Game Engine → Sidecar → Controller → Observability).
  - `game_theory_concepts_simple.md` – Plain-language guide to key game theory concepts.
  - `game_theory_for_microservices.md` – Deep dive on applying game theory to microservices.
  - `demo.yml` – Example pod manifest (`resize-demo`) for exercising in-place scaling.

---

## Why GTCAS?

Standard Kubernetes scaling (HPA/VPA) is reactive and selfish. Each pod scales based on its own local thresholds, often leading to **thrashing** (oscillation) and **noisy neighbor** problems.

GTCAS replaces this with a collaborative model. It views the Kubernetes cluster as a multi-agent system where:

- **The Players**: Microservices (Pods)  
- **The Strategy**: How much CPU/RAM to request  
- **The Goal**: Reach a Nash Equilibrium — a stable state where no service can improve its utility without hurting the global system  

Built in Golang, this controller uses **In-Place Pod Vertical Scaling** to resize pods without restarts, ensuring zero downtime.

---

## Key Features

- **Potential Game Modeling**  
  Prevents oscillation by optimizing a single global potential function (Cluster Health) rather than isolated metrics.

- **In-Place Scaling**  
  Updates Pod resource limits dynamically using Kubernetes Alpha features (no pod restarts required).

- **No-Regret Learning**  
  Implements algorithms (like Regret Matching) where agents learn from past performance to make better resource requests over time.

- **The "Cooperative Handshake"**  
  A safety protocol ensuring memory downscaling never crashes a pod (OOM Kill prevention) by enforcing **Individually Rational** constraints.

- **Correlated Equilibrium**  
  A central arbiter coordinates scaling actions to prevent resource contention, acting like a traffic light for CPU cycles.

---

## Architecture at a Glance

### 1. The Arbiter (Custom Controller)

A centralized Golang controller that monitors the cluster. It acts as the **Correlated Equilibrium** signal, calculating the optimal resource distribution and sending "suggestions" to the pods.

### 2. The Players (Agents)

Your microservices. Each service aims to maximize its own utility (Performance vs. Cost) while respecting the global potential function.

### 3. The Mechanism (In-Place Scaling)

Unlike standard VPA which evicts (kills) pods to resize them, GTCAS patches the Pod spec directly using the `resize` subresource.

---

## Game Theory Concepts

This project is built strictly on the following theoretical pillars:

| **Concept**            | **Implementation in GTCAS**                                                                                                                                      |
|------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| **Potential Game**     | The cluster model guarantees that if a pod improves its own utility (without hurting others), the global system state improves. This ensures convergence.       |
| **Nash Equilibrium**   | Target state where resource requests stabilize. At this point, no service benefits from changing its request, stopping the **thrashing** cycle.                |
| **No-Regret Learning** | Control loop algorithm. Agents look back at past metrics ("I wish I had more CPU at 2 PM") and adjust future strategies to minimize this regret.              |
| **Individually Rational** | Safety constraint. A pod rejects a scaling decision if it drops below its **Punishment Level** (minimum resources needed to survive).                      |
| **Lyapunov Function**  | Uses the potential function as a Lyapunov function to mathematically prove and visualize that the system is converging to stability.                            |

---

## The Cooperative Handshake (Memory Safety)

Vertical scaling of memory is dangerous. If you shrink a container limit faster than the runtime (Java/Go) releases memory, the Pod crashes with an OOM Kill.

GTCAS introduces a negotiation protocol:

1. **Propose**  
   Controller calculates a new, lower memory limit.

2. **Negotiate**  
   Controller calls the Pod sidecar:

   ```http
   POST /admin/shrink?target=500MB
   ```

3. **Act**  
   - Sidecar triggers Garbage Collection (GC).  
   - Sidecar calls `debug.FreeOSMemory()` (Go) or uses JVM flags (Java) to release pages.

4. **Verify**  
   Sidecar checks actual usage:
   - If **Usage < Target** → returns `200 OK`.  
   - If **Usage > Target** → returns `409 Conflict`.  

5. **Commit**  
   Controller only patches Kubernetes if it receives `200 OK`.

---

## Architectural Model

This model relies on a closed-loop control system that separates the **Brain** (Policy) from the **Body** (Execution).

```text
                  +---------------------+
                  |  Observability      |
                  |  Plane (Prometheus) |
                  +----------+----------+
                             ^
                             | 1. Metrics
                             |
      +----------------------+-----------------------+
      |                 Control Plane                |
      |             (GTCAS Controller)              |
      |                                             |
      |  +-------+      +----------------+         |
      |  | CTRL  |----->| Game Theory    |         |
      |  |       |  2   | Engine         |         |
      |  +-------+      | (Regret Match) |         |
      |       |         +--------+-------+         |
      |       | 3. Optimal       |                 |
      |       v    strategy      v                 |
      |   +---------------------------+            |
      |   |         Actuator          |            |
      |   +-------------+-------------+            |
      +-----------------+--------------------------+
                    4. Proposals / Negotiation
                              |
                              v
      +--------------------------------------------------+
      |                 Execution Plane                  |
      |                                                  |
      |   +-------------------+        +-------------+   |
      |   |   Game-Aware Pod  |        |  K8s API    |   |
      |   |                   |        +------+------+   |
      |   | +---------+       |               ^          |
      |   | | Sidecar |<------------------7  |          |
      |   | +----+----+       |               |          |
      |   |      | 5. Trigger GC             | 8. In-   |
      |   |      v            |              | place    |
      |   |  +--------+       |              | resize   |
      |   |  |  App   |-------+--------------+          |
      |   |  +--------+   6. Accept/Reject              |
      |   +-------------------+                         |
      +--------------------------------------------------+

  Prometheus continuously scrapes App metrics.
```

---

## Control Loop Flow

The controller runs a continuous feedback loop (e.g., every 15 seconds):

- **Observation (Input)**  
  Query Prometheus to snapshot the current **Game State** (Golden Signals, CPU/Memory usage).

- **Deliberation (Logic)**  
  - Run the **Regret Matching** algorithm in the Game Theory Engine.  
  - Simulate rounds of the game to find a **Nash Equilibrium** — an allocation where no service wants to deviate.  
  - Evaluate the **Global Potential Function** to ensure moves improve cluster stability.

- **Negotiation (Safety Check)**  
  - Actuator sends proposals to each sidecar: e.g., "Lower memory to 500Mi".  
  - Sidecar forces GC and validates safety under **Individually Rational** constraints.  
  - Unsafe proposals return `409 Conflict` and are discarded.

- **Execution (Action)**  
  - Accepted proposals (`200 OK`) are applied via JSON Patch to the Kubernetes API.  
  - Kubelet performs **in-place resize** of the container’s cgroups without restarting the pod.

---

## Key Interactions

| **Interaction**            | **Protocol**      | **Description**                                                                |
|----------------------------|-------------------|--------------------------------------------------------------------------------|
| Controller → Prometheus    | HTTP / PromQL     | Fetches historical performance data to calculate regret.                       |
| Controller → Sidecar       | HTTP / REST       | Executes the Cooperative Handshake to prevent OOM kills.                       |
| Sidecar → App              | Local / Signal    | Triggers memory release (e.g., `debug.FreeOSMemory()`).                        |
| Controller → Kubernetes API| HTTPS / gRPC      | Patches the pod `resize` subresource for in-place vertical scaling.            |

---

## Usage

### Prerequisites

- **Minikube** with `InPlacePodVerticalScaling` feature gate enabled  
- **Golang 1.21+**  
- **Prometheus** for metric collection  

### 1. Start Minikube with Feature Gates

```bash
minikube start \
  --feature-gates=InPlacePodVerticalScaling=true \
  --extra-config=kubelet.feature-gates=InPlacePodVerticalScaling=true \
  --extra-config=controller-manager.feature-gates=InPlacePodVerticalScaling=true \
  --extra-config=scheduler.feature-gates=InPlacePodVerticalScaling=true
```

### 2. Deploy a Game-Aware Service

Add the annotation to opt-in to the strategy:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: resize-demo
  annotations:
    scaling.mesh/strategy: "potential-game"
spec:
  template:
    spec:
      containers:
      - name: app
        image: my-game-aware-app:latest
        resizePolicy:
        - resourceName: cpu
          restartPolicy: NotRequired
        - resourceName: memory
          restartPolicy: NotRequired
```

---

## Status & Roadmap

Implementation details and phase breakdown live in `PLAN.md`. At a high level:

- **Phase 1** — Actuator (in-place scaling mechanism)  
- **Phase 2** — Game Theory Engine (regret matching & potential game)  
- **Phase 3** — Safety Layer / Sidecar (Individually Rational constraints)  
- **Phase 4** — Controller Integration (closed-loop control)  
- **Phase 5** — Observability & Lyapunov-based convergence visualization  

Refer to `PLAN.md` for exact tasks, definitions of done, and test criteria.


