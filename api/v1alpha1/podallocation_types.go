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
// +kubebuilder:printcolumn:name="Desired CPU",type=string,JSONPath=`.spec.desiredCPULimit`
// +kubebuilder:printcolumn:name="Applied CPU",type=string,JSONPath=`.status.appliedCPULimit`
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

	// DesiredCPULimit is the desired CPU limit for the pod.
	// This is a Kubernetes resource quantity string (e.g., "500m", "1", "2.5").
	// +kubebuilder:validation:Required
	DesiredCPULimit string `json:"desiredCPULimit"`
}

// PodAllocationStatus defines the observed state of PodAllocation.
type PodAllocationStatus struct {
	// AppliedCPULimit is the CPU limit that was last successfully applied to the pod.
	// This may differ from desiredCPULimit if the application is in progress or failed.
	AppliedCPULimit string `json:"appliedCPULimit,omitempty"`

	// Phase represents the current phase of the allocation.
	// Possible values: Pending, Applied, Failed
	//
	// Phase invariants (for correctness and evaluation):
	//   - Applied: appliedCPULimit == desiredCPULimit (allocation successfully applied)
	//   - Pending: reconcile in progress (controller is working to apply desired state)
	//   - Failed: controller must retry later (temporary failure, will be retried)
	//
	// +kubebuilder:validation:Enum=Pending;Applied;Failed
	Phase string `json:"phase,omitempty"`

	// Reason provides a human-readable reason for the current phase.
	Reason string `json:"reason,omitempty"`

	// LastAppliedTime is the timestamp when the CPU limit was last successfully applied.
	LastAppliedTime *metav1.Time `json:"lastAppliedTime,omitempty"`

	// LastAttemptTime is the timestamp of the last attempt to apply the CPU limit.
	LastAttemptTime *metav1.Time `json:"lastAttemptTime,omitempty"`
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
