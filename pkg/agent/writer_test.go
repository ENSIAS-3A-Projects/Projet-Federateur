package agent

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	allocationv1alpha1 "mbcas/api/v1alpha1"
)

func TestWriter_CreatesPodAllocation(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = allocationv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	writer := &Writer{client: fakeClient}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
			UID:       "uid-123",
		},
	}

	ctx := context.Background()
	err := writer.WritePodAllocation(ctx, pod, "450m", "500m", 0.0)
	if err != nil {
		t.Fatalf("WritePodAllocation failed: %v", err)
	}

	pa := &allocationv1alpha1.PodAllocation{}
	err = fakeClient.Get(ctx, types.NamespacedName{
		Namespace: "default",
		Name:      "default-test-pod",
	}, pa)
	if err != nil {
		t.Fatalf("PodAllocation not created: %v", err)
	}

	if pa.Spec.DesiredCPURequest != "450m" {
		t.Errorf("Expected request 450m, got %s", pa.Spec.DesiredCPURequest)
	}
	if pa.Spec.DesiredCPULimit != "500m" {
		t.Errorf("Expected limit 500m, got %s", pa.Spec.DesiredCPULimit)
	}
	if pa.Spec.PodName != "test-pod" {
		t.Errorf("Expected podName test-pod, got %s", pa.Spec.PodName)
	}
}

func TestWriter_UpdatesExistingPodAllocation(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = allocationv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	existing := &allocationv1alpha1.PodAllocation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default-test-pod",
			Namespace: "default",
		},
		Spec: allocationv1alpha1.PodAllocationSpec{
			Namespace:         "default",
			PodName:           "test-pod",
			DesiredCPURequest: "200m",
			DesiredCPULimit:   "250m",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(existing).
		Build()
	writer := &Writer{client: fakeClient}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
			UID:       "uid-123",
		},
	}

	ctx := context.Background()
	err := writer.WritePodAllocation(ctx, pod, "450m", "500m", 0.0)
	if err != nil {
		t.Fatalf("WritePodAllocation update failed: %v", err)
	}

	pa := &allocationv1alpha1.PodAllocation{}
	_ = fakeClient.Get(ctx, types.NamespacedName{
		Namespace: "default",
		Name:      "default-test-pod",
	}, pa)

	if pa.Spec.DesiredCPULimit != "500m" {
		t.Errorf("Expected updated limit 500m, got %s", pa.Spec.DesiredCPULimit)
	}
}

func TestWriter_SkipsUpdateWhenUnchanged(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = allocationv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	existing := &allocationv1alpha1.PodAllocation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default-test-pod",
			Namespace: "default",
		},
		Spec: allocationv1alpha1.PodAllocationSpec{
			Namespace:         "default",
			PodName:           "test-pod",
			DesiredCPURequest: "450m",
			DesiredCPULimit:   "500m",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(existing).
		Build()
	writer := &Writer{client: fakeClient}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
		},
	}

	ctx := context.Background()
	err := writer.WritePodAllocation(ctx, pod, "450m", "500m", 0.0)
	if err != nil {
		t.Fatalf("WritePodAllocation should succeed when unchanged: %v", err)
	}
}

func TestWriter_DeletesPodAllocation(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = allocationv1alpha1.AddToScheme(scheme)

	existing := &allocationv1alpha1.PodAllocation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default-test-pod",
			Namespace: "default",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(existing).
		Build()
	writer := &Writer{client: fakeClient}

	ctx := context.Background()
	err := writer.DeletePodAllocation(ctx, "default", "test-pod")
	if err != nil {
		t.Fatalf("DeletePodAllocation failed: %v", err)
	}

	pa := &allocationv1alpha1.PodAllocation{}
	err = fakeClient.Get(ctx, types.NamespacedName{
		Namespace: "default",
		Name:      "default-test-pod",
	}, pa)
	if err == nil {
		t.Error("PodAllocation should be deleted")
	}
}
