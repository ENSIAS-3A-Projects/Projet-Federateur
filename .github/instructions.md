```markdown
# MBCAS MVP Implementation Guide

This document provides step-by-step instructions for evolving the current MBCAS codebase into a demo-ready MVP. Follow the phases in order. Do not skip Phase 1.

---

## Prerequisites

Before starting, verify your environment:

```
go version                    # Requires 1.21+
kubectl version --client      # Requires 1.27+
minikube version              # Any recent version
docker --version              # Required for builds
```

Ensure you can build and deploy the current system:

```
.\scripts\01-start-cluster.ps1
.\scripts\02-deploy-mbcas.ps1
```

If either fails, fix those issues first. This guide assumes the baseline system works.

---

## Phase 1: Minimal Fixes

These fixes address critical weaknesses that would cause demo failures or silent misbehavior.

### 1.1 Cgroup Validation at Startup

**Problem**: Agent starts successfully even when cgroup detection is completely broken. It then reports zero demand for all pods indefinitely.

**Solution**: Validate cgroup access during agent initialization. Fail fast with a clear error if detection does not work.

**File**: `pkg/agent/cgroup/reader.go`

**Step 1**: Add a validation function after the Reader struct definition.

```go
// ValidateAccess attempts to detect cgroups for at least one running container.
// Returns nil if cgroup detection is working, error otherwise.
// This should be called at agent startup to fail fast if the environment is broken.
func (r *Reader) ValidateAccess() error {
    // Check that the base path exists and is readable
    entries, err := os.ReadDir(CgroupV2BasePath)
    if err != nil {
        return fmt.Errorf("cannot read cgroup base path %s: %w", CgroupV2BasePath, err)
    }

    // Look for kubepods hierarchy
    hasKubepods := false
    for _, entry := range entries {
        if strings.HasPrefix(entry.Name(), "kubepods") {
            hasKubepods = true
            break
        }
    }

    if !hasKubepods {
        return fmt.Errorf("no kubepods cgroup hierarchy found in %s; found: %v", 
            CgroupV2BasePath, directoryNames(entries))
    }

    // Try to find at least one cpu.stat file in the kubepods hierarchy
    pattern := filepath.Join(CgroupV2BasePath, "kubepods*", "**", CPUStatFile)
    matches, err := filepath.Glob(pattern)
    if err != nil {
        // Glob errors are rare; usually means invalid pattern
        klog.V(4).InfoS("Glob pattern failed, trying alternative", "error", err)
    }

    if len(matches) == 0 {
        // Try deeper pattern
        pattern = filepath.Join(CgroupV2BasePath, "kubepods*", "*", "*", CPUStatFile)
        matches, _ = filepath.Glob(pattern)
    }

    if len(matches) == 0 {
        return fmt.Errorf("no cpu.stat files found in kubepods hierarchy; cgroup detection will not work")
    }

    klog.InfoS("Cgroup validation passed", "cpuStatFilesFound", len(matches))
    return nil
}

func directoryNames(entries []os.DirEntry) []string {
    names := make([]string, 0, len(entries))
    for _, e := range entries {
        if e.IsDir() {
            names = append(names, e.Name())
        }
    }
    return names
}
```

**Step 2**: Call validation from agent initialization.

**File**: `pkg/agent/agent.go`

In the `NewAgent` function, after creating the cgroup reader, add:

```go
cgroupReader, err := cgroup.NewReader()
if err != nil {
    cancel()
    return nil, fmt.Errorf("create cgroup reader: %w", err)
}

// Validate cgroup access before proceeding
if err := cgroupReader.ValidateAccess(); err != nil {
    cancel()
    return nil, fmt.Errorf("cgroup validation failed: %w", err)
}
```

**Verification**: 

1. Deploy agent to a working cluster. It should start normally.
2. Modify the CgroupV2BasePath constant temporarily to a non-existent path. Agent should fail with clear error.
3. Restore the constant.

---

### 1.2 Restart Resilience

**Problem**: When agent restarts, all demand history is lost. First write cycle may scale down pods that actually need their current allocation.

**Solution**: During a startup grace period, do not decrease any allocation below its current value.

**File**: `pkg/agent/agent.go`

**Step 1**: Add startup state tracking to the Agent struct.

```go
type Agent struct {
    // ... existing fields ...

    // Startup grace period tracking
    startTime       time.Time
    gracePeriodDone bool
}
```

**Step 2**: Add a constant for the grace period duration.

```go
const (
    // ... existing constants ...

    // StartupGracePeriod is the duration after startup during which
    // allocations are only allowed to increase, not decrease.
    // This prevents incorrect scale-downs when demand history is lost.
    StartupGracePeriod = 45 * time.Second
)
```

**Step 3**: Initialize startTime in NewAgent.

```go
return &Agent{
    // ... existing fields ...
    startTime:       time.Now(),
    gracePeriodDone: false,
}, nil
```

**Step 4**: Add a method to check grace period status.

```go
// isInGracePeriod returns true if the agent is still in the startup grace period.
// During this period, allocations should only increase, not decrease.
func (a *Agent) isInGracePeriod() bool {
    if a.gracePeriodDone {
        return false
    }
    
    if time.Since(a.startTime) > StartupGracePeriod {
        a.gracePeriodDone = true
        klog.InfoS("Startup grace period ended", "duration", StartupGracePeriod)
        return false
    }
    
    return true
}
```

**Step 5**: Modify writeAllocations to respect grace period.

In the `writeAllocations` function, after computing allocations but before writing, add grace period logic:

```go
// Write allocations
for uid, cpuAllocation := range allocations {
    pod, exists := podMap[uid]
    if !exists {
        klog.V(4).InfoS("Pod not found, skipping allocation", "podUID", uid)
        continue
    }

    // Grace period protection: do not decrease allocations during startup
    if a.isInGracePeriod() {
        currentLimit, err := extractCurrentCPULimit(pod)
        if err == nil {
            currentMilli := parseCPUToMilli(currentLimit)
            newMilli := parseCPUToMilli(cpuAllocation)
            
            if newMilli < currentMilli {
                klog.InfoS("Grace period: preventing allocation decrease",
                    "pod", pod.Name,
                    "namespace", pod.Namespace,
                    "current", currentLimit,
                    "computed", cpuAllocation,
                    "using", currentLimit)
                cpuAllocation = currentLimit
            }
        }
    }

    // Check if change is significant
    // ... rest of existing code ...
}
```

**Step 6**: Add helper functions.

```go
// extractCurrentCPULimit gets the current CPU limit from pod spec.
func extractCurrentCPULimit(pod *corev1.Pod) (string, error) {
    if len(pod.Spec.Containers) == 0 {
        return "", fmt.Errorf("pod has no containers")
    }
    
    container := pod.Spec.Containers[0]
    if limit, ok := container.Resources.Limits[corev1.ResourceCPU]; ok {
        return limit.String(), nil
    }
    
    return "", fmt.Errorf("no CPU limit set")
}

// parseCPUToMilli converts a CPU string like "500m" or "1" to millicores.
func parseCPUToMilli(cpu string) int64 {
    qty, err := resource.ParseQuantity(cpu)
    if err != nil {
        return 0
    }
    return qty.MilliValue()
}
```

**Verification**:

1. Deploy agent. Let it run for 60 seconds with some pods.
2. Note current allocations.
3. Restart agent (delete pod, let DaemonSet recreate).
4. Check logs for "Grace period: preventing allocation decrease" messages.
5. Verify allocations did not decrease during first 45 seconds.

---

### 1.3 Explicit Cgroup Failure Logging

**Problem**: Cgroup read failures are silent. Demand is set to zero, which looks like "no demand" rather than "broken measurement."

**Solution**: Track consecutive failures per pod. Log warnings. Only reset demand after sustained failures.

**File**: `pkg/agent/demand/tracker.go`

**Step 1**: Add failure tracking to the Tracker struct.

```go
type Tracker struct {
    smoothed      float64
    zeroCount     int
    
    // Failure tracking
    consecutiveFailures int
    lastFailureTime     time.Time
    totalFailures       int64
}

const (
    // MaxConsecutiveFailures before demand is forced to zero
    MaxConsecutiveFailures = 3
)
```

**Step 2**: Add methods for failure handling.

```go
// RecordFailure records a cgroup read failure.
// Returns true if demand should be treated as zero (sustained failure).
func (t *Tracker) RecordFailure() bool {
    t.consecutiveFailures++
    t.totalFailures++
    t.lastFailureTime = time.Now()
    
    return t.consecutiveFailures >= MaxConsecutiveFailures
}

// RecordSuccess resets the failure counter on successful read.
func (t *Tracker) RecordSuccess() {
    t.consecutiveFailures = 0
}

// FailureStats returns failure statistics for observability.
func (t *Tracker) FailureStats() (consecutive int, total int64) {
    return t.consecutiveFailures, t.totalFailures
}
```

**File**: `pkg/agent/agent.go`

**Step 3**: Modify the sample function to use failure tracking.

Replace the current cgroup reading block in `sample()`:

```go
// Read demand signal from cgroup
rawDemand, err := a.cgroupReader.ReadPodDemand(pod)

a.mu.Lock()
tracker, exists := a.podDemands[pod.UID]
if !exists {
    tracker = demand.NewTracker()
    a.podDemands[pod.UID] = tracker
}

if err != nil {
    // Track failure
    shouldZero := tracker.RecordFailure()
    consecutive, total := tracker.FailureStats()
    
    if shouldZero {
        klog.WarningS("Sustained cgroup read failure, treating demand as zero",
            "pod", pod.Name,
            "namespace", pod.Namespace,
            "consecutiveFailures", consecutive,
            "totalFailures", total,
            "error", err)
        rawDemand = 0.0
    } else {
        klog.V(2).InfoS("Transient cgroup read failure, retaining previous demand",
            "pod", pod.Name,
            "namespace", pod.Namespace,
            "consecutiveFailures", consecutive,
            "error", err)
        // Keep previous smoothed value by not updating
        a.mu.Unlock()
        continue
    }
} else {
    tracker.RecordSuccess()
}

smoothedDemand := tracker.Update(rawDemand)
a.mu.Unlock()
```

**Verification**:

1. Temporarily break cgroup detection for one pod (e.g., add exclusion in findPodCgroupPath).
2. Watch agent logs for warning messages about sustained failures.
3. Verify transient failures (1-2) do not immediately zero demand.
4. Verify sustained failures (3+) do zero demand with clear warning.
5. Remove the artificial break.

---

### 1.4 Health Endpoint

**Problem**: No way to verify agent health without examining logs or cluster state.

**Solution**: Add HTTP health endpoint that reports operational status.

**File**: `pkg/agent/health.go` (new file)

```go
package agent

import (
    "encoding/json"
    "fmt"
    "net/http"
    "sync"
    "time"

    "k8s.io/klog/v2"
)

// HealthStatus represents the agent's health state.
type HealthStatus struct {
    Healthy            bool      `json:"healthy"`
    CgroupDetection    string    `json:"cgroupDetection"`
    PodsTracked        int       `json:"podsTracked"`
    LastSampleTime     time.Time `json:"lastSampleTime"`
    LastWriteTime      time.Time `json:"lastWriteTime"`
    SamplesSinceStart  int64     `json:"samplesSinceStart"`
    WritesSinceStart   int64     `json:"writesSinceStart"`
    StartTime          time.Time `json:"startTime"`
    Uptime             string    `json:"uptime"`
    InGracePeriod      bool      `json:"inGracePeriod"`
}

// HealthServer provides HTTP health endpoints.
type HealthServer struct {
    agent *Agent
    mu    sync.RWMutex
    
    // Counters updated by agent
    lastSampleTime    time.Time
    lastWriteTime     time.Time
    sampleCount       int64
    writeCount        int64
    cgroupStatus      string
}

// NewHealthServer creates a health server for the given agent.
func NewHealthServer(agent *Agent) *HealthServer {
    return &HealthServer{
        agent:        agent,
        cgroupStatus: "unknown",
    }
}

// RecordSample records a successful sample cycle.
func (h *HealthServer) RecordSample() {
    h.mu.Lock()
    defer h.mu.Unlock()
    h.lastSampleTime = time.Now()
    h.sampleCount++
}

// RecordWrite records a successful write cycle.
func (h *HealthServer) RecordWrite() {
    h.mu.Lock()
    defer h.mu.Unlock()
    h.lastWriteTime = time.Now()
    h.writeCount++
}

// SetCgroupStatus sets the cgroup detection status.
func (h *HealthServer) SetCgroupStatus(status string) {
    h.mu.Lock()
    defer h.mu.Unlock()
    h.cgroupStatus = status
}

// GetStatus returns the current health status.
func (h *HealthServer) GetStatus() HealthStatus {
    h.mu.RLock()
    defer h.mu.RUnlock()
    
    h.agent.mu.RLock()
    podsTracked := len(h.agent.podDemands)
    h.agent.mu.RUnlock()
    
    healthy := true
    
    // Unhealthy if no samples in 10 seconds
    if time.Since(h.lastSampleTime) > 10*time.Second && h.sampleCount > 0 {
        healthy = false
    }
    
    // Unhealthy if cgroup detection failed
    if h.cgroupStatus != "ok" {
        healthy = false
    }
    
    return HealthStatus{
        Healthy:           healthy,
        CgroupDetection:   h.cgroupStatus,
        PodsTracked:       podsTracked,
        LastSampleTime:    h.lastSampleTime,
        LastWriteTime:     h.lastWriteTime,
        SamplesSinceStart: h.sampleCount,
        WritesSinceStart:  h.writeCount,
        StartTime:         h.agent.startTime,
        Uptime:            time.Since(h.agent.startTime).Round(time.Second).String(),
        InGracePeriod:     h.agent.isInGracePeriod(),
    }
}

// ServeHTTP handles health check requests.
func (h *HealthServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    status := h.GetStatus()
    
    w.Header().Set("Content-Type", "application/json")
    
    if status.Healthy {
        w.WriteHeader(http.StatusOK)
    } else {
        w.WriteHeader(http.StatusServiceUnavailable)
    }
    
    json.NewEncoder(w).Encode(status)
}

// Start starts the health server on the given port.
func (h *HealthServer) Start(port int) {
    mux := http.NewServeMux()
    mux.Handle("/healthz", h)
    mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
        status := h.GetStatus()
        if status.Healthy && status.SamplesSinceStart > 0 {
            w.WriteHeader(http.StatusOK)
            w.Write([]byte("ready"))
        } else {
            w.WriteHeader(http.StatusServiceUnavailable)
            w.Write([]byte("not ready"))
        }
    })
    
    addr := fmt.Sprintf(":%d", port)
    klog.InfoS("Starting health server", "address", addr)
    
    go func() {
        if err := http.ListenAndServe(addr, mux); err != nil {
            klog.ErrorS(err, "Health server failed")
        }
    }()
}
```

**File**: `pkg/agent/agent.go`

**Step 4**: Add health server to Agent struct and initialization.

```go
type Agent struct {
    // ... existing fields ...
    
    healthServer *HealthServer
}
```

In `NewAgent`, after creating all components:

```go
agent := &Agent{
    // ... existing initialization ...
}

agent.healthServer = NewHealthServer(agent)
return agent, nil
```

**Step 5**: Start health server and record events.

In the `Run` method:

```go
func (a *Agent) Run() error {
    klog.InfoS("Starting node agent", "node", a.nodeName)
    
    // Start health server
    a.healthServer.SetCgroupStatus("ok")
    a.healthServer.Start(8082)
    
    // ... rest of existing code ...
}
```

In `samplingLoop`, after successful sample:

```go
if err := a.sample(); err != nil {
    klog.ErrorS(err, "Sampling error")
} else {
    a.healthServer.RecordSample()
}
```

In `writingLoop`, after successful write:

```go
if err := a.writeAllocations(); err != nil {
    klog.ErrorS(err, "Writing allocations error")
} else {
    a.healthServer.RecordWrite()
}
```

**File**: `config/agent/daemonset.yaml`

**Step 6**: Add health probes to DaemonSet.

Add to the container spec:

```yaml
ports:
- name: health
  containerPort: 8082
  protocol: TCP
livenessProbe:
  httpGet:
    path: /healthz
    port: health
  initialDelaySeconds: 10
  periodSeconds: 10
readinessProbe:
  httpGet:
    path: /readyz
    port: health
  initialDelaySeconds: 5
  periodSeconds: 5
```

**Verification**:

1. Deploy updated agent.
2. Port-forward to agent pod: `kubectl port-forward -n mbcas-system ds/mbcas-agent 8082:8082`
3. Check health: `curl http://localhost:8082/healthz`
4. Verify JSON response with expected fields.
5. Check readiness: `curl http://localhost:8082/readyz`

---

### Phase 1 Completion Checklist

Before proceeding to Phase 2, verify:

- [ ] Agent fails to start if cgroup detection is broken
- [ ] Agent logs clear error message on cgroup validation failure
- [ ] Agent does not decrease allocations during first 45 seconds after restart
- [ ] Agent logs warning on sustained cgroup read failures (3+ consecutive)
- [ ] Agent health endpoint returns valid JSON
- [ ] Agent health endpoint returns 503 when unhealthy
- [ ] DaemonSet health probes work correctly

Run the full demo script sequence to verify nothing is broken:

```
.\scripts\cleanup.ps1 -All
.\scripts\01-start-cluster.ps1
.\scripts\02-deploy-mbcas.ps1
.\scripts\03-deploy-urbanmove.ps1
.\scripts\04-generate-load.ps1 -Duration 60
.\scripts\05-monitor-scaling.ps1 -Count 12
```

---

## Phase 2: Game Theory Clarity

These changes make the game-theoretic model explicit and defensible.

### 2.1 Theory Documentation

**File**: `THEORY.md` (new file in project root)

Create this file with the following content:

```markdown
# MBCAS Game-Theoretic Model

This document defines the formal game-theoretic model implemented by MBCAS.
Each concept maps to specific code locations.

## Overview

MBCAS implements a resource allocation mechanism based on the Nash Bargaining
Solution. The mechanism allocates CPU resources fairly among competing pods
based on observed demand signals.

## Formal Definitions

### Agents

Each pod is an agent. Agents do not make strategic choices; their behavior
is observed through kernel measurements.

**Code location**: `pkg/agent/agent.go` - `discoverPods()` identifies agents

### State (Demand Signal)

Each agent has an observable state: the demand signal d_i ∈ [0,1].

This signal is derived from cgroup throttling:
- d_i = 0: Pod is not being throttled (no unmet demand)
- d_i = 1: Pod is severely throttled (maximum unmet demand)

The signal is smoothed using exponential moving average to reduce noise.

**Code location**: 
- `pkg/agent/cgroup/reader.go` - `ReadPodDemand()` measures raw throttling
- `pkg/agent/demand/tracker.go` - `Update()` applies EMA smoothing

### Strategy Space (Implicit)

The strategy space is implicit. A pod's "strategy" is its CPU consumption
pattern. The mechanism observes consequences (throttling) not intentions.

Key property: Pods cannot misrepresent demand. Throttling is measured by the
kernel, not reported by the pod. This provides **truthfulness by construction**.

### Need Estimation

Each agent's need N_i is estimated from its demand signal:

N_i = min(M_i + (L_i - M_i) × d_i × (1 + h(d_i)), L_i)

Where:
- M_i = minimum allocation (baseline or request)
- L_i = maximum allocation (limit or node cap)
- d_i = demand signal
- h(d_i) = 0.10 + 0.15 × d_i (adaptive headroom)

**Code location**: `pkg/allocation/market.go` - `computeNeed()`

### Utility Function

Each agent's utility is the satisfaction of its need:

U_i = min(A_i, N_i) / N_i

Where A_i is the allocation. Utility is 1.0 when allocation meets need,
proportionally less otherwise.

### Social Welfare

The mechanism maximizes the Nash Social Welfare (NSW):

NSW = Π_i (A_i - M_i)

This is equivalent to maximizing Σ_i log(A_i - M_i), which produces
proportional fairness.

**Code location**: `pkg/allocation/market.go` - `nashReduce()`

### Constraint

The capacity constraint is:

Σ_i A_i ≤ C

Where C is the available node CPU capacity (after system reserve).

**Code location**: `pkg/allocation/market.go` - `ClearMarket()`

## Allocation Modes

### Uncongested Mode

When Σ_i N_i ≤ C:
- Each agent receives exactly its need: A_i = N_i
- This is trivially Pareto efficient
- NSW is maximized (no trade-offs required)

### Congested Mode

When Σ_i N_i > C but Σ_i M_i ≤ C:
- Surplus demand (N_i - M_i) is reduced proportionally
- Reduction ratio: r = (C - Σ_i M_i) / Σ_i (N_i - M_i)
- Allocation: A_i = M_i + r × (N_i - M_i)
- This is the Nash Bargaining Solution

### Overloaded Mode

When Σ_i M_i > C:
- Baselines are scaled proportionally: A_i = M_i × (C / Σ_i M_i)
- This is emergency mode indicating over-commitment
- Fairness is preserved (equal percentage reduction)

## Equilibrium Properties

### Existence

An allocation always exists. The mechanism is deterministic and complete.

### Uniqueness

The allocation is unique for given inputs. No randomness is involved.

### Pareto Efficiency

In all modes, the allocation is Pareto efficient. No reallocation can
improve one agent without harming another.

### Proportional Fairness

Under congestion, surplus is reduced proportionally. Agents with larger
surplus demands lose more in absolute terms but the same percentage.

### Truthfulness

Agents cannot benefit from misrepresenting demand because demand is
measured, not declared. The kernel provides honest signals.

## Convergence

The system converges to equilibrium through:

1. EMA smoothing (α_up = 0.3, α_down = 0.2)
2. Write interval (15 seconds)
3. Controller cooldown (10 seconds)
4. Hysteresis threshold (2% minimum change)

Bounded oscillation is guaranteed by these rate-limiting mechanisms.

## Code-to-Theory Mapping

| Concept | Code Location |
|---------|---------------|
| Agent identification | `pkg/agent/agent.go:discoverPods()` |
| Demand measurement | `pkg/agent/cgroup/reader.go:ReadPodDemand()` |
| Demand smoothing | `pkg/agent/demand/tracker.go:Update()` |
| Need estimation | `pkg/allocation/market.go:computeNeed()` |
| Capacity constraint | `pkg/allocation/market.go:ClearMarket()` |
| Nash bargaining | `pkg/allocation/market.go:nashReduce()` |
| Baseline scaling | `pkg/allocation/market.go:scaleBaselines()` |
| Mode detection | `pkg/allocation/market.go:ClearMarketWithMetadata()` |
```

**Verification**: Review this document with your thesis advisor. Ensure every claim maps to code.

---

### 2.2 Model Metrics

**File**: `pkg/agent/metrics.go` (new file)

```go
package agent

import (
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"
)

var (
    // Demand signal metrics
    metricDemandRaw = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Namespace: "mbcas",
            Name:      "demand_raw",
            Help:      "Raw demand signal from cgroup throttling [0,1]",
        },
        []string{"namespace", "pod"},
    )

    metricDemandSmoothed = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Namespace: "mbcas",
            Name:      "demand_smoothed",
            Help:      "Smoothed demand signal after EMA [0,1]",
        },
        []string{"namespace", "pod"},
    )

    // Allocation metrics
    metricAllocationMilli = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Namespace: "mbcas",
            Name:      "allocation_millicores",
            Help:      "CPU allocation in millicores",
        },
        []string{"namespace", "pod"},
    )

    metricNeedMilli = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Namespace: "mbcas",
            Name:      "need_millicores",
            Help:      "Estimated CPU need in millicores",
        },
        []string{"namespace", "pod"},
    )

    // Utility metric
    metricUtility = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Namespace: "mbcas",
            Name:      "utility",
            Help:      "Pod utility (satisfaction ratio) [0,1]",
        },
        []string{"namespace", "pod"},
    )

    // System-wide metrics
    metricAllocationMode = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Namespace: "mbcas",
            Name:      "allocation_mode",
            Help:      "Current allocation mode: 0=uncongested, 1=congested, 2=overloaded",
        },
        []string{"node"},
    )

    metricNashProduct = promauto.NewGauge(
        prometheus.GaugeOpts{
            Namespace: "mbcas",
            Name:      "nash_product_log",
            Help:      "Log of Nash product (sum of log(allocation - baseline))",
        },
    )

    metricTotalNeed = promauto.NewGauge(
        prometheus.GaugeOpts{
            Namespace: "mbcas",
            Name:      "total_need_millicores",
            Help:      "Total CPU need across all pods in millicores",
        },
    )

    metricTotalAllocation = promauto.NewGauge(
        prometheus.GaugeOpts{
            Namespace: "mbcas",
            Name:      "total_allocation_millicores",
            Help:      "Total CPU allocation across all pods in millicores",
        },
    )

    metricCapacity = promauto.NewGauge(
        prometheus.GaugeOpts{
            Namespace: "mbcas",
            Name:      "capacity_millicores",
            Help:      "Available CPU capacity in millicores",
        },
    )
)

// RecordDemand records demand metrics for a pod.
func RecordDemand(namespace, pod string, raw, smoothed float64) {
    metricDemandRaw.WithLabelValues(namespace, pod).Set(raw)
    metricDemandSmoothed.WithLabelValues(namespace, pod).Set(smoothed)
}

// RecordAllocation records allocation metrics for a pod.
func RecordAllocation(namespace, pod string, allocationMilli, needMilli int64) {
    metricAllocationMilli.WithLabelValues(namespace, pod).Set(float64(allocationMilli))
    metricNeedMilli.WithLabelValues(namespace, pod).Set(float64(needMilli))
    
    // Compute utility
    if needMilli > 0 {
        utility := float64(allocationMilli) / float64(needMilli)
        if utility > 1.0 {
            utility = 1.0
        }
        metricUtility.WithLabelValues(namespace, pod).Set(utility)
    }
}

// RecordSystemMetrics records system-wide allocation metrics.
func RecordSystemMetrics(node string, mode int, nashProductLog float64, 
    totalNeed, totalAlloc, capacity int64) {
    metricAllocationMode.WithLabelValues(node).Set(float64(mode))
    metricNashProduct.Set(nashProductLog)
    metricTotalNeed.Set(float64(totalNeed))
    metricTotalAllocation.Set(float64(totalAlloc))
    metricCapacity.Set(float64(capacity))
}

// ClearPodMetrics removes metrics for a pod that no longer exists.
func ClearPodMetrics(namespace, pod string) {
    metricDemandRaw.DeleteLabelValues(namespace, pod)
    metricDemandSmoothed.DeleteLabelValues(namespace, pod)
    metricAllocationMilli.DeleteLabelValues(namespace, pod)
    metricNeedMilli.DeleteLabelValues(namespace, pod)
    metricUtility.DeleteLabelValues(namespace, pod)
}
```

**File**: `pkg/agent/agent.go`

**Step 2**: Add metrics recording to sample and write loops.

In the `sample` function, after computing smoothed demand:

```go
smoothedDemand := tracker.Update(rawDemand)
a.mu.Unlock()

// Record metrics
RecordDemand(pod.Namespace, pod.Name, rawDemand, smoothedDemand)
```

**File**: `pkg/allocation/market.go`

**Step 3**: Expose need values from allocation for metrics.

Add to `AllocationResult`:

```go
type AllocationResult struct {
    Allocations map[types.UID]int64
    Mode        AllocationMode
    TotalNeed   int64
    TotalAlloc  int64
    Needs       map[types.UID]int64  // Add this field
}
```

In `ClearMarketWithMetadata`, populate the Needs map:

```go
return AllocationResult{
    Allocations: allocs,
    Mode:        ModeUncongested,
    TotalNeed:   totalNeed,
    TotalAlloc:  totalNeed,
    Needs:       needs,  // Add this
}
```

Do the same for congested and overloaded returns.

**File**: `pkg/agent/agent.go`

**Step 4**: Record system metrics after allocation.

In `writeAllocations`, after calling `ClearMarket`:

```go
// Use ClearMarketWithMetadata instead of ClearMarket
result := allocation.ClearMarketWithMetadata(availableMilli, podParams)

// Compute Nash product (log form for numerical stability)
nashProductLog := 0.0
for uid, alloc := range result.Allocations {
    baseline := podParams[uid].MinMilli
    surplus := alloc - baseline
    if surplus > 0 {
        nashProductLog += math.Log(float64(surplus))
    }
}

// Map mode to integer
modeInt := 0
switch result.Mode {
case allocation.ModeUncongested:
    modeInt = 0
case allocation.ModeCongested:
    modeInt = 1
case allocation.ModeOverloaded:
    modeInt = 2
}

RecordSystemMetrics(a.nodeName, modeInt, nashProductLog,
    result.TotalNeed, result.TotalAlloc, availableMilli)

// Record per-pod allocation metrics
for uid, alloc := range result.Allocations {
    pod := podMap[uid]
    if pod != nil {
        need := result.Needs[uid]
        RecordAllocation(pod.Namespace, pod.Name, alloc, need)
    }
}
```

**Step 5**: Add metrics endpoint to agent.

**File**: `pkg/agent/agent.go`

Add import:

```go
import (
    // ... existing imports ...
    "github.com/prometheus/client_golang/prometheus/promhttp"
)
```

In the health server Start function or separately:

```go
// Add to HealthServer.Start or create separate function
mux.Handle("/metrics", promhttp.Handler())
```

**File**: `go.mod`

Add dependency:

```
go get github.com/prometheus/client_golang
```

**Verification**:

1. Deploy updated agent.
2. Port-forward: `kubectl port-forward -n mbcas-system ds/mbcas-agent 8082:8082`
3. Fetch metrics: `curl http://localhost:8082/metrics | grep mbcas_`
4. Verify all expected metrics are present.
5. Generate load on a pod, verify demand and allocation metrics change.

---

### 2.3 Algorithm Verification

This step verifies the code matches the documented theory.

**Checklist**:

1. Open `THEORY.md` and `pkg/allocation/market.go` side by side.

2. Verify `computeNeed()`:
   - Confirm formula matches: N_i = M_i + (L_i - M_i) × d_i + headroom
   - Confirm headroom formula: 0.10 + 0.15 × d_i
   - Confirm bounds clamping

3. Verify `nashReduce()`:
   - Confirm baseline preservation
   - Confirm proportional surplus reduction
   - Confirm ratio calculation: (C - Σ M_i) / Σ (N_i - M_i)

4. Verify `scaleBaselines()`:
   - Confirm proportional scaling: A_i = M_i × (C / Σ M_i)
   - Confirm absolute minimum enforcement

5. Verify mode selection in `ClearMarketWithMetadata()`:
   - Confirm overloaded check: Σ M_i > C
   - Confirm congested check: Σ N_i > C
   - Confirm uncongested is the default

**Add inline comments** to each function referencing THEORY.md:

```go
// computeNeed estimates CPU required to eliminate throttling.
// See THEORY.md section "Need Estimation" for formal definition.
// Formula: N_i = min(M_i + (L_i - M_i) × d_i × (1 + h(d_i)), L_i)
func computeNeed(p PodParams) int64 {
    // ... existing code with added comments ...
}
```

---

### 2.4 Decision Logging

**File**: `pkg/agent/agent.go`

Add structured decision logging in `writeAllocations`:

```go
// Log allocation decision for each pod
for uid, cpuAllocation := range allocations {
    pod, exists := podMap[uid]
    if !exists {
        continue
    }
    
    params := podParams[uid]
    need := result.Needs[uid]
    
    klog.InfoS("Allocation decision",
        "pod", pod.Name,
        "namespace", pod.Namespace,
        "demand", fmt.Sprintf("%.3f", params.Demand),
        "weight", params.Weight,
        "min", params.MinMilli,
        "max", params.MaxMilli,
        "need", need,
        "allocation", cpuAllocation,
        "mode", result.Mode,
    )
}
```

Add mode transition logging:

```go
// Track previous mode for transition logging
var previousMode allocation.AllocationMode

// In writeAllocations, after computing result:
if result.Mode != previousMode {
    klog.InfoS("Allocation mode changed",
        "previous", previousMode,
        "current", result.Mode,
        "totalNeed", result.TotalNeed,
        "capacity", availableMilli,
    )
    previousMode = result.Mode
}
```

Move `previousMode` to Agent struct to persist across calls.

**Verification**:

1. Deploy updated agent.
2. Watch logs: `kubectl logs -n mbcas-system ds/mbcas-agent -f`
3. Generate load, verify "Allocation decision" entries appear with all fields.
4. Verify mode transitions are logged when they occur.

---

### Phase 2 Completion Checklist

- [ ] THEORY.md exists and defines all concepts
- [ ] Each code function references THEORY.md in comments
- [ ] All metrics are exported and visible at /metrics endpoint
- [ ] Allocation decisions are logged with full context
- [ ] Mode transitions are logged
- [ ] Algorithm implementation matches documented formulas

---

## Phase 3: Demo Polish

### 3.1 Dashboard

**File**: `pkg/agent/dashboard.go` (new file)

```go
package agent

import (
    "embed"
    "html/template"
    "net/http"
)

//go:embed dashboard.html
var dashboardHTML embed.FS

// DashboardHandler serves the monitoring dashboard.
type DashboardHandler struct {
    agent *Agent
}

func NewDashboardHandler(agent *Agent) *DashboardHandler {
    return &DashboardHandler{agent: agent}
}

func (d *DashboardHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    tmpl, err := template.ParseFS(dashboardHTML, "dashboard.html")
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    
    w.Header().Set("Content-Type", "text/html")
    tmpl.Execute(w, nil)
}
```

**File**: `pkg/agent/dashboard.html` (new file)

```html
<!DOCTYPE html>
<html>
<head>
    <title>MBCAS Dashboard</title>
    <style>
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
            margin: 0;
            padding: 20px;
            background: #1a1a2e;
            color: #eee;
        }
        .warning-banner {
            background: #ff6b6b;
            color: white;
            padding: 10px 20px;
            margin: -20px -20px 20px -20px;
            font-weight: bold;
            text-align: center;
        }
        h1 {
            color: #00d9ff;
            margin-bottom: 5px;
        }
        .subtitle {
            color: #888;
            margin-bottom: 20px;
        }
        .metrics-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
            gap: 15px;
            margin-bottom: 30px;
        }
        .metric-card {
            background: #16213e;
            border-radius: 8px;
            padding: 15px;
            text-align: center;
        }
        .metric-value {
            font-size: 2em;
            font-weight: bold;
            color: #00d9ff;
        }
        .metric-label {
            color: #888;
            font-size: 0.9em;
        }
        .mode-uncongested { color: #4ade80; }
        .mode-congested { color: #fbbf24; }
        .mode-overloaded { color: #f87171; }
        table {
            width: 100%;
            border-collapse: collapse;
            background: #16213e;
            border-radius: 8px;
            overflow: hidden;
        }
        th, td {
            padding: 12px;
            text-align: left;
            border-bottom: 1px solid #2a2a4a;
        }
        th {
            background: #0f3460;
            color: #00d9ff;
        }
        tr:hover {
            background: #1a1a3e;
        }
        .bar-container {
            background: #2a2a4a;
            border-radius: 4px;
            height: 20px;
            position: relative;
        }
        .bar-fill {
            height: 100%;
            border-radius: 4px;
            transition: width 0.3s;
        }
        .bar-allocation { background: #00d9ff; }
        .bar-need { background: #4ade80; opacity: 0.5; position: absolute; top: 0; }
        #lastUpdate {
            color: #888;
            font-size: 0.9em;
            margin-top: 20px;
        }
    </style>
</head>
<body>
    <div class="warning-banner">
        ⚠️ MBCAS is a demonstration system. Do not use in production.
    </div>
    
    <h1>MBCAS Dashboard</h1>
    <p class="subtitle">Market-Based CPU Allocation System</p>
    
    <div class="metrics-grid">
        <div class="metric-card">
            <div class="metric-value" id="modeDisplay">-</div>
            <div class="metric-label">Allocation Mode</div>
        </div>
        <div class="metric-card">
            <div class="metric-value" id="podsTracked">-</div>
            <div class="metric-label">Pods Tracked</div>
        </div>
        <div class="metric-card">
            <div class="metric-value" id="totalAlloc">-</div>
            <div class="metric-label">Total Allocation</div>
        </div>
        <div class="metric-card">
            <div class="metric-value" id="capacity">-</div>
            <div class="metric-label">Available Capacity</div>
        </div>
        <div class="metric-card">
            <div class="metric-value" id="nashProduct">-</div>
            <div class="metric-label">Nash Product (log)</div>
        </div>
    </div>
    
    <h2>Pod Allocations</h2>
    <table>
        <thead>
            <tr>
                <th>Namespace</th>
                <th>Pod</th>
                <th>Demand</th>
                <th>Need (m)</th>
                <th>Allocation (m)</th>
                <th>Utility</th>
                <th>Allocation vs Need</th>
            </tr>
        </thead>
        <tbody id="podTable">
            <tr><td colspan="7">Loading...</td></tr>
        </tbody>
    </table>
    
    <p id="lastUpdate">Last update: never</p>
    
    <script>
        async function fetchMetrics() {
            try {
                const resp = await fetch('/metrics');
                const text = await resp.text();
                return parsePrometheus(text);
            } catch (e) {
                console.error('Failed to fetch metrics:', e);
                return null;
            }
        }
        
        function parsePrometheus(text) {
            const metrics = {};
            const lines = text.split('\n');
            
            for (const line of lines) {
                if (line.startsWith('#') || !line.trim()) continue;
                
                const match = line.match(/^(\w+)(\{[^}]+\})?\s+([\d.e+-]+)$/);
                if (match) {
                    const name = match[1];
                    const labels = match[2] || '';
                    const value = parseFloat(match[3]);
                    
                    if (!metrics[name]) metrics[name] = [];
                    
                    const labelObj = {};
                    if (labels) {
                        const pairs = labels.slice(1, -1).split(',');
                        for (const pair of pairs) {
                            const [k, v] = pair.split('=');
                            labelObj[k] = v.replace(/"/g, '');
                        }
                    }
                    
                    metrics[name].push({ labels: labelObj, value });
                }
            }
            
            return metrics;
        }
        
        function getMetricValue(metrics, name, labels = {}) {
            const m = metrics[name];
            if (!m) return null;
            
            for (const entry of m) {
                let match = true;
                for (const [k, v] of Object.entries(labels)) {
                    if (entry.labels[k] !== v) {
                        match = false;
                        break;
                    }
                }
                if (match) return entry.value;
            }
            return null;
        }
        
        function getAllWithLabels(metrics, name) {
            return metrics[name] || [];
        }
        
        async function update() {
            const metrics = await fetchMetrics();
            if (!metrics) return;
            
            // System metrics
            const mode = getMetricValue(metrics, 'mbcas_allocation_mode');
            const modeNames = ['Uncongested', 'Congested', 'Overloaded'];
            const modeClasses = ['mode-uncongested', 'mode-congested', 'mode-overloaded'];
            
            const modeDisplay = document.getElementById('modeDisplay');
            modeDisplay.textContent = modeNames[mode] || '-';
            modeDisplay.className = 'metric-value ' + (modeClasses[mode] || '');
            
            document.getElementById('totalAlloc').textContent = 
                Math.round(getMetricValue(metrics, 'mbcas_total_allocation_millicores') || 0) + 'm';
            document.getElementById('capacity').textContent = 
                Math.round(getMetricValue(metrics, 'mbcas_capacity_millicores') || 0) + 'm';
            document.getElementById('nashProduct').textContent = 
                (getMetricValue(metrics, 'mbcas_nash_product_log') || 0).toFixed(2);
            
            // Pod table
            const allocations = getAllWithLabels(metrics, 'mbcas_allocation_millicores');
            const needs = getAllWithLabels(metrics, 'mbcas_need_millicores');
            const demands = getAllWithLabels(metrics, 'mbcas_demand_smoothed');
            const utilities = getAllWithLabels(metrics, 'mbcas_utility');
            
            document.getElementById('podsTracked').textContent = allocations.length;
            
            const tbody = document.getElementById('podTable');
            
            if (allocations.length === 0) {
                tbody.innerHTML = '<tr><td colspan="7">No pods tracked</td></tr>';
            } else {
                const rows = [];
                const maxAlloc = Math.max(...allocations.map(a => a.value), 1);
                
                for (const alloc of allocations) {
                    const ns = alloc.labels.namespace;
                    const pod = alloc.labels.pod;
                    
                    const need = needs.find(n => 
                        n.labels.namespace === ns && n.labels.pod === pod)?.value || 0;
                    const demand = demands.find(d => 
                        d.labels.namespace === ns && d.labels.pod === pod)?.value || 0;
                    const utility = utilities.find(u => 
                        u.labels.namespace === ns && u.labels.pod === pod)?.value || 0;
                    
                    const allocPct = (alloc.value / maxAlloc * 100).toFixed(0);
                    const needPct = (need / maxAlloc * 100).toFixed(0);
                    
                    rows.push(`
                        <tr>
                            <td>${ns}</td>
                            <td>${pod.substring(0, 30)}</td>
                            <td>${demand.toFixed(3)}</td>
                            <td>${Math.round(need)}</td>
                            <td>${Math.round(alloc.value)}</td>
                            <td>${utility.toFixed(2)}</td>
                            <td>
                                <div class="bar-container">
                                    <div class="bar-fill bar-need" style="width: ${needPct}%"></div>
                                    <div class="bar-fill bar-allocation" style="width: ${allocPct}%"></div>
                                </div>
                            </td>
                        </tr>
                    `);
                }
                
                tbody.innerHTML = rows.join('');
            }
            
            document.getElementById('lastUpdate').textContent = 
                'Last update: ' + new Date().toLocaleTimeString();
        }
        
        // Initial update and periodic refresh
        update();
        setInterval(update, 2000);
    </script>
</body>
</html>
```

**File**: `pkg/agent/health.go`

Add dashboard to the HTTP server:

```go
func (h *HealthServer) Start(port int) {
    mux := http.NewServeMux()
    mux.Handle("/healthz", h)
    mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
        // ... existing code ...
    })
    mux.Handle("/metrics", promhttp.Handler())
    mux.Handle("/", NewDashboardHandler(h.agent))  // Add this line
    
    // ... rest of existing code ...
}
```

**Verification**:

1. Deploy updated agent.
2. Port-forward: `kubectl port-forward -n mbcas-system ds/mbcas-agent 8082:8082`
3. Open browser to http://localhost:8082/
4. Verify dashboard loads and shows pod data.
5. Generate load, verify metrics update in real-time.

---

### 3.2 Before/After Comparison Script

**File**: `scripts/06-compare-metrics.ps1` (new file)

```powershell
# MBCAS Demo Script 06 - Before/After Comparison
# Captures metrics before and after MBCAS enablement for comparison
#
# Usage: .\scripts\06-compare-metrics.ps1 [-Duration 120]

param(
    [int]$Duration = 120,
    [string]$OutputDir = ".\demo-results"
)

$ErrorActionPreference = "Stop"
$NAMESPACE = "urbanmove"

function Write-Info { Write-Host "[INFO] $args" -ForegroundColor Green }
function Write-Warn { Write-Host "[WARN] $args" -ForegroundColor Yellow }

# Create output directory
if (-not (Test-Path $OutputDir)) {
    New-Item -ItemType Directory -Path $OutputDir -Force | Out-Null
}

$timestamp = Get-Date -Format "yyyyMMdd-HHmmss"
$beforeFile = Join-Path $OutputDir "before-$timestamp.json"
$afterFile = Join-Path $OutputDir "after-$timestamp.json"
$reportFile = Join-Path $OutputDir "comparison-$timestamp.txt"

function Capture-Metrics {
    param([string]$Label)
    
    Write-Info "Capturing $Label metrics..."
    
    $metrics = @{
        timestamp = (Get-Date).ToString("o")
        label = $Label
        pods = @()
    }
    
    # Get pod resource usage
    $topOutput = kubectl top pods -n $NAMESPACE --no-headers 2>$null
    if ($topOutput) {
        foreach ($line in $topOutput) {
            $parts = $line -split '\s+'
            if ($parts.Count -ge 3) {
                $metrics.pods += @{
                    name = $parts[0]
                    cpuUsage = $parts[1]
                    memUsage = $parts[2]
                }
            }
        }
    }
    
    # Get pod specs
    $podJson = kubectl get pods -n $NAMESPACE -o json 2>$null | ConvertFrom-Json
    foreach ($pod in $podJson.items) {
        $existing = $metrics.pods | Where-Object { $_.name -eq $pod.metadata.name }
        if ($existing) {
            $container = $pod.spec.containers[0]
            $existing.cpuRequest = $container.resources.requests.cpu
            $existing.cpuLimit = $container.resources.limits.cpu
        }
    }
    
    # Get throttling info from MBCAS if available
    $allocations = kubectl get podallocations -n $NAMESPACE -o json 2>$null | ConvertFrom-Json
    if ($allocations.items) {
        foreach ($pa in $allocations.items) {
            $existing = $metrics.pods | Where-Object { $_.name -eq $pa.spec.podName }
            if ($existing) {
                $existing.desiredCPU = $pa.spec.desiredCPULimit
                $existing.appliedCPU = $pa.status.appliedCPULimit
                $existing.phase = $pa.status.phase
            }
        }
    }
    
    return $metrics
}

function Calculate-Stats {
    param($Metrics)
    
    $stats = @{
        totalCpuUsage = 0
        totalCpuLimit = 0
        podsThrottled = 0
        avgUtilization = 0
    }
    
    foreach ($pod in $Metrics.pods) {
        $usage = [int]($pod.cpuUsage -replace 'm', '')
        $limit = [int]($pod.cpuLimit -replace 'm', '')
        
        $stats.totalCpuUsage += $usage
        $stats.totalCpuLimit += $limit
        
        if ($limit -gt 0 -and $usage -ge ($limit * 0.9)) {
            $stats.podsThrottled++
        }
    }
    
    if ($stats.totalCpuLimit -gt 0) {
        $stats.avgUtilization = [math]::Round($stats.totalCpuUsage / $stats.totalCpuLimit * 100, 1)
    }
    
    return $stats
}

function Generate-Report {
    param($Before, $After, $BeforeStats, $AfterStats)
    
    $report = @"
================================================================================
                     MBCAS DEMO - BEFORE/AFTER COMPARISON
================================================================================

Generated: $(Get-Date)
Namespace: $NAMESPACE
Test Duration: $Duration seconds

--------------------------------------------------------------------------------
                              SUMMARY METRICS
--------------------------------------------------------------------------------

                              BEFORE          AFTER           CHANGE
Total CPU Usage:              $($BeforeStats.totalCpuUsage)m            $($AfterStats.totalCpuUsage)m            $(($AfterStats.totalCpuUsage - $BeforeStats.totalCpuUsage))m
Total CPU Limit:              $($BeforeStats.totalCpuLimit)m            $($AfterStats.totalCpuLimit)m            $(($AfterStats.totalCpuLimit - $BeforeStats.totalCpuLimit))m
Pods Near Throttle (>90%):    $($BeforeStats.podsThrottled)               $($AfterStats.podsThrottled)               $(($AfterStats.podsThrottled - $BeforeStats.podsThrottled))
Avg Utilization:              $($BeforeStats.avgUtilization)%            $($AfterStats.avgUtilization)%            $(($AfterStats.avgUtilization - $BeforeStats.avgUtilization))%

--------------------------------------------------------------------------------
                              PER-POD DETAILS
--------------------------------------------------------------------------------

"@
    
    $report += "`nBEFORE (Static Limits):`n"
    $report += "-" * 80 + "`n"
    $report += "{0,-35} {1,10} {2,10} {3,10}`n" -f "POD", "USAGE", "LIMIT", "UTIL%"
    
    foreach ($pod in $Before.pods) {
        $usage = [int]($pod.cpuUsage -replace 'm', '')
        $limit = [int]($pod.cpuLimit -replace 'm', '')
        $util = if ($limit -gt 0) { [math]::Round($usage / $limit * 100, 1) } else { 0 }
        $report += "{0,-35} {1,10} {2,10} {3,10}`n" -f $pod.name.Substring(0, [Math]::Min(35, $pod.name.Length)), "$($usage)m", "$($limit)m", "$util%"
    }
    
    $report += "`nAFTER (MBCAS Dynamic Limits):`n"
    $report += "-" * 80 + "`n"
    $report += "{0,-35} {1,10} {2,10} {3,10} {4,10}`n" -f "POD", "USAGE", "LIMIT", "UTIL%", "MBCAS"
    
    foreach ($pod in $After.pods) {
        $usage = [int]($pod.cpuUsage -replace 'm', '')
        $limit = [int]($pod.cpuLimit -replace 'm', '')
        $util = if ($limit -gt 0) { [math]::Round($usage / $limit * 100, 1) } else { 0 }
        $mbcas = if ($pod.appliedCPU) { $pod.appliedCPU } else { "-" }
        $report += "{0,-35} {1,10} {2,10} {3,10} {4,10}`n" -f $pod.name.Substring(0, [Math]::Min(35, $pod.name.Length)), "$($usage)m", "$($limit)m", "$util%", $mbcas
    }
    
    $report += @"

--------------------------------------------------------------------------------
                              INTERPRETATION
--------------------------------------------------------------------------------

$(if ($AfterStats.podsThrottled -lt $BeforeStats.podsThrottled) { "✓ IMPROVEMENT: Fewer pods at throttle threshold ($($BeforeStats.podsThrottled) → $($AfterStats.podsThrottled))" } else { "○ No change in throttling" })

$(if ($AfterStats.totalCpuLimit -ne $BeforeStats.totalCpuLimit) { "✓ MBCAS adjusted total limits ($($BeforeStats.totalCpuLimit)m → $($AfterStats.totalCpuLimit)m)" } else { "○ Limits unchanged" })

$(if ($After.pods | Where-Object { $_.phase -eq "Applied" }) { "✓ MBCAS actively managing $(($After.pods | Where-Object { $_.phase -eq 'Applied' }).Count) pods" } else { "○ MBCAS not yet applied allocations" })

================================================================================
                                  END REPORT
================================================================================
"@
    
    return $report
}

# Main execution
Write-Host ""
Write-Host "MBCAS Before/After Comparison" -ForegroundColor Cyan
Write-Host "=============================" -ForegroundColor Cyan
Write-Host ""

# Phase 1: Disable MBCAS, capture baseline
Write-Info "Phase 1: Capturing BEFORE metrics (static Kubernetes limits)"
Write-Info "Scaling down MBCAS controller..."
kubectl scale deployment mbcas-controller -n mbcas-system --replicas=0 2>$null

Write-Info "Waiting for metrics to stabilize (30s)..."
Start-Sleep -Seconds 30

$beforeMetrics = Capture-Metrics -Label "before"
$beforeMetrics | ConvertTo-Json -Depth 5 | Set-Content $beforeFile
Write-Info "Saved to $beforeFile"

# Phase 2: Enable MBCAS
Write-Info "Phase 2: Enabling MBCAS..."
kubectl scale deployment mbcas-controller -n mbcas-system --replicas=1 2>$null

Write-Info "Waiting for MBCAS to converge ($Duration seconds)..."
Start-Sleep -Seconds $Duration

# Phase 3: Capture after metrics
Write-Info "Phase 3: Capturing AFTER metrics (MBCAS dynamic limits)"
$afterMetrics = Capture-Metrics -Label "after"
$afterMetrics | ConvertTo-Json -Depth 5 | Set-Content $afterFile
Write-Info "Saved to $afterFile"

# Generate comparison
$beforeStats = Calculate-Stats -Metrics $beforeMetrics
$afterStats = Calculate-Stats -Metrics $afterMetrics

$report = Generate-Report -Before $beforeMetrics -After $afterMetrics -BeforeStats $beforeStats -AfterStats $afterStats

$report | Set-Content $reportFile
Write-Host ""
Write-Host $report
Write-Host ""
Write-Info "Report saved to $reportFile"
```

**Verification**:

1. Deploy full demo stack.
2. Run comparison script: `.\scripts\06-compare-metrics.ps1 -Duration 60`
3. Review generated report in demo-results directory.

---

### 3.3 Demo Workload Manifests

**File**: `k8s/demo/namespace.yaml`

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: demo-workloads
  labels:
    mbcas.io/managed: "true"
```

**File**: `k8s/demo/service-a-overprovisioned.yaml`

```yaml
# Service A: Over-provisioned
# Requests 500m, uses ~100m
# Demonstrates wasted reservation
apiVersion: apps/v1
kind: Deployment
metadata:
  name: service-a-overprovisioned
  namespace: demo-workloads
spec:
  replicas: 1
  selector:
    matchLabels:
      app: service-a
  template:
    metadata:
      labels:
        app: service-a
        demo-role: overprovisioned
    spec:
      containers:
      - name: workload
        image: busybox:1.36
        command: ["sh", "-c", "while true; do echo 'light work'; sleep 1; done"]
        resources:
          requests:
            cpu: "500m"
            memory: "64Mi"
          limits:
            cpu: "1000m"
            memory: "128Mi"
```

**File**: `k8s/demo/service-b-underprovisioned.yaml`

```yaml
# Service B: Under-provisioned
# Requests 100m, limit 200m, wants 400m
# Demonstrates throttling victim
apiVersion: apps/v1
kind: Deployment
metadata:
  name: service-b-underprovisioned
  namespace: demo-workloads
spec:
  replicas: 1
  selector:
    matchLabels:
      app: service-b
  template:
    metadata:
      labels:
        app: service-b
        demo-role: underprovisioned
    spec:
      containers:
      - name: workload
        image: busybox:1.36
        command: ["sh", "-c", "while true; do dd if=/dev/zero bs=1M count=10 2>/dev/null | md5sum > /dev/null; done"]
        resources:
          requests:
            cpu: "100m"
            memory: "64Mi"
          limits:
            cpu: "200m"
            memory: "128Mi"
```

**File**: `k8s/demo/service-c-bursty.yaml`

```yaml
# Service C: Bursty workload
# Usually idle, periodic bursts
# Demonstrates adaptive allocation
apiVersion: apps/v1
kind: Deployment
metadata:
  name: service-c-bursty
  namespace: demo-workloads
spec:
  replicas: 1
  selector:
    matchLabels:
      app: service-c
  template:
    metadata:
      labels:
        app: service-c
        demo-role: bursty
    spec:
      containers:
      - name: workload
        image: busybox:1.36
        # 10 seconds idle, 5 seconds burst, repeat
        command: ["sh", "-c", "while true; do sleep 10; timeout 5 sh -c 'while true; do :; done'; done"]
        resources:
          requests:
            cpu: "200m"
            memory: "64Mi"
          limits:
            cpu: "600m"
            memory: "128Mi"
```

**File**: `k8s/demo/service-d-wellconfigured.yaml`

```yaml
# Service D: Well-configured (control)
# Requests = Limits = Usage
# Demonstrates baseline behavior
apiVersion: apps/v1
kind: Deployment
metadata:
  name: service-d-control
  namespace: demo-workloads
spec:
  replicas: 1
  selector:
    matchLabels:
      app: service-d
  template:
    metadata:
      labels:
        app: service-d
        demo-role: control
    spec:
      containers:
      - name: workload
        image: busybox:1.36
        # Steady 300m usage
        command: ["sh", "-c", "while true; do timeout 0.3 sh -c 'while true; do :; done'; sleep 0.7; done"]
        resources:
          requests:
            cpu: "300m"
            memory: "64Mi"
          limits:
            cpu: "300m"
            memory: "128Mi"
```

**File**: `k8s/demo/kustomization.yaml`

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- namespace.yaml
- service-a-overprovisioned.yaml
- service-b-underprovisioned.yaml
- service-c-bursty.yaml
- service-d-wellconfigured.yaml
```

**File**: `scripts/07-run-demo.ps1` (new file)

```powershell
# MBCAS Demo Script 07 - Full Demo Run
# Deploys demo workloads and walks through the demonstration
#
# Usage: .\scripts\07-run-demo.ps1

$ErrorActionPreference = "Stop"

function Write-Info { Write-Host "[INFO] $args" -ForegroundColor Green }
function Write-Step { Write-Host "`n>> $args" -ForegroundColor Cyan }
function Pause-Demo { 
    Write-Host "`nPress Enter to continue..." -ForegroundColor Yellow
    Read-Host 
}

Write-Host @"

================================================================================
                         MBCAS DEMONSTRATION
================================================================================

This demo shows how MBCAS dynamically adjusts CPU limits based on actual demand,
compared to static Kubernetes resource limits.

Scenario:
  - Service A: Over-provisioned (wastes resources)
  - Service B: Under-provisioned (throttled)
  - Service C: Bursty workload (variable demand)
  - Service D: Well-configured (control group)

================================================================================

"@ -ForegroundColor White

Pause-Demo

# Step 1: Deploy workloads
Write-Step "STEP 1: Deploying demo workloads"
kubectl apply -k k8s/demo/
Write-Info "Waiting for pods to start (30s)..."
Start-Sleep -Seconds 30

kubectl get pods -n demo-workloads -o wide
Pause-Demo

# Step 2: Show initial state
Write-Step "STEP 2: Observing initial state (static limits)"
Write-Host "Notice: Service B is being throttled (high CPU usage vs limit)"
Write-Host ""

kubectl top pods -n demo-workloads 2>$null
Write-Host ""
kubectl get pods -n demo-workloads -o custom-columns="NAME:.metadata.name,REQUEST:.spec.containers[0].resources.requests.cpu,LIMIT:.spec.containers[0].resources.limits.cpu"
Pause-Demo

# Step 3: Enable MBCAS
Write-Step "STEP 3: Enabling MBCAS"
Write-Info "MBCAS agent is already running and observing pods..."
Write-Info "Checking PodAllocations..."

kubectl get podallocations -n demo-workloads -o wide 2>$null
if ($LASTEXITCODE -ne 0) {
    Write-Info "No allocations yet - waiting for agent to discover pods..."
    Start-Sleep -Seconds 20
    kubectl get podallocations -n demo-workloads -o wide
}
Pause-Demo

# Step 4: Wait for convergence
Write-Step "STEP 4: Waiting for MBCAS to converge (60s)"
Write-Info "Watch the allocations adjust based on demand signals..."
Write-Host ""

for ($i = 1; $i -le 6; $i++) {
    Write-Host "--- Iteration $i/6 ---" -ForegroundColor Gray
    kubectl get podallocations -n demo-workloads -o custom-columns="POD:.spec.podName,DESIRED:.spec.desiredCPULimit,APPLIED:.status.appliedCPULimit,PHASE:.status.phase" 2>$null
    Write-Host ""
    Start-Sleep -Seconds 10
}
Pause-Demo

# Step 5: Compare
Write-Step "STEP 5: Comparing results"
Write-Host "Current CPU usage:" -ForegroundColor White
kubectl top pods -n demo-workloads 2>$null

Write-Host "`nCurrent limits (adjusted by MBCAS):" -ForegroundColor White
kubectl get pods -n demo-workloads -o custom-columns="NAME:.metadata.name,LIMIT:.spec.containers[0].resources.limits.cpu"

Write-Host "`nMBCAS allocations:" -ForegroundColor White
kubectl get podallocations -n demo-workloads -o wide
Pause-Demo

# Step 6: Inject burst
Write-Step "STEP 6: Injecting burst load on Service C"
Write-Info "Triggering sustained CPU burst..."

$pod = kubectl get pods -n demo-workloads -l app=service-c -o jsonpath='{.items[0].metadata.name}'
kubectl exec -n demo-workloads $pod -- sh -c "timeout 30 sh -c 'while true; do :; done'" &

Write-Info "Burst running for 30 seconds. Watching MBCAS respond..."
for ($i = 1; $i -le 6; $i++) {
    Start-Sleep -Seconds 5
    Write-Host "--- Second $($i*5) ---" -ForegroundColor Gray
    kubectl get podallocations -n demo-workloads -l app=service-c -o custom-columns="POD:.spec.podName,DESIRED:.spec.desiredCPULimit,PHASE:.status.phase" 2>$null
}
Pause-Demo

# Step 7: Summary
Write-Step "STEP 7: Demo Summary"
Write-Host @"

================================================================================
                         DEMONSTRATION COMPLETE
================================================================================

Key observations (fill from what you just saw above):

1. Service A (over-provisioned): Did MBCAS pull back the limit? How much headroom freed?
2. Service B (under-provisioned): Did throttling clear after allocations applied? What limit did it settle on?
3. Service C (bursty): How quickly did allocations rise during burst and decay after?
4. Service D (control): Did it stay near its configured limit (minimal change)?

Game Theory in Action:
- Each pod's allocation is based on measured demand (truthful signal)
- Total allocation respects node capacity (constraint)
- Under contention, surplus is reduced proportionally (Nash bargaining)
- Idle pods release resources passively (collaboration without negotiation)

================================================================================

"@ -ForegroundColor White

Write-Info "Cleanup: kubectl delete -k k8s/demo/"
```

**Verification**:

1. Run full demo: `.\scripts\07-run-demo.ps1`
2. Follow prompts and observe behavior.
3. Verify all steps complete successfully.

---

### 3.4 Prominent Warnings

**File**: `cmd/agent/main.go`

Add at the start of main():

```go
func main() {
    fmt.Println("================================================================================")
    fmt.Println("  MBCAS - Market-Based CPU Allocation System")
    fmt.Println("  WARNING: This is a demonstration system. Do not use in production.")
    fmt.Println("================================================================================")
    fmt.Println()
    
    // ... rest of existing code ...
}
```

**File**: `cmd/controller/main.go`

Add at the start of main():

```go
func main() {
    fmt.Println("================================================================================")
    fmt.Println("  MBCAS Controller")
    fmt.Println("  WARNING: This is a demonstration system. Do not use in production.")
    fmt.Println("================================================================================")
    fmt.Println()
    
    // ... rest of existing code ...
}
```

**File**: `pkg/allocation/market.go`

Add package-level comment:

```go
// Package allocation implements demand-capped CPU allocation.
//
// WARNING: This is a demonstration implementation. It has not been
// hardened for production use. See THEORY.md for the formal model
// and README.md for usage limitations.
//
// ...existing comment...
```

---

### Phase 3 Completion Checklist

- [ ] Dashboard loads at http://localhost:8082/
- [ ] Dashboard shows real-time metrics and pod allocations
- [ ] Dashboard displays warning banner
- [ ] Comparison script generates before/after report
- [ ] Demo workload manifests deploy successfully
- [ ] Full demo script runs without errors
- [ ] Startup warnings appear in agent and controller logs

---

## Final Verification

Run the complete demo sequence:

```powershell
# Clean start
.\scripts\cleanup.ps1 -All

# Create cluster
.\scripts\01-start-cluster.ps1

# Deploy MBCAS
.\scripts\02-deploy-mbcas.ps1

# Run demo
.\scripts\07-run-demo.ps1

# Generate comparison report
.\scripts\06-compare-metrics.ps1 -Duration 60

# View dashboard
kubectl port-forward -n mbcas-system ds/mbcas-agent 8082:8082
# Open http://localhost:8082 in browser

# Cleanup
.\scripts\cleanup.ps1 -All
```

If all steps complete successfully, the MVP is ready for demonstration.

---

## Out of Scope

The following items are explicitly not included in this MVP:

1. PSI (Pressure Stall Information) integration
2. Multi-node coordination or cluster-level fairness
3. PriorityClass-based weighting
4. Runtime configuration (all parameters are compile-time constants)
5. Production security hardening
6. Memory resource management
7. Informer-based pod discovery
8. Horizontal pod autoscaling integration
9. Admission webhook for resource validation
10. Long-term storage of allocation history

These may be addressed in future work but are not necessary for demonstrating the core game-theoretic allocation mechanism.
```