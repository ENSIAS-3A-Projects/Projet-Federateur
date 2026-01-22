# MBCAS Architecture

This document describes the complete system architecture of MBCAS, from kernel metrics to Kubernetes API updates.

---

## System Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                         Kubernetes Cluster                       │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │                    Control Plane                           │ │
│  │  ┌──────────────────┐         ┌────────────────────────┐  │ │
│  │  │   API Server     │ ◄─────► │  MBCAS Controller      │  │ │
│  │  │  (PodAllocation  │         │  (Deployment)          │  │ │
│  │  │   CRD)           │         │  - Watches CRDs        │  │ │
│  │  └──────────────────┘         │  - Patches Pods        │  │ │
│  │                                └────────────────────────┘  │ │
│  └────────────────────────────────────────────────────────────┘ │
│                                                                  │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │                    Worker Nodes                            │ │
│  │  ┌──────────────────────────────────────────────────────┐ │ │
│  │  │  MBCAS Agent (DaemonSet)                             │ │ │
│  │  │  ┌────────────┐  ┌────────────┐  ┌────────────────┐ │ │ │
│  │  │  │  Discover  │→ │    Sync    │→ │  Bid (ABM)     │ │ │ │
│  │  │  │   Pods     │  │  PodAgents │  │  Q-Learning    │ │ │ │
│  │  │  └────────────┘  └────────────┘  └────────┬───────┘ │ │ │
│  │  │                                            ▼         │ │ │
│  │  │  ┌────────────┐  ┌──────────────────────────────┐  │ │ │
│  │  │  │    Act     │◄ │  Bargain (Game Theory)       │  │ │ │
│  │  │  │   Write    │  │  Nash Bargaining Solution    │  │ │ │
│  │  │  └─────┬──────┘  └──────────────────────────────┘  │ │ │
│  │  └────────┼─────────────────────────────────────────┘ │ │
│  │           │                                             │ │
│  │           ▼                                             │ │
│  │  ┌──────────────────────────────────────────────────┐  │ │
│  │  │  Cgroup v2 Filesystem                            │  │ │
│  │  │  /sys/fs/cgroup/kubepods.slice/...               │  │ │
│  │  │  - cpu.stat (throttled_usec, usage_usec)         │  │ │
│  │  └──────────────────────────────────────────────────┘  │ │
│  │                                                         │ │
│  │  ┌──────────────────────────────────────────────────┐  │ │
│  │  │  Pods (Managed Workloads)                        │  │ │
│  │  │  - CPU limits updated in-place                   │  │ │
│  │  │  - No restarts required                          │  │ │
│  │  └──────────────────────────────────────────────────┘  │ │
│  └─────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────────┘
```

---

## Component Details

### 1. MBCAS Agent (Node-Level)

**Location**: `pkg/agent/agent.go` (~600 lines)  
**Deployment**: DaemonSet (one per node)  
**Responsibilities**:
- Discover pods on the node using Kubernetes informer
- Maintain PodAgent instances with Q-Learning
- Orchestrate dual-loop allocation pipeline
- Write PodAllocation CRDs with shadow price feedback
- Persist Q-tables for continuous learning

#### Dual-Loop Architecture

The agent runs two independent loops for responsive and efficient allocation:

**Fast Loop** (`FastStep()`):
```go
func (a *Agent) FastStep() {
    // Runs every FastLoopInterval (default 2s)
    // 1. Check each pod for throttling/SLO violations
    // 2. If threshold exceeded, increase allocation immediately
    // 3. Only increases (never decreases) - emergency response
    // 4. Bypasses Nash solver for speed
}
```

**Slow Loop** (`SlowStep()`):
```go
func (a *Agent) SlowStep() {
    // Runs every SlowLoopInterval (default 15s)
    // Calls Step() for full optimization
}

func (a *Agent) Step() {
    // 1. Discover pods using informer cache
    pods := a.podInformer.ListPods()
    
    // 2. Sync PodAgents (create new, remove dead, load Q-tables)
    a.syncAgents(pods)
    
    // 3. Collect bids with shadow price feedback (two-pass)
    bids, shadowPrice := a.collectBids(pods)
    
    // 4. Solve Nash Bargaining with shadow price
    resultWithPrice := allocation.NashBargainWithPrice(capacity, bids)
    
    // 5. Write allocations with shadow price
    a.apply(pods, resultWithPrice.Allocations, resultWithPrice.ShadowPrice)
}
```

**Timing**:
- Fast loop: `FastLoopInterval` (default 2s)
- Slow loop: `SlowLoopInterval` (default 15s)
- Q-table persistence: Every 30s

---

### 2. PodAgent (Autonomous Agent)

**Location**: `pkg/agent/pod_agent.go`  
**Lifecycle**: One instance per managed pod  
**Responsibilities**:
- Observe pod metrics (usage, throttling, allocation)
- Learn optimal bidding strategies via Q-Learning
- Generate bids for Nash Bargaining

#### State Machine

```
State Space (Discrete):
┌─────────────┬──────────────┬─────────────────┐
│ Usage Level │Throttle Level│Allocation Level │
├─────────────┼──────────────┼─────────────────┤
│ low         │ none         │ low             │
│ medium      │ some         │ adequate        │
│ high        │ high         │ excess          │
└─────────────┴──────────────┴─────────────────┘

Action Space:
┌─────────────┬────────────────────────────────┐
│ Action      │ Behavior                       │
├─────────────┼────────────────────────────────┤
│ aggressive  │ Bid 1.5x usage, weight 1.2x   │
│ normal      │ Bid 1.2x usage, weight 1.0x   │
│ conservative│ Bid 1.0x usage, weight 0.8x   │
└─────────────┴────────────────────────────────┘
```

#### Q-Learning Update

```
Q(s,a) ← Q(s,a) + α[r + γ·max Q(s',a') - Q(s,a)]

Where:
- α = learning rate (0.1, configurable)
- γ = discount factor (0.9, configurable)
- r = reward (based on throttling, SLO violations, oscillations)
- ε = exploration rate (0.2 initial, decays to 0.01)
```

**Q-Table Persistence**:
- Q-tables saved to ConfigMap every 30s
- Loaded on agent startup for continuous learning
- Per-pod Q-tables preserved across restarts

#### Reward Function

```go
reward = 0.0

// Positive: Meeting demand
if allocation >= usage:
    reward += 10.0
else:
    shortfall = (usage - allocation) / usage
    reward -= shortfall * 20.0

// Negative: Throttling
reward -= throttling * 30.0

// Negative: SLO violations
if sloViolation:
    reward -= 100.0

// Negative: Waste
if allocation > usage*2:
    waste = (allocation - usage*2) / usage
    reward -= waste * 5.0

// Bonus: Zero throttling
if throttling < 0.01:
    reward += 5.0

// IMPROVEMENT: Penalize oscillations
if prevAllocation > 0:
    changeRatio = abs(allocation - prevAllocation) / prevAllocation
    if changeRatio > 0.2:  // Large change
        reward -= (changeRatio - 0.2) * 10.0
    elif changeRatio < 0.05:  // Small change (stable)
        reward += 2.0
```

---

### 3. Nash Bargaining Solver with Shadow Prices

**Location**: `pkg/allocation/nash_simple.go`  
**Algorithm**: Maximizes Nash product of utilities with shadow price computation  
**Guarantees**:
- Pareto efficiency (no waste)
- Fairness (weighted by priority)
- Independence of Irrelevant Alternatives (IIA)
- Market-clearing shadow price for demand feedback

#### Algorithm Flow

```
Input: capacity, bids[]
Output: allocations[], shadowPrice

1. Compute baselines (minimum viable allocations)
   baseline_i = bid.Min

2. Check feasibility
   if Σ baseline_i > capacity:
       return scaleBaselines()  // Emergency mode

3. Compute surplus
   total_surplus = capacity - Σ baseline_i

4. Distribute surplus proportionally by weight
   surplus_i = (weight_i / Σ weight_j) * total_surplus
   
5. Handle capped agents
   if allocation_i > bid.Max:
       allocation_i = bid.Max
       redistribute leftover to others

6. Compute shadow price
   if totalDemand > capacity:
       congestionRatio = (totalDemand - capacity) / capacity
       shadowPrice = congestionRatio * (avgWeight)
   else:
       shadowPrice = 0.0  // Uncongested

7. Return allocations and shadow price
   allocation_i = baseline_i + surplus_i
```

#### Nash Product

Maximizes: `∏(allocation_i - baseline_i)^weight_i`

This ensures:
- Higher weight → more surplus
- Everyone gets at least baseline
- Product maximization = Pareto optimal

#### Shadow Price Feedback

The shadow price (Lagrange multiplier) indicates resource scarcity:
- **High shadow price (>0.3)**: Resources scarce → Agents reduce demand
- **Low shadow price (<0.3)**: Resources abundant → Agents can increase demand
- **Zero shadow price**: Uncongested → No demand adjustment needed

Agents use shadow price in two ways:
1. **Action Selection**: Adjust Q-values (penalize aggressive when price high)
2. **Demand Adjustment**: Reduce bid demand by up to 50% when price > 0.3

---

### 4. Cgroup Reader

**Location**: `pkg/agent/cgroup/reader.go`  
**Kernel Interface**: Cgroup v2 filesystem  
**Thread-Safe**: Protected by internal mutex  
**Metrics**:

```
/sys/fs/cgroup/kubepods.slice/.../pod<uid>.slice/cpu.stat

throttled_usec: Total time pod was throttled (microseconds)
usage_usec:     Total CPU time used (microseconds)
```

#### Demand Calculation

```go
// Delta between samples
deltaThrottled = current.throttled_usec - last.throttled_usec
deltaUsage = current.usage_usec - last.usage_usec
deltaTime = current.timestamp - last.timestamp

// Minimum usage threshold (1ms) to avoid numerical instability
if deltaUsage < 1000 microseconds:
    return demand = 0.0  // Invalid sample

// Calculate actual CPU usage in millicores
actualUsageMilli = (deltaUsage / 1e6) / deltaTime * 1000

// Throttling ratio
throttlingRatio = deltaThrottled / deltaUsage

// Normalize to [0,1]
demand = throttlingRatio / 0.1  // 10% throttling = 1.0 demand
demand = clamp(demand, 0.0, 1.0)
```

**Features**:
- **Retry Logic**: Exponential backoff for transient filesystem errors
- **Path Caching**: Caches discovered cgroup paths to avoid repeated glob operations
- **Sample Validation**: Minimum usage threshold prevents division by zero
- **Cleanup**: Removes samples for deleted pods

---

### 5. Controller (Cluster-Level)

**Location**: `pkg/controller/podallocation_controller.go`  
**Deployment**: Single replica Deployment  
**Responsibilities**:
- Watch PodAllocation CRDs
- Patch pod CPU limits/requests via Kubernetes API (in-place resize)
- Handle resize status updates and verification
- Multi-container pod support with proportional allocation
- Safety checks (cooldown, step size limits)

#### Reconciliation Loop

```go
func (c *Controller) Reconcile(req Request) {
    // 1. Get PodAllocation
    alloc := c.client.Get(req.Name)
    
    // 2. Get target Pod
    pod := c.client.Get(alloc.Spec.PodName)
    
    // 3. Check exclusions (managed label, Guaranteed QoS)
    if isExcluded(pod) || shouldSkipGuaranteed(pod):
        return markApplied(alloc)
    
    // 4. Extract current CPU from pod
    currentLimit := extractCPULimit(pod)
    currentRequest := extractCPURequest(pod)
    
    // 5. Safety checks
    if err := checkSafety(alloc, currentLimit, alloc.Spec.DesiredCPULimit):
        return markPending(alloc, err)
    
    // 6. Apply patch (in-place resize)
    if len(pod.Spec.Containers) == 1:
        // Single container: use actuator
        actuator.ApplyScalingWithResources(...)
    else:
        // Multi-container: proportional allocation
        for container in pod.Spec.Containers:
            ratio = container.originalLimit / totalOriginalLimit
            container.limit = desiredLimit * ratio
            container.request = desiredRequest * ratio
        patchAllContainers(pod, containerPatches)
    
    // 7. Verify resize was applied
    after := c.client.Get(pod)
    if verifyResize(after, alloc.Spec):
        alloc.Status.Phase = "Applied"
    else:
        alloc.Status.Phase = "Pending"  // Requeue
    
    // 8. Update status
    c.client.UpdateStatus(alloc)
}
```

**Safety Mechanisms**:
- **Cooldown**: 5s minimum between resizes per pod
- **Step Size Limit**: Maximum 10x change factor per resize
- **Absolute Delta Cap**: Maximum 20 cores change per resize
- **Verification**: Confirms kubelet actually applied the resize

---

## Data Flow

### Complete Allocation Cycle (Slow Loop)

```
Time: T=0s
┌─────────────────────────────────────────────────────────────┐
│ 1. Agent samples cgroup metrics                            │
│    Pod A: usage=800m, throttled=50ms → demand=0.5          │
│    Pod B: usage=200m, throttled=0ms  → demand=0.0          │
└─────────────────────────────────────────────────────────────┘

Time: T=0.1s
┌─────────────────────────────────────────────────────────────┐
│ 2. PodAgents compute initial bids (first pass)            │
│    Agent A: state="high:some:adequate" → action=aggressive │
│             initialBid={demand=1200m, weight=1.2, min=100m} │
│    Agent B: state="low:none:adequate" → action=conservative│
│             initialBid={demand=200m, weight=0.8, min=100m}   │
└─────────────────────────────────────────────────────────────┘

Time: T=0.2s
┌─────────────────────────────────────────────────────────────┐
│ 3. Preview Nash Bargaining to compute shadow price        │
│    Capacity: 1500m, Total Demand: 1400m                   │
│    Shadow Price: 0.15 (moderate congestion)                │
└─────────────────────────────────────────────────────────────┘

Time: T=0.3s
┌─────────────────────────────────────────────────────────────┐
│ 4. PodAgents adjust bids with shadow price feedback        │
│    Agent A: shadowPrice=0.15 → reduce demand by 7.5%      │
│             finalBid={demand=1110m, weight=1.2, min=100m}   │
│    Agent B: shadowPrice=0.15 → no adjustment (low)         │
│             finalBid={demand=200m, weight=0.8, min=100m}    │
└─────────────────────────────────────────────────────────────┘

Time: T=0.4s
┌─────────────────────────────────────────────────────────────┐
│ 5. Final Nash Bargaining solves allocation                │
│    Capacity: 1500m                                          │
│    Baselines: A=100m, B=100m → total=200m                  │
│    Surplus: 1500m - 200m = 1300m                           │
│    Weighted distribution:                                   │
│      A gets: 100m + (1.2/2.0)*1300m = 880m                 │
│      B gets: 100m + (0.8/2.0)*1300m = 620m                 │
│    Final Shadow Price: 0.12                                │
└─────────────────────────────────────────────────────────────┘

Time: T=0.5s
┌─────────────────────────────────────────────────────────────┐
│ 6. Agent applies exponential smoothing                     │
│    Pod A: lastSmoothed=800m, newAlloc=880m                 │
│           smoothed = 0.1*880 + 0.9*800 = 808m (slow up)    │
│    Pod B: lastSmoothed=600m, newAlloc=620m                 │
│           smoothed = 0.1*620 + 0.9*600 = 602m                │
└─────────────────────────────────────────────────────────────┘

Time: T=0.6s
┌─────────────────────────────────────────────────────────────┐
│ 7. Agent writes PodAllocation CRDs with shadow price      │
│    PodAllocation/pod-a: {request=727m, limit=808m,         │
│                          shadowPrice=0.12}                 │
│    PodAllocation/pod-b: {request=542m, limit=602m,         │
│                          shadowPrice=0.12}                 │
└─────────────────────────────────────────────────────────────┘

Time: T=1s
┌─────────────────────────────────────────────────────────────┐
│ 8. Controller patches pods (in-place resize)               │
│    PATCH /api/v1/pods/pod-a/resize → CPU limit=808m       │
│    PATCH /api/v1/pods/pod-b/resize → CPU limit=602m       │
└─────────────────────────────────────────────────────────────┘

Time: T=1.5s
┌─────────────────────────────────────────────────────────────┐
│ 9. Controller verifies resize was applied                  │
│    Pod A: actualLimit=808m ✓ → Status=Applied             │
│    Pod B: actualLimit=602m ✓ → Status=Applied             │
└─────────────────────────────────────────────────────────────┘

Time: T=15s (next slow cycle)
┌─────────────────────────────────────────────────────────────┐
│ 10. PodAgents observe outcomes and learn                   │
│     Agent A: throttling reduced → reward=+8.0              │
│              Q("high:some:adequate", "aggressive") += 0.8   │
│     Agent B: no change → reward=+10.0                      │
│              Q("low:none:adequate", "conservative") += 1.0  │
│     Q-tables persisted to ConfigMap                        │
└─────────────────────────────────────────────────────────────┘
```

### Fast Loop Cycle (Emergency Response)

```
Time: T=2s (fast loop interval)
┌─────────────────────────────────────────────────────────────┐
│ 1. Agent checks each pod for violations                    │
│    Pod A: throttling=0.15 > threshold(0.1) → needs boost   │
│    Pod B: throttling=0.02 < threshold → OK                 │
└─────────────────────────────────────────────────────────────┘

Time: T=2.1s
┌─────────────────────────────────────────────────────────────┐
│ 2. Fast step up (bypasses Nash solver)                    │
│    Pod A: currentAlloc=808m                                │
│           stepSize = 0.20 + 0.20*0.15 = 0.23               │
│           newAlloc = 808m * 1.23 = 994m                    │
│    Write PodAllocation immediately                         │
└─────────────────────────────────────────────────────────────┘
```

---

## Configuration

### Agent Configuration

Loaded from ConfigMap `mbcas-agent-config` with environment variable overrides:

```yaml
# Timing
samplingInterval: "1s"          # How often to read cgroups
fastLoopInterval: "2s"          # Fast SLO guardrail loop
slowLoopInterval: "15s"          # Slow optimization loop
writeInterval: "10s"             # How often to write allocations

# Resource Management
systemReservePercent: "10.0"     # Reserve for system pods
baselineCPUPerPod: "100m"        # Minimum per pod
totalClusterCPUCapacityMilli: "4000"  # Total CPU (4 cores)

# Stability Controls
minChangePercent: "5.0"          # Hysteresis threshold
startupGracePeriod: "45s"        # Grace period for new pods

# Q-Learning
agentLearningRate: "0.1"         # α (learning rate)
agentExplorationRate: "0.2"      # ε (exploration)
agentDiscountFactor: "0.9"      # γ (future reward weight)

# Fast Loop Thresholds
throttlingThreshold: "0.1"       # Throttling ratio trigger
fastStepSizeMin: "0.20"         # Min fast step (20%)
fastStepSizeMax: "0.40"          # Max fast step (40%)

# Optional Features
prometheusURL: ""                # SLO checking (empty = disabled)
costEfficiencyMode: "false"     # Aggressive cost optimization
```

### Pod Annotations

```yaml
# Opt-out
mbcas.io/managed: "false"

# Set minimum
mbcas.io/min-cpu: "500m"

# SLO target for reward function
mbcas.io/target-latency-ms: "100"
```

---

## Scalability

### Complexity Analysis

- **Agent per node**: O(n) where n = pods on node
- **Nash solver**: O(n) linear scan
- **Cgroup reads**: O(n) filesystem reads
- **API writes**: O(n) CRD creates/updates

### Resource Usage

- **Agent CPU**: ~10m per node + 0.1m per pod
- **Agent Memory**: ~50MB base + 1MB per pod
- **Controller CPU**: ~5m
- **Controller Memory**: ~30MB

### Tested Limits

- 100 pods per node: <1% CPU overhead
- 1000 pods cluster-wide: <100ms allocation time

---

## Failure Modes

### Agent Crash
- **Impact**: No new allocations on that node
- **Recovery**: DaemonSet restarts agent, loads Q-tables from ConfigMap
- **Mitigation**: Q-tables persisted every 30s, learning state preserved

### Controller Crash
- **Impact**: No pod patches applied
- **Recovery**: Deployment restarts controller, processes pending CRDs
- **Mitigation**: CRDs persist in etcd, status-based cooldown prevents duplicate work

### Cgroup Read Failure
- **Impact**: Pod skipped for that cycle
- **Recovery**: Retry with exponential backoff (3 attempts)
- **Mitigation**: Path caching, sample validation, graceful degradation

### API Server Unavailable
- **Impact**: Cannot write CRDs or patch pods
- **Recovery**: Client retries with backoff
- **Mitigation**: Local caching, informer cache, graceful degradation

### Q-Table Persistence Failure
- **Impact**: Learning state may be lost on restart
- **Recovery**: Agent continues with fresh Q-tables
- **Mitigation**: Non-blocking, retries every 30s

---

## Security

### RBAC Permissions

**Agent**:
- `get`, `list`, `watch` on Pods
- `create`, `update` on PodAllocations
- `get` on Nodes

**Controller**:
- `get`, `list`, `watch` on PodAllocations
- `patch` on Pods
- `update` on PodAllocation status

### Cgroup Access

- Agent runs as privileged (needs host cgroup access)
- Read-only access to `/sys/fs/cgroup`
- No write access to cgroups (Kubernetes manages)

---

## Observability

### Health Endpoints

**Agent** (port 8082):
- `/healthz`: Basic health check
- `/readyz`: Readiness check (waits for informer sync)
- `/metrics`: Prometheus metrics endpoint

**Controller** (port 8080):
- `/healthz`: Health check
- `/readyz`: Readiness check
- `/metrics`: Prometheus metrics endpoint

### Metrics (Prometheus)

```
# Agent metrics
mbcas_managed_pods                    # Number of pods managed
mbcas_agent_allocation_duration_seconds
mbcas_agent_cgroup_read_errors_total

# Nash solver metrics
mbcas_nash_iterations
mbcas_nash_mode{mode="uncongested|congested|overloaded"}
mbcas_nash_shadow_price               # Current shadow price

# Controller metrics
mbcas_controller_reconcile_duration_seconds
mbcas_controller_pod_patch_errors_total
mbcas_controller_resize_verified_total
```

### Logs

```bash
# Agent logs
kubectl logs -n mbcas-system -l app.kubernetes.io/component=agent

# Controller logs
kubectl logs -n mbcas-system -l app.kubernetes.io/component=controller

# View PodAllocations
kubectl get podallocations -A -o wide
```

### Debugging

```bash
# Check agent configuration
kubectl get configmap mbcas-agent-config -n mbcas-system -o yaml

# Check Q-table persistence
kubectl get configmap -n mbcas-system | grep qtable

# View PodAllocation status
kubectl describe podallocation <name> -n <namespace>
```

---

## Future Enhancements

1. **Memory Management**: Extend to memory allocation
2. **Multi-Resource**: Joint CPU+Memory Nash Bargaining
3. **Predictive**: Use Kalman filters for demand forecasting
4. **Distributed**: Multi-node coordination via price signals
5. **SLO Integration**: Prometheus latency metrics for reward function
