package agent

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func TestAgent_UsesNodeCapacity(t *testing.T) {
	ctx := context.Background()

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node",
		},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("4000m"),
			},
		},
	}

	fakeK8s := k8sfake.NewSimpleClientset(node)

	agent := &Agent{
		k8sClient: fakeK8s,
		nodeName:  "test-node",
		ctx:       ctx,
		config:    DefaultConfig(),
	}

	capacity := agent.getUnmanagedPodsCPU()

	if capacity != 4000 {
		t.Errorf("Expected node capacity 4000m, got %d", capacity)
	}
}

func TestAgent_SubtractsUnmanagedPods(t *testing.T) {
	ctx := context.Background()

	systemPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kube-proxy",
			Namespace: "kube-system",
		},
		Spec: corev1.PodSpec{
			NodeName: "test-node",
			Containers: []corev1.Container{
				{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("100m"),
						},
					},
				},
			},
		},
	}

	fakeK8s := k8sfake.NewSimpleClientset(systemPod)

	agent := &Agent{
		k8sClient: fakeK8s,
		nodeName:  "test-node",
		ctx:       ctx,
		config:    DefaultConfig(),
	}

	unmanagedCPU := agent.getUnmanagedPodsCPU()

	if unmanagedCPU != 100 {
		t.Errorf("Expected unmanaged CPU 100m, got %d", unmanagedCPU)
	}
}
