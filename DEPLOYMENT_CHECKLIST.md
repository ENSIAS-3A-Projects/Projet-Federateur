# Deployment Checklist for MBCAS

## Prerequisites

### 1. Kubernetes Cluster Requirements
- **Kubernetes version:** 1.27+ (required for `pods/resize` subresource)
- **Feature gate:** `InPlacePodVerticalScaling` must be enabled
  - For Minikube: `minikube start --feature-gates=InPlacePodVerticalScaling=true`
  - For kubeadm: Add to `/etc/kubernetes/manifests/kube-apiserver.yaml`
  - For managed clusters: Check provider documentation

### 2. Verify Feature Gate
```bash
# Check if pods/resize subresource is available
kubectl api-resources | grep resize
# Should show: pods/resize
```

## What Will Work ✅

1. **CRD Installation** - PodAllocation CRD is defined and ready
2. **RBAC** - Both controller and agent RBAC are properly configured
3. **Controller Logic** - Phase 2 controller is complete and tested
4. **Agent Logic** - Phase 3/4 agent logic is complete
5. **Market Solver** - Phase 4 allocation algorithm is tested

## What Needs Fixing ⚠️

### 1. Agent DaemonSet Security Context
**Issue:** Current DaemonSet has:
```yaml
securityContext:
  runAsNonRoot: true
  runAsUser: 1000
  capabilities:
    drop:
    - ALL
```

**Problem:** Reading cgroups typically requires root or specific capabilities.

**Fix Options:**
- Option A (Recommended for testing): Run as root with minimal capabilities
  ```yaml
  securityContext:
    runAsNonRoot: false
    runAsUser: 0
    capabilities:
      add:
      - SYS_ADMIN  # Needed for cgroup access
      drop:
      - ALL
  ```

- Option B (Production): Use a non-root user with proper cgroup permissions
  - Requires cgroup delegation configured on the node
  - More complex but more secure

### 2. Missing Controller Deployment
**Issue:** No deployment manifest for the controller.

**Needed:** Create `config/controller/deployment.yaml`:
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: mbcas-controller
  namespace: system
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: mbcas
      app.kubernetes.io/component: controller
  template:
    metadata:
      labels:
        app.kubernetes.io/name: mbcas
        app.kubernetes.io/component: controller
    spec:
      serviceAccountName: mbcas-controller
      containers:
      - name: controller
        image: mbcas-controller:latest
        imagePullPolicy: IfNotPresent
        command:
        - /controller
        resources:
          requests:
            cpu: 100m
            memory: 128Mi
          limits:
            cpu: 200m
            memory: 256Mi
```

### 3. Missing Docker Images
**Issue:** No Dockerfiles or build instructions.

**Needed:**
- `Dockerfile.controller` for controller binary
- `Dockerfile.agent` for agent binary
- Build and push instructions

### 4. Missing CRD Installation
**Issue:** CRD YAML exists but no installation instructions.

**Fix:** Add to `config/crd/kustomization.yaml` or provide install command:
```bash
kubectl apply -f config/crd/bases/allocation.mbcas.io_podallocations.yaml
```

### 5. Cgroup Path Detection
**Issue:** Agent uses best-effort cgroup path discovery.

**Potential Issues:**
- May not work on all Kubernetes distributions
- CRI-O vs containerd vs Docker have different cgroup layouts
- May need distribution-specific path detection

**Mitigation:** Test on your target distribution first.

## Deployment Steps (Once Fixed)

1. **Build Images:**
   ```bash
   # Build controller
   docker build -f Dockerfile.controller -t mbcas-controller:latest .
   
   # Build agent
   docker build -f Dockerfile.agent -t mbcas-agent:latest .
   
   # Push to registry (if needed)
   docker push <registry>/mbcas-controller:latest
   docker push <registry>/mbcas-agent:latest
   ```

2. **Install CRD:**
   ```bash
   kubectl apply -f config/crd/bases/allocation.mbcas.io_podallocations.yaml
   ```

3. **Install RBAC:**
   ```bash
   kubectl apply -f config/rbac/
   kubectl apply -f config/agent/rbac.yaml
   ```

4. **Deploy Controller:**
   ```bash
   kubectl apply -f config/controller/deployment.yaml
   ```

5. **Deploy Agent:**
   ```bash
   kubectl apply -f config/agent/daemonset.yaml
   ```

6. **Verify:**
   ```bash
   # Check controller
   kubectl get pods -n system -l app.kubernetes.io/component=controller
   
   # Check agent (should be one per node)
   kubectl get pods -n system -l app.kubernetes.io/component=agent
   
   # Check PodAllocations
   kubectl get podallocations --all-namespaces
   ```

## Testing on a Running App

### What Will Work:
- ✅ Controller will watch PodAllocation CRDs
- ✅ Controller will apply CPU limits via pods/resize
- ✅ Agent will discover pods on nodes
- ✅ Agent will compute market allocations
- ✅ Agent will write PodAllocation CRDs

### What Might Not Work:
- ⚠️ Agent cgroup reading (security context issue)
- ⚠️ Cgroup path detection (distribution-specific)
- ⚠️ Pod resizing (if feature gate not enabled)

### Quick Test:
1. Deploy a test pod with CPU requests/limits
2. Check if agent creates PodAllocation for it
3. Check if controller applies the allocation
4. Monitor pod CPU limits changing

## Recommendations

1. **Start with a test cluster** (Minikube/Kind) to verify everything works
2. **Fix security context** before production deployment
3. **Test cgroup path detection** on your target Kubernetes distribution
4. **Monitor logs** for agent and controller errors
5. **Start with a small namespace** to test before rolling out cluster-wide

## Current Status

- **Code:** ✅ Complete and tested
- **Manifests:** ⚠️ Mostly complete, missing controller deployment
- **Images:** ❌ Need Dockerfiles
- **Documentation:** ⚠️ Needs deployment guide
- **Security:** ⚠️ Agent security context needs adjustment

**Overall:** ~80% ready for deployment, needs the fixes above.

