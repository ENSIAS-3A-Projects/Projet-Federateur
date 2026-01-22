package integration

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	allocationv1alpha1 "mbcas/api/v1alpha1"
	"mbcas/pkg/agent"
)

func TestIntegration_AgentWritesControllerReconciles(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	scheme := runtime.NewScheme()
	_ = allocationv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
			UID:       "uid-123",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "main",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("100m"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("200m"),
						},
					},
				},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pod).
		Build()

	// Create writer with fake client for testing
	writer := agent.NewWriterForTesting(fakeClient)

	err := writer.WritePodAllocation(ctx, pod, "450m", "500m", 0.0)
	if err != nil {
		t.Fatalf("Writer failed: %v", err)
	}

	pa := &allocationv1alpha1.PodAllocation{}
	err = fakeClient.Get(ctx, types.NamespacedName{
		Namespace: "default",
		Name:      "default-test-pod",
	}, pa)
	if err != nil {
		t.Fatalf("PodAllocation not found: %v", err)
	}

	if pa.Spec.DesiredCPULimit != "500m" {
		t.Errorf("PodAllocation has wrong limit: %s", pa.Spec.DesiredCPULimit)
	}
	if pa.Spec.PodName != "test-pod" {
		t.Errorf("PodAllocation references wrong pod: %s", pa.Spec.PodName)
	}
}
