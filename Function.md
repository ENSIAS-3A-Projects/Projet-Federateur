# Function Logic: Desired CPU Calculation (MBCAS)

This document captures the end-to-end path that produces `spec.desiredCPULimit` in `PodAllocation`.

## 1) Demand Sampling (Agent)
- File: `pkg/agent/cgroup/reader.go`
- Function: `ReadPodDemand(pod)`
- Logic:
  - Locate pod cgroup (`/sys/fs/cgroup` glob).
  - Read `cpu.stat` and take deltas since last sample.
  - Compute throttling ratio: `delta_throttled / delta_usage`.
  - Normalize to [0,1] with threshold 0.1 (10% throttling → demand 1.0).
  - First sample yields 0 (no delta yet).

## 2) Demand Smoothing (Agent)
- File: `pkg/agent/demand/tracker.go`
- Function: `Tracker.Update(rawDemand)`
- Logic:
  - EMA with fast-up (`alphaIncrease=0.3`), faster-down (`alphaDecrease=0.2`).
  - Consecutive zeros trigger fast decay (`fastDecayAlpha=0.5` after 5 zeros).
  - Clamp to [0,1]; floor <0.01 to 0 to avoid stuck micro-demand.

## 3) Pod Parameters (Agent)
- File: `pkg/agent/demand/calculator.go`
- Function: `ParamsForPod(pod, demand, baselineMilli, nodeCapMilli)`
- Logic:
  - Request CPU → `requestMilli`; Limit CPU → `limitMilli` (fallback node cap if 0).
  - Weight = max(1, requestMilli).
  - Bid = weight * demand.
  - Min = max(baselineMilli, requestMilli).
  - Max = limitMilli (or nodeCapMilli if limit missing); capped at 90% node cap.

## 4) Market Clearing (Core)
- File: `pkg/allocation/market.go`
- Function: `ClearMarket(capacityMilli, pods map[UID]PodParams)`
- Logic:
  - Allocate minimums (scale down proportionally if mins exceed capacity).
  - Compute effective bids (weight × demand); if zero bids, fall back to weights.
  - Distribute remaining capacity proportionally; enforce per-pod max via water-filling.
  - Deterministic rounding to int64 millicores.

## 5) Agent Orchestration
- File: `pkg/agent/agent.go`
- Functions:
  - `sample()` gathers demands (1s interval).
  - `computeAllocations()` builds `PodParams` and calls `ClearMarket`.
  - `writeAllocations()` (15s interval) converts milli CPU → strings (e.g., "500m"), checks hysteresis (`MinChangePercent=2%`), then calls writer.

## 6) Writing PodAllocation (Agent)
- File: `pkg/agent/writer.go`
- Function: `WritePodAllocation(ctx, pod, cpuAllocation)`
- Logic:
  - Creates/updates `PodAllocation` CR `spec.desiredCPULimit` with the computed CPU string.

## 7) Controller Application
- File: `pkg/controller/podallocation_controller.go`
- Logic (summary):
  - Watches `PodAllocation` and target Pod.
  - Safety checks (cooldown, bounds, QoS rules, etc.).
  - Applies `status.appliedCPULimit` and patches pod CPU limit via in-place resize.
  - If blocked, sets `status.phase=Pending` with reason (e.g., `SafetyCheckFailed`).

## Observability
- Agent logs: demand samples and allocation writes (`agent.go`).
- CRD view: `kubectl get podallocations -n <ns> -o yaml` shows `spec.desiredCPULimit` and `status.appliedCPULimit`.

## Key Tunables
- Sampling: 1s; Write interval: 15s; Hysteresis: 2%.
- Smoothing: fast up 0.3, down 0.2, fast decay 0.5 after 5 zero samples.
- Baseline per pod: `100m` (`BaselineCPUPerPod`).
- System reserve: 10% of node CPU (`SystemReservePercent`).
