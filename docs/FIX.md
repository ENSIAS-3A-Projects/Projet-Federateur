**UPDATE**: Most issues identified below have been **resolved**. This document is kept for historical reference. See status notes for each item.

## What’s correct ✅

* **Dockerfile.agent / Dockerfile.controller**: sound approach (multi-stage Go build → minimal runtime image). Just make sure your repo really has `./cmd/agent` and `./cmd/controller` as the build targets.
* **kind-config.yaml**: valid Kind config. Kind **does support** the top-level `featureGates:` key. ([Kind][1])
* **namespace.yaml**: fine.

## What’s *not* correct yet (will break deployment) ❌

### 1) Namespace + ServiceAccount drift (P0) ✅ **RESOLVED**

~~Right now your manifests/RBAC are inconsistent:~~

**Status**: All manifests now use `mbcas-system` namespace consistently:
* Agent DaemonSet: `mbcas-system` + SA `mbcas-agent` ✅
* Agent RBAC: `mbcas-system` + SA `mbcas-agent` ✅
* Controller RBAC: `mbcas-system` + SA `mbcas-controller` ✅
* Controller Deployment: `mbcas-system` + SA `mbcas-controller` ✅

All namespace and service account references are now consistent.

### 2) `mvp-test.sh` has checks/paths that will fail (P0) ⚠️ **PARTIALLY RESOLVED**

**Status**: 
* ✅ Script paths updated to match repo layout (`config/agent/rbac.yaml`, `config/controller/deployment.yaml`, etc.)
* ⚠️ Feature gate check improved but still uses version check (more reliable than `api-resources | grep`)
* 💡 **Recommendation**: For production, add actual patch test as suggested below

A better "resize is available" validation is to **actually patch a test pod using**:
`kubectl patch pod ... --subresource=resize ...` (kubectl ≥ 1.32). ([Latest from Abhimanyu's Blog][2])

## “Works but should improve” ⚠️

### 3) Kind version pinning (strongly recommended) ⚠️ **PARTIALLY ADDRESSED**

In-place resize requires **Kubernetes server ≥ 1.27** (feature gate) and `kubectl` **≥ 1.32** for `--subresource=resize`. ([Kubernetes][3])

**Status**: 
* ✅ `kind-config.yaml` now includes comments about version pinning
* ⚠️ Image version is commented out (user must uncomment and set)
* 💡 **Recommendation**: Uncomment and set `image: kindest/node:v1.35.0` (or latest v1.35.x) for reproducibility

### 4) Agent securityContext is very privileged ✅ **RESOLVED (with note)**

**Status**: Agent now runs as root with `SYS_ADMIN` capability, which is required for cgroup access.

**Note**: `SYS_ADMIN` is a powerful capability. For MVP this is acceptable, but for production consider:
* Testing if root access alone (without SYS_ADMIN) is sufficient
* Using cgroup delegation if available on target Kubernetes distribution
* Documenting the security trade-off clearly

### 5) readOnlyRootFilesystem without /tmp ⚠️ **NOT YET ADDRESSED**

Both agent and controller set `readOnlyRootFilesystem: true`. Some Go libs/components may want `/tmp`.

**Status**: Not yet addressed. If you encounter issues with Go libraries requiring `/tmp`, add:
```yaml
volumeMounts:
- name: tmp
  mountPath: /tmp
volumes:
- name: tmp
  emptyDir: {}
```

**Recommendation**: Monitor for runtime errors; add `/tmp` mount if needed.

---

## Minimal “make it correct” checklist

If you want the shortest path to a working MVP:

1. Standardize on **namespace = `mbcas-system`** everywhere
2. Standardize SAs:

   * agent: `mbcas-agent`
   * controller: `mbcas-controller` (or rename deployment to `controller-manager`, but be consistent)
3. Fix rolebindings to point to the right SA + namespace
4. Pin Kind Kubernetes version (node image)
5. Fix `mvp-test.sh` to validate resize by doing a real `kubectl patch ... --subresource=resize` test (and fix manifest paths)

If you paste your *current* “authoritative” tree layout (just `ls -R` output), I can tell you exactly which filenames/paths in `mvp-test.sh` and kustomizations need to change.

[1]: https://kind.sigs.k8s.io/docs/user/configuration/ "kind – Configuration"
[2]: https://blog.abhimanyu-saharan.com/posts/inplacepodverticalscaling-scale-pods-without-restarting "InPlacePodVerticalScaling: Scale Pods Without Restarting"
[3]: https://kubernetes.io/docs/tasks/configure-pod-container/resize-container-resources/ "Resize CPU and Memory Resources assigned to Containers | Kubernetes"
