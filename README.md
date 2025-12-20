# Equilibrium: A Game-Theoretic Sidecar for Microservices

[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Architecture](https://img.shields.io/badge/Pattern-Sidecar-green)](https://learn.microsoft.com/en-us/azure/architecture/patterns/sidecar)
[![Language](https://img.shields.io/badge/Written%20In-Go%20%2F%20Rust-cyan)]()
[![Kubernetes](https://img.shields.io/badge/K8s-Native-blue)]()

## 📖 Abstract

**Equilibrium** is a drop-in **Network Sidecar** that transforms any standard Kubernetes cluster into a self-optimizing market.

Unlike standard Service Meshes that use static rules (Circuit Breakers, Round Robin), Equilibrium attaches an **Intelligent Agent** to every microservice container. These agents play a continuous **Cooperative Game** to negotiate traffic routing and resource usage in real-time.

**Key Features:**
* **Zero Code Changes:** Works with *any* tech stack (Java, Python, Node.js, etc.) via the Sidecar pattern.
* **Decentralized:** No central "Master Node" bottleneck; decision-making is distributed.
* **Mathematically Proven:** Uses Nash Equilibrium guarantees to prevent oscillation and ensure stability.

---

## 🏗️ The Architecture: The "Smart Sidecar"

We utilize the **Sidecar Pattern**. The "Game Theory Logic" runs in a lightweight binary (Proxy) that sits alongside your application container in the same Kubernetes Pod.

1.  **Interception:** The Sidecar intercepts all incoming and outgoing network traffic (HTTP/gRPC) from the "Legacy App."
2.  **Piggybacking:** Agents exchange Game Theory signals (Bids, Regret Scores, Congestion Levels) via **HTTP Headers** on standard requests (e.g., `X-GT-Bid`, `X-GT-Regret`).
3.  **Decision:** The Sidecar decides *where* to route the request based on **Regret Matching** algorithms, completely transparent to the legacy app.

---

## 🧠 Core Mechanics (Language Agnostic)

The sidecar implements game theory concepts as infrastructure rules:

### 1. The Routing Game (No-Regret Learning)
* **Problem:** Traditional Round-Robin doesn't know if a specific downstream node is thrashing.
* **Solution:** The Sidecar proxy measures the response time of every request it forwards.
* **Algorithm:** It calculates a **Regret Score** for every downstream destination. "I regret sending to Pod A because Pod B replied 50ms faster." Future requests are routed probabilistically to minimize this regret.

### 2. The Thundering Herd Defense (Mixed Strategy)
* **Problem:** If Pod A becomes healthy, all other Pods immediately flood it, crashing it again.
* **Solution:** Agents use a **Mixed Strategy** (Randomization). Even if Pod A is the "best," the sidecar will only route 80% of traffic there, keeping 20% on others to maintain valid data and prevent predictable exploitation.

### 3. Traffic Policing (The Folk Theorem)
* **Problem:** A rogue microservice (or bug) floods the network with retries.
* **Solution:** Since interactions are repeated, Sidecars maintain a "Reputation Score" for callers. If a caller defects (floods), the Sidecar enforces a **Punishment Level** (hard throttling) until the caller cooperates, enforcing stability without human intervention.

---

## 📂 Repository Structure

```bash
├── /sidecar-proxy        # The Core "Brain" (Written in Go/Rust)
│   ├── interceptor.go    # Captures HTTP/TCP traffic
│   ├── logic/
│   │   ├── regret.go     # Implementation of Regret Matching
│   │   ├── nash.go       # Equilibrium checks
│   │   └── auction.go    # Bidding logic for priority
│   └── main.go
├── /k8s-injector         # Kubernetes Mutating Webhook
│   ├── injector.yaml     # Automatically adds Sidecar to your pods
│   └── webhook.go
├── /dashboard            # Visualization (Optional)
│   └── graph.js          # View real-time Regret minimization
└── README.md

🚀 Installation & Usage
This system is designed to be installed on a running cluster with zero downtime.

Step 1: Install the Injector
Deploy the Mutating Webhook. This tells Kubernetes to automatically inject the equilibrium-sidecar into your pods.

Bash

kubectl apply -f k8s-injector/install.yaml
Step 2: Annotate your Services
You do not change your code. Simply add an annotation to your existing Deployment.yaml.

YAML

apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-legacy-java-app
spec:
  template:
    metadata:
      annotations:
        # This triggers the injection
        equilibrium.io/inject: "true"
        # Optional: Define the agent's strategy profile
        equilibrium.io/strategy: "aggressive-learning"
    spec:
      containers:
        - name: app
          image: my-company/old-java-app:latest
Step 3: Deploy
Bash

kubectl apply -f my-legacy-java-app.yaml
Result: Kubernetes automatically modifies the pod to include the Sidecar. The Sidecar takes over routing immediately.

📊 How to Verify It Works
Since this runs on real traffic, verification is done via metrics (Prometheus).

Deploy a "Bad" Service: Deploy a microservice that randomly introduces 500ms latency.

Observe the Shift: Watch the Prometheus "Regret" metric. You will see the Sidecars automatically shift traffic away from the bad node.

Check Convergence: Ensure the traffic distribution stabilizes (reaches Nash Equilibrium) rather than flapping back and forth.

🛠 Tech Stack Details
Language: Go (Golang) or Rust.

Reason: The sidecar must have <5ms overhead. Python is too slow for a proxy handling every network packet.

Networking: eBPF or IPTables.

Reason: To transparently hijack traffic from the application container without changing the app's localhost config.

Protocol: Standard HTTP/1.1 and gRPC.

Reason: Piggybacking game signals on standard headers ensures compatibility with existing firewalls and tracing tools (Jaeger/Zipkin).

🔮 Roadmap
[ ] v0.1: Basic Regret Matching Load Balancer.

[ ] v0.2: "Correlated Equilibrium" using a Redis backend for shared signaling.

[ ] v1.0: Full support for "The Folk Theorem" punishment mechanisms for security.

📄 License
Distributed under the MIT License. See LICENSE for more information.