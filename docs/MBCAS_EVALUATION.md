# MBCAS Evaluation Report
## Market-Based CPU Allocation System for Kubernetes

---

## 1. Executive Summary (10 bullets max)

1. **MBCAS is ~80% implemented** — core control loop (agent → CRD → controller → actuator) is complete and testable.

2. **Game theory is appropriately simplified** — implements Fisher-market proportional fairness (maximize Σ log(cpu_i)) which is the right choice for this context; no overly academic complexity.

3. **Kubernetes-native design is sound** — uses CRDs for state, controller-runtime for reconciliation, `pods/resize` for actuation. No custom networking or protocols.

4. **Deployment artifacts are complete** — Dockerfiles, controller deployment, and namespace manifests are now in place. MVP is deployable.

5. **Demand signal is reasonable but limited** — uses cgroup throttling ratio only; PSI integration deferred. This is acceptable for MVP but may miss memory pressure.

6. **The mechanism IS incentive-compatible in this context** — pods cannot "game" their throttling signals; revealed preference through behavior is sound.

7. **Stability controls are present** — smoothing (EMA), hysteresis (10%), cooldowns (30s), step limits (1.5x), system reserve (10%). May need tuning but foundation is solid.

8. **The primary risk is cgroup path detection** — uses glob patterns that may fail on certain Kubernetes distributions (CRI-O, EKS, etc.).

9. **Simpler alternatives exist** — VPA + PriorityClass + quotas could achieve 70-80% of the benefit with zero custom code. MBCAS provides faster reaction and no config overhead in exchange for operational complexity.

10. **MVP is ready for deployment** — all required artifacts (Dockerfiles, deployment manifests, RBAC) are in place. Requires Kubernetes 1.27+ with `InPlacePodVerticalScaling` feature gate enabled (kubectl 1.32+ recommended for `--subresource=resize` support).

---

## 2. Repo Map + Architecture Diagram

### Directory Structure
```
mbcas/
├── api/v1alpha1/              # CRD types (PodAllocation)
│   ├── podallocation_types.go
│   ├── groupversion_info.go
│   └── zz_generated.deepcopy.go
├── cmd/
│   ├── agent/main.go          # DaemonSet binary entrypoint
│   └── controller/main.go     # Deployment binary entrypoint
├── config/
│   ├── namespace.yaml         # mbcas-system namespace
│   ├── agent/
│   │   ├── daemonset.yaml
│   │   └── rbac.yaml
│   ├── controller/
│   │   └── deployment.yaml    # Controller deployment
│   ├── rbac/                   # Controller RBAC
│   │   ├── service_account.yaml
│   │   ├── role.yaml
│   │   └── role_binding.yaml
│   └── crd/
│       └── bases/allocation.mbcas.io_podallocations.yaml
├── pkg/
│   ├── actuator/              # Phase 1: pods/resize library
│   ├── agent/                 # Phase 3: node agent
│   │   ├── agent.go
│   │   ├── writer.go
│   │   ├── cgroup/reader.go   # Kernel signal reader
│   │   └── demand/
│   │       ├── tracker.go     # EMA smoothing
│   │       └── calculator.go  # K8s → market params
│   ├── allocation/            # Phase 4: market solver
│   │   └── market.go          # ClearMarket() algorithm
│   ├── controller/            # Phase 2: reconciliation
│   │   ├── podallocation_controller.go
│   │   └── pod_event_handler.go
│   └── podtool/               # CLI for testing
├── main.go                    # CLI entrypoint
├── Dockerfile.controller      # Controller container build
├── Dockerfile.agent           # Agent container build
├── go.mod / go.sum
├── Makefile
├── scripts/
│   ├── mvp-test.sh            # End-to-end deployment script
│   └── kind-config.yaml       # Kind cluster config
└── PLAN.md, README.md, DEPLOYMENT_CHECKLIST.md
```

### Architecture Diagram (Data Flow)
```
┌─────────────────────────────────────────────────────────────────────────┐
│                           Linux Kernel (per Node)                        │
├─────────────────────────────────────────────────────────────────────────┤
│  /sys/fs/cgroup/kubepods.slice/.../cpu.stat                             │
│    • throttled_usec  ─────┐                                             │
│    • usage_usec      ─────┼──► Throttling Ratio = throttled/usage       │
└───────────────────────────┼─────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                     Node Agent (DaemonSet per Node)                      │
├─────────────────────────────────────────────────────────────────────────┤
│  pkg/agent/cgroup/reader.go  ──► Raw demand [0,1]                       │
│                                      │                                   │
│  pkg/agent/demand/tracker.go ◄──────┘                                   │
│    • EMA smoothing (α=0.3 up, 0.1 down)                                 │
│                                      │                                   │
│  pkg/agent/demand/calculator.go ◄───┘                                   │
│    • Extract K8s request/limit → weight/min/max                         │
│    • Compute bid = weight × demand                                      │
│                                      │                                   │
│  pkg/allocation/market.go    ◄──────┘                                   │
│    • ClearMarket(): proportional fairness                               │
│    • Water-filling with bounds                                          │
│                                      │                                   │
│  pkg/agent/writer.go         ◄──────┘                                   │
│    • Create/Update PodAllocation CRD                                    │
└─────────────────────────────┬───────────────────────────────────────────┘
                              │ (every 15s, if change > 10%)
                              ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                    Kubernetes API Server                                 │
├─────────────────────────────────────────────────────────────────────────┤
│  PodAllocation CRD                                                      │
│    spec.desiredCPULimit: "500m"                                         │
│    status.appliedCPULimit: "400m"                                       │
│    status.phase: Pending|Applied|Failed                                 │
└─────────────────────────────┬───────────────────────────────────────────┘
                              │ (watch)
                              ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                  Controller (Deployment, single replica)                 │
├─────────────────────────────────────────────────────────────────────────┤
│  pkg/controller/podallocation_controller.go                             │
│    • Watch PodAllocation + Pod                                          │
│    • Compare desired vs actual CPU limit                                │
│    • Enforce cooldown (30s), step limit (1.5x)                          │
│    • Skip Guaranteed QoS pods                                           │
│                                      │                                   │
│  pkg/actuator/actuator.go    ◄──────┘                                   │
│    • PATCH /api/v1/pods/{name}/resize                                   │
│    • Update limits (policy: limits-only)                                │
│    • Retry on conflict                                                  │
└─────────────────────────────┬───────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                           Pod (Target)                                   │
├─────────────────────────────────────────────────────────────────────────┤
│  spec.containers[0].resources.limits.cpu: "500m" ← UPDATED              │
│  (requests unchanged to preserve QoS class)                             │
└─────────────────────────────────────────────────────────────────────────┘
```

---

## 3. Implementation Reality Table

| Feature/Idea | Code Location | Status | Risks/Issues | Recommendation |
|-------------|---------------|--------|--------------|----------------|
| **PodAllocation CRD** | `api/v1alpha1/`, `config/crd/` | ✅ Done | None | **Keep** |
| **Controller (Phase 2)** | `pkg/controller/`, `cmd/controller/` | ✅ Done | None | **Keep** |
| **Actuator (Phase 1)** | `pkg/actuator/` | ✅ Done | Requires K8s 1.27+ feature gate | **Keep** |
| **Agent (Phase 3)** | `pkg/agent/`, `cmd/agent/` | ✅ Done | Cgroup path detection fragile | **Keep**, add fallback paths |
| **Cgroup Reader** | `pkg/agent/cgroup/reader.go` | ✅ Done | Glob patterns may miss some distros; reads `throttled_usec` not PSI | **Keep**, test on target distro |
| **Demand Smoothing** | `pkg/agent/demand/tracker.go` | ✅ Done | α values may need tuning | **Keep** |
| **Market Solver (Phase 4)** | `pkg/allocation/market.go` | ✅ Done | Deterministic, tested | **Keep** |
| **K8s→Market Params** | `pkg/agent/demand/calculator.go` | ✅ Done | Uses requests as weight; PriorityClass not yet integrated | **Keep**, defer PriorityClass |
| **Agent DaemonSet** | `config/agent/daemonset.yaml` | ✅ Done | Security context fixed (runs as root with SYS_ADMIN) | **Keep** |
| **Agent RBAC** | `config/agent/rbac.yaml` | ✅ Done | None | **Keep** |
| **Controller Deployment** | `config/controller/deployment.yaml` | ✅ Done | Complete with health probes | **Keep** |
| **Controller RBAC** | `config/rbac/` | ✅ Done | None | **Keep** |
| **Dockerfiles** | `Dockerfile.controller`, `Dockerfile.agent` | ✅ Done | Multi-stage builds in root directory | **Keep** |
| **Namespace manifest** | `config/namespace.yaml` | ✅ Done | Creates `mbcas-system` namespace | **Keep** |
| **PSI Integration** | Not implemented | ❌ Missing (intentional) | PSI gives node-level memory pressure | **Defer**: not needed for CPU-only MVP |
| **PriorityClass Integration** | `calculator.go` has placeholder | ❌ Missing | Could improve fairness for priority workloads | **Defer** |
| **Multi-container pod handling** | Uses `container[0]` | ⚠️ Partial | May not be ideal for sidecars | **Keep** for MVP, document limitation |
| **Warm-up period** | Referenced in PLAN.md | ❌ Missing | New pods may oscillate initially | **Add** in agent (skip first N samples) |
| **Guaranteed QoS exclusion** | `controller.go:isGuaranteedQoS()` | ✅ Done | None | **Keep** |

---

## 4. Critical Issues & Correctness Risks (Prioritized)

### P0 (Blocking MVP)

1. ~~**Missing Dockerfiles**~~ ✅ **RESOLVED**
   - Dockerfiles now exist in root directory
   - Status: Complete

2. ~~**Missing Controller Deployment Manifest**~~ ✅ **RESOLVED**
   - `config/controller/deployment.yaml` now exists
   - Status: Complete

3. **Kubernetes Feature Gate Required**
   - `InPlacePodVerticalScaling` must be enabled (Kubernetes 1.27+)
   - `kubectl --subresource=resize` requires kubectl 1.32+
   - Users may not know this; deployment will fail silently
   - Fix: Add check in controller startup, document prominently, pin Kind version

### P1 (May Break in Some Environments)

4. ~~**Agent Security Context Too Restrictive**~~ ✅ **RESOLVED**
   - Agent now runs as root with `SYS_ADMIN` capability
   - File: `config/agent/daemonset.yaml`
   - Status: Fixed

5. **Cgroup Path Detection Fragile**
   - Glob patterns assume specific kubelet cgroup driver (systemd slice)
   - May fail on: Docker-shim, CRI-O with different layout, EKS/GKE custom paths
   - File: `pkg/agent/cgroup/reader.go:findPodCgroupPath()`
   - Fix: Add multiple fallback patterns, log which pattern matched

6. **No Warm-up Period for New Pods**
   - New pods start with demand=0, then may spike
   - Could cause oscillation
   - File: `pkg/agent/agent.go:sample()`
   - Fix: Skip first 5-10 samples for new pods

### P2 (Correctness/Edge Cases)

7. **Throttling Ratio Threshold is Arbitrary**
   - `threshold := 0.1` (10% throttling = full demand) is a magic number
   - May be too sensitive or too insensitive
   - File: `pkg/agent/cgroup/reader.go:ReadPodDemand()`
   - Fix: Make configurable or calibrate empirically

8. **Multi-Container Pods Use container[0] Only**
   - Sidecars may have different resource needs
   - File: `pkg/agent/demand/calculator.go`, `pkg/controller/podallocation_controller.go`
   - Fix: Document limitation; consider aggregating across containers later

9. **No Handling for pods/resize Failures**
   - If resize fails (e.g., insufficient resources), status is "Failed" but retry is immediate
   - Could cause API spam
   - File: `pkg/controller/podallocation_controller.go:Reconcile()`
   - Fix: Add backoff on failure (RequeueAfter: 1m)

### P3 (Theoretical Concerns)

10. **Game Theory Assumptions vs Kubernetes Reality**
    - **Heterogeneous nodes**: Market clearing is per-node; cluster-wide fairness not guaranteed
    - **Noisy neighbors**: Throttling caused by other tenants looks like demand
    - **Measurement delay**: 1s sampling + 15s write interval = 16s minimum reaction time
    - **Starvation under overcommit**: If Σ min > capacity, all pods get scaled down proportionally
    - Mitigations are present (smoothing, hysteresis) but not formally proven stable

---

## 5. MVP Definition

### Goal
Demonstrate that MBCAS can:
1. Detect CPU pressure from throttling
2. Compute proportionally fair allocations
3. Apply CPU limit changes via `pods/resize`
4. Reduce throttling compared to baseline

### Workload
- **2-3 CPU-bound microservices** (e.g., `stress-ng` or simple Python/Go HTTP servers)
- **1 node Kind cluster** with feature gate enabled
- **Load generator** (e.g., `hey`, `wrk`, or custom)

### Success Metrics

| Metric | Baseline | Target | How to Measure |
|--------|----------|--------|----------------|
| CPU throttling (throttled_usec/s) | >100ms/s under load | <20ms/s | `kubectl exec + cat /sys/fs/cgroup/.../cpu.stat` |
| Allocation convergence time | N/A | <60s after load change | Watch PodAllocation status timestamps |
| Limit oscillation | N/A | <3 changes/5min steady state | `kubectl get podallocations -w` |
| No starvation | N/A | All pods ≥ 100m | Check allocations |

### Minimum Components
1. Kind cluster with `InPlacePodVerticalScaling=true`
2. CRD installed
3. Controller running
4. Agent running (1 node)
5. 2+ test pods with CPU requests/limits

### What to Explicitly Defer (MVP Cut List)
- PSI integration
- PriorityClass weighting
- Multi-cluster support
- Prometheus metrics export
- Admission webhook validation
- Multiple container support per pod
- Memory allocation

---

## 6. Step-by-Step GUIDE.md Content

```markdown
# MBCAS MVP Deployment Guide

## Prerequisites

- Docker (for building images)
- Kind (or Minikube with feature gate support)
- kubectl
- Go 1.21+ (for building from source)

## Step 1: Create Kind Cluster with Feature Gate

```bash
cat <<EOF | kind create cluster --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
featureGates:
  InPlacePodVerticalScaling: true
nodes:
- role: control-plane
- role: worker
EOF
```

Verify feature gate:
```bash
kubectl api-resources | grep -i resize
# Should show: pods/resize
```

## Step 2: Build and Load Images

Create `Dockerfile.controller`:
```dockerfile
FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o controller ./cmd/controller

FROM alpine:3.19
COPY --from=builder /app/controller /controller
ENTRYPOINT ["/controller"]
```

Create `Dockerfile.agent`:
```dockerfile
FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o agent ./cmd/agent

FROM alpine:3.19
COPY --from=builder /app/agent /agent
ENTRYPOINT ["/agent"]
```

Build and load:
```bash
docker build -f Dockerfile.controller -t mbcas-controller:latest .
docker build -f Dockerfile.agent -t mbcas-agent:latest .
kind load docker-image mbcas-controller:latest
kind load docker-image mbcas-agent:latest
```

## Step 3: Create Namespace and Install CRD

```bash
kubectl create namespace mbcas-system

kubectl apply -f config/crd/bases/allocation.mbcas.io_podallocations.yaml
```

Verify:
```bash
kubectl get crd podallocations.allocation.mbcas.io
```

## Step 4: Install RBAC

```bash
kubectl apply -f config/rbac/
kubectl apply -f config/agent/rbac.yaml
```

## Step 5: Deploy Controller

Create `config/controller/deployment.yaml`:
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: mbcas-controller
  namespace: mbcas-system
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: mbcas-controller
  template:
    metadata:
      labels:
        app.kubernetes.io/name: mbcas-controller
    spec:
      serviceAccountName: mbcas-controller
      containers:
      - name: controller
        image: mbcas-controller:latest
        imagePullPolicy: Never  # Kind uses local images
        resources:
          requests:
            cpu: 50m
            memory: 64Mi
          limits:
            cpu: 200m
            memory: 256Mi
```

Apply:
```bash
kubectl apply -f config/controller/deployment.yaml
```

## Step 6: Fix and Deploy Agent

Edit `config/agent/daemonset.yaml`, change security context:
```yaml
securityContext:
  runAsNonRoot: false
  runAsUser: 0
  capabilities:
    add:
    - SYS_ADMIN
    drop:
    - NET_RAW
```

Apply:
```bash
kubectl apply -f config/agent/daemonset.yaml
```

## Step 7: Verify Installation

```bash
# Controller running
kubectl get pods -n mbcas-system -l app.kubernetes.io/name=mbcas-controller

# Agent running (one per worker node)
kubectl get pods -n mbcas-system -l app.kubernetes.io/component=agent

# Check logs
kubectl logs -n mbcas-system -l app.kubernetes.io/name=mbcas-controller --tail=20
kubectl logs -n mbcas-system -l app.kubernetes.io/component=agent --tail=20
```

## Step 8: Deploy Test Workloads

Create `test-workloads.yaml`:
```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: test-mbcas
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cpu-stress-1
  namespace: test-mbcas
spec:
  replicas: 1
  selector:
    matchLabels:
      app: cpu-stress-1
  template:
    metadata:
      labels:
        app: cpu-stress-1
    spec:
      containers:
      - name: stress
        image: progrium/stress
        args: ["--cpu", "1", "--timeout", "3600s"]
        resources:
          requests:
            cpu: 100m
          limits:
            cpu: 500m
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cpu-stress-2
  namespace: test-mbcas
spec:
  replicas: 1
  selector:
    matchLabels:
      app: cpu-stress-2
  template:
    metadata:
      labels:
        app: cpu-stress-2
    spec:
      containers:
      - name: stress
        image: progrium/stress
        args: ["--cpu", "1", "--timeout", "3600s"]
        resources:
          requests:
            cpu: 100m
          limits:
            cpu: 500m
```

Apply and observe:
```bash
kubectl apply -f test-workloads.yaml

# Watch PodAllocations being created
kubectl get podallocations -n test-mbcas -w

# After 30-60 seconds, check pod limits
kubectl get pods -n test-mbcas -o jsonpath='{range .items[*]}{.metadata.name}: {.spec.containers[0].resources.limits.cpu}{"\n"}{end}'
```

## Step 9: Verify Metrics

Check throttling before/after:
```bash
# Get pod name
POD=$(kubectl get pods -n test-mbcas -l app=cpu-stress-1 -o name | head -1)

# Check cgroup stats (requires exec capability)
kubectl exec -n test-mbcas $POD -- cat /sys/fs/cgroup/cpu.stat | grep throttled
```

## Step 10: Cleanup

```bash
kubectl delete namespace test-mbcas
kubectl delete namespace mbcas-system
kubectl delete crd podallocations.allocation.mbcas.io
kind delete cluster
```

## Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| Agent CrashLoopBackOff | Cgroup not mounted or wrong path | Check `/sys/fs/cgroup` exists; verify cgroup v2 |
| No PodAllocations created | Agent not finding pods or cgroups | Check agent logs; verify pods on same node |
| Controller not applying changes | `pods/resize` not available | Verify feature gate; check `kubectl api-resources` |
| Allocations stuck in Pending | Cooldown or step limit | Wait 30s; check controller logs |
| Oscillating allocations | Hysteresis too low or smoothing insufficient | Increase MinChangePercent or smoothing alpha |
```

---

## 7. Backlog After MVP (Phase 2+)

### Phase 2: Production Hardening
- [ ] Add Prometheus metrics endpoint (optional, operator pattern)
- [ ] Implement warm-up period for new pods
- [ ] Add backoff on controller failures
- [ ] Support multiple cgroup path patterns (CRI-O, containerd, Docker)
- [ ] Add admission webhook to prevent invalid PodAllocations
- [ ] Leader election for controller (if running multiple replicas)

### Phase 3: Enhanced Signals
- [ ] Integrate PSI (Pressure Stall Information) for memory pressure
- [ ] Add network I/O pressure signals (optional)
- [ ] Per-container demand aggregation for multi-container pods

### Phase 4: Advanced Fairness
- [ ] PriorityClass integration (higher priority = higher weight)
- [ ] Namespace quotas as hard caps
- [ ] Cross-node coordination for cluster-wide fairness

### Phase 5: Observability
- [ ] Grafana dashboard for allocation visualization
- [ ] Alert rules for allocation anomalies
- [ ] Allocation audit log

### Phase 6: Academic Validation
- [ ] Formal stability analysis (Lyapunov function)
- [ ] Comparative benchmark vs VPA
- [ ] Publish evaluation results

---

## 8. Questions/Unknowns

### Blocking Questions (Need Answers Before Deployment)

1. **Target Kubernetes distribution?**
   - Cgroup path patterns vary by distribution
   - EKS, GKE, AKS, Rancher, OpenShift have different layouts
   - **Assumption**: Kind/Minikube for MVP; will need testing on production distro

2. **Feature gate availability in production cluster?**
   - `InPlacePodVerticalScaling` is beta in K8s 1.27+ (stable in 1.33+)
   - `kubectl --subresource=resize` requires kubectl 1.32+
   - Some managed providers may not expose this
   - **Assumption**: User can enable feature gate or will use self-managed cluster
   - **Recommendation**: Pin Kind node image to v1.35.x or later for MVP testing

### Non-Blocking Unknowns (Can Proceed with Assumptions)

3. **Optimal smoothing parameters?**
   - α=0.3 (up) and α=0.1 (down) are reasonable defaults
   - **Assumption**: Tune empirically during testing

4. **Throttling threshold calibration?**
   - 10% throttling = full demand is a guess
   - **Assumption**: Start with this, measure actual throttling distribution

5. **Interaction with cluster autoscaler?**
   - If MBCAS raises limits, CA may not scale up when needed
   - **Assumption**: Test separately; CA looks at requests, not limits

---

## Appendix: Could This Be Done Simpler?

### Alternative: VPA + PriorityClass + Quotas

| Capability | VPA Approach | MBCAS | Winner |
|------------|--------------|-------|--------|
| Auto-sizing | ✅ Historical-based | ✅ Pressure-based | MBCAS (faster) |
| Fairness | ❌ No cross-pod awareness | ✅ Proportional fairness | MBCAS |
| No config needed | ❌ Requires target metrics | ✅ Zero config | MBCAS |
| Production-ready | ✅ Mature | ⚠️ New | VPA |
| Kernel awareness | ❌ Uses metrics-server | ✅ Direct cgroup | MBCAS |
| Restart-free | ❌ (UpdateMode: Recreate for limits) | ✅ In-place | MBCAS |

**Verdict**: If you need fast, config-free, kernel-aware allocation without restarts, MBCAS is justified. If you can tolerate restarts and some configuration, VPA is simpler.

### Minimum Viable Alternative (No Custom Code)

For 60% of the benefit with 0% custom code:
1. Set aggressive CPU limits (2x request)
2. Use VPA in "Off" mode for recommendations only
3. Use PriorityClass for critical workloads
4. Use ResourceQuota per namespace
5. Enable descheduler for rebalancing

This won't provide real-time fairness but will prevent most CPU starvation.
