package agent

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	allocationv1alpha1 "mbcas/api/v1alpha1"
)

// Writer handles writing CPU allocations back to Kubernetes.
type Writer struct {
	client client.Client
}

// NewWriter creates a new writer with controller-runtime client.
func NewWriter(config *rest.Config) (*Writer, error) {
	scheme := runtime.NewScheme()
	_ = allocationv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	c, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("create controller-runtime client: %w", err)
	}
	return &Writer{client: c}, nil
}

// WritePodAllocation creates or updates a PodAllocation custom resource.
func (w *Writer) WritePodAllocation(ctx context.Context, pod *corev1.Pod, request, limit string, shadowPrice float64) error {
	name := fmt.Sprintf("%s-%s", pod.Namespace, pod.Name)
	
	pa := &allocationv1alpha1.PodAllocation{}
	err := w.client.Get(ctx, types.NamespacedName{
		Namespace: pod.Namespace,
		Name:      name,
	}, pa)

	if apierrors.IsNotFound(err) {
		pa = &allocationv1alpha1.PodAllocation{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: pod.Namespace,
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "v1",
						Kind:       "Pod",
						Name:       pod.Name,
						UID:        pod.UID,
					},
				},
			},
			Spec: allocationv1alpha1.PodAllocationSpec{
				Namespace:         pod.Namespace,
				PodName:           pod.Name,
				DesiredCPURequest: request,
				DesiredCPULimit:   limit,
			},
		}
		if err := w.client.Create(ctx, pa); err != nil {
			return fmt.Errorf("create PodAllocation: %w", err)
		}
		klog.V(2).InfoS("Created PodAllocation", "name", name, "request", request, "limit", limit)
		return nil
	}

	if err != nil {
		return fmt.Errorf("get PodAllocation: %w", err)
	}

	if pa.Spec.DesiredCPURequest == request && pa.Spec.DesiredCPULimit == limit {
		return nil
	}

	pa.Spec.DesiredCPURequest = request
	pa.Spec.DesiredCPULimit = limit
	if err := w.client.Update(ctx, pa); err != nil {
		return fmt.Errorf("update PodAllocation: %w", err)
	}
	klog.V(2).InfoS("Updated PodAllocation", "name", name, "request", request, "limit", limit)
	return nil
}

// DeletePodAllocation deletes a PodAllocation custom resource.
func (w *Writer) DeletePodAllocation(ctx context.Context, namespace, podName string) error {
	name := fmt.Sprintf("%s-%s", namespace, podName)
	pa := &allocationv1alpha1.PodAllocation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	if err := w.client.Delete(ctx, pa); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete PodAllocation: %w", err)
	}
	return nil
}

// NewWriterForTesting creates a Writer with a provided client for testing purposes.
func NewWriterForTesting(c client.Client) *Writer {
	return &Writer{client: c}
}

// GetActualAllocation reads the actual applied allocation from PodAllocation CR status.
func (w *Writer) GetActualAllocation(ctx context.Context, pod *corev1.Pod) int64 {
	name := fmt.Sprintf("%s-%s", pod.Namespace, pod.Name)
	pa := &allocationv1alpha1.PodAllocation{}
	err := w.client.Get(ctx, types.NamespacedName{
		Namespace: pod.Namespace,
		Name:      name,
	}, pa)
	if err != nil {
		return 0
	}
	if pa.Status.AppliedCPULimit != "" {
		qty, err := resource.ParseQuantity(pa.Status.AppliedCPULimit)
		if err == nil {
			return qty.MilliValue()
		}
	}
	return 0
}
