## Kubernetes Pod Resource Tool

This repository contains a single Go CLI that provides **three pod-focused operations**:

- **list**: list pods in the cluster (user namespaces only)
- **usage**: show CPU/memory requests and limits for a specific pod
- **update**: change CPU/memory requests and limits for a pod and show **before/after** values

Currently this tool is **pods-only by design** to avoid the rolling-update behavior that comes with changing Deployments/StatefulSets.

### Prerequisites

- Go 1.20+ installed
- Access to a Kubernetes cluster
- A valid kubeconfig (e.g. `~/.kube/config`) or in-cluster configuration

The tool will:

- Prefer `KUBECONFIG` if set
- Otherwise use `~/.kube/config` if it exists
- Otherwise fall back to in-cluster configuration

### Installation / Build

From this folder:

```bash
go build -o k8s-pod-tool
```

This produces a `k8s-pod-tool` (or `k8s-pod-tool.exe` on Windows) binary.

You can also run it directly with `go run`:

```bash
go run . list
```

### Commands

#### 1. List pods

Lists pods in all **non-system namespaces** (`kube-system`, `kube-public`, `kube-node-lease`, `local-path-storage` are ignored).

```bash
go run . list
```

Limit to a single namespace:

```bash
go run . list -namespace default
```

Sample output:

```text
User pods currently running in the cluster:
- ns=default name=user-service-6486b8775-vr86l phase=Running node=worker-1
```

#### 2. Show pod resource usage (spec)

Shows CPU and memory **requests** and **limits** configured on each container in the pod spec.

```bash
go run . usage -namespace default -pod user-service-6486b8775-vr86l
```

Sample output:

```text
Pod resource usage (spec) for ns=default name=user-service-6486b8775-vr86l
  container=user-service
    request.cpu=250m  request.memory=256Mi
    limit.cpu=500m    limit.memory=512Mi
```

> Note: This shows the **requested/limited resources from the pod spec**, not live CPU usage metrics.

#### 3. Update pod resource allocation (in-place resize)

Changes the CPU and/or memory requests and limits for a given pod’s container, using the Kubernetes **`pod/resize` subresource** (when enabled), then shows values **before and after**.

```bash
go run . update \
  -namespace default \
  -pod resize-demo \
  -container pause \
  -cpu 800m \
  -memory 512Mi
```

Flags:

- **`-namespace`**: pod namespace (default: `default`)
- **`-pod`**: pod name (required)
- **`-container`**: container name inside the pod (optional, defaults to first container)
- **`-cpu`**: new CPU value (e.g. `250m`, `1`) applied to both requests and limits
- **`-memory`**: new memory value (e.g. `512Mi`) applied to both requests and limits
- **`-dry-run`**: show current values and the intended change **without applying** the patch

Example with dry run:

```bash
go run . update \
  -namespace default \
  -pod resize-demo \
  -container pause \
  -cpu 250m \
  -memory 512Mi \
  -dry-run
```

### Behavior Notes

- **Pods only**: this tool intentionally operates only on pods. Changing a Deployment/StatefulSet template will typically cause a rolling restart; pods are used here for more explicit, targeted changes.
- **In-place resizing via subresource**: updates use the Kubernetes **`pod/resize` subresource**, mirroring the behavior of:

```bash
kubectl patch pod <name> -n <ns> --subresource resize --type merge \
  --patch '{"spec":{"containers":[{"name":"<container>","resources":{"requests":{"cpu":"...","memory":"..."},"limits":{"cpu":"...","memory":"..."}}}]}}'
```

- **Before/after listing**: the `update` command:
  - Fetches the pod and prints current CPU/memory requests & limits.
  - Applies the patch (unless `-dry-run`).
  - Fetches the pod again and prints updated values.

### Extensibility

The code is structured so you can later:

- Add support for Deployments/StatefulSets (by acting on their pod templates).
- Integrate real-time metrics (via Metrics API) if desired.


# Collaborative Microservice Auto-Scaling: A Potential Game Approach

### Implementation and Optimization of Collaborative Strategies in Microservices Architectures Using Game Theory and Agent-Based Models

## 📖 Abstract

This project implements a **Collaborative Auto-Scaling Engine** for Kubernetes clusters using **Golang**. Unlike traditional autoscalers (HPA) that react to local CPU thresholds (often leading to "thrashing" or oscillation), this system models the cluster as a **Potential Game**.

Microservices act as rational agents that negotiate scaling actions. By utilizing a **Lyapunov Function** derived from **M/M/c Queuing Theory**, the system mathematically guarantees convergence to a **Nash Equilibrium**, ensuring global stability and optimal resource allocation.

## ⚡ Key Features

* **Stochastic Modeling:** Uses **M/M/c Queuing Theory** (Erlang-C formula) to predict latency based on traffic intensity, rather than raw CPU usage.
* **Oscillation-Free Convergence:** Implements a global **Potential Function** (Lyapunov function). Scaling actions are only permitted if they reduce the global system "energy," guaranteeing the system settles into a stable state.
* **Collaborative Scaling:** Models service dependencies. Upstream services will not scale up if downstream bottlenecks prevent global throughput improvements, enforcing **Feasible Payoffs**.
* **Agent-Based Architecture:** Written in **Go**, simulating independent agents negotiating for resources.

## 🧠 Theoretical Framework

### 1. The Potential Game

We model the cluster state  as a Potential Game. The system seeks to minimize a global potential function :

Where:

* : Expected Latency of service  (derived from M/M/c).
* : Operational cost of service  (replica count).
* : Configurable weights.

Because  is monotonically decreasing with every valid move, the system avoids infinite loops and reaches **Convergence**.

### 2. M/M/c Queuing Model

Instead of static thresholds, we calculate the expected wait time  for a request using the Erlang-C formula:

This provides a "No-Regret" foundation for decision-making, ensuring scaling decisions are based on mathematical probability rather than heuristics.

## 🏗️ Architecture

The system is built in **Go** and consists of three core components:

1. **The Agents (Microservices):** Lightweight goroutines that monitor their own arrival rates () and service rates ().
2. **The Arbiter (Game Engine):** A central controller that calculates the Global Potential . It rejects any scaling proposal that does not satisfy .
3. **The Dependency Graph:** A directed acyclic graph (DAG) representing service calls (e.g., `API -> Auth -> Database`).

## 🚀 Getting Started

### Prerequisites

* Go 1.21+
* (Optional) Minikube or a Kubernetes cluster for the live adapter.

### Installation

```bash
git clone https://github.com/yourusername/collaborative-scaling-game.git
cd collaborative-scaling-game
go mod tidy

```

### Running the Simulation

The project includes a `main.go` simulation that demonstrates the convergence algorithm under a synthetic load spike.

```bash
go run main.go

```

**Expected Output:**

```text
--- SPIKE DETECTED: 15 -> 150 Req/s ---
[ACCEPT] Order-Service -> 3 replicas. Potential: 540.20 -> 420.10
[ACCEPT] Order-Service -> 4 replicas. Potential: 420.10 -> 380.50
[DENIED] Order-Service -> 5 replicas. Potential Increase Detected.
--- SYSTEM CONVERGED (Nash Equilibrium Reached) ---

```

## 🧪 Experimental Validation

To validate the **"No-Regret Learning"** and stability:

1. **Baseline:** We compare against standard Kubernetes HPA (CPU utilization > 50%).
2. **Scenario A (Impulse):** Sudden step-function load increase.
3. **Scenario B (Flapping):** Oscillating load near the threshold.
4. **Metric:** We measure the "Settling Time" (time to reach equilibrium) and "Overshoot" (wasted resources).

## 📅 Roadmap

* [ ] Core Potential Game Algorithm in Go
* [ ] M/M/c Queuing Theory Integration
* [ ] Dependency Awareness (Cooperative Scaling)
* [ ] Kubernetes `client-go` Adapter (Real-world implementation)
* [ ] Prometheus Exporter for Potential Metrics

## 🤝 Contributing

This is an academic research project. Contributions regarding optimization of the Lyapunov function or alternative game-theoretic models (e.g., **Bargaining Games**) are welcome.

## 📄 License

Distributed under the MIT License. See `LICENSE` for more information.