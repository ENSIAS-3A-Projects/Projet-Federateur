# MBCAS vs VPA Benchmark Metrics Comparison Analysis

## Executive Summary

This analysis compares the performance of **MBCAS** (Multi-Body Cooperative Allocation System) against **VPA** (Vertical Pod Autoscaler) across 8 different workload patterns over a 10-minute benchmark period.

### Key Findings

1. **Stability**: MBCAS shows **38.4% fewer allocation changes** overall (479 vs 777), indicating better stability
2. **Resource Utilization**: MBCAS has higher average CPU usage (604.3m vs 268.3m), suggesting more aggressive allocation
3. **Throttling**: Both systems show similar average throttling ratios (~0.063)
4. **Efficiency**: MBCAS shows higher request utilization (109.4% vs 100.0%), but this includes cases where usage exceeds allocation

---

## Test Configuration

- **Duration**: ~10 minutes per system
- **Workloads**: 8 different patterns
- **Node CPU**: 20,000m (20 cores)
- **Sample Interval**: 5 seconds

---

## Detailed Workload Analysis

### 1. Bursty Workload

**Allocation Stability:**
- MBCAS: 76 changes
- VPA: 166 changes
- **MBCAS has 54.2% fewer changes** ✓

**Resource Usage:**
- MBCAS avg: 415.4m
- VPA avg: 254.3m
- MBCAS uses 63.4% more CPU

**Throttling:**
- Both systems: ~0.056 average throttling ratio
- MBCAS: 100s throttling duration
- VPA: 205s throttling duration (105% longer)

**Final Allocation:**
- MBCAS: request=579m, limit=644m
- VPA: request=296m, limit=592m
- MBCAS allocates more aggressively

**Efficiency:**
- MBCAS request utilization: 71.7%
- VPA request utilization: 85.9%
- VPA is more efficient for this workload

**Verdict**: MBCAS is more stable but less efficient for bursty workloads.

---

### 2. Idle Workload

**Allocation Stability:**
- MBCAS: 27 changes
- VPA: 23 changes
- MBCAS has 17.4% more changes

**Resource Usage:**
- MBCAS avg: 1.6m
- VPA avg: 12.0m
- MBCAS uses 86.6% less CPU (better for idle)

**Final Allocation:**
- MBCAS: request=10m, limit=10m
- VPA: request=50m, limit=100m
- MBCAS scales down more aggressively

**Verdict**: MBCAS handles idle workloads better, scaling down more effectively.

---

### 3. Overprovisioned Workload

**Allocation Stability:**
- MBCAS: 29 changes
- VPA: 21 changes
- MBCAS has 38.1% more changes

**Resource Usage:**
- MBCAS avg: 7.6m
- VPA avg: 39.3m
- MBCAS uses 80.7% less CPU

**Final Allocation:**
- MBCAS: request=10m, limit=10m
- VPA: request=50m, limit=100m
- MBCAS correctly identifies overprovisioning

**Verdict**: MBCAS better identifies and corrects overprovisioning.

---

### 4. Ramping Workload

**Allocation Stability:**
- MBCAS: 74 changes
- VPA: 168 changes
- **MBCAS has 56.0% fewer changes** ✓

**Resource Usage:**
- MBCAS avg: 447.1m
- VPA avg: 395.6m
- MBCAS uses 13.0% more CPU

**Final Allocation:**
- MBCAS: request=207m, limit=231m
- VPA: request=296m, limit=592m
- MBCAS allocates less but usage exceeds allocation (216% utilization)

**Efficiency:**
- MBCAS request utilization: 216.0% (usage exceeds allocation!)
- VPA request utilization: 133.6%

**Verdict**: MBCAS is more stable but under-allocates for ramping workloads, causing usage to exceed requests.

---

### 5. Spiky Workload

**Allocation Stability:**
- MBCAS: 71 changes
- VPA: 23 changes
- MBCAS has 208.7% more changes ⚠️

**Resource Usage:**
- MBCAS avg: 408.2m
- VPA avg: 468.6m
- MBCAS uses 12.9% less CPU

**Final Allocation:**
- MBCAS: request=754m, limit=838m
- VPA: request=511m, limit=1022m

**Efficiency:**
- MBCAS request utilization: 54.1%
- VPA request utilization: 91.7%

**Verdict**: VPA handles spiky workloads better with fewer changes and better utilization.

---

### 6. Steady-High Workload

**Allocation Stability:**
- MBCAS: 65 changes
- VPA: 164 changes
- **MBCAS has 60.4% fewer changes** ✓

**Resource Usage:**
- MBCAS avg: 863.5m
- VPA avg: 379.6m
- MBCAS uses 127.5% more CPU

**Final Allocation:**
- MBCAS: request=487m, limit=542m
- VPA: request=323m, limit=646m
- MBCAS usage (863.5m) exceeds request (487m) - 177% utilization

**Efficiency:**
- MBCAS request utilization: 177.3% (usage exceeds allocation!)
- VPA request utilization: 117.5%

**Verdict**: MBCAS is more stable but under-allocates for steady-high workloads.

---

### 7. Steady-Low Workload

**Allocation Stability:**
- MBCAS: 69 changes
- VPA: 168 changes
- **MBCAS has 58.9% fewer changes** ✓

**Resource Usage:**
- MBCAS avg: 478.1m
- VPA avg: 353.0m
- MBCAS uses 35.4% more CPU

**Final Allocation:**
- MBCAS: request=315m, limit=351m
- VPA: request=296m, limit=592m
- MBCAS usage (478.1m) exceeds request (315m) - 152% utilization

**Efficiency:**
- MBCAS request utilization: 151.8% (usage exceeds allocation!)
- VPA request utilization: 119.3%

**Verdict**: MBCAS is more stable but under-allocates for steady-low workloads.

---

### 8. Throttle-Prone Workload

**Allocation Stability:**
- MBCAS: 68 changes
- VPA: 44 changes
- MBCAS has 54.5% more changes

**Resource Usage:**
- MBCAS avg: 2212.9m
- VPA avg: 243.8m
- MBCAS uses 807.8% more CPU (significant difference!)

**Throttling:**
- Both systems: ~0.44 average throttling ratio
- MBCAS: 595s throttling duration
- VPA: 1200s throttling duration (101% longer)

**Final Allocation:**
- MBCAS: request=1972m, limit=2192m
- VPA: request=163m, limit=163m
- MBCAS allocates much more aggressively

**Efficiency:**
- MBCAS request utilization: 112.2%
- VPA request utilization: 149.5%

**Verdict**: MBCAS allocates much more aggressively for throttle-prone workloads, resulting in less throttling duration but higher resource usage.

---

## Overall Summary Statistics

### Allocation Stability
- **Total Changes**: MBCAS 479 vs VPA 777
- **Reduction**: 298 changes (38.4% fewer) ✓
- **Winner**: MBCAS (significantly more stable)

### Resource Usage
- **Average CPU Usage**: MBCAS 604.3m vs VPA 268.3m
- **Difference**: +336.0m (MBCAS uses 125% more)
- **Note**: This includes cases where MBCAS usage exceeds allocations

### Throttling
- **Average Throttling Ratio**: MBCAS 0.063 vs VPA 0.062
- **Difference**: Nearly identical
- **Winner**: Tie

### Resource Efficiency
- **Average Request Utilization**: MBCAS 109.4% vs VPA 100.0%
- **Note**: MBCAS >100% indicates usage exceeding requests in some workloads
- **Winner**: VPA (more consistent utilization)

---

## Key Insights

### Strengths of MBCAS

1. **Stability**: 38.4% fewer allocation changes overall
2. **Idle/Overprovisioned Handling**: Better at scaling down unused resources
3. **Throttle-Prone Workloads**: Allocates more aggressively, reducing throttling duration
4. **Bursty Workloads**: More stable allocation changes

### Weaknesses of MBCAS

1. **Under-allocation**: Several workloads show usage exceeding requests (ramping, steady-high, steady-low)
2. **Spiky Workloads**: 208% more allocation changes than VPA
3. **Resource Efficiency**: Lower request utilization in some workloads (bursty, spiky)
4. **Higher Resource Usage**: Uses significantly more CPU on average

### Strengths of VPA

1. **Consistency**: More consistent resource utilization (closer to 100%)
2. **Spiky Workloads**: Handles spikes better with fewer changes
3. **Resource Efficiency**: Better utilization in bursty and spiky workloads
4. **Predictability**: Usage rarely exceeds allocated requests

### Weaknesses of VPA

1. **Stability**: 38.4% more allocation changes overall
2. **Idle Resources**: Less aggressive at scaling down
3. **Throttle-Prone**: Longer throttling duration despite similar ratios

---

## Recommendations

### For MBCAS

1. **Fix Under-allocation**: Address cases where usage exceeds requests (ramping, steady-high, steady-low)
2. **Improve Spiky Handling**: Reduce allocation changes for spiky workloads
3. **Optimize Resource Efficiency**: Improve utilization for bursty workloads
4. **Balance Aggressiveness**: Consider being less aggressive for steady workloads to improve efficiency

### For VPA

1. **Improve Stability**: Reduce allocation changes, especially for bursty, ramping, and steady workloads
2. **Better Idle Handling**: More aggressive scaling down for idle/overprovisioned workloads
3. **Throttle-Prone Optimization**: Reduce throttling duration for throttle-prone workloads

---

## Conclusion

**MBCAS** demonstrates superior **stability** (38.4% fewer changes) and better handling of **idle/overprovisioned** and **throttle-prone** workloads. However, it suffers from **under-allocation** issues in several workload types and **higher resource usage** overall.

**VPA** shows better **resource efficiency** and **consistency**, with usage rarely exceeding allocations. However, it has **more allocation changes** and is less effective at handling idle resources.

The choice between systems depends on priorities:
- **Choose MBCAS** if stability and aggressive resource allocation are priorities
- **Choose VPA** if resource efficiency and predictable allocations are priorities

---

*Analysis generated from benchmark metrics collected on 2026-01-21*
