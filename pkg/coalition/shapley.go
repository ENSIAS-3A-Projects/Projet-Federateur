package coalition

import (
	"math/rand"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

// ShapleyCredits tracks each service's accumulated contribution credits.
type ShapleyCredits struct {
	Credits map[types.UID]float64
	History []CreditTransaction
}

// CreditTransaction records a credit change.
type CreditTransaction struct {
	Timestamp time.Time
	Agent     types.UID
	Delta     float64
	Reason    string // "helped_coalition", "consumed_burst", etc.
}

// NewShapleyCredits creates a new credit tracker.
func NewShapleyCredits() *ShapleyCredits {
	return &ShapleyCredits{
		Credits: make(map[types.UID]float64),
		History: make([]CreditTransaction, 0),
	}
}

// ComputeShapleyValue computes the Shapley value for each agent.
//
// φ_i(v) = Σ_{S⊆N\{i}} [|S|!(n-|S|-1)!/n!] · [v(S∪{i}) - v(S)]
//
// This is the unique fair attribution satisfying:
// - Efficiency: Σ φ_i = v(N)
// - Symmetry: Symmetric players get equal value
// - Dummy: Players with zero marginal contribution get zero
// - Additivity: φ(v+w) = φ(v) + φ(w)
//
// Uses Monte Carlo approximation for efficiency (O(k·n) vs O(2^n))
//
// Parameters:
//   - game: The coalition game with players and values
//   - samples: Number of Monte Carlo samples
//   - rng: Random number generator (pass nil for default seeded RNG)
func ComputeShapleyValue(
	game *CoalitionGame,
	samples int,
	rng *rand.Rand,
) map[types.UID]float64 {
	n := len(game.Players)
	shapley := make(map[types.UID]float64)

	if n == 0 || samples <= 0 {
		return shapley
	}

	// Use provided RNG or create a seeded one for reproducibility
	if rng == nil {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}

	for i := 0; i < samples; i++ {
		// Random permutation (arrival order)
		perm := rng.Perm(n)

		// Track coalition as players "arrive"
		coalition := make([]types.UID, 0, n)
		prevValue := 0.0

		for _, idx := range perm {
			player := game.Players[idx]
			coalition = append(coalition, player)

			// Get coalition value
			key := coalitionKey(coalition)
			currValue := game.Values[key]

			// Marginal contribution = v(S∪{i}) - v(S)
			marginal := currValue - prevValue
			shapley[player] += marginal

			prevValue = currValue
		}
	}

	// Average over samples
	for uid := range shapley {
		shapley[uid] /= float64(samples)
	}

	return shapley
}

// UpdateCredits updates the credit ledger based on Shapley values.
// Called after each epoch to settle contributions.
func (sc *ShapleyCredits) UpdateCredits(
	shapleyValues map[types.UID]float64,
	actualConsumption map[types.UID]int64,
	epoch time.Time,
) {
	for uid, sv := range shapleyValues {
		// Credit = Shapley value - actual consumption
		// Positive = helped more than consumed (earns credits)
		// Negative = consumed more than helped (spends credits)
		consumption := float64(actualConsumption[uid])
		delta := sv - consumption

		sc.Credits[uid] += delta
		sc.History = append(sc.History, CreditTransaction{
			Timestamp: epoch,
			Agent:     uid,
			Delta:     delta,
			Reason:    "shapley_settlement",
		})
	}
}

// AdjustAllocationByCredits modifies allocations based on credit balance.
// Agents with positive credits get priority during contention.
func (sc *ShapleyCredits) AdjustAllocationByCredits(
	baseAllocations map[types.UID]int64,
	contention float64, // 0 = no contention, 1 = severe contention
) map[types.UID]int64 {
	if contention < 0.1 {
		return baseAllocations // No adjustment needed
	}

	result := make(map[types.UID]int64)

	// Normalize credits to adjustment factors
	maxCredit := 0.0
	minCredit := 0.0
	for _, c := range sc.Credits {
		if c > maxCredit {
			maxCredit = c
		}
		if c < minCredit {
			minCredit = c
		}
	}

	creditRange := maxCredit - minCredit
	if creditRange < 1 {
		return baseAllocations
	}

	// Adjustment: ±10% based on credit position
	for uid, base := range baseAllocations {
		credit := sc.Credits[uid]
		normalized := (credit - minCredit) / creditRange // [0, 1]
		adjustmentFactor := 0.9 + 0.2*normalized         // [0.9, 1.1]

		// Apply adjustment scaled by contention
		factor := 1.0 + contention*(adjustmentFactor-1.0)
		result[uid] = int64(float64(base) * factor)
	}

	return result
}
