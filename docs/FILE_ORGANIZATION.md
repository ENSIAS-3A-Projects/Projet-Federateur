# File Organization Summary

This document summarizes the organization of deployment and configuration files in the MBCAS project.

## Directory Structure

```
mbcas/
├── Dockerfile.controller          # Controller container build
├── Dockerfile.agent                # Agent container build
├── config/
│   ├── namespace.yaml              # mbcas-system namespace definition
│   ├── controller/
│   │   └── deployment.yaml         # Controller Deployment manifest
│   ├── agent/
│   │   ├── daemonset.yaml         # Agent DaemonSet (FIXED: security context)
│   │   ├── rbac.yaml              # Agent RBAC (ServiceAccount, ClusterRole, ClusterRoleBinding)
│   │   └── kustomization.yaml
│   ├── rbac/
│   │   ├── service_account.yaml   # Controller ServiceAccount (mbcas-controller)
│   │   ├── role.yaml              # Controller ClusterRole
│   │   ├── role_binding.yaml      # Controller ClusterRoleBinding
│   │   └── kustomization.yaml
│   └── crd/
│       └── bases/
│           └── allocation.mbcas.io_podallocations.yaml
├── scripts/
│   ├── mvp-test.sh                # End-to-end deployment and test script
│   └── kind-config.yaml           # Kind cluster configuration with feature gate
└── docs/
    ├── GPT.GUIDE.md               # MVP deployment guide
    ├── MBCAS_EVALUATION.md        # Evaluation report
    └── FILE_ORGANIZATION.md       # This file
```

## Key Changes Made

### 1. Namespace Standardization
- **Changed from:** `system` namespace
- **Changed to:** `mbcas-system` namespace
- **Files updated:**
  - `config/rbac/service_account.yaml`
  - `config/rbac/role_binding.yaml`
  - `config/agent/rbac.yaml`
  - `config/agent/daemonset.yaml`

### 2. Service Account Naming
- **Controller:** Changed from `controller-manager` to `mbcas-controller` for consistency
- **Agent:** Already using `mbcas-agent` (no change needed)
- **Files updated:**
  - `config/rbac/service_account.yaml`
  - `config/rbac/role_binding.yaml`

### 3. Agent DaemonSet Security Context Fix
- **Fixed:** Security context to allow cgroup access
- **Changes:**
  - `runAsNonRoot: false`
  - `runAsUser: 0` (root)
  - Added `SYS_ADMIN` capability
  - `readOnlyRootFilesystem: true` (for security)
- **File:** `config/agent/daemonset.yaml`

### 4. New Files Added
- **Controller Deployment:** `config/controller/deployment.yaml` (was missing)
- **Namespace Manifest:** `config/namespace.yaml`
- **Dockerfiles:** `Dockerfile.controller`, `Dockerfile.agent` (in root)
- **Deployment Scripts:** `scripts/mvp-test.sh`, `scripts/kind-config.yaml`
- **Documentation:** `docs/GPT.GUIDE.md`, `docs/MBCAS_EVALUATION.md`

## Validation Checklist

✅ All files moved to proper directories
✅ Namespace references standardized to `mbcas-system`
✅ Service account names consistent (`mbcas-controller`, `mbcas-agent`)
✅ Agent DaemonSet security context fixed for cgroup access
✅ Controller deployment manifest created
✅ Script paths updated to reflect new file locations
✅ Dockerfiles reference correct build paths (`./cmd/controller`, `./cmd/agent`)
✅ Documentation references updated

## Deployment Readiness

The project is now ready for deployment with:
- ✅ Complete deployment manifests
- ✅ Fixed security contexts
- ✅ Consistent namespace and naming
- ✅ Build scripts and Dockerfiles
- ✅ End-to-end test script

## Next Steps

1. ✅ `go.sum` exists (verified)
2. Test deployment using `scripts/mvp-test.sh`
3. Verify feature gate is enabled: The script now checks Kubernetes version (more reliable than `kubectl api-resources | grep resize`)
4. **Optional**: Uncomment and set Kind node image version in `scripts/kind-config.yaml` for reproducibility

