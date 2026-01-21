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

**Location**: `pkg/agent/agent.go` (200 lines)  
**Deployment**: DaemonSet (one per node)  
**Responsibilities**:
- Discover pods on the node
- Maintain PodAgent instances
- Orchestrate the allocation pipeline
- Write PodAllocation CRDs

#### Agent Pipeline

```go
func (a *Agent) Step() {
    // 1. Discover pods using informer cache
    pods := a.podInformer.ListPods()
    
    // 2. Sync PodAgents (create new, remove dead)
    a.syncAgents(pods)
    
    // 3. Collect bids from all agents
    bids := a.collectBids(pods)
    
    // 4. Solve Nash Bargaining
    results := allocation.NashBargain(capacity, bids)
    
    // 5. Write allocations
    a.apply(pods, results)
}
```

**Timing**: Runs every `WriteInterval` (default 5s)

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
- α = learning rate (0.1)
- γ = discount factor (0.9)
- r = reward (based on throttling, SLO violations)
```

#### Reward Function

```go
reward = 0.0

// Positive: Meeting demand
if allocation >= usage:
    reward += 10.0

// Negative: Throttling
reward -= throttling * 30.0

// Negative: SLO violations
if sloViolation:
    reward -= 100.0

// Negative: Waste
if allocation > usage*2:
    reward -= waste * 5.0
```

---

### 3. Nash Bargaining Solver

**Location**: `pkg/allocation/nash_simple.go`  
**Algorithm**: Maximizes Nash product of utilities  
**Guarantees**:
- Pareto efficiency (no waste)
- Fairness (weighted by priority)
- Independence of Irrelevant Alternatives (IIA)

#### Algorithm Flow

```
Input: capacity, bids[]
Output: allocations[]

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

6. Return allocations
   allocation_i = baseline_i + surplus_i
```

#### Nash Product

Maximizes: `∏(allocation_i - baseline_i)^weight_i`

This ensures:
- Higher weight → more surplus
- Everyone gets at least baseline
- Product maximization = Pareto optimal

---

### 4. Cgroup Reader

**Location**: `pkg/agent/cgroup/reader.go`  
**Kernel Interface**: Cgroup v2 filesystem  
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

// Throttling ratio
throttlingRatio = deltaThrottled / deltaUsage

// Normalize to [0,1]
demand = throttlingRatio / 0.1  // 10% throttling = 1.0 demand
demand = clamp(demand, 0.0, 1.0)
```

---

### 5. Controller (Cluster-Level)

**Location**: `pkg/controller/podallocation_controller.go`  
**Deployment**: Single replica Deployment  
**Responsibilities**:
- Watch PodAllocation CRDs
- Patch pod CPU limits via Kubernetes API
- Handle resize status updates

#### Reconciliation Loop

```go
func (c *Controller) Reconcile(req Request) {
    // 1. Get PodAllocation
    alloc := c.client.Get(req.Name)
    
    // 2. Get target Pod
    pod := c.client.Get(alloc.Spec.PodName)
    
    // 3. Compute patch
    patch := {
        "spec": {
            "containers": [{
                "resources": {
                    "requests": {"cpu": alloc.Spec.CPURequest},
                    "limits": {"cpu": alloc.Spec.CPULimit}
                }
            }]
        }
    }
    
    // 4. Apply patch (in-place resize)
    c.client.Patch(pod, patch)
    
    // 5. Update status
    alloc.Status.Phase = "Applied"
    c.client.UpdateStatus(alloc)
}
```

---

## Data Flow

### Complete Allocation Cycle

```
Time: T=0s
┌─────────────────────────────────────────────────────────────┐
│ 1. Agent samples cgroup metrics                            │
│    Pod A: usage=800m, throttled=50ms → demand=0.5          │
│    Pod B: usage=200m, throttled=0ms  → demand=0.0          │
└─────────────────────────────────────────────────────────────┘

Time: T=0.1s
┌─────────────────────────────────────────────────────────────┐
│ 2. PodAgents compute bids                                  │
│    Agent A: state="high:some:adequate" → action=aggressive │
│             bid={demand=1200m, weight=1.2, min=100m}        │
│    Agent B: state="low:none:adequate" → action=conservative│
│             bid={demand=200m, weight=0.8, min=100m}         │
└─────────────────────────────────────────────────────────────┘

Time: T=0.2s
┌─────────────────────────────────────────────────────────────┐
│ 3. Nash Bargaining solves allocation                       │
│    Capacity: 1500m                                          │
│    Baselines: A=100m, B=100m → total=200m                  │
│    Surplus: 1500m - 200m = 1300m                           │
│    Weighted distribution:                                   │
│      A gets: 100m + (1.2/2.0)*1300m = 880m                 │
│      B gets: 100m + (0.8/2.0)*1300m = 620m                 │
└─────────────────────────────────────────────────────────────┘

Time: T=0.3s
┌─────────────────────────────────────────────────────────────┐
│ 4. Agent writes PodAllocation CRDs                         │
│    PodAllocation/pod-a: {request=792m, limit=880m}         │
│    PodAllocation/pod-b: {request=558m, limit=620m}         │
└─────────────────────────────────────────────────────────────┘

Time: T=1s
┌─────────────────────────────────────────────────────────────┐
│ 5. Controller patches pods                                 │
│    PATCH /api/v1/pods/pod-a → CPU limit=880m              │
│    PATCH /api/v1/pods/pod-b → CPU limit=620m              │
└─────────────────────────────────────────────────────────────┘

Time: T=5s (next cycle)
┌─────────────────────────────────────────────────────────────┐
│ 6. PodAgents observe outcomes and learn                   │
│    Agent A: throttling reduced → reward=+8.0 → update Q   │
│    Agent B: no change → reward=+10.0 → update Q           │
└─────────────────────────────────────────────────────────────┘
```

---

## Configuration

### Agent Configuration

Loaded from ConfigMap `mbcas-agent-config`:

```yaml
# Timing
samplingInterval: "1s"      # How often to read cgroups
writeInterval: "5s"          # How often to write allocations

# Resource Management
systemReservePercent: "10.0" # Reserve for system pods
baselineCPUPerPod: "100m"    # Minimum per pod

# Q-Learning
agentLearningRate: "0.1"     # α (learning rate)
agentExplorationRate: "0.2"  # ε (exploration)
agentDiscountFactor: "0.9"   # γ (future reward weight)
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
- **Recovery**: DaemonSet restarts agent, resumes from current state
- **Mitigation**: PodAgents are stateless (Q-tables rebuilt)

### Controller Crash
- **Impact**: No pod patches applied
- **Recovery**: Deployment restarts controller, processes pending CRDs
- **Mitigation**: CRDs persist in etcd

### Cgroup Read Failure
- **Impact**: Pod skipped for that cycle
- **Recovery**: Retry on next cycle (5s later)
- **Mitigation**: Exponential backoff, failure tracking

### API Server Unavailable
- **Impact**: Cannot write CRDs or patch pods
- **Recovery**: Client retries with backoff
- **Mitigation**: Local caching, graceful degradation

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

### Metrics (Prometheus)

```
# Agent metrics
mbcas_agent_pods_managed
mbcas_agent_allocation_duration_seconds
mbcas_agent_cgroup_read_errors_total

# Nash solver metrics
mbcas_nash_iterations
mbcas_nash_mode{mode="uncongested|congested|overloaded"}

# Controller metrics
mbcas_controller_reconcile_duration_seconds
mbcas_controller_pod_patch_errors_total
```

### Logs

```bash
# Agent logs
kubectl logs -n mbcas-system -l app.kubernetes.io/component=agent

# Controller logs
kubectl logs -n mbcas-system -l app.kubernetes.io/component=controller
```

---

## Future Enhancements

1. **Memory Management**: Extend to memory allocation
2. **Multi-Resource**: Joint CPU+Memory Nash Bargaining
3. **Predictive**: Use Kalman filters for demand forecasting
4. **Distributed**: Multi-node coordination via price signals
5. **SLO Integration**: Prometheus latency metrics for reward function
