package price

import (
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"

	mbcastypes "mbcas/pkg/types"
)

// ShadowPrices holds the current resource shadow prices for a node.
// These are the Lagrange multipliers from the bargaining optimization.
type ShadowPrices struct {
	mu        sync.RWMutex
	CPU       float64 // Price per millicore
	Memory    float64 // Price per MB
	Conn      float64 // Price per connection token
	UpdatedAt time.Time
}

// PriceHeaders are injected into service-to-service requests.
const (
	HeaderPriceCPU  = "X-Price-CPU"
	HeaderPriceMem  = "X-Price-Mem"
	HeaderPriceConn = "X-Price-Conn"
)

// AllocationParams is an alias to the shared type for backward compatibility.
type AllocationParams = mbcastypes.AllocationParams

// Price signal tuning constants
const (
	// DefaultPriceScale scales utilization to a reasonable price range
	DefaultPriceScale = 10.0

	// MemoryPriceRatio is the Memory/CPU price ratio (memory typically cheaper)
	MemoryPriceRatio = 0.5

	// ConnPriceRatio is the Connection/CPU price ratio (connections more scarce)
	ConnPriceRatio = 2.0

	// MinimumDemand is the absolute minimum demand floor
	MinimumDemand = int64(10)
)

// NewShadowPrices creates a new ShadowPrices instance.
func NewShadowPrices() *ShadowPrices {
	return &ShadowPrices{
		UpdatedAt: time.Now(),
	}
}

// ComputeShadowPrices calculates shadow prices from bargaining dual variables.
//
// In the Nash bargaining convex program:
//
//	max Σ w_i log(x_i - d_i)
//	s.t. Σ x_i ≤ C
//
// The shadow price λ is the Lagrange multiplier for the capacity constraint.
// At optimum: w_i / (x_i - d_i) = λ for all uncapped agents.
func ComputeShadowPrices(
	allocations map[types.UID]int64,
	params map[types.UID]AllocationParams,
	capacity int64,
) *ShadowPrices {
	// Guard against division by zero
	if capacity <= 0 {
		return &ShadowPrices{
			CPU:       0,
			Memory:    0,
			Conn:      0,
			UpdatedAt: time.Now(),
		}
	}

	// Find an uncapped agent to compute λ
	var lambda float64
	for uid, alloc := range allocations {
		p := params[uid]
		surplus := float64(alloc - p.Baseline)
		if surplus > 0 && alloc < p.MaxAlloc {
			// λ = w_i / (x_i - d_i) for uncapped agent
			lambda = p.Weight / surplus
			break
		}
	}

	// If all capped or all at baseline, use heuristic
	if lambda == 0 {
		totalAlloc := int64(0)
		for _, a := range allocations {
			totalAlloc += a
		}
		utilization := float64(totalAlloc) / float64(capacity)
		lambda = utilization * DefaultPriceScale
	}

	return &ShadowPrices{
		CPU:       lambda,
		Memory:    lambda * MemoryPriceRatio,
		Conn:      lambda * ConnPriceRatio,
		UpdatedAt: time.Now(),
	}
}

// DemandResponse adjusts an agent's demand based on received prices.
// This implements the "price-taking" behavior from mechanism design.
//
// If price is high → reduce demand (back off)
// If price is low → increase demand (request more)
func DemandResponse(
	currentDemand int64,
	receivedPrice float64,
	marginalUtility float64,
	elasticity float64, // How responsive to price changes [0, 1]
) int64 {
	// Optimal demand: set marginal utility = price
	// Δdemand ∝ (marginal_utility - price)
	priceDelta := marginalUtility - receivedPrice

	// Adjustment factor based on elasticity
	adjustment := priceDelta * elasticity

	// Apply bounded adjustment (max ±20% per epoch)
	maxAdjustment := float64(currentDemand) * 0.2
	if adjustment > maxAdjustment {
		adjustment = maxAdjustment
	}
	if adjustment < -maxAdjustment {
		adjustment = -maxAdjustment
	}

	newDemand := currentDemand + int64(adjustment)
	if newDemand < 10 {
		newDemand = 10 // Minimum demand
	}

	return newDemand
}

// PropagatePrice injects price headers into outgoing requests.
// Called by the sidecar before forwarding a request.
func (sp *ShadowPrices) PropagatePrice(_ context.Context, headers map[string]string) {
	sp.mu.RLock()
	defer sp.mu.RUnlock()

	headers[HeaderPriceCPU] = fmt.Sprintf("%.6f", sp.CPU)
	headers[HeaderPriceMem] = fmt.Sprintf("%.6f", sp.Memory)
	headers[HeaderPriceConn] = fmt.Sprintf("%.6f", sp.Conn)
}

// GetCPU returns the current CPU price.
func (sp *ShadowPrices) GetCPU() float64 {
	sp.mu.RLock()
	defer sp.mu.RUnlock()
	return sp.CPU
}

// GetMemory returns the current memory price.
func (sp *ShadowPrices) GetMemory() float64 {
	sp.mu.RLock()
	defer sp.mu.RUnlock()
	return sp.Memory
}

// GetConn returns the current connection price.
func (sp *ShadowPrices) GetConn() float64 {
	sp.mu.RLock()
	defer sp.mu.RUnlock()
	return sp.Conn
}

// Update sets new price values.
func (sp *ShadowPrices) Update(cpu, memory, conn float64) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	sp.CPU = cpu
	sp.Memory = memory
	sp.Conn = conn
	sp.UpdatedAt = time.Now()
}
