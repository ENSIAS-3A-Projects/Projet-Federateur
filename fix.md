````markdown
# FIX.md — Phase 1 Hardening Plan (Actuator + CLI)

This document proposes concrete Phase 1 fixes to make the in-place resize **actuator** and **CLI** more robust, safer, and closer to production expectations.

Phase 1 already works end-to-end (feature check → patch `pods/resize` → show before/after), but it needs hardening in three areas:

1. **Input validation** (container existence, resource quantities)
2. **Policy control** (requests vs limits, QoS implications)
3. **Operational correctness** (resize is async → optional wait + better error handling)

---

## Goals

- Fail fast with clear errors for invalid input (bad container name, invalid CPU/memory quantities).
- Allow **explicit policy** for what gets resized:
  - limits only
  - requests only
  - requests+limits (current behavior)
- Offer an optional `--wait` mode to block until resize completes (or times out).
- Improve CLI UX and error messages (RBAC vs unsupported feature vs invalid quantities).
- Add unit tests for patch generation and validation logic.

---

## Non-goals (Phase 1)

- Full controller-runtime reconciliation loop (that’s Phase 4).
- Multi-resource market solver (Phase 3).
- Guaranteeing safe memory downscales for every runtime (addressed via guardrails later).

---

## Step-by-step implementation guide

### Step 0 — Create a feature branch
1. Create a branch:
   ```bash
   git checkout -b phase1-hardening
````

2. Add this file as `FIX.md` at repo root.

---

## Step 1 — Validate container name early

**Problem:** If `-container` is wrong, the patch may fail in a confusing way.

**Action:**

1. In `pkg/actuator/actuator.go`, add a helper:

   * `resolveContainer(pod, containerName) (string, error)`
   * If `containerName` is empty: default to first container (existing behavior).
   * If provided: ensure it matches an existing container.

**Implementation sketch:**

```go
func resolveContainer(pod *corev1.Pod, name string) (string, error) {
    if len(pod.Spec.Containers) == 0 {
        return "", fmt.Errorf("pod has no containers")
    }
    if name == "" {
        return pod.Spec.Containers[0].Name, nil
    }
    for _, c := range pod.Spec.Containers {
        if c.Name == name {
            return name, nil
        }
    }
    return "", fmt.Errorf("container %q not found in pod", name)
}
```

**Acceptance criteria:**

* `k8s-pod-tool update` fails with a clear message if a container is not found.
* Default behavior (no `-container`) remains unchanged.

---

## Step 2 — Parse and validate CPU/memory quantities (preflight)

**Problem:** Invalid quantity strings fail late at the API server.

**Action:**

1. In `pkg/actuator/actuator.go`:

   * Before building the patch, validate `newCPU` and `newMem` using `resource.ParseQuantity`.
   * Normalize quantities by re-serializing them (`q.String()`).

2. Decide whether CPU/mem are optional independently (keep current behavior: both optional but at least one required).

**Implementation sketch:**

```go
import "k8s.io/apimachinery/pkg/api/resource"

func parseQuantityOrEmpty(s string) (string, error) {
    if s == "" { return "", nil }
    q, err := resource.ParseQuantity(s)
    if err != nil { return "", fmt.Errorf("invalid quantity %q: %w", s, err) }
    return q.String(), nil
}
```

**Acceptance criteria:**

* Invalid inputs like `-cpu foo` or `-memory 12MB` fail immediately with helpful messages.
* Valid inputs are normalized (e.g., `0.5` becomes `500m` if appropriate).

---

## Step 3 — Add explicit resize policy (requests vs limits)

**Problem:** Phase 1 currently sets **requests and limits** to the same value. That can change QoS class and eviction behavior unexpectedly.

**Action:**

1. Extend actuator options with a policy:

   * `ResizePolicy = "both" | "limits" | "requests"`
2. Update patch builder so it sets only the selected fields.

**Suggested API changes:**

In actuator:

```go
type ResizePolicy string
const (
    PolicyBoth    ResizePolicy = "both"
    PolicyLimits  ResizePolicy = "limits"
    PolicyRequests ResizePolicy = "requests"
)

type Options struct {
    DryRun      bool
    MaxRetries  int
    Policy      ResizePolicy // NEW: default "both"
    Wait        bool         // added in Step 4
    WaitTimeout time.Duration
    PollInterval time.Duration
}
```

Update patch generation logic:

* If `PolicyLimits`: set `resources.limits.{cpu|memory}` only
* If `PolicyRequests`: set `resources.requests.{cpu|memory}` only
* If `PolicyBoth`: set both (current behavior)

CLI changes in `pkg/podtool/update.go`:

* Add flag: `-policy` with default `both`

  ```bash
  -policy both|limits|requests
  ```

**Acceptance criteria:**

* Users can choose limits-only resizing (recommended for many clusters).
* Default behavior remains backward compatible (`both`).

---

## Step 4 — Add optional “wait for completion” mode (async correctness)

**Problem:** `pods/resize` may be accepted but not completed immediately. Fetching “after” pod does not guarantee the resize is finished.

**Action:**

1. Add `Options.Wait`, `Options.WaitTimeout`, `Options.PollInterval`.
2. After patch is accepted, poll the pod until:

   * resize is no longer “InProgress”, OR
   * timeout is reached.

**Concrete sub-steps:**

1. Find the type of `pod.Status.Resize` in your Kubernetes API version:

   * Open in module cache: `k8s.io/api/core/v1/types.go`
2. Implement `isResizeInProgress(pod) bool` based on actual fields.

**Example (adjust to real type):**

```go
func isResizeInProgress(p *corev1.Pod) bool {
    if p.Status.Resize == nil { return false }
    return p.Status.Resize.Status == corev1.PodResizeStatusInProgress
}
```

3. Poll loop:

```go
deadline := time.Now().Add(opts.WaitTimeout)
for time.Now().Before(deadline) {
    p, err := client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
    if err != nil { return before, nil, fmt.Errorf("wait: get pod: %w", err) }
    if !isResizeInProgress(p) { return before, p, nil }
    time.Sleep(opts.PollInterval)
}
return before, nil, fmt.Errorf("resize did not complete within %s", opts.WaitTimeout)
```

CLI changes:

* Add flags:

  * `-wait` (default false)
  * `-wait-timeout` (default e.g. 30s)
  * `-poll` (default e.g. 500ms)

**Acceptance criteria:**

* Without `-wait`, behavior stays fast and unchanged.
* With `-wait`, CLI returns only after resize completes or times out.
* Timeout produces a clear message but does not claim success.

---

## Step 5 — Improve error classification & operator hints

**Problem:** Not all failures are the same (RBAC, unsupported feature, invalid quantity, conflict, etc.). Current messages are helpful but can be improved.

**Action:**

1. In actuator, when patch fails, classify:

   * `StatusReasonForbidden` (RBAC)
   * `StatusReasonNotFound` (pod missing)
   * `StatusReasonInvalid` / `StatusReasonBadRequest` (bad payload or unsupported resize semantics)
   * `StatusReasonConflict` (already retried; include attempt count)

2. In CLI, print a short “next steps” hint depending on classification:

   * Forbidden → “check RBAC: need patch on pods/resize”
   * Missing resize subresource → “enable InPlacePodVerticalScaling”
   * Invalid → “verify quantities/policy and runtime support”

**Acceptance criteria:**

* Errors guide the operator to the right fix quickly.
* No misleading “feature gate missing” message when it’s actually RBAC.

---

## Step 6 — Improve dry-run usefulness (optional but recommended)

**Problem:** Dry-run currently returns only “before” and does not show what would be patched.

**Action:**

1. Export a planning function:

   * `actuator.PlanScaling(...) (before *corev1.Pod, patch []byte, err error)`
2. In CLI `-dry-run`, print the planned patch (or the computed resource changes).

**Acceptance criteria:**

* `-dry-run` shows exactly what would change without touching the API.

---

## Step 7 — Add unit tests for Phase 1

**What to test (unit-level):**

1. `resolveContainer`:

   * empty → first container
   * invalid name → error
2. `parseQuantityOrEmpty`:

   * invalid → error
   * valid → normalized string
3. Patch builder:

   * PolicyBoth sets requests+limits
   * PolicyLimits sets limits only
   * PolicyRequests sets requests only
   * CPU only, memory only, both

**Implementation approach:**

* Create `pkg/actuator/actuator_test.go`
* Build patch bytes → `json.Unmarshal` into `map[string]any` and assert fields exist / not exist.

**Acceptance criteria:**

* `go test ./...` passes locally with no cluster.
* Patch structure is validated for each policy.

---

## Step 8 — Manual integration test checklist (Minikube)

1. Start Minikube with feature gate:

   ```bash
   minikube start --feature-gates=InPlacePodVerticalScaling=true
   ```

2. Deploy a simple workload:

   ```bash
   kubectl create deployment demo --image=nginx
   kubectl scale deployment demo --replicas=1
   kubectl get pods
   ```

3. Run:

   ```bash
   ./k8s-pod-tool.exe usage -namespace default -pod <pod>
   ./k8s-pod-tool.exe update -namespace default -pod <pod> -cpu 250m -policy limits --dry-run
   ./k8s-pod-tool.exe update -namespace default -pod <pod> -cpu 250m -policy limits -wait -wait-timeout 30s
   ```

4. Verify:

   * `kubectl get pod <pod> -o yaml` reflects the changes
   * `status.resize` indicates completion (if available)

---

## Final acceptance criteria (Phase 1 “production-adjacent”)

* ✅ invalid inputs fail fast (container, CPU/mem quantities)
* ✅ explicit resizing policy supported (`both|limits|requests`)
* ✅ optional wait-for-completion works reliably
* ✅ error messages distinguish RBAC vs feature support vs invalid payload
* ✅ unit tests cover patch creation + validation
* ✅ README/CLI help updated with new flags and examples

```
::contentReference[oaicite:0]{index=0}
```
