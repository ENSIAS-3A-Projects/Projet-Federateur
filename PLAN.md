# MBCAS Development & Research Plan

**Roadmap for Market-Based CPU Allocation System**

This document outlines the development milestones, research objectives, and implementation plan for MBCAS, incorporating recent discoveries and corrections.

---

## Table of Contents

1. [Project Vision & Goals](#project-vision--goals)
2. [Current Status](#current-status)
3. [Phase 1: Foundation & Robustness (Weeks 1-2)](#phase-1-foundation--robustness-weeks-1-2)
4. [Phase 2: Workload-Aware Allocation (Weeks 3-4)](#phase-2-workload-aware-allocation-weeks-3-4)
5. [Phase 3: Advanced Game Theory (Weeks 5-6)](#phase-3-advanced-game-theory-weeks-5-6)
6. [Phase 4: Predictive Optimization (Weeks 7-8)](#phase-4-predictive-optimization-weeks-7-8)
7. [Phase 5: Evaluation & Validation (Weeks 9-10)](#phase-5-evaluation--validation-weeks-9-10)
8. [Phase 6: Dissertation & Publication (Weeks 11-12)](#phase-6-dissertation--publication-weeks-11-12)
9. [Success Metrics](#success-metrics)
10. [Risk Mitigation](#risk-mitigation)

---

## Project Vision & Goals

### Core Objective

Build a **production-ready, research-validated** resource allocation system that demonstrates how **competitive game theory + multi-agent reinforcement learning** can achieve efficient container resource management.

### Key Differentiators

1. **Need-Based** (not fair-share): Each pod gets what it NEEDS, minimizing waste
2. **Competitive** (not cooperative): Agents bid selfishly, efficiency emerges
3. **Learning** (not static): Q-learning discovers optimal strategies empirically
4. **Market-Based** (not centralized): Shadow prices coordinate competition

### Research Questions

**RQ1**: Can multi-agent RL discover game-theoretic equilibrium strategies (truthful bidding) without explicit programming?

**RQ2**: Does competitive resource allocation achieve higher efficiency than cooperative fair-share approaches?

**RQ3**: How do shadow prices enable decentralized coordination in non-cooperative games?

**RQ4**: Can workload-aware adaptive strategies reduce oscillations while maintaining responsiveness?

---

## Current Status

### ✅ Completed (As of Week 0)

**Core System**:
- [x] Dual-loop architecture (FastLoop + SlowLoop)
- [x] Q-learning agents per pod
- [x] Market-clearing mechanism with shadow prices
- [x] In-place resize via Kubernetes API
- [x] PodAllocation CRD and controller
- [x] cgroup metric collection
- [x] Two-pass bidding with price feedback

**Robustness Fixes** (YOUR CONTRIBUTIONS):
- [x] Race condition fixes (mutex protection)
- [x] Memory leak prevention (Q-table size limit: 5000)
- [x] Resource cleanup (orphaned PodAllocations)
- [x] Arithmetic safety (overflow checks, division by zero)
- [x] Capacity caps (75% per-pod, node capacity ceiling)
- [x] Cooldown jitter (break resonance with periodic workloads)
- [x] Terminating/evicted pod filtering
- [x] Startup grace period using actual pod start time

**Testing**:
- [x] Single idle pod convergence (807m → 19m in 230s)
- [x] Multi-pod competition (8 pods on 4-core node)
- [x] MBCAS vs VPA comparison (100x faster to optimization)
- [x] Noisy neighbor isolation (75% cap works)
- [x] Spike workload response (FastLoop triggers)

**Documentation**:
- [x] README with competitive game theory framing
- [x] ARCHITECTURE with market mechanism details
- [x] GAME_THEORY with mechanism design proofs
- [x] Benchmark results and analysis

### ⚠️ Known Issues (To Address)

**Oscillations**:
- Bursty workloads: 69 allocation changes in 10min (too many)
- Root cause: Controller lag, no burst prediction
- Impact: Instability, poor user experience

**Predictive Capability**:
- Ramping workloads: Lags behind growth
- Kalman filter: Implemented but may not be active
- Impact: Underallocation during ramp-up

**Applied Allocation Tracking**:
- Agent uses `lastAllocations` (desired) not actual
- Controller applies allocations with delay
- Impact: Agent reacts to stale state, causes oscillations

**Workload Classification**:
- All workloads treated identically
- No distinction between idle, steady, bursty, periodic
- Impact: One-size-fits-all parameters, suboptimal

---

## Phase 1: Foundation & Robustness (Weeks 1-2)

### Goal
**Production-ready core system with stable allocations**

### Milestone 1.1: Applied Allocation Tracking

**Problem**: Agent reacts to desired allocation (not yet applied by controller)

**Tasks**:
- [ ] Modify `collectBids()` to read `PodAllocation.Status.AppliedResources`
- [ ] Add fallback chain: Status → Pod.Status → Pod.Spec
- [ ] Track allocation lag metric: `mbcas_allocation_lag_seconds{pod}`
- [ ] Log warning when lag > 30s (controller bottleneck)

**Acceptance Criteria**:
- [ ] Agent uses actual applied allocation in bid computation
- [ ] Allocation changes reduce by 30-40% for steady workloads
- [ ] Lag metric < 10s for P90

**Estimated Effort**: 1 day

---

### Milestone 1.2: Hysteresis Zones (Stability)

**Problem**: Small fluctuations cause unnecessary allocation changes

**Tasks**:
- [ ] Define "good enough" zone: ±10% of target allocation
- [ ] Skip updates if allocation within zone
- [ ] Add state persistence: Must be in new state for 3 samples before acting
- [ ] Dampen changes near equilibrium: `changeRate *= 0.5` if `|Δ| < 100m`

**Implementation**:
```go
func shouldUpdateAllocation(current, target int64) bool {
    lowerBound := target * 0.9
    upperBound := target * 1.1
    
    if current >= lowerBound && current <= upperBound:
        return false  // Good enough, don't change
    
    return true
}
```

**Acceptance Criteria**:
- [ ] Steady workloads: <5 allocation changes per hour
- [ ] No oscillations (allocation ping-pong)
- [ ] Still responsive to real changes (throttling, usage spike)

**Estimated Effort**: 2 days

---

### Milestone 1.3: FastLoop Decay Timers

**Problem**: Emergency allocations persist too long after spike ends

**Tasks**:
- [ ] Tag FastLoop allocations: `source: "fast_loop_emergency"`
- [ ] Add decay timer: After 120s without throttling, decay toward SlowLoop target
- [ ] Implement gradual decay: `lerp(current, target, 0.1)` per cycle
- [ ] Reset decay if throttling recurs

**Implementation**:
```go
type AllocationMetadata struct {
    Value      int64
    Source     string  // "slow_loop" | "fast_loop_emergency"
    Timestamp  time.Time
}

func decayEmergencyAllocation(meta AllocationMetadata, slowTarget int64) int64 {
    if meta.Source != "fast_loop_emergency":
        return meta.Value
    
    if time.Since(meta.Timestamp) < 120*time.Second:
        return meta.Value  // Keep emergency allocation
    
    // Gradual decay
    return int64(0.9*float64(meta.Value) + 0.1*float64(slowTarget))
}
```

**Acceptance Criteria**:
- [ ] Post-burst allocation returns to baseline within 2 minutes
- [ ] No premature decay (still elevated if throttling continues)
- [ ] Metric: `mbcas_fastloop_active_duration_seconds{pod}`

**Estimated Effort**: 2 days

---

### Milestone 1.4: Convergence Detection

**Problem**: System never declares "stable", keeps adjusting

**Tasks**:
- [ ] Track allocation history: Last 10 allocations
- [ ] Compute coefficient of variation: `CV = stddev / mean`
- [ ] Declare converged if `CV < 0.05` for 5 minutes
- [ ] When converged: Increase cooldown to 120s, reduce epsilon to 0.05
- [ ] Exit convergence if throttling > 0.1 OR usage changes > 50%

**Implementation**:
```go
type ConvergenceTracker struct {
    AllocationHistory []int64
    ConvergedAt       time.Time
    IsConverged       bool
}

func (ct *ConvergenceTracker) Update(newAlloc int64) {
    ct.AllocationHistory = append(ct.AllocationHistory, newAlloc)
    if len(ct.AllocationHistory) > 10:
        ct.AllocationHistory = ct.AllocationHistory[1:]
    
    cv := coefficientOfVariation(ct.AllocationHistory)
    
    if cv < 0.05 && len(ct.AllocationHistory) == 10:
        if !ct.IsConverged:
            ct.ConvergedAt = time.Now()
            ct.IsConverged = true
    } else {
        ct.IsConverged = false
    }
}
```

**Acceptance Criteria**:
- [ ] 80%+ of pods reach convergence within 5 minutes
- [ ] Converged pods have <2 allocation changes per hour
- [ ] System detects and exits convergence on workload changes

**Estimated Effort**: 2 days

---

### Phase 1 Deliverables

**Code**:
- Applied allocation tracking
- Hysteresis zones
- FastLoop decay
- Convergence detection

**Metrics**:
- `mbcas_allocation_lag_seconds`
- `mbcas_convergence_status{pod}`
- `mbcas_hysteresis_skips_total{pod}`

**Documentation**:
- Updated ARCHITECTURE.md with stability mechanisms
- Performance tuning guide

**Target Date**: End of Week 2

---

## Phase 2: Workload-Aware Allocation (Weeks 3-4)

### Goal
**Adaptive strategies for different workload patterns**

### Milestone 2.1: Workload Classification

**Problem**: Treating all workloads identically is suboptimal

**Tasks**:
- [ ] Add annotation: `mbcas.io/workload-type: [idle|steady|bursty|periodic|ramping]`
- [ ] Implement auto-detection:
  - Idle: Usage < 10% of allocation for 5min
  - Steady: CV < 0.2 over 10 samples
  - Bursty: CV > 0.5, short spikes (<60s)
  - Periodic: FFT detects regular frequency
  - Ramping: Linear regression slope > threshold, R² > 0.8
- [ ] Store classification in PodAgent state
- [ ] Expose metric: `mbcas_workload_classification{pod, type}`

**Implementation**:
```go
func classifyWorkload(usageHistory []int64) string {
    cv := coefficientOfVariation(usageHistory)
    
    if mean(usageHistory) < 100:  // < 100m average
        return "idle"
    
    if cv < 0.2:
        return "steady"
    
    if cv > 0.5 && detectShortSpikes(usageHistory):
        return "bursty"
    
    if detectPeriodicity(usageHistory):
        return "periodic"
    
    slope, r2 := linearRegression(usageHistory)
    if slope > 10 && r2 > 0.8:  // Growing at 10m/sample
        return "ramping"
    
    return "unknown"
}
```

**Acceptance Criteria**:
- [ ] 80%+ workloads correctly classified
- [ ] Classification updates within 5 minutes of workload change
- [ ] Metric exposed in Prometheus

**Estimated Effort**: 3 days

---

### Milestone 2.2: Stratified Q-Learning

**Problem**: One Q-table for all workloads doesn't capture differences

**Tasks**:
- [ ] Split Q-tables by workload type: `QTable[workload_type][state][action]`
- [ ] Different reward functions per type:
  - Idle: Reward aggressive downscaling (`-waste*20`)
  - Steady: Reward stability (`-oscillation*15`)
  - Bursty: Reward quick recovery (`-throttling*25`)
  - Periodic: Reward phase-aware allocation
  - Ramping: Reward predictive increases
- [ ] Separate epsilon per type:
  - Idle/Steady: Low epsilon (0.1) - exploit known strategies
  - Bursty/Ramping: Higher epsilon (0.3) - explore adaptively

**Implementation**:
```go
type StratifiedQTable struct {
    Tables map[string]map[string]map[string]float64
    // Tables[workload_type][state][action] = Q-value
}

func (pa *PodAgent) computeRewardByType(workloadType string) float64 {
    switch workloadType {
    case "idle":
        reward = -waste * 20  // Aggressive waste penalty
    case "steady":
        reward = +stability * 10 - oscillation * 15
    case "bursty":
        reward = -throttling * 25 + quick_recovery * 20
    // ...
    }
    return reward
}
```

**Acceptance Criteria**:
- [ ] Q-learning converges 50% faster (fewer iterations to stable strategy)
- [ ] Idle pods: More aggressive downscaling
- [ ] Bursty pods: Faster response to spikes
- [ ] Steady pods: Fewer allocation changes

**Estimated Effort**: 4 days

---

### Milestone 2.3: Workload-Specific Parameters

**Problem**: One-size-fits-all smoothing/thresholds

**Tasks**:
- [ ] Create parameter matrix per workload type
- [ ] Load parameters from ConfigMap (user-customizable)
- [ ] Apply parameters in bid computation

**Parameter Matrix**:
```yaml
workloadParameters:
  idle:
    alphaUp: 0.1      # Very slow increases
    alphaDown: 0.8    # Very fast decreases
    fastThreshold: 0.3  # Rarely trigger FastLoop
    cooldownBase: 60s # Long cooldown
  steady:
    alphaUp: 0.2
    alphaDown: 0.3
    fastThreshold: 0.15
    cooldownBase: 45s
  bursty:
    alphaUp: 0.5      # Fast increases
    alphaDown: 0.4    # Moderate decreases
    fastThreshold: 0.1  # Trigger FastLoop easily
    cooldownBase: 15s # Short cooldown
  periodic:
    alphaUp: 0.3
    alphaDown: 0.4
    fastThreshold: 0.2
    cooldownBase: 30s ± 10s  # Jitter breaks resonance
  ramping:
    alphaUp: 0.6      # Very fast increases
    alphaDown: 0.2    # Slow decreases
    fastThreshold: 0.1
    cooldownBase: 20s
```

**Acceptance Criteria**:
- [ ] Oscillations reduce 50% for bursty workloads
- [ ] Idle workloads converge faster (230s → <120s)
- [ ] Parameters user-configurable via ConfigMap

**Estimated Effort**: 2 days

---

### Milestone 2.4: Burst Detection & Memory

**Problem**: Can't distinguish burst from sustained load

**Tasks**:
- [ ] Add to PodAgent: `BurstHistory []BurstEvent`
- [ ] Detect burst pattern: Spike > 2x baseline, duration < 60s
- [ ] Record burst statistics: Duration, amplitude, frequency
- [ ] Use in FastStep:
  - If burst is typical → Moderate response (headroom allocation)
  - If sustained → Aggressive response
  - If periodic → Phase-aware allocation

**Implementation**:
```go
type BurstEvent struct {
    StartTime    time.Time
    Duration     time.Duration
    PeakUsage    int64
    BaselineUsage int64
}

func detectBurst(usageHistory []int64, timestamps []time.Time) *BurstEvent {
    baseline := percentile(usageHistory, 10)  // P10 as baseline
    peak := max(usageHistory)
    
    if peak > baseline * 2:  // Spike > 2x baseline
        // Find spike duration
        duration := computeSpikeDuration(usageHistory, baseline*1.5)
        
        if duration < 60*time.Second:
            return &BurstEvent{
                StartTime: timestamps[0],
                Duration: duration,
                PeakUsage: peak,
                BaselineUsage: baseline,
            }
        }
    }
    return nil
}
```

**Acceptance Criteria**:
- [ ] FastLoop triggers 70% less on known/typical bursts
- [ ] Burst memory persists last 100 bursts
- [ ] Metric: `mbcas_burst_detected_total{pod, burst_type}`

**Estimated Effort**: 3 days

---

### Phase 2 Deliverables

**Code**:
- Workload classification
- Stratified Q-learning
- Workload-specific parameters
- Burst detection

**Metrics**:
- `mbcas_workload_classification`
- `mbcas_burst_detected_total`
- `mbcas_qtable_size_by_workload_type`

**Documentation**:
- Workload classification guide
- Parameter tuning guide
- Burst handling behavior

**Target Date**: End of Week 4

---

## Phase 3: Advanced Game Theory (Weeks 5-6)

### Goal
**Demonstrate novel game-theoretic contributions**

### Milestone 3.1: Workload-Type-Aware Weights

**Problem**: Weights are static (based on pod requests only)

**Tasks**:
- [ ] Extend weight calculation with workload type
- [ ] Add annotation: `mbcas.io/priority-class: [critical|normal|low]`
- [ ] Combine workload type + priority for final weight
- [ ] Update Nash solver to use heterogeneous weights

**Formula**:
```go
baseWeight = podRequest / 1000  // Traditional weight

typeMultiplier = map[string]float64{
    "critical": 1.5,   // Latency-sensitive, high priority
    "normal":   1.0,
    "low":      0.7,
    "idle":     0.3,
}

finalWeight = baseWeight * typeMultiplier[workloadType] * typeMultiplier[priorityClass]
```

**Acceptance Criteria**:
- [ ] Critical pods get 40%+ more allocation under contention
- [ ] Idle pods get proportionally less
- [ ] Metric: `mbcas_effective_weight{pod, workload_type, priority}`

**Estimated Effort**: 2 days

---

### Milestone 3.2: Enhanced Shadow Price Feedback

**Problem**: Shadow price computed but minimally used

**Tasks**:
- [ ] Make agents more price-responsive
- [ ] Adjust sensitivity by workload type:
  - Critical: Low price sensitivity (inelastic)
  - Batch: High price sensitivity (elastic)
- [ ] Implement price elasticity parameter
- [ ] Expose shadow price in metrics

**Implementation**:
```go
func adjustDemandByPrice(demand, shadowPrice, elasticity) int64 {
    if shadowPrice > 0.5:  // High contention
        reduction = shadowPrice * elasticity  // Elastic workloads reduce more
        reductionFactor = 1.0 - min(reduction, 0.5)  // Cap at 50% reduction
        return demand * reductionFactor
    else if shadowPrice < 0.2:  // Low contention
        increase = (0.2 - shadowPrice) * elasticity
        increaseFactor = 1.0 + min(increase, 0.2)  // Cap at 20% increase
        return demand * increaseFactor
    }
    return demand
}

elasticity = map[string]float64{
    "critical": 0.2,  // Inelastic (needs don't change much with price)
    "normal":   0.5,
    "batch":    0.9,  // Elastic (can defer if price is high)
}
```

**Acceptance Criteria**:
- [ ] Nash convergence 50% faster under contention
- [ ] Shadow price accurately reflects scarcity
- [ ] Metric: `mbcas_shadow_price{node}`

**Estimated Effort**: 3 days

---

### Milestone 3.3: Voluntary Participation (Refusal Mechanism)

**Problem**: All pods forced to participate (no true game theory)

**Tasks**:
- [ ] Add annotation: `mbcas.io/min-acceptable-cpu: "500m"`
- [ ] Implement soft refusal via compliance signal
- [ ] Add compliance level to PodAgent: `ComplianceLevel float64`
- [ ] Use compliance in reward: If allocation < min-acceptable → Large penalty
- [ ] Nash adapts over time: If pod refuses 3+ times → Increase baseline

**Implementation**:
```go
func (pa *PodAgent) evaluateCompliance(allocation, minAcceptable) float64 {
    if allocation >= minAcceptable:
        return 1.0  // Full compliance
    
    // Partial compliance
    compliance := float64(allocation) / float64(minAcceptable)
    return compliance
}

func computeRewardWithCompliance(allocation, usage, compliance) float64 {
    reward := baseReward(allocation, usage)
    
    if compliance < 0.8:
        reward -= 20  // Strong negative signal (soft refusal)
    
    return reward
}
```

**Acceptance Criteria**:
- [ ] Pods can signal dissatisfaction (compliance < 1.0)
- [ ] Nash solver learns to increase allocations for frequently-refusing pods
- [ ] Metric: `mbcas_compliance_level{pod}`

**Estimated Effort**: 4 days

---

### Phase 3 Deliverables

**Code**:
- Workload-aware weights
- Enhanced shadow price
- Voluntary participation

**Documentation**:
- Game theory properties proof
- Mechanism design analysis
- Individual rationality guarantees

**Research Output**:
- Formal proof of incentive compatibility
- Empirical validation of Nash equilibrium
- Shadow price as market mechanism

**Target Date**: End of Week 6

---

## Phase 4: Predictive Optimization (Weeks 7-8)

### Goal
**Proactive allocation instead of reactive**

### Milestone 4.1: Enable & Validate Kalman Filter

**Problem**: Prediction exists but may be inactive

**Tasks**:
- [ ] Audit `EnableKalmanPrediction` usage in codebase
- [ ] Verify Kalman predictions are used in demand calculation
- [ ] If disabled, implement:
  ```go
  predictedUsage = kalmanFilter.predict()
  demand = max(currentUsage, predictedUsage * 1.2)
  ```
- [ ] Add Kalman state to metrics
- [ ] Compare predicted vs actual (accuracy tracking)

**Acceptance Criteria**:
- [ ] Prediction accuracy > 80% for ramping workloads
- [ ] Ramping pods never underallocated
- [ ] Metric: `mbcas_predicted_usage_milli{pod}`

**Estimated Effort**: 3 days

---

### Milestone 4.2: Periodic Workload Phase-Aware Allocation

**Problem**: Cron jobs cause allocation churn

**Tasks**:
- [ ] Detect periodicity using autocorrelation
- [ ] Record period and phase: `Period: 1800s, Phase: peak at :00 and :30`
- [ ] Pre-allocate before peak: If `nextPeakIn < 120s` → allocate for peak
- [ ] Reduce allocation after peak with delay: If `timeSincePeak > 300s` → gradual reduction

**Implementation**:
```go
func detectPeriod(usageHistory, timestamps) (period, phase) {
    // Compute autocorrelation at various lags
    lags := []int{60, 120, 300, 600, 1800, 3600}  // 1min to 1hour
    
    for lag in lags:
        correlation := autocorrelation(usageHistory, lag)
        if correlation > 0.7:
            return lag, computePhase(timestamps, lag)
    
    return 0, 0  // Not periodic
}

func allocateForPeriodicWorkload(period, phase, currentTime) int64 {
    nextPeak := computeNextPeak(period, phase, currentTime)
    
    if nextPeak < 120*time.Second:
        return peakAllocation  // Pre-allocate
    else if timeSincePeak > 300*time.Second:
        return baselineAllocation  // Reduce after peak
    else:
        return currentAllocation  // Maintain during peak
}
```

**Acceptance Criteria**:
- [ ] Periodic workloads get 90% fewer allocation changes
- [ ] No throttling during peaks (pre-allocated)
- [ ] Metric: `mbcas_workload_period_seconds{pod}`

**Estimated Effort**: 4 days

---

### Milestone 4.3: Multi-Step Prediction for Ramping

**Problem**: Ramp-up lags behind growth

**Tasks**:
- [ ] Implement trend detection: Linear regression on usage history
- [ ] Predict N steps ahead: `predicted[t+3] = usage[t] + 3*slope`
- [ ] Allocate for t+3 (1 minute ahead, 4× SlowLoop interval)
- [ ] Detect trend reversal: If slope changes sign → reset model

**Implementation**:
```go
func predictRampingUsage(usageHistory, timestamps) int64 {
    slope, r2 := linearRegression(usageHistory)
    
    if slope > threshold && r2 > 0.8:
        // Strong upward trend
        currentUsage := usageHistory[len(usageHistory)-1]
        
        // Predict 3 cycles ahead (45s at 15s/cycle)
        predictedUsage := currentUsage + 3*slope
        
        return predictedUsage
    }
    
    return usageHistory[len(usageHistory)-1]  // No trend
}
```

**Acceptance Criteria**:
- [ ] Ramping workloads never under-allocated
- [ ] Prediction accuracy > 75% for ramps
- [ ] Metric: `mbcas_ramp_slope{pod}`

**Estimated Effort**: 3 days

---

### Phase 4 Deliverables

**Code**:
- Kalman filter validation
- Periodic workload handling
- Ramping prediction

**Documentation**:
- Prediction accuracy analysis
- Workload pattern recognition guide

**Research Output**:
- Predictive allocation strategies
- Comparison: Reactive vs Proactive

**Target Date**: End of Week 8

---

## Phase 5: Evaluation & Validation (Weeks 9-10)

### Goal
**Rigorous academic evaluation proving contributions**

### Milestone 5.1: Comprehensive Benchmark Suite

**Tasks**:
- [ ] Implement test harness for all workload types
- [ ] Tier 1 scenarios (MUST TEST):
  - Idle overprovisioned (4 pods, 100m each → 20m)
  - Constant load (4 pods, steady 1000m each)
  - Bursty (4 pods, spikes 100m → 3000m)
  - Multi-pod competition (8 pods on 4000m capacity)
  - Noisy neighbor (1 pod trying to monopolize)
- [ ] Tier 2 scenarios (SHOULD TEST):
  - SLO-driven (latency target: 100ms P99)
  - Batch + service mix (2 batch, 2 services)
  - Periodic (cron job every 30min)
  - Ramping (linear growth 100m → 2000m over 5min)
- [ ] Tier 3 scenarios (NICE TO HAVE):
  - Multi-tenant with quotas
  - Cold starts (JVM, ML models)
  - Cascading failures

**Acceptance Criteria**:
- [ ] All Tier 1 scenarios pass
- [ ] 75%+ Tier 2 scenarios pass
- [ ] Results comparable to VPA where applicable
- [ ] MBCAS shows >50% improvement in at least 3 metrics

**Estimated Effort**: 5 days

---

### Milestone 5.2: Game Theory Validation

**Tasks**:
- [ ] Prove Nash equilibrium properties empirically:
  - No agent can improve by unilateral deviation
  - Shadow price equals marginal utility at equilibrium
- [ ] Measure game-theoretic metrics:
  - Time to Nash convergence
  - Pareto efficiency (cluster-level utilization)
  - Strategic stability (agents don't deviate)
- [ ] Compare cooperative vs non-cooperative outcomes
- [ ] Test mechanism design properties:
  - Incentive compatibility (truthful bidding is optimal)
  - Individual rationality (all pods ≥ baseline)
  - Budget balance (Σ allocation = capacity)

**Experiments**:
```
Experiment 1: Strategic Deviation Test
  Setup: 4 pods, Nash equilibrium allocation
  Test: Force pod A to overbid by 50%
  Measure: Pod A's payoff before vs after deviation
  Expected: Payoff decreases (overbidding is suboptimal)

Experiment 2: Market Efficiency Test
  Setup: 8 pods with varying demands
  Measure: Σ allocation_i vs capacity
  Expected: Σ allocation_i ≈ capacity (±5%)

Experiment 3: Convergence Speed Test
  Setup: Random initial allocations
  Measure: Time until stable (CV < 0.05)
  Expected: <5 minutes for 90% of pods
```

**Acceptance Criteria**:
- [ ] Nash equilibrium achieved in 90%+ of cases
- [ ] Pareto efficiency (cluster-level) > 85%
- [ ] No successful strategic deviations observed

**Estimated Effort**: 4 days

---

### Milestone 5.3: ABM Validation

**Tasks**:
- [ ] Emergent behavior analysis:
  - Does system self-organize to efficient allocation?
  - Do agents learn optimal policies?
  - How does heterogeneity affect convergence?
- [ ] Measure ABM metrics:
  - Q-learning convergence rate (iterations to stable Q-values)
  - Exploration-exploitation balance (epsilon decay curve)
  - Agent adaptation speed (time to learn new workload pattern)
- [ ] Analyze interaction patterns:
  - How do agents influence each other?
  - Does cooperation emerge (e.g., voluntary demand reduction)?
  - Are there feedback loops (e.g., cascading throttling)?

**Experiments**:
```
Experiment 4: Learning Speed Test
  Setup: New pod added to system
  Measure: Q-table convergence time
  Expected: <100 iterations to stable strategy

Experiment 5: Heterogeneity Test
  Setup: Mix of idle, steady, bursty pods
  Measure: System-wide efficiency and fairness
  Expected: Efficient allocation despite differences

Experiment 6: Emergent Cooperation Test
  Setup: Severe contention (demand = 2× capacity)
  Measure: Do agents voluntarily reduce demand?
  Expected: Shadow price triggers voluntary reductions
```

**Acceptance Criteria**:
- [ ] Agents learn optimal policies (Q-learning converges)
- [ ] Heterogeneous agents coexist efficiently
- [ ] System self-organizes without central control

**Estimated Effort**: 3 days

---

### Milestone 5.4: Statistical Significance Testing

**Tasks**:
- [ ] Compare MBCAS vs VPA vs Static on all metrics
- [ ] Run each scenario 10 times (statistical power)
- [ ] T-tests for metric improvements
- [ ] ANOVA for multi-system comparison
- [ ] Confidence intervals on all metrics (95% CI)
- [ ] Effect size calculation (Cohen's d)

**Metrics to Test**:
```
Primary Metrics:
  - CPU efficiency (usage / allocation)
  - Waste ratio ((allocation - usage) / allocation)
  - Throttling duration (seconds throttled / total time)
  - Allocation changes (count / hour)
  - Convergence time (seconds to stable)

Secondary Metrics:
  - P99 latency (for SLO workloads)
  - Jain's fairness index (under contention)
  - Shadow price volatility
  - Q-learning reward trend
```

**Acceptance Criteria**:
- [ ] MBCAS statistically significantly better (p < 0.05) on ≥3 primary metrics
- [ ] Effect sizes are medium-to-large (Cohen's d > 0.5)
- [ ] No statistically significant regressions on any metric

**Estimated Effort**: 3 days

---

### Phase 5 Deliverables

**Data**:
- Benchmark results (10 runs × 8 scenarios = 80 data points)
- Statistical analysis (t-tests, ANOVA, confidence intervals)
- Game theory validation results
- ABM emergence analysis

**Visualizations**:
- Time-series allocation evolution plots
- Nash equilibrium convergence graphs
- Q-learning reward accumulation curves
- Workload classification accuracy heatmap

**Documentation**:
- Comprehensive evaluation report
- Statistical methodology
- Results interpretation

**Target Date**: End of Week 10

---

## Phase 6: Dissertation & Publication (Weeks 11-12)

### Goal
**Academic-quality documentation and dissemination**

### Milestone 6.1: Dissertation Chapter

**Structure**:
```
Chapter: MBCAS - Market-Based CPU Allocation for Kubernetes

1. Introduction
   - Problem: Resource allocation under uncertainty
   - Limitations of fair-share approaches
   - Need-based optimization opportunity

2. Related Work
   - VPA, HPA, kube-scheduler
   - Dominant Resource Fairness (DRF)
   - Game-theoretic resource allocation
   - Multi-agent reinforcement learning

3. System Design
   - Competitive game formulation
   - Mechanism design (incentive compatibility)
   - Market-clearing with shadow prices
   - Q-learning agent architecture

4. Implementation
   - Kubernetes integration
   - Dual-loop control
   - Workload classification
   - Robustness mechanisms

5. Evaluation
   - Benchmark methodology
   - Game-theoretic validation
   - Statistical significance testing
   - Comparison with baselines

6. Results
   - Efficiency improvements
   - Nash equilibrium convergence
   - Q-learning behavior
   - Workload-specific performance

7. Discussion
   - Theoretical contributions
   - Practical implications
   - Limitations
   - Future work

8. Conclusion
```

**Acceptance Criteria**:
- [ ] 40-60 pages of high-quality academic writing
- [ ] All claims supported by data
- [ ] Proper citations (30+ references)
- [ ] Professional figures and tables

**Estimated Effort**: 10 days

---

### Milestone 6.2: Conference Paper

**Target Venue**: 
- NSDI (Networked Systems Design & Implementation)
- SoCC (Symposium on Cloud Computing)
- OSDI (Operating Systems Design & Implementation)

**Structure** (12 pages):
```
1. Abstract (200 words)
2. Introduction (1.5 pages)
3. Background & Motivation (1 page)
4. Design (3 pages)
5. Implementation (1.5 pages)
6. Evaluation (3 pages)
7. Related Work (1 page)
8. Conclusion (0.5 pages)
9. References (0.5 pages)
```

**Key Contributions to Highlight**:
1. First system combining competitive game theory + multi-agent RL for container allocation
2. Mechanism design ensuring truthful bidding is dominant strategy
3. Shadow price market mechanism for decentralized coordination
4. Empirical validation showing >50% efficiency improvement over VPA

**Acceptance Criteria**:
- [ ] Submission-ready paper (12 pages, conference template)
- [ ] Reproducible evaluation (public dataset/code)
- [ ] Clear novelty statement

**Estimated Effort**: 8 days

---

### Milestone 6.3: Open Source Release

**Tasks**:
- [ ] Clean up codebase (remove debug code, TODOs)
- [ ] Comprehensive README with quickstart
- [ ] Installation guide (minikube, EKS, GKE)
- [ ] API documentation (GoDoc)
- [ ] Tutorial videos (YouTube)
- [ ] Example manifests (various workloads)
- [ ] Contribution guidelines
- [ ] License (MIT)

**Repository Structure**:
```
mbcas/
├── README.md (comprehensive)
├── docs/
│   ├── ARCHITECTURE.md
│   ├── GAME_THEORY.md
│   ├── INSTALLATION.md
│   ├── CONFIGURATION.md
│   ├── TROUBLESHOOTING.md
│   └── EVALUATION.md
├── examples/
│   ├── idle-pod.yaml
│   ├── bursty-workload.yaml
│   ├── multi-tenant.yaml
│   └── slo-driven.yaml
├── benchmarks/
│   ├── run-benchmarks.sh
│   └── results/
├── test/
├── pkg/
├── cmd/
└── config/
```

**Acceptance Criteria**:
- [ ] Repository public on GitHub
- [ ] CI/CD pipeline (GitHub Actions)
- [ ] >80% test coverage
- [ ] Docker images on Docker Hub

**Estimated Effort**: 5 days

---

### Phase 6 Deliverables

**Academic**:
- Dissertation chapter (40-60 pages)
- Conference paper (12 pages)
- Presentation slides

**Open Source**:
- Public GitHub repository
- Docker images
- Documentation
- Tutorial videos

**Target Date**: End of Week 12

---

## Success Metrics

### Functional Metrics

| Metric | Target | Rationale |
|--------|--------|-----------|
| **CPU Efficiency** | >85% | usage / allocation |
| **Waste Ratio** | <15% | (allocation - usage) / allocation |
| **Throttling Duration** | <5% of time | Minimize performance degradation |
| **Convergence Time** | <5 minutes for 80% of pods | Fast adaptation |
| **Allocation Changes** | <10/hour for steady workloads | Stability |
| **Time to First Allocation** | <30 seconds | Responsiveness |

### Game-Theoretic Metrics

| Metric | Target | Rationale |
|--------|--------|-----------|
| **Nash Equilibrium Convergence** | 90% of scenarios | Strategic stability |
| **Incentive Compatibility** | No successful deviations | Truthful bidding optimal |
| **Market Clearing** | Σ allocation = capacity ± 5% | Efficiency |
| **Shadow Price Accuracy** | Correlation > 0.8 with congestion | Price signals work |

### Learning Metrics

| Metric | Target | Rationale |
|--------|--------|-----------|
| **Q-Learning Convergence** | <100 iterations | Fast learning |
| **Prediction Accuracy** | >75% for ramping workloads | Proactive allocation |
| **Workload Classification** | >80% accuracy | Adaptive strategies |
| **Burst Detection** | >90% precision & recall | Smart response |

### Comparison with Baselines

| Metric | vs VPA | vs Static |
|--------|--------|-----------|
| **Time to Allocation** | >100x faster | >∞ faster |
| **CPU Efficiency** | +15-25% | +30-40% |
| **Waste Reduction** | -50% | -70% |
| **Responsiveness** | +200x faster | +∞ faster |

---

## Risk Mitigation

### Risk 1: Nash Equilibrium May Not Converge

**Likelihood**: Low (Q-learning theory guarantees convergence)

**Impact**: High (undermines game theory claims)

**Mitigation**:
- Implement convergence detection
- Add fallback to greedy allocation if no convergence after 10min
- Extensive testing with various scenarios

---

### Risk 2: Oscillations Persist Despite Fixes

**Likelihood**: Medium (complex feedback loops)

**Impact**: Medium (usability suffers but system still works)

**Mitigation**:
- Incremental fixes (hysteresis, decay, tracking)
- A/B testing each fix independently
- Conservative defaults (favor stability over responsiveness)

---

### Risk 3: Kubernetes API Rate Limiting

**Likelihood**: Medium (FastLoop writes frequently)

**Impact**: Low (system degrades gracefully)

**Mitigation**:
- Cooldown mechanisms (already implemented)
- Batch updates when possible
- Client-side throttling

---

### Risk 4: Q-Table Memory Explosion

**Likelihood**: Low (already capped at 5000 states)

**Impact**: Low (agent just forgets old states)

**Mitigation**:
- Fixed maximum size (YOUR FIX)
- Better: LRU eviction instead of random
- Monitor Q-table sizes in production

---

### Risk 5: Evaluation Shows No Improvement

**Likelihood**: Low (preliminary results are positive)

**Impact**: High (fails academic validation)

**Mitigation**:
- Run evaluation early (Week 9)
- Iterate on fixes if needed
- Lower bar: "Comparable to VPA with added benefits"

---

## Timeline Summary

| Phase | Weeks | Key Deliverables | Status |
|-------|-------|------------------|--------|
| **0. Foundation** | Weeks 0 | Core system, robustness fixes | ✅ DONE |
| **1. Robustness** | Weeks 1-2 | Applied allocation, hysteresis, convergence | 🔄 IN PROGRESS |
| **2. Workload-Aware** | Weeks 3-4 | Classification, stratified Q-learning, burst detection | ⏳ PLANNED |
| **3. Game Theory** | Weeks 5-6 | Weights, shadow price, voluntary participation | ⏳ PLANNED |
| **4. Predictive** | Weeks 7-8 | Kalman, periodic handling, ramping prediction | ⏳ PLANNED |
| **5. Evaluation** | Weeks 9-10 | Benchmarks, validation, statistical tests | ⏳ PLANNED |
| **6. Dissertation** | Weeks 11-12 | Chapter, paper, open source release | ⏳ PLANNED |

**Total Duration**: 12 weeks

---

## Next Steps (Immediate Actions)

### Week 1 Focus

**Day 1-2**: Applied Allocation Tracking
- [ ] Modify `collectBids()` to read actual allocations
- [ ] Test with 4-pod scenario
- [ ] Measure reduction in oscillations

**Day 3-4**: Hysteresis Zones
- [ ] Implement "good enough" zone logic
- [ ] Add state persistence (3-sample rule)
- [ ] Test with steady workload

**Day 5**: FastLoop Decay
- [ ] Tag emergency allocations
- [ ] Implement gradual decay
- [ ] Test with spike workload

### Week 2 Focus

**Day 6-7**: Convergence Detection
- [ ] Track allocation history
- [ ] Compute CV, detect stable state
- [ ] Test convergence behavior

**Day 8-10**: Integration Testing
- [ ] Run full Phase 1 test suite
- [ ] Measure all stability metrics
- [ ] Document improvements

---

**This plan balances**:
- ✅ **Academic rigor** (game theory proofs, statistical validation)
- ✅ **Practical engineering** (robustness, performance, usability)
- ✅ **Novel contributions** (competitive game theory + multi-agent RL)
- ✅ **Reproducibility** (open source, benchmarks, documentation)

**Ready to execute!** 🚀