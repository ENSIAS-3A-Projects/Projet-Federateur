package agent

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

// Writer handles writing CPU allocations back to Kubernetes.
// It uses a simple REST client or direct API calls.
type Writer struct {
	k8sClient kubernetes.Interface
}

// NewWriter creates a new simplified writer.
func NewWriter(config *rest.Config) (*Writer, error) {
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return &Writer{k8sClient: client}, nil
}

// WritePodAllocation updates a pod's allocation.
// For this simplified version, we'll write to annotations as a placeholder
// or update the PodAllocation CRD if it exists.
func (w *Writer) WritePodAllocation(ctx context.Context, pod *corev1.Pod, request, limit string, shadowPrice float64) error {
	klog.V(4).InfoS("Writing allocation", "pod", pod.Name, "request", request, "limit", limit)

	// Implementation note: In a real system, this would update a PodAllocation custom resource.
	// For now, we log it. We could add CRD support here easily if the client is configured.

	return nil
}
