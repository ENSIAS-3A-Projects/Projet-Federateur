// Package types provides shared type definitions used across MBCAS packages.
// This avoids duplicate type definitions and enables better interoperability.
package types

import (
	"k8s.io/apimachinery/pkg/types"
)

// AllocationParams is the canonical type for allocation parameters.
// Used by coalition, price, and stability packages.
type AllocationParams struct {
	UID      types.UID // Pod/service identifier
	Baseline int64     // Minimum allocation (disagreement point)
	MaxAlloc int64     // Maximum allocation (from K8s limits)
	Weight   float64   // Bargaining weight (from K8s requests)
	SLOGap   float64   // Gap from SLO target (for stability calculations)
}

// NewAllocationParams creates AllocationParams with required fields.
func NewAllocationParams(uid types.UID, baseline, maxAlloc int64, weight float64) AllocationParams {
	return AllocationParams{
		UID:      uid,
		Baseline: baseline,
		MaxAlloc: maxAlloc,
		Weight:   weight,
		SLOGap:   0,
	}
}

// WithSLOGap returns a copy with the SLOGap field set.
func (p AllocationParams) WithSLOGap(gap float64) AllocationParams {
	p.SLOGap = gap
	return p
}
