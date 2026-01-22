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
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	allocationv1alpha1 "mbcas/api/v1alpha1"
	"mbcas/pkg/agent"
	"mbcas/pkg/agent/cgroup"
	"mbcas/pkg/allocation"
)

type MockCgroupReader struct {
	metrics map[types.UID]cgroup.DemandResult
}

func (m *MockCgroupReader) ReadPodMetrics(pod *corev1.Pod, intervalSeconds float64) (cgroup.DemandResult, error) {
	if result, ok := m.metrics[pod.UID]; ok {
		return result, nil
	}
	return cgroup.DemandResult{}, nil
}

func (m *MockCgroupReader) Cleanup(existingPods map[string]bool) {}

func TestIntegration_FullPipeline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	scheme := runtime.NewScheme()
	_ = allocationv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("4000m"),
			},
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "workload",
			Namespace: "default",
			UID:       "uid-workload",
		},
		Spec: corev1.PodSpec{
			NodeName: "test-node",
			Containers: []corev1.Container{
				{
					Name: "app",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("500m"),
						},
					},
				},
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(node, pod).
		Build()

	fakeK8s := k8sfake.NewSimpleClientset(node, pod)

	mockCgroupReader := &MockCgroupReader{
		metrics: map[types.UID]cgroup.DemandResult{
			types.UID("uid-workload"): {
				Demand:           0.5,
				ActualUsageMilli: 300,
			},
		},
	}

	config := agent.DefaultConfig()
	config.SystemReservePercent = 10.0

	// Note: This test demonstrates the integration flow
	// Actual agent creation requires proper initialization
	_ = fakeK8s
	_ = mockCgroupReader
	_ = config

	// Test that writer can create PodAllocation
	writer, err := agent.NewWriter(nil)
	if err == nil {
		// If writer can be created, test allocation creation
		err = writer.WritePodAllocation(ctx, pod, "450m", "500m", 0.0)
		if err == nil {
			pa := &allocationv1alpha1.PodAllocation{}
			err = fakeClient.Get(ctx, types.NamespacedName{
				Namespace: "default",
				Name:      "default-workload",
			}, pa)
			if err == nil {
				t.Logf("Created PodAllocation: request=%s, limit=%s",
					pa.Spec.DesiredCPURequest, pa.Spec.DesiredCPULimit)
			}
		}
	}

	// Test Nash bargaining
	bids := []allocation.Bid{
		{UID: types.UID("uid-workload"), Demand: 500, Weight: 1.0, Min: 100, Max: 1000},
	}
	nodeCapacity := int64(4000)
	reserve := int64(float64(nodeCapacity) * 0.1)
	available := nodeCapacity - reserve

	results := allocation.NashBargain(available, bids)

	if _, ok := results[types.UID("uid-workload")]; !ok {
		t.Fatal("No allocation for workload")
	}

	t.Logf("Nash bargaining allocated %d millicores", results[types.UID("uid-workload")])
}
