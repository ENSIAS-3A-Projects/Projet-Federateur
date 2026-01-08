package integration

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	allocationv1alpha1 "mbcas/api/v1alpha1"
)

// TestEnvironment wraps envtest.Environment with helper methods.
type TestEnvironment struct {
	*envtest.Environment
	Config     *rest.Config
	K8sClient  kubernetes.Interface
	Client     client.Client
	Scheme     *runtime.Scheme
	Cancel     context.CancelFunc
	Ctx        context.Context
}

// SetupTestEnvironment creates a test Kubernetes environment using envtest.
func SetupTestEnvironment() (*TestEnvironment, error) {
	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{"../../config/crd/bases"},
	}

	cfg, err := testEnv.Start()
	if err != nil {
		return nil, fmt.Errorf("start test environment: %w", err)
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("add client-go scheme: %w", err)
	}
	if err := allocationv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("add allocation scheme: %w", err)
	}

	k8sClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("create k8s client: %w", err)
	}

	client, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("create controller client: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &TestEnvironment{
		Environment: testEnv,
		Config:      cfg,
		K8sClient:   k8sClient,
		Client:      client,
		Scheme:      scheme,
		Cancel:      cancel,
		Ctx:         ctx,
	}, nil
}

// Cleanup stops the test environment.
func (te *TestEnvironment) Cleanup() error {
	te.Cancel()
	return te.Environment.Stop()
}

// CreateTestPod creates a test pod for integration testing.
func CreateTestPod(namespace, name, nodeName string, cpuRequest, cpuLimit string) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"app": name,
			},
		},
		Spec: corev1.PodSpec{
			NodeName: nodeName,
			Containers: []corev1.Container{
				{
					Name:  "test-container",
					Image: "busybox:latest",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{},
						Limits:   corev1.ResourceList{},
					},
				},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	if cpuRequest != "" {
		qty, _ := resource.ParseQuantity(cpuRequest)
		pod.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = qty
	}
	if cpuLimit != "" {
		qty, _ := resource.ParseQuantity(cpuLimit)
		pod.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU] = qty
	}

	return pod
}

// CreateTestNode creates a test node for integration testing.
func CreateTestNode(name string, cpuCapacity string) *corev1.Node {
	qty, _ := resource.ParseQuantity(cpuCapacity)
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU: qty,
			},
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU: qty,
			},
		},
	}
}

// WaitForPodAllocation waits for a PodAllocation to be created and applied.
// PodAllocation name is the pod UID.
func WaitForPodAllocation(ctx context.Context, c client.Client, namespace string, podUID types.UID, timeout time.Duration) (*allocationv1alpha1.PodAllocation, error) {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("timeout waiting for PodAllocation")
			}

			pa := &allocationv1alpha1.PodAllocation{}
			key := types.NamespacedName{
				Namespace: namespace,
				Name:      string(podUID),
			}
			if err := c.Get(ctx, key, pa); err == nil {
				if pa.Status.Phase == "Applied" {
					return pa, nil
				}
			}
		}
	}
}

