# Game Theory and Economic Concepts in MBCAS

**Document Purpose:** Comprehensive extraction of all game theory, economic theory, and mathematical optimization concepts used in the MBCAS (Market-Based CPU Allocation System) codebase.

---

## Table of Contents

1. [Cooperative Game Theory](#1-cooperative-game-theory)
2. [Bargaining Theory](#2-bargaining-theory)
3. [Economic Principles](#3-economic-principles)
4. [Control Theory](#4-control-theory)
5. [Optimization Theory](#5-optimization-theory)
6. [Mechanism Design](#6-mechanism-design)
7. [Mathematical Foundations](#7-mathematical-foundations)

---

## 1. Cooperative Game Theory

### 1.1 Nash Bargaining Solution (NBS)

**Location:** `pkg/allocation/nash_bargaining.go`

**Mathematical Formulation:**
```
Objective: max Π_i (x_i - d_i)^w_i

Equivalent Convex Form:
max Σ_i w_i · log(x_i - d_i)

Subject to:
  - Σ_i x_i ≤ C          (capacity constraint)
  - x_i ≥ d_i            (individual rationality)
  - x_i ≤ max_i          (bounds)

Where:
  x_i     = allocation to agent i (CPU in millicores)
  d_i     = disagreement point (baseline/minimum allocation)
  w_i     = bargaining weight (priority/importance)
  C       = total available capacity
  max_i   = maximum allocation for agent i
```

**Key Properties (Axioms):**

1. **Pareto Optimality**
   - No allocation can improve one agent without hurting another
   - All available capacity is used (or agents are at their maximum)
   - Implementation: `VerifyNashAxioms()` checks `totalAlloc == capacity || allCapped`

2. **Individual Rationality**
   - Every agent gets at least their disagreement point
   - Ensures no agent is worse off than going alone
   - Implementation: `x_i ≥ d_i` enforced in allocation

3. **Symmetry**
   - Agents with equal weights get equal surplus
   - Surplus_i = x_i - d_i
   - Implementation: Verified with 10% tolerance for rounding

4. **Independence of Irrelevant Alternatives (IIA)**
   - Adding/removing agents doesn't affect others' relative shares
   - Implicit in the proportional allocation algorithm

**Algorithm: Water-Filling with Weights**

```
Step 1: Allocate baselines
  ∀i: x_i = d_i

Step 2: Distribute surplus proportional to weights
  surplus_i = (availableSurplus) × (w_i / Σw_j)
  x_i = d_i + surplus_i

Step 3: Handle capped agents
  If x_i > max_i:
    x_i = max_i
    Redistribute excess to uncapped agents
```

**Implementation Details:**
- Adaptive gain for faster convergence: `adaptiveGain = 1.0 + 0.5 × min(1.0, residualRatio)`
- Maximum iterations: 100 (prevents infinite loops)
- Handles overloaded case: Scales baselines when `Σd_i > C`

---

### 1.2 Kalai-Smorodinsky Solution (KS)

**Location:** `pkg/allocation/kalai_smorodinsky.go`

**Mathematical Formulation:**
```
Objective: max min_i (x_i - d_i) / (u_i - d_i)

Where:
  u_i = ideal point (aspiration/utopia point)
      = min(demand_i, max_i)
  
Ensures proportional satisfaction:
  (x_i - d_i) / (u_i - d_i) = λ  (same for all i)
```

**Key Properties:**

1. **Proportional Fairness**
   - Each agent gets the same fraction of their gain range
   - Gain range = u_i - d_i (ideal minus baseline)
   - More intuitive than Nash for many scenarios

2. **Monotonicity in Aspirations**
   - If agent i's ideal increases, their allocation increases
   - Nash doesn't have this property

3. **Faster Convergence**
   - O(n) complexity vs O(n²) for Nash
   - Direct solution without iteration
   - Better for dynamic workloads

**Algorithm:**

```
Step 1: Compute ideal points
  u_i = min(demand_i, max_i)
  gainRange_i = u_i - d_i

Step 2: Find maximum proportional gain λ
  λ = (capacity - Σd_i) / Σ(w_i × gainRange_i)

Step 3: Allocate proportionally
  x_i = d_i + λ × w_i × gainRange_i

Step 4: Redistribute from capped agents
  (Similar to Nash redistribution)
```

**When to Use:**
- **Kalai-Smorodinsky**: Dynamic workloads, need fast convergence
- **Nash Bargaining**: Provable fairness required, stable workloads

---

### 1.3 Coalition Formation

**Location:** `pkg/coalition/coalition.go`

**Concept:** Services on the same request path form coalitions to cooperate

**Characteristic Function:**
```
v(S) = value of coalition S
     = baseline_latency - optimized_latency
     = latency reduction achievable by pooling resources
```

**Coalition Structure:**
```
Request Path: A → B → C → D

Coalition Members: {A, B, C, D}
Coalition Value: v({A,B,C,D}) = Σ latency_reduction
```

**Complexity Management:**
- Maximum coalition size: 8 members (prevents 2^n explosion)
- Overlapping sub-coalitions for long paths
- Overlap size: MaxCoalitionSize / 2 = 4

**Example:**
```
Path: A→B→C→D→E→F→G→H→I→J (10 services)

Split into:
  Coalition 1: {A,B,C,D,E,F,G,H}  (positions 0-7)
  Coalition 2: {E,F,G,H,I,J}      (positions 4-9, overlap=4)
```

---

### 1.4 ε-Core Stability

**Location:** `pkg/coalition/coalition.go`

**Definition:**
```
An allocation x is in the ε-core if for all subsets S:
  Σ_{i∈S} x_i ≥ v(S) - ε

Where:
  v(S) = value of coalition S
  ε    = tolerance parameter (small positive number)
```

**Interpretation:**
- No subset S can "block" by doing significantly better alone
- Prevents coalition from defecting
- ε allows small violations for computational tractability

**Blocking Coalition:**
```
A coalition S blocks allocation x if:
  v(S) - ε > Σ_{i∈S} x_i

Meaning: S can achieve more value by defecting
```

**Algorithm:**
```
For each subset S ⊆ N (2^n subsets):
  1. Compute Σ_{i∈S} x_i (current allocation to S)
  2. Get v(S) from characteristic function
  3. Check: v(S) - ε > Σ_{i∈S} x_i
  4. If true: S is blocking, allocation unstable
```

**Complexity:** O(2^n) - requires n ≤ MaxCoalitionSize = 8

**Resolution:**
```
If blocking coalition found:
  1. Transfer resources from non-members to members
  2. Increase allocation to blocking members
  3. Decrease allocation to non-blocking members
  4. Re-check stability
```

---

### 1.5 Shapley Value

**Location:** `pkg/coalition/shapley.go`

**Mathematical Definition:**
```
φ_i(v) = Σ_{S⊆N\{i}} [|S|!(n-|S|-1)!/n!] · [v(S∪{i}) - v(S)]

Where:
  φ_i(v)        = Shapley value for player i
  v(S∪{i})      = value with i in coalition S
  v(S)          = value without i
  v(S∪{i})-v(S) = marginal contribution of i to S
```

**Interpretation:**
- **Fair attribution** of coalition value to individual members
- Weighted average of marginal contributions across all orderings
- Unique solution satisfying fairness axioms

**Axioms:**

1. **Efficiency:** `Σ_i φ_i = v(N)` (total value distributed)
2. **Symmetry:** Symmetric players get equal value
3. **Dummy:** Zero marginal contribution → zero value
4. **Additivity:** `φ(v+w) = φ(v) + φ(w)`

**Monte Carlo Approximation:**
```
Algorithm (O(k·n) vs exact O(2^n)):
  For sample = 1 to k:
    1. Generate random permutation π of players
    2. For each player i in order π:
       - S = players before i in π
       - marginal_i = v(S∪{i}) - v(S)
       - shapley[i] += marginal_i
  
  Return: shapley[i] / k for all i
```

**Credit System:**
```
Credit_i = Shapley_value_i - Actual_consumption_i

Positive credit: Helped more than consumed (earns credits)
Negative credit: Consumed more than helped (spends credits)

During contention:
  Allocation adjustment = ±10% based on credit balance
```

---

## 2. Bargaining Theory

### 2.1 Disagreement Point

**Concept:** Minimum allocation each agent gets if bargaining fails

**Implementation:**
```
d_i = baseline_i
    = max(
        annotation "mbcas.io/min-cpu",
        QoS-based default,
        100m  // absolute minimum
      )

QoS-based defaults:
  - Guaranteed: original request
  - Burstable:  baseline (100m)
  - BestEffort: baseline / 2 (50m)
```

**Economic Interpretation:**
- **Outside option:** What agent gets by going alone
- **Reservation utility:** Minimum acceptable outcome
- **Ensures participation:** Agents won't join if allocation < d_i

---

### 2.2 Bargaining Power (Weights)

**Location:** `pkg/allocation/market.go`

**Weight Calculation:**
```
w_i = (original_request_i / Σ original_request_j) × n

Normalized so: Σ w_i = n (number of agents)
```

**Interpretation:**
- Higher request → higher weight → more bargaining power
- Reflects "importance" or "priority" of the workload
- Used in Nash product: (x_i - d_i)^w_i

**Alternative Weight Sources:**
1. Kubernetes Priority Class
2. Annotation `mbcas.io/weight`
3. SLO strictness (tighter SLO → higher weight)

---

### 2.3 Surplus Division

**Concept:** How to divide resources above baselines

**Nash Approach:**
```
Surplus_i = x_i - d_i

Weighted Nash:
  surplus_i ∝ w_i
  
Equal weights (w_i = 1):
  surplus_i = surplus_j  (equal division)
```

**Kalai-Smorodinsky Approach:**
```
Proportional to gain range:
  surplus_i / (u_i - d_i) = λ  (constant for all i)
```

---

## 3. Economic Principles

### 3.1 Shadow Prices (Lagrange Multipliers)

**Location:** `pkg/price/price_signal.go`

**Mathematical Foundation:**
```
Lagrangian:
  L = Σ w_i log(x_i - d_i) - λ(Σ x_i - C)

First-order condition (KKT):
  ∂L/∂x_i = w_i/(x_i - d_i) - λ = 0

Shadow price:
  λ = w_i / (x_i - d_i)  (for uncapped agents)
```

**Economic Interpretation:**
- **λ = marginal value** of capacity
- **Price per millicore** of CPU
- **Scarcity signal:** High λ → high contention

**Computation:**
```
If uncapped agent exists:
  λ = w_i / (x_i - d_i)
Else (all capped):
  λ = (Σ allocations / capacity) × scale_factor
```

**Price Propagation:**
```
HTTP Headers:
  X-Price-CPU:  λ_cpu
  X-Price-Mem:  λ_cpu × 0.5  (memory cheaper)
  X-Price-Conn: λ_cpu × 2.0  (connections scarcer)
```

---

### 3.2 Market Clearing

**Location:** `pkg/allocation/market.go`

**Concept:** Find allocation where supply = demand

**Three Modes:**

1. **Uncongested (Σ needs ≤ capacity)**
   ```
   Everyone gets what they need
   x_i = need_i
   Price: λ ≈ 0 (abundant resources)
   ```

2. **Congested (Σ needs > capacity)**
   ```
   Nash Bargaining applied
   x_i from NBS solution
   Price: λ > 0 (scarcity)
   ```

3. **Overloaded (Σ baselines > capacity)**
   ```
   Emergency: scale baselines
   x_i = (d_i × w_i / Σ(d_j × w_j)) × C
   Price: λ → ∞ (crisis)
   ```

**Need Computation:**
```
need_i = actual_usage_i × (1 + headroom_factor)

headroom_factor = 0.05  (5% safety margin)

Clamped to: [baseline_i, max_i]
```

---

### 3.3 Demand Response

**Location:** `pkg/price/price_signal.go`

**Concept:** Agents adjust demand based on price signals

**Algorithm:**
```
Optimal demand: marginal_utility = price

Adjustment:
  Δdemand = elasticity × (marginal_utility - price)
  
  new_demand = current_demand + Δdemand

Bounded: max ±20% per epoch
```

**Elasticity:**
- **Elastic (e ≈ 1):** Highly responsive to price
- **Inelastic (e ≈ 0):** Insensitive to price (critical workload)

**Economic Interpretation:**
- High price → reduce demand (back off)
- Low price → increase demand (request more)
- Enables **decentralized coordination** without central planner

---

### 3.4 Walrasian Equilibrium

**Concept:** Allocation where no agent wants to trade

**Conditions:**
```
1. Market clearing: Σ x_i = C
2. Utility maximization: Each agent maximizes u_i given price λ
3. Budget balance: λ × x_i ≤ budget_i
```

**MBCAS Implementation:**
- Nash solution is a **competitive equilibrium**
- Shadow price λ is the **equilibrium price**
- No agent can improve by changing allocation (Pareto optimal)

---

## 4. Control Theory

### 4.1 Lyapunov Stability

**Location:** `pkg/stability/lyapunov.go`

**Lyapunov Function:**
```
V = -Σ log(surplus_i) + α·Σ(SLO_violation_i)² + β·Var(surplus)

Components:
  1. Negative Nash product (for minimization)
  2. SLO violation penalty
  3. Fairness term (variance of surpluses)
```

**Stability Condition:**
```
V(t+1) ≤ V(t)  (potential must decrease)

If V increases:
  - Reduce step size (slow down)
  - Reject update (keep old allocation)
```

**Adaptive Step Size:**
```
If V decreased (good):
  step_size *= 1.1  (speed up)
  
If V increased (bad):
  step_size *= 0.5  (slow down)

Bounded: [min_step, max_step] = [0.2, 1.0]
```

**Bounded Update:**
```
new_alloc = old_alloc + step_size × (desired - old_alloc)

Congestion-aware:
  adaptive_step = step × (0.5 + 0.5 × congestion_factor)
```

---

### 4.2 Exponential Smoothing

**Location:** `pkg/agent/demand/tracker.go`

**Algorithm:**
```
smoothed(t) = α × raw(t) + (1-α) × smoothed(t-1)

Where:
  α = smoothing factor ∈ [0, 1]
  Higher α = more responsive (less smoothing)
  Lower α = more stable (more smoothing)
```

**Asymmetric Smoothing:**
```
Increase (demand rising):
  α_up = 0.3  (fast response to increases)
  
Decrease (demand falling):
  α_down = 0.1  (slow response to decreases)

Rationale: Prevent throttling (fast up), avoid waste (slow down)
```

**Cost Efficiency Mode:**
```
Reversed asymmetry:
  α_up = 0.1    (slow to increase)
  α_down = 0.3  (fast to decrease)
  
Goal: Minimize CPU allocation while maintaining SLOs
```

---

### 4.3 Kalman Filtering

**Location:** `pkg/agent/demand/predictor.go`

**State Space Model:**
```
State: [demand, velocity]

Prediction:
  demand(t+1) = demand(t) + velocity(t)
  velocity(t+1) = velocity(t)

Update (Kalman gain):
  K = P × H^T × (H × P × H^T + R)^-1
  
  state_new = state_pred + K × (measurement - H × state_pred)
```

**Purpose:**
- **Predict future demand** for bursty workloads
- **Filter noise** from cgroup measurements
- **Anticipate spikes** before they occur

---

## 5. Optimization Theory

### 5.1 Convex Optimization

**Nash Bargaining as Convex Program:**
```
max Σ_i w_i · log(x_i - d_i)

Subject to:
  Σ_i x_i ≤ C
  x_i ≥ d_i
  x_i ≤ max_i
```

**Properties:**
- **Objective:** Concave (log is concave)
- **Constraints:** Linear (convex set)
- **Solution:** Unique global optimum

**KKT Conditions:**
```
Stationarity: w_i/(x_i - d_i) = λ + μ_i
Primal feasibility: Σ x_i ≤ C, x_i ≥ d_i
Dual feasibility: λ ≥ 0, μ_i ≥ 0
Complementary slackness: μ_i(max_i - x_i) = 0
```

---

### 5.2 Primal-Dual Method

**Location:** `pkg/allocation/primal_dual.go`

**Algorithm:**
```
Initialize: x = d (baselines), λ = 0

Repeat:
  Primal step: Update allocations
    x_i = d_i + w_i/λ  (for uncapped agents)
  
  Dual step: Update price
    λ = λ + step × (Σ x_i - C)
    
Until convergence: |Σ x_i - C| < ε
```

**Interpretation:**
- **Primal:** Agents maximize utility given price
- **Dual:** Price adjusts to clear market
- **Converges** to Nash equilibrium

---

### 5.3 Water-Filling Algorithm

**Concept:** Fill "containers" (agents) proportionally until capacity exhausted

**Analogy:**
```
Agents = containers with different widths (weights)
Capacity = total water available
Fill level = allocation

Pour water: fills wider containers faster (higher weight)
Stop when: total water = capacity OR all containers full
```

**Implementation:**
```
1. Start with baselines (minimum water level)
2. Add surplus proportional to width (weight)
3. Cap overflowing containers (at max)
4. Redistribute overflow to uncapped containers
```

---

## 6. Mechanism Design

### 6.1 Incentive Compatibility

**Concept:** Agents should report true demand (no gaming)

**MBCAS Approach:**
- **Demand sensing** from kernel signals (cgroup throttling)
- **Cannot be gamed:** Agents can't fake throttling
- **Truthful by design:** No strategic reporting needed

**Contrast with VPA:**
- VPA uses historical percentiles (can be gamed)
- Agents can inflate usage to get more resources

---

### 6.2 Strategy-Proofness

**Definition:** Truthful reporting is dominant strategy

**MBCAS Properties:**
- **Kernel-based demand:** No strategic reporting possible
- **Nash solution:** Incentive-compatible for cooperative games
- **Shadow prices:** Encourage efficient resource use

---

### 6.3 Vickrey-Clarke-Groves (VCG) Mechanism

**Concept:** Charge agents for externality they impose

**VCG Payment:**
```
payment_i = Σ_{j≠i} v_j(x_{-i}) - Σ_{j≠i} v_j(x)

Where:
  x    = allocation with i
  x_{-i} = allocation without i
  
Interpretation: i pays for harm to others
```

**MBCAS Future Work:**
- Credit system approximates VCG
- Shapley value = fair cost allocation
- Could implement explicit VCG payments

---

## 7. Mathematical Foundations

### 7.1 Utility Functions

**Location:** `pkg/allocation/utility.go`

**General Form:**
```
u_i(x) = w_i · SLOScore_i(x) - λ_cpu · x_cpu - λ_mem · x_mem

Components:
  1. SLO satisfaction (benefit)
  2. Resource cost (shadow prices)
```

**SLO Score (Sigmoid):**
```
SLOScore = 1 / (1 + exp(k · (latency - target)))

Where:
  k = sensitivity (steepness)
  latency = current p99 latency
  target = SLO target latency

Properties:
  - Bounded: [0, 1]
  - Smooth: differentiable
  - Concave: diminishing returns
```

**Marginal Utility:**
```
∂u/∂x_cpu = w_i · (∂SLO/∂x_cpu) - λ_cpu

Using queueing theory:
  Latency = BaseLatency / (1 - utilization)
  utilization = usage / allocation
  
  ∂Latency/∂CPU = -BaseLatency · load / (CPU² · (1-ρ)²)
```

---

### 7.2 Queueing Theory

**M/M/1 Queue Model:**
```
Latency = 1 / (μ - λ)

Where:
  μ = service rate (CPU capacity)
  λ = arrival rate (load)
  ρ = λ/μ (utilization)

Approximation:
  Latency ≈ BaseLatency / (1 - ρ)
```

**Application in MBCAS:**
```
utilization = actual_usage / allocated_cpu

If ρ → 1: Latency → ∞ (saturation)
If ρ → 0: Latency → BaseLatency (idle)
```

---

### 7.3 Information Theory

**Entropy (Fairness Measure):**
```
H = -Σ p_i log(p_i)

Where:
  p_i = surplus_i / Σ surplus_j

Maximum entropy = equal surpluses (most fair)
```

**Kullback-Leibler Divergence:**
```
D_KL(P || Q) = Σ p_i log(p_i / q_i)

Measures: distance from current allocation to ideal
```

---

### 7.4 Probability Theory

**Monte Carlo Sampling (Shapley):**
```
E[φ_i] = (1/k) Σ_{s=1}^k marginal_contribution_i^(s)

Convergence: O(1/√k) by Central Limit Theorem
```

**Confidence Intervals:**
```
φ_i ± 1.96 × σ/√k  (95% confidence)

Where σ = standard deviation of marginal contributions
```

---

## Summary Table: Game Theory Concepts

| Concept | Theory | Location | Purpose |
|---------|--------|----------|---------|
| **Nash Bargaining** | Cooperative Game Theory | `nash_bargaining.go` | Fair surplus distribution |
| **Kalai-Smorodinsky** | Bargaining Theory | `kalai_smorodinsky.go` | Proportional fairness |
| **Shapley Value** | Cooperative Game Theory | `shapley.go` | Credit attribution |
| **Coalition Formation** | Cooperative Game Theory | `coalition.go` | Service cooperation |
| **ε-Core Stability** | Cooperative Game Theory | `coalition.go` | Prevent defection |
| **Shadow Prices** | Economics (Lagrange) | `price_signal.go` | Scarcity signals |
| **Market Clearing** | Economics | `market.go` | Supply-demand balance |
| **Demand Response** | Economics | `price_signal.go` | Price-based adjustment |
| **Lyapunov Stability** | Control Theory | `lyapunov.go` | Convergence guarantee |
| **Exponential Smoothing** | Control Theory | `demand/tracker.go` | Noise filtering |
| **Kalman Filtering** | Control Theory | `demand/predictor.go` | Demand prediction |
| **Utility Functions** | Microeconomics | `utility.go` | Agent preferences |
| **Convex Optimization** | Optimization Theory | `nash_bargaining.go` | Global optimum |
| **Primal-Dual** | Optimization Theory | `primal_dual.go` | Market equilibrium |
| **Water-Filling** | Optimization Theory | `nash_bargaining.go` | Resource allocation |

---

## Academic References

### Foundational Papers

1. **Nash, J. (1950).** "The Bargaining Problem." *Econometrica*, 18(2), 155-162.
   - Original Nash Bargaining Solution

2. **Kalai, E., & Smorodinsky, M. (1975).** "Other Solutions to Nash's Bargaining Problem." *Econometrica*, 43(3), 513-518.
   - Kalai-Smorodinsky Solution

3. **Shapley, L. S. (1953).** "A Value for n-Person Games." *Contributions to the Theory of Games*, 2(28), 307-317.
   - Shapley Value

4. **Gillies, D. B. (1959).** "Solutions to General Non-Zero-Sum Games." *Contributions to the Theory of Games*, 4, 47-85.
   - Core concept

5. **Lyapunov, A. M. (1892).** "The General Problem of the Stability of Motion."
   - Lyapunov stability theory

6. **Kalman, R. E. (1960).** "A New Approach to Linear Filtering and Prediction Problems." *Journal of Basic Engineering*, 82(1), 35-45.
   - Kalman filtering

7. **Vickrey, W. (1961).** "Counterspeculation, Auctions, and Competitive Sealed Tenders." *Journal of Finance*, 16(1), 8-37.
   - VCG mechanism

---

## Implementation Status

| Concept | Status | Notes |
|---------|--------|-------|
| Nash Bargaining | ✅ Implemented | Core allocation algorithm |
| Kalai-Smorodinsky | ✅ Implemented | Alternative solver |
| Shadow Prices | ✅ Implemented | Computed after allocation |
| Lyapunov Stability | ✅ Implemented | Adaptive step size |
| Exponential Smoothing | ✅ Implemented | Demand tracking |
| Utility Functions | ✅ Implemented | SLO-based utilities |
| Shapley Value | ✅ Implemented | Credit system |
| Coalition Formation | ⚠️ Partial | Disabled (no tracing) |
| ε-Core Stability | ⚠️ Partial | Implemented but not active |
| Kalman Filtering | ⚠️ Optional | Configurable feature |
| Primal-Dual | ⚠️ Partial | Alternative to water-filling |
| VCG Mechanism | ❌ Future Work | Planned enhancement |

---

## Conclusion

MBCAS implements a **rich set of game-theoretic and economic principles** to achieve fair, efficient, and stable CPU allocation in Kubernetes. The system combines:

- **Cooperative game theory** (Nash, Shapley, coalitions)
- **Economic mechanisms** (shadow prices, market clearing)
- **Control theory** (Lyapunov stability, Kalman filtering)
- **Optimization theory** (convex programming, primal-dual)

This multi-disciplinary approach provides **provable fairness**, **economic efficiency**, and **convergence guarantees** that traditional autoscalers lack.
