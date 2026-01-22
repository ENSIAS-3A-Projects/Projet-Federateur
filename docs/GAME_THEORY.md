# Game Theory Concepts in MBCAS

This document explains the game-theoretic and agent-based modeling concepts used in MBCAS.

---

## Table of Contents

1. [Nash Bargaining Solution](#nash-bargaining-solution)
2. [Pareto Efficiency](#pareto-efficiency)
3. [Agent-Based Modeling (ABM)](#agent-based-modeling-abm)
4. [Q-Learning for Resource Allocation](#q-learning-for-resource-allocation)
5. [Why This Combination Works](#why-this-combination-works)

---

## Nash Bargaining Solution

### What is it?

The **Nash Bargaining Solution** is a method from cooperative game theory that determines how rational agents should divide a resource when they can negotiate.

### The Problem

Given:
- A set of agents (pods)
- A divisible resource (CPU)
- Each agent has a **disagreement point** (minimum they need to survive)
- Each agent has a **utility function** (how much they value additional CPU)

Find: An allocation that is **fair** and **efficient**.

### The Solution

Nash proved there is a unique allocation that maximizes the **Nash product**:

```
Maximize: ∏ (utility_i - disagreement_i)^weight_i

Subject to:
- Σ allocation_i ≤ capacity
- allocation_i ≥ disagreement_i  (everyone gets at least minimum)
```

### In MBCAS

```
disagreement_i = baseline (100m minimum CPU)
utility_i = allocation_i (linear utility)
weight_i = pod priority/importance

Nash Product = ∏ (allocation_i - baseline_i)^weight_i
```

**Example**:
```
Pod A: weight=2.0, baseline=100m
Pod B: weight=1.0, baseline=100m
Capacity: 1000m

Surplus = 1000m - 200m = 800m

Maximize: (alloc_A - 100)^2.0 × (alloc_B - 100)^1.0

Solution:
  alloc_A = 100 + (2.0/3.0)*800 = 633m
  alloc_B = 100 + (1.0/3.0)*800 = 367m
```

### Shadow Price Feedback

The Nash solver computes a **shadow price** (Lagrange multiplier) that indicates resource scarcity:

```
if totalDemand > capacity:
    congestionRatio = (totalDemand - capacity) / capacity
    shadowPrice = congestionRatio * (averageWeight)
else:
    shadowPrice = 0.0  # Uncongested
```

This shadow price is fed back to agents, enabling **price-responsive bidding**:
- **High shadow price (>0.3)**: Resources scarce → Agents reduce demand
- **Low shadow price (<0.3)**: Resources abundant → Agents can increase demand
- **Zero shadow price**: Uncongested → No adjustment needed

This creates a **market mechanism** where agents respond to price signals, improving efficiency.

### Nash Axioms

The Nash Bargaining Solution satisfies four axioms:

1. **Pareto Efficiency**: No waste (can't improve anyone without hurting someone else)
2. **Symmetry**: Identical agents get identical allocations
3. **Independence of Irrelevant Alternatives (IIA)**: Adding/removing an agent doesn't unfairly change others
4. **Invariance to Affine Transformations**: Scaling utilities doesn't change fairness

---

## Pareto Efficiency

### Definition

An allocation is **Pareto efficient** if you cannot make any agent better off without making at least one other agent worse off.

### Visual Representation

```
CPU for Pod B
    ↑
    │     ╱╲  ← Pareto Frontier
    │    ╱  ╲    (all efficient allocations)
    │   ╱ ★  ╲
    │  ╱      ╲  ★ = Nash Solution
    │ ╱        ╲
    │╱__________╲→ CPU for Pod A
    
Feasible Region (below frontier)
```

**Points on the frontier**: Pareto efficient  
**Points inside**: Wasteful (could give more to someone)  
**Nash point (★)**: The unique fair point on the frontier

### Why Pareto Efficiency Matters

- **No waste**: Every CPU cycle is used productively
- **Optimality**: Cannot improve the system without trade-offs
- **Fairness**: Combined with Nash, ensures fair distribution

### In MBCAS

The Nash solver guarantees Pareto efficiency by:
1. Allocating all available CPU (no waste)
2. Respecting capacity constraints
3. Maximizing collective welfare (Nash product)

---

## Agent-Based Modeling (ABM)

### What is ABM?

**Agent-Based Modeling** is a computational approach where:
- The system consists of **autonomous agents**
- Each agent has **local observations** and **decision rules**
- **Emergent behavior** arises from agent interactions
- No central controller dictates behavior

### Agents in MBCAS

Each pod has a **PodAgent** that:
- **Observes**: Own CPU usage, throttling, allocation
- **Decides**: Which bidding strategy to use
- **Learns**: Adapts strategy based on outcomes
- **Acts**: Submits bid to Nash solver

### Key ABM Principles

1. **Autonomy**: Each agent makes independent decisions
2. **Local Information**: Agents only see their own metrics
3. **Adaptation**: Agents learn from experience
4. **Emergence**: System-wide efficiency emerges without central planning

### Heterogeneous Agents

Agents differ in:
- **SLO targets**: Some pods are latency-sensitive
- **Weights**: Different priorities/importance
- **Learning rates**: Some adapt faster than others
- **Exploration**: Different risk tolerances

---

## Q-Learning for Resource Allocation

### What is Q-Learning?

**Q-Learning** is a reinforcement learning algorithm where an agent learns a **quality function** Q(s,a) that estimates the expected reward for taking action `a` in state `s`.

### The Q-Function

```
Q(state, action) → expected cumulative reward

Example:
Q("high usage, high throttling, low allocation", "aggressive bid") = 8.5
Q("low usage, no throttling, adequate allocation", "conservative bid") = 12.0
```

### Learning Update Rule

```
Q(s,a) ← Q(s,a) + α[r + γ·max Q(s',a') - Q(s,a)]
                     └─────┬─────┘
                      TD Error (temporal difference)

Where:
- α = learning rate (0.1) - how fast to learn
- γ = discount factor (0.9) - value of future rewards
- r = immediate reward
- s' = next state
- max Q(s',a') = best future reward
```

### State Space in MBCAS

**Discrete states** (3 × 3 × 3 = 27 total states):

```
Usage Level:
- low: usage ≤ 500m
- medium: 500m < usage ≤ 1000m
- high: usage > 1000m

Throttle Level:
- none: throttling ≤ 0.1
- some: 0.1 < throttling ≤ 0.3
- high: throttling > 0.3

Allocation Level:
- low: allocation < usage
- adequate: usage ≤ allocation ≤ 2×usage
- excess: allocation > 2×usage
```

**State encoding**: `"usage:throttle:allocation"`  
Example: `"high:some:low"` → pod is heavily used, throttled, under-allocated

### Action Space

Three bidding strategies:

```
┌─────────────┬──────────────┬─────────────┬─────────────┐
│ Action      │ Demand Mult  │ Weight Mult │ When to Use │
├─────────────┼──────────────┼─────────────┼─────────────┤
│ aggressive  │ 1.5×         │ 1.2×        │ Throttled   │
│ normal      │ 1.2×         │ 1.0×        │ Balanced    │
│ conservative│ 1.0×         │ 0.8×        │ Idle        │
└─────────────┴──────────────┴─────────────┴─────────────┘
```

### Reward Function

```go
reward = 0.0

// Positive: Meeting demand
if allocation >= usage:
    reward += 10.0
else:
    shortfall = (usage - allocation) / usage
    reward -= shortfall * 20.0

// Penalty: Throttling
reward -= throttling * 30.0

// Heavy penalty: SLO violations
if sloViolation:
    reward -= 100.0

// Small penalty: Over-allocation (waste)
if allocation > usage*2:
    waste = (allocation - usage*2) / usage
    reward -= waste * 5.0

// Bonus: Zero throttling
if throttling < 0.01:
    reward += 5.0
```

**Design rationale**:
- Throttling is heavily penalized (30×)
- SLO violations are catastrophic (100 penalty)
- Waste is discouraged but not critical (5×)
- Zero throttling is rewarded (encourages stability)

### Exploration vs Exploitation

**ε-greedy policy**:

```python
if random() < epsilon:
    action = random_action()  # Explore
else:
    action = argmax Q(state, a)  # Exploit
```

**Exploration decay**:
```
epsilon = epsilon * 0.999  (each update)
epsilon_min = 0.01  (always explore 1%)
```

Early on: High exploration (try different strategies)  
Over time: Low exploration (use learned optimal strategy)

### Shadow Price Integration

Agents adjust their action selection based on shadow price:

```go
// Adjust Q-values by shadow price
if shadowPrice > 0.3:
    if action == "aggressive":
        q -= shadowPrice * 5.0  // Penalize aggressive when scarce
    else if action == "conservative":
        q += shadowPrice * 2.0  // Reward conservative when scarce

// Select best action from adjusted Q-values
action = argmax adjustedQ(state, a)
```

Additionally, agents reduce demand when shadow price is high:

```go
if shadowPrice > 0.3:
    reductionFactor = 1.0 - (shadowPrice * 0.5)  // Max 50% reduction
    demand = demand * reductionFactor
```

This creates a **price-taking behavior** where agents respond to market conditions.

### Learning Example

```
Cycle 1:
  State: "high:high:low"
  Action: "aggressive" (random exploration)
  Allocation: 800m
  Outcome: Throttling reduced to 0.2
  Reward: +5.0
  Update: Q("high:high:low", "aggressive") += 0.1 * [5.0 + 0.9*max(...) - Q(...)]

Cycle 2:
  State: "high:some:adequate"
  Action: "aggressive" (exploit - highest Q)
  Allocation: 900m
  Outcome: No throttling
  Reward: +15.0
  Update: Q("high:some:adequate", "aggressive") += 0.1 * [15.0 + ...]

Cycle 10:
  State: "high:high:low"
  Action: "aggressive" (learned - Q-value is now high)
  Allocation: 850m
  Outcome: Throttling eliminated
  Reward: +20.0
  → Agent has learned: "When throttled, bid aggressively"
```

---

## Why This Combination Works

### ABM + Game Theory = Adaptive Fairness

```
┌─────────────────────────────────────────────────────────┐
│                                                         │
│  Agent-Based Modeling          Game Theory             │
│  (Individual Learning)         (Collective Fairness)   │
│                                                         │
│  ┌──────────────┐              ┌──────────────┐        │
│  │  PodAgents   │              │    Nash      │        │
│  │  Learn to    │──── Bids ───►│  Bargaining  │        │
│  │  Bid Optimally│              │  Solver      │        │
│  └──────┬───────┘              └──────┬───────┘        │
│         │                              │                │
│         │ Rewards                      │ Allocations    │
│         │                              │ Shadow Price   │
│         │                              │                │
│         └──────────────────────────────┘                │
│                                                         │
│  Result: Agents learn to bid truthfully because        │
│          Nash ensures fair treatment                   │
│          Shadow prices guide demand adjustment         │
└─────────────────────────────────────────────────────────┘
```

### Two-Pass Bidding with Shadow Price Feedback

The system uses a **two-pass bidding mechanism**:

**Pass 1: Initial Bids**
- Agents compute bids without shadow price
- Nash solver computes preview allocation and shadow price

**Pass 2: Adjusted Bids**
- Agents receive shadow price feedback
- Adjust action selection (Q-values) and demand based on price
- Nash solver computes final allocation

This creates a **market-clearing mechanism** where:
1. Initial bids reveal demand
2. Shadow price signals scarcity
3. Agents adjust bids based on price
4. Final allocation clears the market efficiently

### Complementary Strengths

| Aspect | ABM Provides | Game Theory Provides |
|--------|-------------|---------------------|
| **Adaptation** | Agents learn from experience | - |
| **Fairness** | - | Nash ensures equitable distribution |
| **Efficiency** | Agents optimize locally | Nash ensures global Pareto efficiency |
| **Robustness** | Agents adapt to changing workloads | Nash handles resource scarcity |
| **Incentives** | - | Truthful bidding is optimal |
| **Price Signals** | Agents respond to shadow prices | Shadow price indicates market conditions |
| **Market Clearing** | Price-responsive demand | Two-pass bidding clears market |

### Emergent Properties

1. **Self-Stabilizing**: System converges to equilibrium without central control
2. **Adaptive**: Responds to workload changes via learning
3. **Fair**: Nash guarantees no agent is exploited
4. **Efficient**: Pareto optimal allocation, no waste

### Theoretical Guarantees

**From Game Theory**:
- ✅ Pareto efficiency (no waste)
- ✅ Fairness (Nash axioms)
- ✅ Unique equilibrium exists

**From Reinforcement Learning**:
- ✅ Q-Learning converges to optimal policy (under conditions)
- ✅ ε-greedy ensures exploration
- ✅ Discount factor balances short/long-term rewards

---

## Comparison to Alternatives

### vs. Proportional Fair Share

```
Fair Share: allocation_i = (request_i / Σ request_j) * capacity

Problems:
- Static requests don't reflect actual demand
- No learning or adaptation
- Can waste resources on idle pods

MBCAS: Bids reflect real-time demand, agents learn optimal requests
```

### vs. Priority-Based Scheduling

```
Priority: Sort by priority, allocate greedily

Problems:
- Low-priority pods can starve
- No fairness guarantees
- Doesn't account for actual usage

MBCAS: Nash ensures everyone gets baseline, surplus distributed fairly
```

### vs. Vertical Pod Autoscaler (VPA)

```
VPA: Analyze historical usage, recommend static limits

Problems:
- Slow to react (minutes to hours)
- Requires pod restarts
- No coordination between pods

MBCAS: Real-time demand sensing, in-place updates, Nash coordination
```

---

## Mathematical Formulation

### The Complete Optimization Problem

```
Given:
- n pods with agents
- Capacity C (total CPU)
- Each pod i has:
  - Baseline b_i (minimum CPU)
  - Weight w_i (priority)
  - Demand d_i (from Q-Learning bid, adjusted by shadow price)

Solve:
  maximize  ∏ (x_i - b_i)^w_i
  
  subject to:
    Σ x_i ≤ C           (capacity constraint)
    x_i ≥ b_i  ∀i       (baseline guarantee)
    x_i ≤ max_i  ∀i     (maximum cap)

Where:
  x_i = CPU allocation to pod i
```

### Lagrangian Solution

```
L = Σ w_i·log(x_i - b_i) - λ(Σ x_i - C)

∂L/∂x_i = w_i/(x_i - b_i) - λ = 0

⟹ x_i = b_i + w_i/λ

Solving for λ using Σ x_i = C:
  λ = Σ w_i / (C - Σ b_i)

Final allocation:
  x_i = b_i + w_i · (C - Σ b_i) / Σ w_j
```

The **Lagrange multiplier λ** is the **shadow price**:
- **High λ**: Resources scarce, agents should reduce demand
- **Low λ**: Resources abundant, agents can increase demand
- **Zero λ**: Uncongested, no adjustment needed

### Shadow Price Computation

```
if Σ d_i > C:  // Congested
    congestionRatio = (Σ d_i - C) / C
    shadowPrice = congestionRatio * (Σ w_i / n)
else:  // Uncongested
    shadowPrice = 0.0
```

This shadow price is fed back to agents in the two-pass bidding mechanism, creating a **market-clearing equilibrium**. The Nash solver computes allocations exactly as shown above!

---

## References

### Game Theory
- Nash, J. (1950). "The Bargaining Problem". *Econometrica*, 18(2), 155-162.
- Osborne, M. J., & Rubinstein, A. (1994). *A Course in Game Theory*. MIT Press.

### Reinforcement Learning
- Watkins, C. J., & Dayan, P. (1992). "Q-learning". *Machine Learning*, 8(3-4), 279-292.
- Sutton, R. S., & Barto, A. G. (2018). *Reinforcement Learning: An Introduction*. MIT Press.

### Agent-Based Modeling
- Wooldridge, M. (2009). *An Introduction to MultiAgent Systems*. Wiley.
- Bonabeau, E. (2002). "Agent-based modeling: Methods and techniques for simulating human systems". *PNAS*, 99(3), 7280-7287.

### Resource Allocation
- Ghodsi, A., et al. (2011). "Dominant Resource Fairness: Fair Allocation of Multiple Resource Types". *NSDI*.
- Grandl, R., et al. (2014). "Multi-resource packing for cluster schedulers". *SIGCOMM*.
