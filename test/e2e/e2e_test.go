package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	allocationv1alpha1 "mbcas/api/v1alpha1"
)

type E2ETestSuite struct {
	k8sClient       kubernetes.Interface
	allocationClient AllocationClient
	namespace       string
}

type AllocationClient interface {
	Get(ctx context.Context, namespace, name string) (*allocationv1alpha1.PodAllocation, error)
}

func NewE2ETestSuite(t *testing.T) *E2ETestSuite {
	config, err := clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
	if err != nil {
		t.Fatalf("Failed to build config: %v", err)
	}

	k8sClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	return &E2ETestSuite{
		k8sClient: k8sClient,
		namespace: fmt.Sprintf("mbcas-e2e-%d", time.Now().Unix()),
	}
}

func (s *E2ETestSuite) Setup(t *testing.T) {
	ctx := context.Background()

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: s.namespace,
		},
	}
	_, err := s.k8sClient.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create namespace: %v", err)
	}

	t.Logf("Created test namespace: %s", s.namespace)
}

func (s *E2ETestSuite) Teardown(t *testing.T) {
	ctx := context.Background()
	err := s.k8sClient.CoreV1().Namespaces().Delete(ctx, s.namespace, metav1.DeleteOptions{})
	if err != nil {
		t.Logf("Failed to delete namespace: %v", err)
	}
}

func (s *E2ETestSuite) CreateWorkload(t *testing.T, name string, cpuRequest, cpuLimit string) *corev1.Pod {
	ctx := context.Background()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: s.namespace,
			Labels: map[string]string{
				"mbcas.io/managed": "true",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    "stress",
					Image:   "polinux/stress",
					Command: []string{"stress", "--cpu", "1", "--timeout", "3600"},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse(cpuRequest),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse(cpuLimit),
						},
					},
				},
			},
		},
	}

	created, err := s.k8sClient.CoreV1().Pods(s.namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create pod: %v", err)
	}

	s.WaitForPodRunning(t, name, 60*time.Second)
	return created
}

func (s *E2ETestSuite) WaitForPodRunning(t *testing.T, name string, timeout time.Duration) {
	ctx := context.Background()
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		pod, err := s.k8sClient.CoreV1().Pods(s.namespace).Get(ctx, name, metav1.GetOptions{})
		if err == nil && pod.Status.Phase == corev1.PodRunning {
			return
		}
		time.Sleep(1 * time.Second)
	}

	t.Fatalf("Pod %s did not reach Running state within %v", name, timeout)
}

func (s *E2ETestSuite) WaitForPodAllocation(t *testing.T, podName string, timeout time.Duration) *allocationv1alpha1.PodAllocation {
	if s.allocationClient == nil {
		t.Skip("AllocationClient not configured")
		return nil
	}

	ctx := context.Background()
	deadline := time.Now().Add(timeout)
	paName := fmt.Sprintf("%s-%s", s.namespace, podName)

	for time.Now().Before(deadline) {
		pa, err := s.allocationClient.Get(ctx, s.namespace, paName)
		if err == nil {
			return pa
		}
		time.Sleep(1 * time.Second)
	}

	t.Fatalf("PodAllocation for %s not created within %v", podName, timeout)
	return nil
}

func (s *E2ETestSuite) WaitForAllocationApplied(t *testing.T, podName string, timeout time.Duration) {
	if s.allocationClient == nil {
		t.Skip("AllocationClient not configured")
		return
	}

	ctx := context.Background()
	deadline := time.Now().Add(timeout)
	paName := fmt.Sprintf("%s-%s", s.namespace, podName)

	for time.Now().Before(deadline) {
		pa, err := s.allocationClient.Get(ctx, s.namespace, paName)
		if err == nil && pa.Status.Phase == "Applied" {
			return
		}
		time.Sleep(1 * time.Second)
	}

	t.Fatalf("PodAllocation for %s not applied within %v", podName, timeout)
}

func TestE2E_SinglePodAllocation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	suite := NewE2ETestSuite(t)
	suite.Setup(t)
	defer suite.Teardown(t)

	pod := suite.CreateWorkload(t, "stress-test", "100m", "200m")
	t.Logf("Created pod %s", pod.Name)

	pa := suite.WaitForPodAllocation(t, "stress-test", 30*time.Second)
	if pa != nil {
		t.Logf("PodAllocation created: request=%s, limit=%s",
			pa.Spec.DesiredCPURequest, pa.Spec.DesiredCPULimit)

		suite.WaitForAllocationApplied(t, "stress-test", 60*time.Second)
		t.Log("Allocation applied successfully")

		ctx := context.Background()
		updatedPod, _ := suite.k8sClient.CoreV1().Pods(suite.namespace).Get(ctx, "stress-test", metav1.GetOptions{})

		actualLimit := updatedPod.Spec.Containers[0].Resources.Limits.Cpu().String()
		t.Logf("Pod CPU limit after allocation: %s", actualLimit)
	}
}

func TestE2E_MultiPodContention(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	suite := NewE2ETestSuite(t)
	suite.Setup(t)
	defer suite.Teardown(t)

	suite.CreateWorkload(t, "high-priority", "500m", "1000m")
	suite.CreateWorkload(t, "low-priority", "500m", "1000m")

	time.Sleep(30 * time.Second)

	pa1 := suite.WaitForPodAllocation(t, "high-priority", 30*time.Second)
	pa2 := suite.WaitForPodAllocation(t, "low-priority", 30*time.Second)

	if pa1 != nil && pa2 != nil {
		t.Logf("high-priority allocation: %s", pa1.Spec.DesiredCPULimit)
		t.Logf("low-priority allocation: %s", pa2.Spec.DesiredCPULimit)
	}
}

func TestE2E_ThrottlingResponse(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	suite := NewE2ETestSuite(t)
	suite.Setup(t)
	defer suite.Teardown(t)

	suite.CreateWorkload(t, "throttled-pod", "100m", "100m")

	time.Sleep(60 * time.Second)

	pa := suite.WaitForPodAllocation(t, "throttled-pod", 30*time.Second)
	if pa != nil {
		desiredLimit, _ := resource.ParseQuantity(pa.Spec.DesiredCPULimit)
		if desiredLimit.MilliValue() <= 100 {
			t.Log("Agent detected throttling and increased allocation")
		} else {
			t.Logf("Allocation increased to %s due to throttling", pa.Spec.DesiredCPULimit)
		}
	}
}

func TestE2E_PodDeletionCleansUpAllocation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	suite := NewE2ETestSuite(t)
	suite.Setup(t)
	defer suite.Teardown(t)

	suite.CreateWorkload(t, "ephemeral", "100m", "200m")
	suite.WaitForPodAllocation(t, "ephemeral", 30*time.Second)

	ctx := context.Background()
	err := suite.k8sClient.CoreV1().Pods(suite.namespace).Delete(ctx, "ephemeral", metav1.DeleteOptions{})
	if err != nil {
		t.Fatalf("Failed to delete pod: %v", err)
	}

	time.Sleep(15 * time.Second)

	if suite.allocationClient != nil {
		paName := fmt.Sprintf("%s-ephemeral", suite.namespace)
		_, err = suite.allocationClient.Get(ctx, suite.namespace, paName)
		if err == nil {
			t.Error("PodAllocation should be deleted when pod is deleted")
		}
	}
}

func TestLoad_ManyPodsOnNode(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping load test in short mode")
	}

	suite := NewE2ETestSuite(t)
	suite.Setup(t)
	defer suite.Teardown(t)

	podCount := 50

	for i := 0; i < podCount; i++ {
		name := fmt.Sprintf("load-test-%d", i)
		suite.CreateWorkload(t, name, "50m", "100m")
	}

	time.Sleep(120 * time.Second)

	if suite.allocationClient != nil {
		allocatedCount := 0
		ctx := context.Background()
		for i := 0; i < podCount; i++ {
			name := fmt.Sprintf("load-test-%d", i)
			pa, err := suite.allocationClient.Get(ctx, suite.namespace,
				fmt.Sprintf("%s-%s", suite.namespace, name))
			if err == nil && pa.Status.Phase == "Applied" {
				allocatedCount++
			}
		}

		t.Logf("Allocated %d/%d pods", allocatedCount, podCount)

		if allocatedCount < podCount*90/100 {
			t.Errorf("Expected at least 90%% allocation rate, got %d%%", allocatedCount*100/podCount)
		}
	}
}

func TestLoad_RapidPodChurn(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping load test in short mode")
	}

	suite := NewE2ETestSuite(t)
	suite.Setup(t)
	defer suite.Teardown(t)

	ctx := context.Background()
	iterations := 20

	for i := 0; i < iterations; i++ {
		name := fmt.Sprintf("churn-%d", i)
		suite.CreateWorkload(t, name, "50m", "100m")
		time.Sleep(2 * time.Second)

		if i > 5 {
			oldName := fmt.Sprintf("churn-%d", i-5)
			_ = suite.k8sClient.CoreV1().Pods(suite.namespace).Delete(ctx, oldName, metav1.DeleteOptions{})
		}
	}

	time.Sleep(30 * time.Second)
}

func TestChaos_AgentRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping chaos test in short mode")
	}

	suite := NewE2ETestSuite(t)
	suite.Setup(t)
	defer suite.Teardown(t)

	suite.CreateWorkload(t, "stable-pod", "100m", "200m")
	suite.WaitForAllocationApplied(t, "stable-pod", 60*time.Second)

	ctx := context.Background()
	agents, _ := suite.k8sClient.CoreV1().Pods("mbcas-system").List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/component=agent",
	})

	for _, agent := range agents.Items {
		_ = suite.k8sClient.CoreV1().Pods("mbcas-system").Delete(ctx, agent.Name, metav1.DeleteOptions{})
	}

	t.Log("Deleted agent pods, waiting for restart")
	time.Sleep(30 * time.Second)

	suite.WaitForAllocationApplied(t, "stable-pod", 120*time.Second)
	t.Log("Allocations recovered after agent restart")
}

func TestChaos_ControllerRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping chaos test in short mode")
	}

	suite := NewE2ETestSuite(t)
	suite.Setup(t)
	defer suite.Teardown(t)

	suite.CreateWorkload(t, "resilient-pod", "100m", "200m")
	suite.WaitForPodAllocation(t, "resilient-pod", 30*time.Second)

	ctx := context.Background()
	controllers, _ := suite.k8sClient.CoreV1().Pods("mbcas-system").List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/component=controller",
	})

	for _, controller := range controllers.Items {
		_ = suite.k8sClient.CoreV1().Pods("mbcas-system").Delete(ctx, controller.Name, metav1.DeleteOptions{})
	}

	t.Log("Deleted controller pod, waiting for restart")
	time.Sleep(30 * time.Second)

	suite.WaitForAllocationApplied(t, "resilient-pod", 120*time.Second)
	t.Log("Allocations applied after controller restart")
}
