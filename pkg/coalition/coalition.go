package coalition

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/types"

	mbcastypes "mbcas/pkg/types"
)

// MaxCoalitionSize limits the number of members in a single coalition.
// This prevents O(2^n) explosion in IsInEpsilonCore:
//   - 8 members → 2^8 = 256 subset checks (fast)
//   - 20 members → 2^20 = 1,048,576 checks (CPU death)
//
// Paths longer than this are split into sub-coalitions.
const MaxCoalitionSize = 8

// Coalition represents a group of services on a shared request path.
type Coalition struct {
	ID          string
	Members     []types.UID
	PathID      string // Trace/span path identifier
	Value       float64
	Stable      bool // Is in ε-core?
	Allocations map[types.UID]int64
}

// CoalitionGame represents the characteristic function v(S) for all subsets.
type CoalitionGame struct {
	Players []types.UID
	Values  map[string]float64 // coalition key -> value
	Epsilon float64            // ε for ε-core stability
}

// AllocationParams is an alias to the shared type for backward compatibility.
type AllocationParams = mbcastypes.AllocationParams

// NewCoalitionFromPath creates coalition(s) from a trace path.
// If the path has more than MaxCoalitionSize members, it is automatically
// split into OVERLAPPING sub-coalitions to prevent exponential complexity
// while ensuring no service loses coalition benefits.
//
// Overlap size is MaxCoalitionSize/2, so for 8-member coalitions:
// Path A→B→C→D→E→F→G→H→I→J produces:
//   - Part 0: A,B,C,D,E,F,G,H (positions 0-7)
//   - Part 1: E,F,G,H,I,J     (positions 4-9, overlapping by 4)
func NewCoalitionFromPath(pathID string, members []types.UID) []*Coalition {
	if len(members) == 0 {
		return nil
	}

	// If within size limit, create single coalition
	if len(members) <= MaxCoalitionSize {
		return []*Coalition{{
			ID:      generateCoalitionID(pathID, members),
			Members: members,
			PathID:  pathID,
			Stable:  false,
		}}
	}

	// Split into overlapping sub-coalitions
	// Overlap size = MaxCoalitionSize / 2 (ensures middle services are in 2 coalitions)
	var coalitions []*Coalition
	partIndex := 0
	overlap := MaxCoalitionSize / 2
	step := MaxCoalitionSize - overlap

	for i := 0; i < len(members); i += step {
		end := i + MaxCoalitionSize
		if end > len(members) {
			end = len(members)
		}

		// Skip if we would create a tiny coalition (less than overlap)
		if end-i < overlap && partIndex > 0 {
			break
		}

		subMembers := make([]types.UID, end-i)
		copy(subMembers, members[i:end])

		coalitions = append(coalitions, &Coalition{
			ID:      generateCoalitionID(pathID, subMembers),
			Members: subMembers,
			PathID:  fmt.Sprintf("%s-part%d", pathID, partIndex),
			Stable:  false,
		})
		partIndex++

		// Stop if we've included all members
		if end >= len(members) {
			break
		}
	}

	return coalitions
}

// ComputeValue calculates the coalition value.
// v(C) = latency reduction achievable by pooling resources
//
// For a request path A → B → C:
// v({A,B,C}) = baseline_latency - optimized_latency
func (c *Coalition) ComputeValue(
	baselineLatency float64,
	capacityShare int64,
	memberParams map[types.UID]AllocationParams,
) float64 {
	// Solve internal Nash bargaining for coalition
	internalAllocs := solveInternalBargaining(c.Members, capacityShare, memberParams)

	// Estimate optimized latency with these allocations
	optimizedLatency := estimateLatency(internalAllocs, memberParams)

	c.Value = baselineLatency - optimizedLatency
	if c.Value < 0 {
		c.Value = 0 // Coalition can't make things worse than baseline
	}

	return c.Value
}

// IsInEpsilonCore checks if the current allocation is ε-core stable.
//
// Definition: An allocation x is in the ε-core if for all subsets S:
//
//	Σ_{i∈S} x_i ≥ v(S) - ε
//
// In words: no subset S can "block" by doing significantly better alone.
//
// SAFETY: This function has O(2^n) complexity where n = len(Players).
// Use NewCoalitionFromPath to ensure n ≤ MaxCoalitionSize.
func (g *CoalitionGame) IsInEpsilonCore(allocations map[types.UID]int64) (bool, *BlockingCoalition) {
	n := len(g.Players)

	// Safety check: refuse to process if too many players
	if n > MaxCoalitionSize {
		// Return as unstable with a warning
		return false, &BlockingCoalition{
			Members:    g.Players,
			Value:      0,
			CurrentSum: 0,
			Deficit:    0,
		}
	}

	// Check all 2^n - 1 non-empty subsets
	for mask := 1; mask < (1 << n); mask++ {
		subset := g.subsetFromMask(mask)
		if len(subset) == n {
			continue // Skip grand coalition
		}

		// Compute allocation sum for this subset
		subsetAlloc := int64(0)
		for _, uid := range subset {
			subsetAlloc += allocations[uid]
		}

		// Get coalition value for this subset
		subsetKey := coalitionKey(subset)
		subsetValue := g.Values[subsetKey]

		// Check blocking condition: v(S) - ε > Σ_{i∈S} x_i
		if subsetValue-g.Epsilon > float64(subsetAlloc) {
			return false, &BlockingCoalition{
				Members:    subset,
				Value:      subsetValue,
				CurrentSum: subsetAlloc,
				Deficit:    subsetValue - float64(subsetAlloc),
			}
		}
	}

	return true, nil
}

// BlockingCoalition represents a subset that blocks the current allocation.
type BlockingCoalition struct {
	Members    []types.UID
	Value      float64 // v(S)
	CurrentSum int64   // Σ_{i∈S} x_i
	Deficit    float64 // v(S) - Σ x_i
}

// ResolveBlocking adjusts allocations to eliminate blocking coalition.
// Strategy: Increase allocation to blocking members at expense of non-members.
func (g *CoalitionGame) ResolveBlocking(
	allocations map[types.UID]int64,
	blocking *BlockingCoalition,
	capacity int64,
) map[types.UID]int64 {
	result := make(map[types.UID]int64)
	for k, v := range allocations {
		result[k] = v
	}

	// Calculate how much to transfer
	deficit := int64(blocking.Deficit) + 1 // Round up

	// Take from non-blocking members (those not in blocking coalition)
	nonMembers := g.complement(blocking.Members)
	if len(nonMembers) == 0 {
		return result // No one to take from
	}

	perNonMember := deficit / int64(len(nonMembers))
	if perNonMember == 0 {
		perNonMember = 1
	}

	for _, uid := range nonMembers {
		reduction := perNonMember
		// Don't reduce below baseline (100m minimum)
		if result[uid]-reduction < 100 {
			reduction = result[uid] - 100
			if reduction < 0 {
				reduction = 0
			}
		}
		result[uid] -= reduction
	}

	// Give to blocking members proportionally
	perBlockingMember := deficit / int64(len(blocking.Members))
	if perBlockingMember == 0 {
		perBlockingMember = 1
	}

	for _, uid := range blocking.Members {
		result[uid] += perBlockingMember
	}

	return result
}

// Helper functions

func (g *CoalitionGame) subsetFromMask(mask int) []types.UID {
	subset := make([]types.UID, 0)
	for i, uid := range g.Players {
		if mask&(1<<i) != 0 {
			subset = append(subset, uid)
		}
	}
	return subset
}

func (g *CoalitionGame) complement(subset []types.UID) []types.UID {
	inSubset := make(map[types.UID]bool)
	for _, uid := range subset {
		inSubset[uid] = true
	}

	complement := make([]types.UID, 0)
	for _, uid := range g.Players {
		if !inSubset[uid] {
			complement = append(complement, uid)
		}
	}
	return complement
}

func generateCoalitionID(pathID string, members []types.UID) string {
	return fmt.Sprintf("%s:%s", pathID, coalitionKey(members))
}

func coalitionKey(members []types.UID) string {
	// Sort and concatenate for deterministic key
	sorted := make([]string, len(members))
	for i, uid := range members {
		sorted[i] = string(uid)
	}
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}

// solveInternalBargaining computes optimal allocation within a coalition.
// This is a simplified version - in production, use NashBargainingSolution.
func solveInternalBargaining(
	members []types.UID,
	capacity int64,
	params map[types.UID]AllocationParams,
) map[types.UID]int64 {
	allocations := make(map[types.UID]int64)

	if len(members) == 0 {
		return allocations
	}

	// Simple proportional allocation by weight
	totalWeight := 0.0
	for _, uid := range members {
		if p, ok := params[uid]; ok {
			totalWeight += p.Weight
		}
	}

	if totalWeight == 0 {
		// Equal division
		share := capacity / int64(len(members))
		for _, uid := range members {
			allocations[uid] = share
		}
		return allocations
	}

	for _, uid := range members {
		if p, ok := params[uid]; ok {
			alloc := int64(float64(capacity) * (p.Weight / totalWeight))
			if alloc < p.Baseline {
				alloc = p.Baseline
			}
			if alloc > p.MaxAlloc {
				alloc = p.MaxAlloc
			}
			allocations[uid] = alloc
		}
	}

	return allocations
}

// estimateLatency estimates the total latency given allocations.
// This is a placeholder - in production, use actual latency models.
func estimateLatency(
	allocations map[types.UID]int64,
	_ map[types.UID]AllocationParams,
) float64 {
	// Simple model: latency inversely proportional to allocation
	// Higher allocation → lower latency
	totalLatency := 0.0
	for _, alloc := range allocations {
		if alloc > 0 {
			// Base latency of 10ms, reduced by allocation factor
			latency := 10.0 * (100.0 / float64(alloc))
			totalLatency += latency
		}
	}
	return totalLatency
}
