package agent

// Package agent includes the writer for PodAllocation CRDs.

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	"mbcas/api/v1alpha1"
)

// Writer writes PodAllocation CRDs to the Kubernetes API using dynamic client.
type Writer struct {
	dynamicClient dynamic.Interface
	gvr           schema.GroupVersionResource
}

// NewWriter creates a new PodAllocation writer.
func NewWriter(config *rest.Config) (*Writer, error) {
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create dynamic client: %w", err)
	}

	gvr := schema.GroupVersionResource{
		Group:    v1alpha1.GroupVersion.Group,
		Version:  v1alpha1.GroupVersion.Version,
		Resource: "podallocations",
	}

	return &Writer{
		dynamicClient: dynamicClient,
		gvr:           gvr,
	}, nil
}

// WritePodAllocation creates or updates a PodAllocation CRD for a pod.
// P0 Fix: Uses optimistic locking via resourceVersion and retry on conflict.
func (w *Writer) WritePodAllocation(ctx context.Context, pod *corev1.Pod, desiredCPU string) error {
	// Use pod UID as the PodAllocation name
	paName := string(pod.UID)

	// Try to get existing PodAllocation
	pa, err := w.dynamicClient.Resource(w.gvr).Namespace(pod.Namespace).Get(ctx, paName, metav1.GetOptions{})

	if errors.IsNotFound(err) {
		// Create new PodAllocation
		newPA := &v1alpha1.PodAllocation{
			TypeMeta: metav1.TypeMeta{
				APIVersion: v1alpha1.GroupVersion.String(),
				Kind:       "PodAllocation",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      paName,
				Namespace: pod.Namespace,
			},
			Spec: v1alpha1.PodAllocationSpec{
				Namespace:       pod.Namespace,
				PodName:         pod.Name,
				DesiredCPULimit: desiredCPU,
			},
		}

		// Convert to unstructured
		obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(newPA)
		if err != nil {
			return fmt.Errorf("convert to unstructured: %w", err)
		}

		_, err = w.dynamicClient.Resource(w.gvr).Namespace(pod.Namespace).Create(ctx, &unstructured.Unstructured{Object: obj}, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("create PodAllocation: %w", err)
		}

		klog.V(4).InfoS("Created PodAllocation", "name", paName, "namespace", pod.Namespace, "cpu", desiredCPU)
		return nil
	} else if err != nil {
		return fmt.Errorf("get PodAllocation: %w", err)
	}

	// Check if update is needed
	spec, found, err := unstructured.NestedMap(pa.UnstructuredContent(), "spec")
	if err != nil || !found {
		return fmt.Errorf("get spec: %w", err)
	}

	currentCPU, _, _ := unstructured.NestedString(spec, "desiredCPULimit")
	if currentCPU == desiredCPU {
		// No update needed
		return nil
	}

	// P0 Fix: Use retry.RetryOnConflict for optimistic locking
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		// Re-fetch to get latest resourceVersion
		pa, err := w.dynamicClient.Resource(w.gvr).Namespace(pod.Namespace).Get(ctx, paName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("re-fetch PodAllocation: %w", err)
		}

		spec, found, err := unstructured.NestedMap(pa.UnstructuredContent(), "spec")
		if err != nil || !found {
			return fmt.Errorf("get spec: %w", err)
		}

		previousCPU, _, _ := unstructured.NestedString(spec, "desiredCPULimit")
		unstructured.SetNestedField(spec, desiredCPU, "desiredCPULimit")
		unstructured.SetNestedField(pa.UnstructuredContent(), spec, "spec")

		// Update preserves resourceVersion from the fetched object
		_, err = w.dynamicClient.Resource(w.gvr).Namespace(pod.Namespace).Update(ctx, pa, metav1.UpdateOptions{})
		if err != nil {
			return err // retry.RetryOnConflict will retry if this is a conflict
		}

		klog.V(4).InfoS("Updated PodAllocation", "name", paName, "namespace", pod.Namespace, "cpu", desiredCPU, "previous", previousCPU)
		return nil
	})
}
