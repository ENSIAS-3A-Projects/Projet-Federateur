package allocation

import (
	"math"

	"k8s.io/apimachinery/pkg/types"
)

// UtilityParams defines the parameters for computing agent utility.
// Based on: u_i(x) = w_i · SLOScore_i(x) - λ_cpu·x^cpu - λ_mem·x^mem
type UtilityParams struct {
	// SLO-related
	TargetLatencyMs  float64 // Target p99 latency
	CurrentLatencyMs float64 // Observed p99 latency
	SLOWeight        float64 // w_i: importance of SLO satisfaction

	// Sensitivity controls the steepness of the SLO sigmoid function.
	// Lower values = lazy response (system reacts slowly to SLO violations)
	// Higher values = sharp on/off behavior (oscillation risk)
	// Recommended range: 0.05 - 0.2. Default: 0.1
	Sensitivity float64

	// Resource costs (shadow prices from previous epoch)
	LambdaCPU  float64 // λ_cpu: cost per millicore
	LambdaMem  float64 // λ_mem: cost per MB
	LambdaConn float64 // λ_conn: cost per connection token

	// Disagreement point (baseline rights)
	BaselineCPU  int64 // d_i^cpu: minimum viable CPU
	BaselineMem  int64 // d_i^mem: minimum viable memory
	BaselineConn int64 // d_i^conn: minimum viable connections

	// Current allocation
	AllocCPU  int64
	AllocMem  int64
	AllocConn int64

	// Bounds
	MaxCPU  int64
	MaxMem  int64
	MaxConn int64

	// Throttling pressure (0-1) from cgroup signals
	// Higher values indicate more CPU throttling pressure
	ThrottlingPressure float64

	// Actual CPU usage in millicores
	ActualUsageMilli int64
}

// DefaultSensitivity is the default value for the SLO sigmoid steepness.
const DefaultSensitivity = 0.1

// Utility computes the utility value for an agent given current allocation.
// u_i(x) = w_i · SLOScore(x) - Σ λ_r · x_r
//
// SLOScore uses a sigmoid function to model diminishing returns:
// SLOScore = 1 / (1 + exp(k · (latency - target)))
//
// This guarantees:
// - Concavity (diminishing returns)
// - Bounded output [0, 1] for SLO satisfaction
// - Smooth gradients for optimization
func (p *UtilityParams) Utility() float64 {
	sloScore := p.SLOScore()

	resourceCost := p.LambdaCPU*float64(p.AllocCPU) +
		p.LambdaMem*float64(p.AllocMem) +
		p.LambdaConn*float64(p.AllocConn)

	return p.SLOWeight*sloScore - resourceCost
}

// SLOScore computes SLO satisfaction using a sigmoid function.
// Returns value in [0, 1] where 1 = perfect satisfaction.
//
// Mathematical form: σ(x) = 1 / (1 + exp(k·(latency - target)))
// where k (Sensitivity) controls the steepness of the transition.
func (p *UtilityParams) SLOScore() float64 {
	if p.TargetLatencyMs <= 0 {
		return 1.0 // No SLO defined
	}

	// Use configurable Sensitivity, fallback to default if not set
	k := p.Sensitivity
	if k <= 0 {
		k = DefaultSensitivity
	}

	// Sigmoid: approaches 1 when latency < target, 0 when latency >> target
	exponent := k * (p.CurrentLatencyMs - p.TargetLatencyMs)
	return 1.0 / (1.0 + math.Exp(exponent))
}

// MarginalUtilityCPU computes ∂u/∂x_cpu (gradient for optimization).
// Used by the bargaining solver to find optimal allocation.
//
// Enhanced model includes:
// 1. SLO-based marginal value from latency signals
// 2. Throttling pressure as additional signal
// 3. Better CPU→Latency relationship using queueing theory
//
// For resource r: ∂u/∂x_r = w_i · (∂SLO/∂x_r) - λ_r
//
// We model the CPU→Latency relationship using queueing theory:
// Latency = BaseLatency / (1 - utilization)
// where utilization = load / CPU
// ∂Latency/∂CPU ≈ -BaseLatency * load / CPU²
//
// For the SLO sigmoid, chain rule gives:
// ∂SLO/∂CPU = (∂SLO/∂Latency) · (∂Latency/∂CPU)
func (p *UtilityParams) MarginalUtilityCPU() float64 {
	// If no allocation, return high marginal utility to indicate need
	if p.AllocCPU <= 0 {
		return p.SLOWeight * (1.0 + p.ThrottlingPressure)
	}

	// Enhanced CPU→Latency model using queueing theory
	// Model: Latency = BaseLatency / (1 - ρ) where ρ = utilization
	// utilization ≈ ActualUsage / AllocCPU
	utilization := 0.0
	if p.AllocCPU > 0 && p.ActualUsageMilli > 0 {
		utilization = float64(p.ActualUsageMilli) / float64(p.AllocCPU)
		// Clamp utilization to prevent division by zero
		if utilization >= 1.0 {
			utilization = 0.99
		}
	}

	// Base latency estimate (use current latency if available, otherwise estimate)
	baseLatency := p.CurrentLatencyMs
	if baseLatency <= 0 {
		// Estimate base latency from target (if no current measurement)
		baseLatency = p.TargetLatencyMs
		if baseLatency <= 0 {
			baseLatency = 100.0 // Default 100ms
		}
	}

	// ∂Latency/∂CPU using queueing model
	// Latency = baseLatency / (1 - utilization)
	// utilization = load / CPU, so ∂utilization/∂CPU = -load / CPU²
	// ∂Latency/∂CPU = baseLatency * load / (CPU² * (1 - utilization)²)
	var dLatency_dCPU float64
	if utilization > 0 && p.AllocCPU > 0 {
		load := float64(p.ActualUsageMilli)
		denom := float64(p.AllocCPU) * float64(p.AllocCPU) * (1.0 - utilization) * (1.0 - utilization)
		if denom > 0 {
			dLatency_dCPU = -baseLatency * load / denom
		} else {
			// Fallback to simpler model
			dLatency_dCPU = -baseLatency / float64(p.AllocCPU)
		}
	} else {
		// No utilization data, use simpler model
		dLatency_dCPU = -baseLatency / float64(p.AllocCPU)
	}

	// ∂SLO/∂Latency from sigmoid derivative
	// σ'(x) = -k · σ(x) · (1 - σ(x))
	sloScore := p.SLOScore()
	k := p.Sensitivity
	if k <= 0 {
		k = DefaultSensitivity
	}
	dSLO_dLatency := -k * sloScore * (1 - sloScore)

	// Chain rule: ∂SLO/∂CPU = ∂SLO/∂Latency · ∂Latency/∂CPU
	dSLO_dCPU := dSLO_dLatency * dLatency_dCPU

	// Add throttling pressure as additional marginal value signal
	// Higher throttling pressure increases marginal utility
	throttlingBonus := p.ThrottlingPressure * p.SLOWeight * 0.5 // Scale throttling contribution

	// Total marginal utility: SLO component + throttling bonus - shadow price
	return p.SLOWeight*dSLO_dCPU + throttlingBonus - p.LambdaCPU
}

// SurplusCPU computes allocation above disagreement point.
// surplus_i = x_i - d_i (clamped to >= 0)
func (p *UtilityParams) SurplusCPU() int64 {
	surplus := p.AllocCPU - p.BaselineCPU
	if surplus < 0 {
		return 0
	}
	return surplus
}

// LogSurplusCPU computes log(x_i - d_i) for Nash product.
// Returns -Inf if at or below baseline (which is correct for Nash product).
func (p *UtilityParams) LogSurplusCPU() float64 {
	surplus := p.SurplusCPU()
	if surplus <= 0 {
		return math.Inf(-1)
	}
	return math.Log(float64(surplus))
}

// NewUtilityParamsFromPodParams converts market PodParams to game-theoretic UtilityParams.
// This merges the calculator.go functionality into the utility system.
//
// Parameters:
//   - params: Market parameters from demand.Calculator
//   - targetLatencyMs: SLO target latency (0 if none)
//   - currentLatencyMs: Current observed latency
//   - shadowPriceCPU: Current CPU shadow price (from price_signal.go)
//   - throttlingPressure: Throttling pressure signal (0-1) from cgroup metrics
//
// Returns UtilityParams ready for game-theoretic calculations.
func NewUtilityParamsFromPodParams(
	params PodParams,
	targetLatencyMs float64,
	currentLatencyMs float64,
	shadowPriceCPU float64,
	throttlingPressure float64,
) *UtilityParams {
	// Clamp throttling pressure to [0, 1]
	if throttlingPressure < 0 {
		throttlingPressure = 0
	}
	if throttlingPressure > 1 {
		throttlingPressure = 1
	}

	return &UtilityParams{
		// SLO-related
		TargetLatencyMs:  targetLatencyMs,
		CurrentLatencyMs: currentLatencyMs,
		SLOWeight:        params.Weight, // Use market weight as SLO importance
		Sensitivity:      DefaultSensitivity,

		// Resource costs
		LambdaCPU:  shadowPriceCPU,
		LambdaMem:  0, // Not used in CPU-only allocation
		LambdaConn: 0, // Not used in CPU-only allocation

		// Baseline from market params
		BaselineCPU:  params.MinMilli,
		BaselineMem:  0,
		BaselineConn: 0,

		// Current allocation (will be set after market clearing)
		AllocCPU:  0,
		AllocMem:  0,
		AllocConn: 0,

		// Bounds
		MaxCPU:  params.MaxMilli,
		MaxMem:  0,
		MaxConn: 0,

		// Throttling and usage signals
		ThrottlingPressure: throttlingPressure,
		ActualUsageMilli:   params.ActualUsageMilli,
	}
}

// SetAllocation updates the current allocation for utility calculation.
func (p *UtilityParams) SetAllocation(cpuMilli int64) {
	p.AllocCPU = cpuMilli
}

// ToNashBargainingParams converts UtilityParams to NashBargainingParams for the solver.
func (p *UtilityParams) ToNashBargainingParams(uid string) NashBargainingParams {
	return NashBargainingParams{
		UID:          types.UID(uid),
		Weight:       p.SLOWeight,
		Baseline:     p.BaselineCPU,
		MaxAlloc:     p.MaxCPU,
		MarginalUtil: p.MarginalUtilityCPU(),
		Demand:       p.AllocCPU, // Current allocation as demand estimate
	}
}
