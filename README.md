# MBCAS — Market-Based CPU Allocation System

**Demo-Ready | CPU-Only | Manages Everyone**

---

## Scope & Constraints

MBCAS is a **Kubernetes-native demo** that automatically distributes CPU resources among pods using game theory (Fisher market proportional fairness). This is an **educational/evaluation system**, not production-ready.

### What This Demo Does

| Capability | Status | Notes |
|------------|--------|-------|
| **CPU-only management** | ✅ | Memory limits are not touched |
| **Manage all pods by default** | ✅ | LimitRange ensures CPU bounds exist |
| **In-place pod resizing** | ✅ | Uses Kubernetes Alpha feature gate |
| **Proportional fairness** | ✅ | Game theory market solver |
| **Real kernel signals** | ✅ | Reads cgroup v2 throttling |
| **Zero configuration** | ✅ | Just deploy and it works |

### What This Demo Does NOT Do

| Non-Goal | Reason |
|----------|--------|
| **Production stability** | Alpha feature gates, limited testing |
| **Memory management** | Out of scope — CPU-only design |
| **Multi-cluster support** | Single cluster demo only |
| **Persist intent to manifests** | Ephemeral resizes only (see below) |
| **CRI-O compatibility** | Cgroup path detection is containerd-specific |

### Ephemeral vs Persistent Resizing

**MBCAS uses ephemeral resizing only:**
- ✅ CPU limit changes apply to **running pods**
- ⚠️ Pod restarts **revert to manifest values**
- ⚠️ Horizontal scaling (new replicas) **use manifest values**

This is intentional for the demo. Persisting intent would require:
1. Writing back to GitOps manifests (violates declarative model)
2. Or storing "desired" state in CRD (adds complexity)

For this demo, the ephemeral model is sufficient to demonstrate the market-based allocation algorithm.

---

## Prerequisites

| Requirement | Version | Notes |
|-------------|---------|-------|
| **Windows 10/11** | Any | PowerShell 5.1+ |
| **Minikube** | v1.32+ | [Install guide](https://minikube.sigs.k8s.io/docs/start/) |
| **kubectl** | v1.27+ | [Install guide](https://kubernetes.io/docs/tasks/tools/) |
| **Docker Desktop** | Latest | Required for Minikube driver |

### Feature Gate Requirement

MBCAS requires the **InPlacePodVerticalScaling** Alpha feature gate (Kubernetes 1.27+). The demo script enables this automatically via Minikube configuration.

---

## Demo Execution Order

Run scripts in sequence from PowerShell:

```powershell
cd "c:\Users\achra\Desktop\Game Theory"

# Step 1: Create cluster with feature gates and policies
.\scripts\01-start-cluster.ps1

# Step 2: Build and deploy MBCAS components
.\scripts\02-deploy-mbcas.ps1

# Step 3: Deploy UrbanMoveMS microservices stack
.\scripts\03-deploy-urbanmove.ps1

# Step 4: Generate CPU load on services
.\scripts\04-generate-load.ps1

# Step 5: Watch MBCAS react (live dashboard)
.\scripts\05-monitor-scaling.ps1

# Cleanup when done
.\scripts\cleanup.ps1        # Remove demo resources
.\scripts\cleanup.ps1 -All   # Also delete cluster
```

### Script Summary

| Script | Duration | Purpose |
|--------|----------|---------|
| `01-start-cluster.ps1` | 2-5 min | Create Minikube cluster, enable feature gate, apply LimitRange |
| `02-deploy-mbcas.ps1` | 1-2 min | Build Docker images, deploy CRD + RBAC + controller + agent |
| `03-deploy-urbanmove.ps1` | 3-5 min | Build microservices images, deploy full UrbanMoveMS stack |
| `04-generate-load.ps1` | 1 min | Start CPU stress on services |
| `05-monitor-scaling.ps1` | Ongoing | Live dashboard showing CPU usage and MBCAS decisions |
| `cleanup.ps1` | 30 sec | Remove all demo resources |

---

## What the Demo Proves

### ✅ Successfully Demonstrates

1. **Automatic CPU detection** — MBCAS reads cgroup throttling signals without user configuration
2. **Fair allocation** — Under contention, CPU is distributed proportionally to demand
3. **In-place resizing** — Pod CPU limits change without restart
4. **Real-time reaction** — Allocation decisions happen in ~15 second cycles
5. **LimitRange integration** — Pods without limits get defaults and become manageable
6. **Multi-service scaling** — MBCAS fairly distributes CPU across independent microservices

### Expected Demo Output

When running `05-monitor-scaling.ps1` with UrbanMoveMS under load:

```
========================================
  MBCAS Live Monitor - Press Ctrl+C to stop
========================================

POD               NAMESPACE   REQUEST    LIMIT      CPU_USAGE   ALLOCATION
bus-service-...   urbanmove   100m       500m       450m        ↑ 600m
ticket-service-..  urbanmove   100m       500m       200m        → 500m
gateway-...       urbanmove   100m       500m       490m        ↑ 700m

PodAllocations (urbanmove namespace):
bus-service-...   urbanmove   600m       (pending)
ticket-service-..  urbanmove   500m       (applied)
gateway-...       urbanmove   700m       (applied)
```

**Key observations:**
- Multiple services competing for CPU are fairly allocated
- Services with higher demand get higher allocations
- Allocations rebalance in ~15s cycles as demand changes
- No service starves (proportional fairness guarantee)

---

## Known Limitations (Honest)

### Critical Limitations

| Limitation | Impact | Mitigation |
|------------|--------|------------|
| **Alpha feature gate** | May change in future K8s versions | Pin to K8s 1.27-1.31 |
| **Containerd-only cgroup paths** | Won't work with CRI-O | Use containerd runtime |
| **Single-node demo** | Not validated on multi-node | Use single-node Minikube |
| **Metrics-server race** | May take 30s after pod start | Script waits for readiness |

### Design Limitations

| Limitation | Reason | Future Work |
|------------|--------|-------------|
| **CPU-only** | Memory resizing has different dynamics | Add memory support |
| **No PSI integration** | Linux 4.20+ pressure metrics not used | Roadmap item |
| **Guaranteed QoS hardcoded** | Cannot resize pods with requests=limits | Policy-driven in future |
| **No persistence** | Restarts revert to manifests | GitOps integration possible |

### Known Failure Modes

1. **Pod discovery fails** — If cgroup paths don't match glob patterns
2. **Resize fails** — If pod doesn't support in-place resizing (RestartPolicy)
3. **Allocation stale** — If PodAllocation CRD is orphaned after pod deletion
4. **Node pressure** — If total demand > node capacity (allocation is capped)

---

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     Kubernetes Node                          │
│  ┌───────────────────────────────────────────────────────┐  │
│  │                  MBCAS Agent (DaemonSet)              │  │
│  │  ┌─────────────┐  ┌──────────────┐  ┌─────────────┐  │  │
│  │  │ cgroup      │→ │ Demand       │→ │ Market      │  │  │
│  │  │ Reader      │  │ Tracker      │  │ Solver      │  │  │
│  │  │ (throttle)  │  │ (EMA smooth) │  │ (fairness)  │  │  │
│  │  └─────────────┘  └──────────────┘  └─────────────┘  │  │
│  │                           │                           │  │
│  │                           ▼                           │  │
│  │                  PodAllocation CRD                    │  │
│  └───────────────────────────────────────────────────────┘  │
│                             │                                │
│                             ▼                                │
│  ┌───────────────────────────────────────────────────────┐  │
│  │              MBCAS Controller (Deployment)            │  │
│  │  ┌─────────────┐  ┌──────────────┐  ┌─────────────┐  │  │
│  │  │ Watch       │→ │ Validate     │→ │ Apply       │  │  │
│  │  │ CRD changes │  │ Safety       │  │ Resize      │  │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

**Data Flow:**
1. Agent reads cgroup throttling (`/sys/fs/cgroup/...`)
2. Demand tracker smooths signals with EMA
3. Market solver computes fair allocation
4. Agent writes `PodAllocation` CRD
5. Controller watches CRD changes
6. Controller validates safety (cooldown, max step)
7. Controller applies resize via `PATCH /pods/{name}/resize`

---

## Directory Structure

```
mbcas/
├── scripts/                    # PowerShell demo scripts
│   ├── 01-start-cluster.ps1    # Cluster bootstrap
│   ├── 02-deploy-mbcas.ps1     # MBCAS deployment
│   ├── 03-deploy-demo-app.ps1  # Demo workloads
│   ├── 04-generate-load.ps1    # Load generation
│   ├── 05-monitor-scaling.ps1  # Live monitoring
│   └── cleanup.ps1             # Cleanup
├── k8s/                        # Kubernetes manifests
│   ├── mbcas/                  # MBCAS components
│   │   ├── namespace.yaml
│   │   ├── crd.yaml
│   │   ├── rbac.yaml
│   │   ├── controller-deployment.yaml
│   │   └── agent-daemonset.yaml
│   ├── policy/                 # Cluster policies
│   │   ├── limitrange-default.yaml
│   │   ├── namespace-demo.yaml
│   │   └── namespace-exclusions.yaml
│   └── vpa/                    # VPA conflict prevention
│       └── vpa-policy.yaml
├── pkg/                        # Go source code
│   ├── agent/                  # Node-level components
│   ├── controller/             # Cluster-level reconciler
│   ├── allocation/             # Market solver
│   └── actuator/               # Pod resize operations
├── api/v1alpha1/               # CRD types
└── cmd/                        # Entrypoints
```

---

## Troubleshooting

### Cluster won't start

```powershell
# Check Minikube status
minikube status -p mbcas

# Check Docker is running
docker ps

# Delete and recreate
minikube delete -p mbcas
.\scripts\01-start-cluster.ps1
```

### MBCAS agent not running

```powershell
# Check agent logs
kubectl logs -n mbcas-system daemonset/mbcas-agent

# Common issue: cgroup path detection
# Agent expects /sys/fs/cgroup/kubepods.slice/...
```

### Pods not being managed

```powershell
# Check LimitRange is applied
kubectl get limitrange -n demo

# Check pod has CPU limits
kubectl get pod <name> -n demo -o jsonpath='{.spec.containers[*].resources}'

# Check PodAllocation CRDs
kubectl get podallocations -n demo
```

### Resize not happening

```powershell
# Check controller logs
kubectl logs -n mbcas-system deployment/mbcas-controller

# Check feature gate is enabled
kubectl get --raw /apis/admission.k8s.io/v1/mutatingwebhookconfigurations
```

---

## References

- [Kubernetes In-Place Pod Vertical Scaling](https://kubernetes.io/docs/concepts/configuration/resize-pod-cpu-memory/)
- [Fisher Market Proportional Fairness](https://en.wikipedia.org/wiki/Fisher_market)
- [cgroups v2 CPU Controller](https://www.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html#cpu)
- [KEP-1287: In-Place Update of Pod Resources](https://github.com/kubernetes/enhancements/tree/master/keps/sig-node/1287-in-place-update-pod-resources)

---

## Contributors

Benabbou Imane, Bouazza Imane, Alaoui Sosse Saad, Taqi Mohamed Chadi

---

**⚠️ This is a demo/educational project. Not for production use.**
