# MBCAS Demo Readiness Checklist

## ✅ Status: READY FOR DEMO

All required components are in place. The system is ready for end-to-end demonstration.

---

## Prerequisites Check

Before running the demo, ensure you have:

- [x] **Docker** installed and running
- [x] **Minikube** installed (`minikube` command available)
- [x] **kubectl** installed (version 1.32+ recommended for `--subresource=resize`)
- [x] **Go 1.21+** (for building, though Dockerfiles handle this)
- [x] **Bash** or **PowerShell** (for running deployment scripts)

**Quick check:**
```bash
docker --version
minikube version
kubectl version --client
go version
```

---

## Component Status

### ✅ Phase 1: Infrastructure (Actuator)
- [x] `pkg/actuator/actuator.go` - Complete
- [x] In-place pod resizing via `pods/resize` subresource
- [x] Error handling and retry logic
- [x] CLI tool (`pkg/podtool`) for manual testing

### ✅ Phase 2: Control Contract (CRD + Controller)
- [x] `PodAllocation` CRD defined (`api/v1alpha1/`)
- [x] CRD manifest generated (`config/crd/bases/`)
- [x] Controller implementation (`pkg/controller/`)
- [x] Controller deployment manifest (`config/controller/deployment.yaml`)
- [x] RBAC configured (`config/rbac/`)
- [x] Pod event handler for reconciliation

### ✅ Phase 3: Node Agent (Demand Sensing)
- [x] Agent implementation (`pkg/agent/`)
- [x] Cgroup reader (`pkg/agent/cgroup/reader.go`)
- [x] Demand tracker with EMA smoothing (`pkg/agent/demand/tracker.go`)
- [x] Agent DaemonSet manifest (`config/agent/daemonset.yaml`)
- [x] Agent RBAC (`config/agent/rbac.yaml`)
- [x] Security context fixed (root + SYS_ADMIN for cgroup access)

### ✅ Phase 4: Market-Based Allocation
- [x] Market solver (`pkg/allocation/market.go`)
- [x] Water-filling algorithm with bounds
- [x] Deterministic rounding
- [x] Calculator for K8s → market params (`pkg/agent/demand/calculator.go`)
- [x] Unit tests (`pkg/allocation/market_test.go`, `pkg/agent/demand/calculator_test.go`)

### ✅ Deployment Artifacts
- [x] `Dockerfile.controller` - Multi-stage build
- [x] `Dockerfile.agent` - Multi-stage build
- [x] `config/namespace.yaml` - Creates `mbcas-system` namespace
- [x] `scripts/mvp-test.sh` / `scripts/mvp-test.ps1` - End-to-end deployment scripts
- [x] `scripts/minikube-setup.sh` / `scripts/minikube-setup.ps1` - Minikube cluster setup

---

## Quick Start for Demo

### Option 1: One-Command Deployment (Recommended)

**On Windows (PowerShell):**
```powershell
# From project root
.\scripts\mvp-test.ps1 all
```

**On Linux/Mac (Bash):**
```bash
# From project root
bash scripts/mvp-test.sh all
```

This will:
1. Create Minikube cluster with feature gate enabled
2. Build Docker images (using Minikube's Docker daemon)
3. Deploy all MBCAS components
4. Deploy test workloads
5. Show status

### Option 2: Step-by-Step

**On Windows (PowerShell):**
```powershell
# 1. Create cluster
.\scripts\mvp-test.ps1 cluster

# 2. Build images
.\scripts\mvp-test.ps1 build

# 3. Deploy MBCAS
.\scripts\mvp-test.ps1 deploy

# 4. Deploy test workloads
.\scripts\mvp-test.ps1 test

# 5. Monitor
.\scripts\mvp-test.ps1 monitor
```

**On Linux/Mac (Bash):**
```bash
# 1. Create cluster
bash scripts/mvp-test.sh cluster

# 2. Build images
bash scripts/mvp-test.sh build

# 3. Deploy MBCAS
bash scripts/mvp-test.sh deploy

# 4. Deploy test workloads
bash scripts/mvp-test.sh test

# 5. Monitor
bash scripts/mvp-test.sh monitor
```

---

## Demo Flow

1. **Initial State**
   - Cluster created with Kind
   - MBCAS components deployed (controller + agent)
   - Test workloads deployed with CPU requests/limits

2. **Observation Phase**
   - Watch PodAllocation CRDs being created:
     ```bash
     kubectl get podallocations -A -w
     ```
   - Check agent logs:
     ```bash
     kubectl logs -n mbcas-system -l app.kubernetes.io/component=agent -f
     ```
   - Check controller logs:
     ```bash
     kubectl logs -n mbcas-system -l app.kubernetes.io/component=controller -f
     ```

3. **Load Generation**
   - Test workloads generate CPU load
   - Agent detects throttling via cgroups
   - Market solver computes allocations
   - Controller applies CPU limit changes

4. **Verification**
   - Watch pod CPU limits change:
     ```bash
     kubectl get pods -n test-mbcas -o jsonpath='{range .items[*]}{.metadata.name}: {.spec.containers[0].resources.limits.cpu}{"\n"}{end}'
     ```
   - Verify PodAllocation status:
     ```bash
     kubectl get podallocations -n test-mbcas -o wide
     ```

---

## Known Limitations (For Demo Context)

These are documented and acceptable for MVP:

1. **Single Container Per Pod** - Only `container[0]` is managed
2. **Guaranteed QoS Pods** - Skipped by controller (by design)
3. **Cgroup Path Detection** - Best-effort, may need adjustment for different K8s distributions
4. **No Warm-up Period** - New pods may oscillate initially
5. **Kind/Minikube Focus** - Tested primarily on Kind clusters

---

## Troubleshooting Quick Reference

| Issue | Solution |
|-------|----------|
| `pods/resize not found` | Feature gate not enabled - recreate cluster with `kind-config.yaml` |
| Agent CrashLoopBackOff | Check cgroup mount and security context |
| No PodAllocations created | Check agent logs, verify pods are on same node |
| Controller not applying | Check RBAC, verify ServiceAccount name matches |
| Allocations stuck in Pending | Check cooldown (30s) and step limit (1.5x) |

See `docs/GPT.GUIDE.md` section 8 for detailed troubleshooting.

---

## Success Criteria

Demo is successful if:

- ✅ Controller pod is Running
- ✅ Agent pod(s) are Running (one per node)
- ✅ PodAllocation CRDs are created for test pods
- ✅ CPU limits change under load (visible via `kubectl get pod`)
- ✅ System remains stable (no constant flapping)

---

## Post-Demo Cleanup

```bash
bash scripts/mvp-test.sh cleanup
```

Or manually:
```bash
kubectl delete namespace test-mbcas
kubectl delete namespace mbcas-system
kubectl delete crd podallocations.allocation.mbcas.io
minikube delete -p mbcas
```

---

## Documentation

- **Quick Start**: `docs/GPT.GUIDE.md`
- **Architecture**: `docs/MBCAS_EVALUATION.md`
- **File Organization**: `docs/FILE_ORGANIZATION.md`
- **Project Plan**: `PLAN.md`
- **Main README**: `README.md`

---

## Summary

**✅ All systems ready for demo!**

The project has:
- Complete implementation (Phases 1-4)
- All deployment artifacts
- Automated deployment script
- Comprehensive documentation
- Test workloads included

**Next step**: Run `bash scripts/mvp-test.sh all` and follow the demo flow above.

