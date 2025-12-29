

# MBCAS — Market-Based CPU Allocation System for Kubernetes

**Status:** MVP-ready (80% complete)  
**Latest:** End-to-end deployment tested on Minikube/Kind  
**Contributors:** Benabbou Imane, Bouazza Imane, Alaoui Sosse Saad, Taqi Mohamed Chadi

---

## What Is This?

**MBCAS** is a **Kubernetes-native CPU allocation system** that automatically and fairly distributes CPU resources among pods based on real kernel pressure signals—**without any user configuration, external metrics systems, or application changes**.

Instead of asking "how much CPU should this pod get?", MBCAS observes **what the kernel is telling us** about CPU demand (via cgroup throttling) and automatically adjusts pod CPU limits to match actual need. It's like a thermostat for Kubernetes CPU: measure pressure → adjust limits → stabilize.

**How it's different from Kubernetes HPA/VPA:**
- ❌ HPA/VPA need user annotations and external metrics pipelines (Prometheus)
- ✅ MBCAS reads kernel signals directly (no external dependencies)
- ✅ MBCAS reacts in seconds (not minutes)
- ✅ MBCAS uses game theory to ensure fairness (proportional fairness allocation)
- ✅ MBCAS requires zero configuration

---

## Why Should I Care?

### The Problem MBCAS Solves

Your Kubernetes cluster struggles with:
- **CPU overprovisioning** — you're reserved 4 cores but only use 1.5 cores consistently
- **Throttling under load** — brief traffic spikes cause cascading timeouts
- **Operational complexity** — tuning HPA/VPA targets is tedious and error-prone
- **Slow reaction time** — autoscalers react to historical metrics, not live pressure
- **Unfair resource allocation** — without a fairness mechanism, aggressive services starve others

### What MBCAS Provides

✅ **Automatic CPU allocation** based on real demand (kernel throttling ratio)  
✅ **Fairness guarantee** — no pod starves, load shared proportionally to demand  
✅ **Fast reaction** — measures and adjusts every 15 seconds  
✅ **Zero configuration** — just deploy and it works  
✅ **No application changes** — works with any containerized workload  
✅ **Kubernetes-native** — uses CRDs, controllers, and in-place pod resizing  

### Use Cases

- **Multi-tenant clusters** where CPU fairness is critical
- **Bursty workloads** (batch jobs + always-on services) sharing nodes
- **Cost optimization** — run more pods per node by autoscaling CPU dynamically
- **Dev/test environments** — automatic resource tuning without manual tweaking
- **Mission-critical services** — avoid throttling surprises

---

## Is It Usable or Experimental?

### ✅ Usable (MVP Ready)

**MBCAS is deployment-ready for testing.** All components are implemented and tested:

| Component | Status | Proof |
|-----------|--------|-------|
| **Kernel signal reader** (cgroup throttling) | ✅ Implemented & tested | `pkg/agent/cgroup/reader.go` (195 lines) |
| **Demand tracking** (EMA smoothing) | ✅ Implemented & tested | `pkg/agent/demand/tracker.go` (43 lines) |
| **Market solver** (proportional fairness) | ✅ Implemented & tested | `pkg/allocation/market.go` + 529 test lines |
| **Kubernetes controller** (reconciliation loop) | ✅ Implemented & tested | `pkg/controller/podallocation_controller.go` (357 lines) |
| **Pod resizer** (in-place scaling) | ✅ Implemented & tested | `pkg/actuator/actuator.go` + 655 test lines |
| **Deployment artifacts** | ✅ Complete | Dockerfiles, manifests, RBAC, scripts |
| **CLI tool** | ✅ Implemented | `main.go` + `pkg/podtool/` for manual testing |

**Unit test coverage:** ~1,200 test lines (49% test-to-code ratio for infrastructure code)

### ⚠️ Experimental (Limitations)

**Not recommended for production clusters without validation:**

- **Tested on:** Minikube, Kind clusters
- **Not yet tested on:** EKS, GKE, large multi-node clusters (>10 nodes)
- **Single metric:** Uses only CPU throttling; PSI integration planned
- **Cgroup path detection:** Uses glob patterns (may fail on CRI-O, containerd variants)
- **Performance:** Sequential pod discovery (scales OK up to ~100 pods/node)

**Mitigation:** MVP is fully functional for demonstration and evaluation. Production deployment requires:
1. Load testing on target Kubernetes distribution
2. Multi-node validation (DaemonSet runs on all nodes)
3. Validation of cgroup path detection on your CRI
4. Optional: PSI integration if memory pressure matters

---

## How Do I Run It?

### Prerequisites

- **Kubernetes cluster** (v1.27+) with `InPlacePodVerticalScaling` feature gate enabled
- **kubectl** (v1.32+ recommended for `--subresource=resize` CLI support)
- For testing: **Minikube** or **Kind**

### Quick Start (Minikube)

```bash
# 1. Clone and navigate
cd mbcas

# 2. One-command deploy (sets up cluster, builds, deploys, runs tests)
bash scripts/mvp-test.sh all

# On Windows PowerShell:
.\scripts\mvp-test.ps1 all
```

This script:
- ✅ Creates Minikube cluster with feature gate enabled
- ✅ Builds Docker images (controller + agent)
- ✅ Loads images into Minikube
- ✅ Deploys MBCAS (CRD, RBAC, controller, agent)
- ✅ Deploys test workload (nginx)
- ✅ Shows verification commands

**Expected runtime:** ~3-5 minutes

### Verify Deployment

```bash
# 1. Check components
kubectl -n mbcas-system get pods
# Output: mbcas-controller (Deployment) + mbcas-agent (DaemonSet on each node)

# 2. Watch allocations appear
kubectl get podallocations -A -o wide
# Output: Pod UUID, namespace, pod name, desired CPU, applied CPU, status

# 3. Monitor controller
kubectl -n mbcas-system logs deploy/mbcas-controller -f
# Look for: "Updated PodAllocation" events

# 4. Monitor agent
kubectl -n mbcas-system logs ds/mbcas-agent -f
# Look for: "Pod demand sample" entries
```

### Deploy Your Own Workload

```bash
# Deploy any microservice
kubectl apply -f your-app.yaml

# MBCAS will automatically:
# 1. Discover the pod
# 2. Read its CPU throttling
# 3. Compute fair allocation
# 4. Create/update PodAllocation CRD
# 5. Apply in-place CPU limit changes

# Monitor your pod's CPU limit changing
watch -n 2 "kubectl get pod <name> -o jsonpath='{.spec.containers[0].resources.limits.cpu}'"
```

### Create Real CPU Contention (to see MBCAS in action)

```bash
# Option 1: Send load to your microservice
ab -n 10000 -c 100 http://your-service

# Option 2: Reduce initial CPU limit to trigger throttling
kubectl set resources deployment/your-app --limits=cpu=100m

# Option 3: Deploy a CPU hog on same node
kubectl run cpu-burn --image=progrium/stress --requests=cpu=1 -- --cpu 1
```

---

## How Does It Work? (60-second version)

```
1. Agent (DaemonSet)
   → Reads pod's cgroup throttling ratio every 1 second
   → Smooths the signal (EMA: fast up, slow down)
   → Every 15s, computes fair allocation using Fisher-market algorithm
   → Writes PodAllocation CRD

2. Controller (Deployment)
   → Watches PodAllocation changes
   → Compares desired CPU vs actual pod limit
   → Applies safety rules (cooldown 30s, step limit 1.5x)
   → Calls pods/resize subresource to update limit

3. Pod Container
   → Limit is updated in-place (no restart)
   → If limit increases → more CPU available
   → If limit decreases → kernel enforces new cap

Result: Automatic, fair, stable CPU allocation without user config
```

---

## Key Files to Understand

| File | Purpose |
|------|---------|
| `pkg/agent/agent.go` | Main agent loop (sampling, writing) |
| `pkg/agent/cgroup/reader.go` | Kernel throttling signal reader |
| `pkg/allocation/market.go` | Fisher-market solver (proportional fairness) |
| `pkg/controller/podallocation_controller.go` | Reconciliation loop |
| `pkg/actuator/actuator.go` | In-place pod resizing library |
| `api/v1alpha1/podallocation_types.go` | CRD definition |
| `config/` | Kubernetes manifests (deployment, daemonset, RBAC) |
| `scripts/mvp-test.sh` | End-to-end deployment script |
| `COMPLETE_CODEBASE_ANALYSIS.md` | Detailed technical analysis (80+ pages) |

---

## Deployment Readiness Checklist

| Item | Status |
|------|--------|
| Core implementation (agent, controller, actuator, market) | ✅ 100% |
| Unit tests | ✅ 100% |
| Docker images (controller, agent) | ✅ Ready |
| Kubernetes manifests (all files) | ✅ Complete |
| RBAC configuration | ✅ Correct |
| Deployment scripts (bash + PowerShell) | ✅ Functional |
| Documentation | ✅ Comprehensive |
| MVP deployment tested | ✅ Minikube/Kind |
| Production load testing | ⚠️ Not yet |
| Multi-node scalability testing | ⚠️ Not yet |

---

## Known Limitations & Future Work

### Limitations
- **Single metric:** CPU throttling only (PSI integration planned)
- **Cgroup detection:** Uses glob patterns (may need tuning for some CRIs)
- **Pod discovery:** Sequential API calls (OK for MVP, optimized for scale later)
- **Tested platforms:** Minikube, Kind (untested on EKS, GKE, Rancher)

### Planned Enhancements
- PSI (Pressure Stall Information) for memory awareness
- Informer-based pod discovery (faster, lower API load)
- PriorityClass weighting for weighted fairness
- Metrics endpoint (Prometheus integration)
- Multi-container pod support
- Predictive demand (ML-based)

---

## References

**Game Theory Concepts:**
- Fisher markets and proportional fairness
- Nash Social Welfare maximization
- See: `game_theory_concepts_simple.md` & `game_theory_for_microservices.md`

**Academic Context:**
- Projet Fédérateur: "Collaborative strategies in microservices using game theory"
- MBCAS demonstrates practical application of game-theoretic resource coordination

**Kubernetes Features Used:**
- CRDs (Custom Resource Definitions) — for PodAllocation state
- Controller-Runtime — for reconciliation loop
- In-place pod resizing (`pods/resize` subresource) — Kubernetes 1.27+
- DaemonSet + cgroup v2 access — for node agent

---

## Quick Troubleshooting

| Problem | Solution |
|---------|----------|
| `pods/resize` not found | Ensure `InPlacePodVerticalScaling=true` feature gate. Use `mvp-test.sh` to create cluster correctly. |
| Agent can't read cgroups | Check security context: agent needs root + SYS_ADMIN capability. Verify cgroup v2 available (`/sys/fs/cgroup`). |
| No PodAllocation objects created | Check agent logs: `kubectl -n mbcas-system logs ds/mbcas-agent`. Verify pods are running on the node. |
| Controller not resizing pods | Check controller logs: `kubectl -n mbcas-system logs deploy/mbcas-controller`. Verify RBAC allows `pods/resize` patch. |
| Pods stuck in "Pending" status | Check cooldown (30s) and step size limits (1.5x). Controller waits if safety rules trigger. |

---

## Support & Feedback

This is a **research project** developed as part of a collaborative academic initiative. For questions or feedback:
- Review `COMPLETE_CODEBASE_ANALYSIS.md` for detailed implementation notes
- Check `docs/` directory for architecture guides and evaluation reports
- Refer to game theory primers in `game_theory_concepts_simple.md`

---

## Summary

**MBCAS** is a working prototype of **kernel-informed, game-theoretic CPU allocation for Kubernetes**. It's fully implemented, tested, and ready for MVP deployment. Use it to:
- Learn how market mechanisms can coordinate microservices
- Evaluate fairness-aware autoscaling in your environment
- Reduce operational overhead of manual resource tuning
- Demonstrate practical game theory applications

**Next step:** Run `bash scripts/mvp-test.sh all` and watch MBCAS allocate CPU dynamically.
