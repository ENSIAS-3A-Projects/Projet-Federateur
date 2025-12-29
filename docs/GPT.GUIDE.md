# GUIDE.md — Getting to a working MVP (Kind + your microservice)

## MVP goal
Run MBCAS end-to-end on a Kind cluster:
1) Agent measures per-pod CPU pressure (cgroup throttling),
2) Agent writes PodAllocation objects,
3) Controller applies in-place CPU limit changes via pods/resize,
4) You observe your microservice pods getting CPU limits adjusted under load.

This MVP is CPU-only, per-node, Kubernetes-native (CRD + controller + DaemonSet agent).

---

## 0) What already exists (so we don’t reinvent anything)

### Agent loop
- Samples every 1s, writes allocations every 15s, with a 10% hysteresis threshold.
- Skips kube-system.
- Uses market-based allocator (Phase 4): computeAllocations() calls allocation.ClearMarket().
Source: pkg/agent/agent.go (sampling/write loop, constants, market call). 

### CRD
- PodAllocation CRD has spec.desiredCPULimit (and helpful printer columns).
Source: allocation.mbcas.io_podallocations.yaml

### Controller
- Watches PodAllocation + Pods.
- Skips Guaranteed QoS pods.
- Applies CPU **limits** only (PolicyLimits) via actuator + pods/resize.
- Has cooldown + max-step safety.
Source: pkg/controller/podallocation_controller.go

### Bootstrap script
- mvp-test.sh creates a Kind cluster, builds images, loads images, applies CRD/RBAC/controller/agent, and prints validation commands.
Source: mvp-test.sh

---

## 1) Pre-reqs on your machine
- docker
- kind
- kubectl
- go toolchain (only if you want local builds; script uses Dockerfiles anyway)

---

## 2) Pick ONE operator namespace (IMPORTANT)
Your manifests are mixed between `system` and `mbcas-system`.

For MVP, standardize on:
- Namespace: `mbcas-system`

Do this:
- Ensure namespace.yaml creates `mbcas-system`
- Ensure ALL ServiceAccounts / RoleBindings referenced by the controller and agent are in `mbcas-system`
- If a YAML hardcodes `namespace: system`, change it to `mbcas-system`

Why: the agent DaemonSet you use for MVP is already in `mbcas-system` (agent-daemonset-fixed.yaml). If RBAC creates the ServiceAccount in `system`, the DaemonSet won’t find it.

---

## 3) One-command bootstrap (recommended)
Run the existing script:

  bash ./scripts/mvp-test.sh

What it does:
- Creates a Kind cluster
- Builds mbcas-controller + mbcas-agent images (Dockerfile.controller / Dockerfile.agent)
- Loads them into Kind
- Applies CRD, RBAC, controller deployment, agent daemonset
- Prints verification commands

If it fails: see the Troubleshooting section.

---

## 4) Deploy YOUR microservice into the cluster
You can apply your existing Kubernetes manifests directly:

  kubectl apply -f /path/to/your-app-manifests/

### MUST-HAVES for compatibility
1) Each CPU-managed pod must have CPU resources set on container[0] (MVP assumption).
2) Do NOT run as Guaranteed QoS if you expect resizing:
   - Guaranteed QoS often means requests == limits; controller will skip those pods.

Recommended pattern:
- requests.cpu: small baseline (e.g., 100m)
- limits.cpu: initial cap (e.g., 500m or 1000m)

Quick patch example (no YAML editing):
  kubectl -n <ns> set resources deploy/<name> --requests=cpu=100m --limits=cpu=500m

If your app uses multiple containers per pod:
- For MVP: keep the main container as container[0], or run single-container pods.

---

## 5) Create real CPU contention (so the system has something to react to)
You need throttling to happen.

Easy options:
A) Increase load to your microservice (your normal traffic/load tool).
B) Temporarily reduce CPU limits to induce throttling, then let MBCAS scale them up:
   - set limits.cpu low (e.g. 100m) and send load
C) Run a CPU-hog test pod in the same node to increase contention.

Key: in Kind, keep pods on the same worker node for the clearest demo.

---

## 6) Observe the system working

### 6.1 PodAllocation objects appear and update
  kubectl get podallocations -A
  kubectl get podallocations -A -o wide

PodAllocation prints desiredCPULimit in columns (CRD additionalPrinterColumns).

### 6.2 Controller events + resizing
  kubectl -n mbcas-system logs deploy/mbcas-controller -f

Watch for events/resizes (or errors about pods/resize).

### 6.3 Watch actual CPU limits change on your pods
  kubectl -n <ns> get pod <pod> -o jsonpath='{.spec.containers[0].resources.limits.cpu}{"\n"}'

Repeat while load changes.

### 6.4 Agent logs (optional but useful)
  kubectl -n mbcas-system logs ds/mbcas-agent -f

You should see allocation decisions every write interval.

---

## 7) MVP success criteria (keep it simple)
- Agent is Running on the node(s)
- Controller is Running
- PodAllocation resources get created/updated for your microservice pods
- Under load, your pod CPU limits change (via in-place resize)
- System remains stable (no constant flapping)

---

## 8) Troubleshooting (most common MVP blockers)

### A) "pods/resize not found" or resize calls fail
Cause: InPlacePodVerticalScaling feature gate not enabled.
Fix:
- Ensure Minikube is started with `--feature-gates=InPlacePodVerticalScaling=true` (mvp-test.sh already does this).
- Recreate the cluster if needed: `minikube delete -p mbcas` then run the script again.

### B) "forbidden" when patching pods/resize
Cause: RBAC missing for pods/resize.
Fix:
- Ensure controller’s Role/ClusterRole includes pods/resize verbs.
- Ensure RoleBinding/ClusterRoleBinding points to the correct ServiceAccount in mbcas-system.

### C) Agent can't read cgroups / no data
Cause: DaemonSet security context / mounts.
Fix:
- Ensure `config/agent/daemonset.yaml` has correct security context (runs as root with SYS_ADMIN).
- Ensure it mounts /sys/fs/cgroup from the host.

### D) Nothing resizes even though PodAllocation updates
Common causes:
- Pods are Guaranteed QoS (requests == limits) and controller skips them
- Your microservice pods have no CPU limits set
- Multi-container pod where container[0] is not the “real” workload

---

## 9) After MVP (don’t do before)
Once it works end-to-end, you can improve:
- multi-container handling (sum/max per container)
- faster pod discovery (informer cache instead of list)
- better cgroup mapping robustness
- optional metrics endpoint for easier evaluation
