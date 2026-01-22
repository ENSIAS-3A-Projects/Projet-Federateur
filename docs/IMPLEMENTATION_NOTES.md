# Implementation Notes: MBCAS.pdf vs Actual Implementation

This document notes discrepancies between the MBCAS.pdf document and the actual implementation in the codebase.

## Key Differences

### 1. Dual-Loop Architecture

**PDF**: Describes a single allocation loop.

**Implementation**: Uses a **dual-loop architecture**:
- **Fast Loop** (2s): Emergency response to SLO violations and throttling
- **Slow Loop** (10-15s): Full Nash Bargaining optimization

This was added to improve responsiveness while maintaining efficiency.

### 2. Shadow Price Feedback

**PDF**: May not describe shadow price feedback mechanism.

**Implementation**: Implements a **two-pass bidding mechanism** with shadow price feedback:
1. First pass: Agents compute initial bids
2. Nash solver computes preview allocation and shadow price
3. Second pass: Agents adjust bids based on shadow price
4. Final Nash solver computes final allocation

Shadow prices enable price-responsive demand adjustment, improving market efficiency.

### 3. Q-Table Persistence

**PDF**: May not mention Q-table persistence.

**Implementation**: Q-tables are persisted to ConfigMaps every 30 seconds and loaded on agent startup. This preserves learning state across restarts.

### 4. Stability Improvements

**PDF**: May not detail stability mechanisms.

**Implementation**: Includes multiple stability mechanisms:
- **Exponential Smoothing**: Asymmetric smoothing (fast down, slow up) prevents oscillations
- **Hysteresis**: Minimum change threshold (5%) prevents churn
- **Cooldown Periods**: 30s cooldown between allocation writes per pod
- **Allocation Caps**: Absolute maximum of 10 cores per pod
- **Oscillation Detection**: Penalizes large allocation changes in reward function

### 5. Multi-Container Pod Support

**PDF**: May not describe multi-container handling.

**Implementation**: Controller supports multi-container pods with **proportional allocation** based on original container limits.

### 6. Fixed Capacity Model

**PDF**: May describe dynamic node capacity detection.

**Implementation**: Uses **user-defined fixed capacity** (`totalClusterCPUCapacityMilli`) to avoid bugs in minikube node capacity reporting.

### 7. Actual Allocation Tracking

**PDF**: May describe using desired allocations.

**Implementation**: Uses **actual applied allocations** from PodAllocation CR status, not desired allocations. This reduces lag and oscillations.

### 8. Throttling Trend Analysis

**PDF**: May describe using current throttling value.

**Implementation**: Uses **throttling trend** (last 3 samples) for pattern learning, reducing sensitivity to spikes.

### 9. SLO Checking Integration

**PDF**: May not describe Prometheus integration.

**Implementation**: Optional **Prometheus integration** for latency-based SLO checking. Fast loop responds to SLO violations.

### 10. Cost Efficiency Mode

**PDF**: May not describe cost optimization mode.

**Implementation**: Optional **cost efficiency mode** with:
- Asymmetric smoothing (fast down, slow up)
- Idle decay
- Target throttling tolerance

### 11. Health Endpoints

**PDF**: May not describe observability features.

**Implementation**: Agent exposes `/healthz`, `/readyz`, and `/metrics` endpoints on port 8082.

### 12. Controller Safety Mechanisms

**PDF**: May not detail safety checks.

**Implementation**: Controller includes:
- **Cooldown**: 5s minimum between resizes per pod
- **Step Size Limits**: Maximum 10x change factor
- **Absolute Delta Cap**: Maximum 20 cores change per resize
- **Resize Verification**: Confirms kubelet actually applied the resize

## Recommendations

If updating MBCAS.pdf, consider:

1. **Add dual-loop architecture section** describing fast and slow loops
2. **Document shadow price feedback mechanism** and two-pass bidding
3. **Describe Q-table persistence** and its benefits
4. **Detail stability mechanisms** (smoothing, hysteresis, cooldown)
5. **Document multi-container pod support**
6. **Explain fixed capacity model** and rationale
7. **Describe actual allocation tracking** vs desired allocation
8. **Document throttling trend analysis**
9. **Add SLO checking section** if Prometheus integration is used
10. **Document cost efficiency mode** if applicable
11. **Add observability section** (health endpoints, metrics)
12. **Detail controller safety mechanisms**

## Implementation Status

All features described above are **fully implemented** and tested in the codebase. The documentation (README.md, ARCHITECTURE.md, GAME_THEORY.md) has been updated to reflect the actual implementation.
