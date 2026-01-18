package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PodAllocation is the Schema for the podallocations API.
// It represents a single authoritative control object for CPU allocation decisions.
//
// The PodAllocation is Namespaced (not Cluster-scoped) to:
//   - Align with Pod lifecycle (pods are namespaced)
//   - Avoid RBAC complexity
//   - Prevent cross-tenant leakage
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.spec.namespace`
// +kubebuilder:printcolumn:name="Pod",type=string,JSONPath=`.spec.podName`
// +kubebuilder:printcolumn:name="Desired Request",type=string,JSONPath=`.spec.desiredCPURequest`
// +kubebuilder:printcolumn:name="Desired Limit",type=string,JSONPath=`.spec.desiredCPULimit`
// +kubebuilder:printcolumn:name="Applied Request",type=string,JSONPath=`.status.appliedCPURequest`
// +kubebuilder:printcolumn:name="Applied Limit",type=string,JSONPath=`.status.appliedCPULimit`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type PodAllocation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PodAllocationSpec   `json:"spec,omitempty"`
	Status PodAllocationStatus `json:"status,omitempty"`
}

// PodAllocationSpec defines the desired state of PodAllocation.
type PodAllocationSpec struct {
	// Namespace is the namespace of the target pod.
	// +kubebuilder:validation:Required
	Namespace string `json:"namespace"`

	// PodName is the name of the target pod.
	// +kubebuilder:validation:Required
	PodName string `json:"podName"`

	// DesiredCPURequest is the desired CPU request for the pod.
	// This is a Kubernetes resource quantity string (e.g., "100m", "500m").
	// Request should be set slightly above actual usage for scheduling efficiency.
	// +kubebuilder:validation:Required
	DesiredCPURequest string `json:"desiredCPURequest"`

	// DesiredCPULimit is the desired CPU limit for the pod.
	// This is a Kubernetes resource quantity string (e.g., "500m", "1", "2.5").
	// Limit should be set slightly above request for burst headroom.
	// +kubebuilder:validation:Required
	DesiredCPULimit string `json:"desiredCPULimit"`
}

// PodAllocationStatus defines the observed state of PodAllocation.
type PodAllocationStatus struct {
	// AppliedCPURequest is the CPU request that was last successfully applied to the pod.
	AppliedCPURequest string `json:"appliedCPURequest,omitempty"`

	// AppliedCPULimit is the CPU limit that was last successfully applied to the pod.
	// This may differ from desiredCPULimit if the application is in progress or failed.
	AppliedCPULimit string `json:"appliedCPULimit,omitempty"`

	// Phase represents the current phase of the allocation.
	// Possible values: Pending, Applied, Failed
	//
	// Phase invariants (for correctness and evaluation):
	//   - Applied: appliedCPULimit == desiredCPULimit AND appliedCPURequest == desiredCPURequest
	//   - Pending: reconcile in progress (controller is working to apply desired state)
	//   - Failed: controller must retry later (temporary failure, will be retried)
	//
	// +kubebuilder:validation:Enum=Pending;Applied;Failed
	Phase string `json:"phase,omitempty"`

	// Reason provides a human-readable reason for the current phase.
	Reason string `json:"reason,omitempty"`

	// LastAppliedTime is the timestamp when the CPU resources were last successfully applied.
	LastAppliedTime *metav1.Time `json:"lastAppliedTime,omitempty"`

	// LastAttemptTime is the timestamp of the last attempt to apply the CPU resources.
	LastAttemptTime *metav1.Time `json:"lastAttemptTime,omitempty"`

	// ShadowPriceCPU is the current CPU shadow price (Lagrange multiplier).
	// Agents can read this to adjust their demand based on market conditions.
	// Higher prices indicate resource scarcity, causing agents to reduce demand.
	// Lower prices indicate resource abundance, allowing agents to increase demand.
	ShadowPriceCPU float64 `json:"shadowPriceCPU"`
}

// PodAllocationList contains a list of PodAllocation objects.
//
// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type PodAllocationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PodAllocation `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PodAllocation{}, &PodAllocationList{})
}
