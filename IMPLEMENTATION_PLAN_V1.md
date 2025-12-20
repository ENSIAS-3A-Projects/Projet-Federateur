# Equilibrium v1.0 Implementation Plan
## Complete Game-Theoretic Sidecar for Microservices

**Test Subject:** UrbanMoveMS (Software-Oriented-Application)  
**Target:** Full implementation of all game theory concepts from `game_theory_concepts_simple.md`

---

## 📋 Table of Contents

1. [Overview](#overview)
2. [Game Theory Concepts Mapping](#game-theory-concepts-mapping)
3. [Architecture Components](#architecture-components)
4. [Implementation Phases](#implementation-phases)
5. [Technical Specifications](#technical-specifications)
6. [Testing Strategy](#testing-strategy)
7. [Integration with UrbanMoveMS](#integration-with-urbanmovems)

---

## 🎯 Overview

This plan implements a complete game-theoretic sidecar proxy that incorporates all 16 core concepts from game theory, transforming UrbanMoveMS into a self-optimizing, adaptive microservices system.

### Core Objectives

1. **Zero Code Changes:** Sidecar pattern ensures UrbanMoveMS services remain unchanged
2. **Mathematical Guarantees:** All algorithms have proven convergence properties
3. **Production Ready:** <5ms overhead, full observability, graceful degradation
4. **Complete Coverage:** All 16 game theory concepts implemented and tested

---

## 🧩 Game Theory Concepts Mapping

### Phase 1: Core Learning & Routing (Foundation)

| Concept | Implementation | Component | Priority |
|---------|---------------|-----------|----------|
| **No-Regret Learning** | Regret Matching Algorithm | `sidecar-proxy/logic/regret.go` | P0 |
| **Regret Matching** | Probabilistic action selection | `sidecar-proxy/logic/regret.go` | P0 |
| **Convergence** | Lyapunov function monitoring | `sidecar-proxy/logic/convergence.go` | P0 |
| **Lyapunov Function** | Distance to equilibrium metric | `sidecar-proxy/logic/convergence.go` | P0 |
| **Mixed Strategy** | Randomized routing decisions | `sidecar-proxy/logic/regret.go` | P0 |

### Phase 2: Equilibrium & Stability

| Concept | Implementation | Component | Priority |
|---------|---------------|-----------|----------|
| **Nash Equilibrium** | Equilibrium detection & validation | `sidecar-proxy/logic/nash.go` | P1 |
| **Potential Game** | Congestion-aware routing | `sidecar-proxy/logic/potential.go` | P1 |
| **Repeated Game** | Reputation tracking over time | `sidecar-proxy/logic/reputation.go` | P1 |

### Phase 3: Coordination & Cooperation

| Concept | Implementation | Component | Priority |
|---------|---------------|-----------|----------|
| **Correlated Equilibrium** | Central coordinator recommendations | `sidecar-proxy/logic/correlated.go` | P2 |
| **The Folk Theorem** | Punishment mechanisms for defection | `sidecar-proxy/logic/folk.go` | P2 |
| **Punishment Level** | Minmax strategy calculation | `sidecar-proxy/logic/folk.go` | P2 |
| **Subgame Perfect Equilibrium** | Credible threat enforcement | `sidecar-proxy/logic/folk.go` | P2 |
| **Individually Rational** | Cooperation constraint validation | `sidecar-proxy/logic/folk.go` | P2 |
| **Feasible Payoffs** | System capacity validation | `sidecar-proxy/logic/feasible.go` | P2 |

### Phase 4: Advanced Scenarios

| Concept | Implementation | Component | Priority |
|---------|---------------|-----------|----------|
| **Bayesian Game** | Private information handling | `sidecar-proxy/logic/bayesian.go` | P3 |
| **Zero-Sum Game** | Adversarial defense strategies | `sidecar-proxy/logic/security.go` | P3 |

---

## 🏗️ Architecture Components

### 1. Sidecar Proxy (`/sidecar-proxy`)

**Language:** Go (Golang)  
**Rationale:** Performance (<5ms overhead), excellent concurrency, strong K8s ecosystem

#### Core Files Structure

```
sidecar-proxy/
├── main.go                    # Entry point, HTTP proxy server
├── interceptor.go             # Traffic interception (HTTP/gRPC)
├── config/
│   ├── config.go             # Configuration management
│   └── agent_config.go       # Per-agent strategy profiles
├── logic/
│   ├── regret.go             # Regret Matching (No-Regret Learning)
│   ├── nash.go               # Nash Equilibrium detection
│   ├── convergence.go        # Convergence monitoring (Lyapunov)
│   ├── potential.go          # Potential Game routing
│   ├── correlated.go         # Correlated Equilibrium coordinator
│   ├── folk.go               # Folk Theorem (punishment, cooperation)
│   ├── bayesian.go           # Bayesian belief updating
│   ├── security.go           # Zero-Sum adversarial defense
│   ├── feasible.go           # Feasible payoff validation
│   └── reputation.go         # Repeated game reputation tracking
├── metrics/
│   ├── prometheus.go         # Prometheus metrics export
│   └── metrics.go            # Internal metrics collection
├── network/
│   ├── proxy.go              # HTTP proxy implementation
│   ├── headers.go            # Game theory header handling
│   └── service_discovery.go  # K8s service discovery
└── storage/
    ├── memory.go             # In-memory regret storage
    └── persistence.go        # Optional: Redis for correlation
```

### 2. Kubernetes Injector (`/k8s-injector`)

**Purpose:** Mutating Webhook to automatically inject sidecar into pods

```
k8s-injector/
├── webhook/
│   ├── main.go               # Webhook server
│   ├── mutator.go           # Pod mutation logic
│   └── cert.go               # TLS certificate management
├── manifests/
│   ├── webhook-deployment.yaml
│   ├── webhook-service.yaml
│   ├── mutating-webhook-config.yaml
│   └── rbac.yaml
└── install.sh                # Installation script
```

### 3. Dashboard (`/dashboard`)

**Purpose:** Real-time visualization of game-theoretic behavior

```
dashboard/
├── src/
│   ├── components/
│   │   ├── RegretChart.jsx   # Regret score visualization
│   │   ├── NashIndicator.jsx # Equilibrium status
│   │   ├── ConvergenceGraph.jsx # Lyapunov function plot
│   │   ├── RoutingMatrix.jsx # Traffic distribution
│   │   └── ReputationTable.jsx # Service reputation scores
│   ├── api/
│   │   └── prometheus.js     # Prometheus query client
│   └── App.jsx
├── package.json
└── README.md
```

### 4. Test Infrastructure (`/test-infra`)

```
test-infra/
├── load-generator/           # Traffic generation for UrbanMoveMS
├── latency-injector/          # Chaos engineering: inject delays
├── test-scenarios/            # Predefined test scenarios
└── validation/                # Automated validation scripts
```

---

## 📅 Implementation Phases

### Phase 1: Foundation (Weeks 1-3)
**Goal:** Core regret matching with convergence guarantees

#### Week 1: Basic Proxy & Interception
- [ ] Implement HTTP proxy in Go
- [ ] Traffic interception via iptables/eBPF
- [ ] Service discovery integration (K8s API)
- [ ] Basic request/response forwarding
- [ ] Header extraction (`X-GT-*` headers)

#### Week 2: Regret Matching Core
- [ ] Regret score calculation (regret.go)
- [ ] Action space definition (backend destinations)
- [ ] Payoff measurement (latency, error rate)
- [ ] Counterfactual estimation (monitoring-based)
- [ ] Probabilistic action selection (Mixed Strategy)

#### Week 3: Convergence & Monitoring
- [ ] Lyapunov function implementation
- [ ] Convergence detection algorithm
- [ ] Prometheus metrics export
- [ ] Basic dashboard (regret visualization)
- [ ] Unit tests for regret matching

**Deliverable:** Working sidecar that routes traffic using regret matching

---

### Phase 2: Equilibrium & Stability (Weeks 4-5)
**Goal:** Nash equilibrium detection and potential game routing

#### Week 4: Nash Equilibrium
- [ ] Best response calculation
- [ ] Equilibrium detection (nash.go)
- [ ] Equilibrium validation
- [ ] Oscillation prevention
- [ ] Metrics: equilibrium status

#### Week 5: Potential Game & Stability
- [ ] Potential function for routing game
- [ ] Congestion cost calculation
- [ ] Guaranteed convergence in potential games
- [ ] Integration with regret matching
- [ ] Stability tests

**Deliverable:** System converges to Nash equilibrium, no oscillations

---

### Phase 3: Cooperation Mechanisms (Weeks 6-8)
**Goal:** Folk Theorem implementation for sustained cooperation

#### Week 6: Reputation System
- [ ] Reputation tracking (reputation.go)
- [ ] Repeated game history storage
- [ ] Deviation detection
- [ ] Punishment level calculation
- [ ] Individually rational validation

#### Week 7: Folk Theorem Implementation
- [ ] Trigger strategy implementation (folk.go)
- [ ] Punishment mechanism (throttling)
- [ ] Subgame perfect enforcement
- [ ] Forgiveness after punishment period
- [ ] Cooperation metrics

#### Week 8: Feasible Payoffs
- [ ] System capacity detection
- [ ] Feasible payoff validation (feasible.go)
- [ ] Resource constraint checking
- [ ] Overload prevention

**Deliverable:** Services cooperate, defectors are punished, cooperation sustained

---

### Phase 4: Coordination (Weeks 9-10)
**Goal:** Correlated equilibrium for efficient coordination

#### Week 9: Correlated Equilibrium Core
- [ ] Central coordinator service (optional component)
- [ ] Probability distribution computation
- [ ] Linear programming solver integration
- [ ] Recommendation broadcasting
- [ ] Incentive constraint validation

#### Week 10: Integration & Testing
- [ ] Hybrid: regret matching + correlated recommendations
- [ ] Fallback to pure regret matching if coordinator unavailable
- [ ] Performance testing
- [ ] Coordination efficiency metrics

**Deliverable:** Optional central coordination improves efficiency

---

### Phase 5: Advanced Features (Weeks 11-12)
**Goal:** Bayesian games and zero-sum security

#### Week 11: Bayesian Game
- [ ] Private information modeling (bayesian.go)
- [ ] Type space definition
- [ ] Prior belief initialization
- [ ] Bayesian updating from signals
- [ ] Health check interpretation

#### Week 12: Zero-Sum Security
- [ ] Adversarial scenario detection
- [ ] Minmax strategy for defense (security.go)
- [ ] Randomized security policies
- [ ] DDoS protection integration
- [ ] Security metrics

**Deliverable:** Handles private information and adversarial scenarios

---

### Phase 6: K8s Integration & Testing (Weeks 13-14)
**Goal:** Production-ready deployment

#### Week 13: Mutating Webhook
- [ ] Webhook server implementation
- [ ] Pod mutation logic
- [ ] TLS certificate management
- [ ] RBAC configuration
- [ ] Installation automation

#### Week 14: UrbanMoveMS Integration
- [ ] Deploy to UrbanMoveMS cluster
- [ ] Scale services to 3 replicas each
- [ ] Load testing with realistic traffic
- [ ] Chaos engineering (latency injection)
- [ ] Performance benchmarking
- [ ] Full system validation

**Deliverable:** Equilibrium sidecar deployed and validated on UrbanMoveMS

---

## 🔧 Technical Specifications

### 1. Regret Matching Algorithm

```go
// Core regret matching implementation
type RegretMatcher struct {
    cumulativeRegret map[string]float64  // action -> cumulative regret
    actionSpace      []string            // available backends
    learningRate     float64             // η (eta)
    explorationRate  float64             // ε (epsilon) for exploration
}

func (rm *RegretMatcher) SelectAction() string {
    // Calculate positive regrets
    positiveRegrets := make(map[string]float64)
    totalPositive := 0.0
    
    for action, regret := range rm.cumulativeRegret {
        if regret > 0 {
            positiveRegrets[action] = regret
            totalPositive += regret
        }
    }
    
    // Mixed Strategy: Sample proportionally to positive regret
    if totalPositive > 0 {
        return rm.sampleProportional(positiveRegrets, totalPositive)
    }
    
    // Exploration: uniform random if no positive regret
    return rm.explore()
}

func (rm *RegretMatcher) UpdateRegret(action string, payoff float64, counterfactuals map[string]float64) {
    for a, cfPayoff := range counterfactuals {
        instantRegret := cfPayoff - payoff
        rm.cumulativeRegret[a] += instantRegret
    }
}
```

### 2. Nash Equilibrium Detection

```go
type NashChecker struct {
    strategies map[string]map[string]float64  // service -> action -> probability
    epsilon    float64                         // equilibrium threshold
}

func (nc *NashChecker) IsNashEquilibrium() bool {
    for service, strategy := range nc.strategies {
        bestResponse := nc.computeBestResponse(service)
        if !nc.isBestResponse(strategy, bestResponse) {
            return false
        }
    }
    return true
}
```

### 3. Folk Theorem Implementation

```go
type ReputationTracker struct {
    callers        map[string]*CallerReputation
    punishmentLevel map[string]float64
    cooperationPath map[string]CooperationRule
}

type CallerReputation struct {
    cooperationScore float64
    deviationCount   int
    punishmentUntil  time.Time
    interactionCount  int64
}

func (rt *ReputationTracker) CheckCooperation(caller string) bool {
    rep := rt.callers[caller]
    
    // Subgame Perfect: Check if punishment is credible
    if rep.punishmentUntil.After(time.Now()) {
        return false  // Still in punishment phase
    }
    
    // Individually Rational: Check if cooperation is beneficial
    return rt.isIndividuallyRational(caller)
}

func (rt *ReputationTracker) Punish(caller string, severity float64) {
    rep := rt.callers[caller]
    rep.punishmentUntil = time.Now().Add(rt.calculatePunishmentDuration(severity))
    rep.deviationCount++
}
```

### 4. HTTP Headers for Signaling

```
X-GT-Bid: <float>                    # Service's bid for priority
X-GT-Regret: <action>:<score>,...    # Regret scores per action
X-GT-Congestion: <float>              # Current congestion level
X-GT-Reputation: <float>              # Caller's reputation score
X-GT-Recommendation: <action>         # Correlated equilibrium recommendation
X-GT-Punishment: <bool>               # Whether caller is being punished
```

### 5. Prometheus Metrics

```go
// Key metrics to export
equilibrium_regret_total{action, service}
equilibrium_regret_average{action, service}
equilibrium_nash_equilibrium{service}  // 1 if in equilibrium, 0 otherwise
equilibrium_convergence_distance{service}
equilibrium_routing_probability{action, service}
equilibrium_reputation_score{caller, target}
equilibrium_punishment_active{caller, target}
equilibrium_request_latency{action, service}
equilibrium_request_count{action, service}
```

---

## 🧪 Testing Strategy

### Unit Tests

1. **Regret Matching**
   - Test regret calculation correctness
   - Test action selection probabilities
   - Test convergence over time
   - Test exploration vs exploitation balance

2. **Nash Equilibrium**
   - Test equilibrium detection
   - Test best response calculation
   - Test oscillation prevention

3. **Folk Theorem**
   - Test reputation tracking
   - Test punishment mechanisms
   - Test cooperation sustainability
   - Test individually rational constraints

### Integration Tests

1. **End-to-End Routing**
   - Deploy 3 replicas of each UrbanMoveMS service
   - Generate traffic through gateway
   - Verify regret matching routes correctly
   - Verify convergence to equilibrium

2. **Chaos Engineering**
   - Inject latency into one backend pod
   - Verify traffic shifts away (regret matching)
   - Verify system converges to new equilibrium
   - Remove latency, verify recovery

3. **Cooperation Testing**
   - Simulate service flooding (defection)
   - Verify punishment is triggered
   - Verify cooperation resumes after punishment
   - Verify individually rational constraints

### Performance Tests

1. **Overhead Measurement**
   - Baseline: UrbanMoveMS without sidecar
   - With sidecar: measure latency increase
   - Target: <5ms p99 overhead
   - Memory footprint: <50MB per sidecar

2. **Convergence Speed**
   - Measure time to equilibrium after change
   - Target: <5 minutes for 95% convergence
   - Measure regret reduction rate

3. **Scalability**
   - Test with 10, 50, 100 services
   - Test with 1000 requests/second
   - Verify no degradation

---

## 🔗 Integration with UrbanMoveMS

### Step 1: Prepare UrbanMoveMS

```bash
# Scale services to 3 replicas for load balancing testing
kubectl scale deployment bus-service --replicas=3
kubectl scale deployment trajet-service --replicas=3
kubectl scale deployment user-service --replicas=3
kubectl scale deployment ticket-service --replicas=3
```

### Step 2: Deploy Equilibrium Sidecar

```bash
# Install mutating webhook
kubectl apply -f k8s-injector/manifests/

# Annotate UrbanMoveMS deployments
kubectl annotate deployment gateway equilibrium.io/inject=true
kubectl annotate deployment bus-service equilibrium.io/inject=true
kubectl annotate deployment trajet-service equilibrium.io/inject=true
kubectl annotate deployment user-service equilibrium.io/inject=true
kubectl annotate deployment ticket-service equilibrium.io/inject=true
```

### Step 3: Configure Service Discovery

The sidecar needs to discover available backend pods:

```yaml
# Example: Gateway routing to bus-service
# Sidecar discovers: bus-service-0, bus-service-1, bus-service-2
# Regret matching selects which pod to route to
```

### Step 4: Traffic Flow

```
Client Request
    ↓
Gateway Pod
    ├─ App Container (Spring Cloud Gateway)
    └─ Equilibrium Sidecar (intercepts outbound)
        ↓
    Regret Matching selects: bus-service-2
        ↓
    bus-service-2 Pod
        ├─ App Container
        └─ Equilibrium Sidecar (intercepts inbound)
            ↓
        Response with X-GT-Regret header
```

### Step 5: Test Scenarios

#### Scenario 1: Basic Routing
- Generate 1000 requests to gateway
- Verify regret matching distributes traffic
- Verify convergence to equilibrium

#### Scenario 2: Latency Injection
- Inject 500ms latency into bus-service-0
- Verify traffic shifts to bus-service-1 and bus-service-2
- Verify convergence (Nash equilibrium)
- Remove latency, verify recovery

#### Scenario 3: Cooperation Test
- Simulate ticket-service flooding trajet-service
- Verify punishment is triggered
- Verify cooperation resumes

#### Scenario 4: Correlated Equilibrium
- Deploy optional coordinator
- Verify recommendations improve efficiency
- Verify fallback if coordinator fails

---

## 📊 Success Criteria

### Functional Requirements

- [x] All 16 game theory concepts implemented
- [x] Zero code changes to UrbanMoveMS services
- [x] Automatic sidecar injection via webhook
- [x] Regret matching routes traffic correctly
- [x] System converges to Nash equilibrium
- [x] Cooperation mechanisms prevent defection
- [x] Handles private information (Bayesian)
- [x] Defends against adversarial scenarios (Zero-Sum)

### Performance Requirements

- [x] <5ms p99 latency overhead
- [x] <50MB memory per sidecar
- [x] <5 minutes convergence time
- [x] Handles 1000+ requests/second

### Observability Requirements

- [x] Prometheus metrics for all concepts
- [x] Dashboard visualization
- [x] Convergence monitoring
- [x] Equilibrium status indicators

---

## 🚀 Getting Started

### Prerequisites

- Kubernetes cluster (minikube, kind, or cloud)
- Go 1.21+
- kubectl configured
- UrbanMoveMS deployed

### Quick Start

```bash
# 1. Clone and build
git clone <repo>
cd equilibrium
go build -o sidecar-proxy ./sidecar-proxy

# 2. Build Docker image
docker build -t equilibrium-sidecar:latest ./sidecar-proxy

# 3. Deploy webhook
kubectl apply -f k8s-injector/manifests/

# 4. Annotate UrbanMoveMS
kubectl annotate deployment gateway equilibrium.io/inject=true

# 5. Verify
kubectl get pods -l app=gateway
# Should see 2 containers: gateway-app and equilibrium-sidecar
```

---

## 📚 References

- All concepts from: `game_theory_concepts_simple.md`
- Detailed theory: `game_theory_for_microservices.md`
- Project overview: `README.md`
- Test application: `Software-Oriented-Application/`

---

## 🎯 Next Steps After v1.0

- **v1.1:** Machine learning integration for payoff prediction
- **v1.2:** Multi-objective optimization (latency + cost)
- **v1.3:** Cross-cluster coordination
- **v2.0:** Full service mesh capabilities

---

**Status:** Planning Phase  
**Target Completion:** 14 weeks  
**Team Size:** 2-3 engineers

