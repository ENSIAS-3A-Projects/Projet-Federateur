// +build integration

package integration

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	allocationv1alpha1 "mbcas/api/v1alpha1"
)

// TestAgentStartsAndSamples verifies that the agent can start and begin sampling cgroups.
func TestAgentStartsAndSamples(t *testing.T) {
	te, err := SetupTestEnvironment()
	if err != nil {
		t.Fatalf("Failed to setup test environment: %v", err)
	}
	defer te.Cleanup()

	// Create a test node
	node := CreateTestNode("test-node", "2000m")
	if _, err := te.K8sClient.CoreV1().Nodes().Create(te.Ctx, node, metav1.CreateOptions{}); err != nil {
		t.Fatalf("Failed to create test node: %v", err)
	}

	// Create a test pod
	pod := CreateTestPod("default", "test-pod", "test-node", "100m", "500m")
	pod.Labels["mbcas.io/managed"] = "true"
	createdPod, err := te.K8sClient.CoreV1().Pods("default").Create(te.Ctx, pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create test pod: %v", err)
	}

	// Note: In a real integration test, we would start the agent here
	// and verify it samples the pod. For now, this is a placeholder structure.
	t.Logf("Created test pod: %s", createdPod.Name)
}

// TestControllerReconcilesCRD verifies that the controller updates pod resources from CRD.
func TestControllerReconcilesCRD(t *testing.T) {
	te, err := SetupTestEnvironment()
	if err != nil {
		t.Fatalf("Failed to setup test environment: %v", err)
	}
	defer te.Cleanup()

	// Create a test pod
	pod := CreateTestPod("default", "test-pod", "test-node", "100m", "500m")
	createdPod, err := te.K8sClient.CoreV1().Pods("default").Create(te.Ctx, pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create test pod: %v", err)
	}

	// Create a PodAllocation CRD
	pa := &allocationv1alpha1.PodAllocation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      string(createdPod.UID),
			Namespace: "default",
		},
		Spec: allocationv1alpha1.PodAllocationSpec{
			Namespace:         "default",
			PodName:           "test-pod",
			DesiredCPURequest: "200m",
			DesiredCPULimit:   "600m",
		},
	}

	if err := te.Client.Create(te.Ctx, pa); err != nil {
		t.Fatalf("Failed to create PodAllocation: %v", err)
	}

	// Note: In a real integration test, we would start the controller here
	// and verify it reconciles the CRD and updates the pod resources.
	t.Logf("Created PodAllocation for pod: %s", createdPod.Name)
}

// TestEndToEndAllocation tests the full flow: Create pod → agent detects → writes CRD → controller applies.
func TestEndToEndAllocation(t *testing.T) {
	te, err := SetupTestEnvironment()
	if err != nil {
		t.Fatalf("Failed to setup test environment: %v", err)
	}
	defer te.Cleanup()

	// Create a test node
	node := CreateTestNode("test-node", "2000m")
	if _, err := te.K8sClient.CoreV1().Nodes().Create(te.Ctx, node, metav1.CreateOptions{}); err != nil {
		t.Fatalf("Failed to create test node: %v", err)
	}

	// Create a test pod
	pod := CreateTestPod("default", "test-pod", "test-node", "100m", "500m")
	pod.Labels["mbcas.io/managed"] = "true"
	createdPod, err := te.K8sClient.CoreV1().Pods("default").Create(te.Ctx, pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create test pod: %v", err)
	}

	// Note: In a real integration test, we would:
	// 1. Start the agent and verify it detects the pod
	// 2. Verify the agent writes a PodAllocation CRD
	// 3. Start the controller and verify it applies the allocation
	// 4. Verify the pod's resources are updated

	t.Logf("End-to-end test setup complete for pod: %s", createdPod.Name)
}

// TestSLOViolationTriggersFastUp verifies that high latency triggers fast guardrail.
func TestSLOViolationTriggersFastUp(t *testing.T) {
	te, err := SetupTestEnvironment()
	if err != nil {
		t.Fatalf("Failed to setup test environment: %v", err)
	}
	defer te.Cleanup()

	// Create a test pod with SLO annotation
	pod := CreateTestPod("default", "test-pod", "test-node", "100m", "500m")
	pod.Labels["mbcas.io/managed"] = "true"
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	pod.Annotations["mbcas.io/target-latency-ms"] = "100"

	createdPod, err := te.K8sClient.CoreV1().Pods("default").Create(te.Ctx, pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create test pod: %v", err)
	}

	// Note: In a real integration test, we would:
	// 1. Inject high latency metrics (via Prometheus mock)
	// 2. Verify fast guardrail triggers
	// 3. Verify allocation increases quickly

	t.Logf("SLO violation test setup complete for pod: %s", createdPod.Name)
}

// TestMarketClearingUnderContention verifies fair allocation when multiple pods compete for CPU.
func TestMarketClearingUnderContention(t *testing.T) {
	te, err := SetupTestEnvironment()
	if err != nil {
		t.Fatalf("Failed to setup test environment: %v", err)
	}
	defer te.Cleanup()

	// Create a test node with limited capacity
	node := CreateTestNode("test-node", "1000m")
	if _, err := te.K8sClient.CoreV1().Nodes().Create(te.Ctx, node, metav1.CreateOptions{}); err != nil {
		t.Fatalf("Failed to create test node: %v", err)
	}

	// Create multiple competing pods
	for i := 0; i < 3; i++ {
		pod := CreateTestPod("default", "test-pod-"+string(rune('a'+i)), "test-node", "200m", "1000m")
		pod.Labels["mbcas.io/managed"] = "true"
		if _, err := te.K8sClient.CoreV1().Pods("default").Create(te.Ctx, pod, metav1.CreateOptions{}); err != nil {
			t.Fatalf("Failed to create test pod %d: %v", i, err)
		}
	}

	// Note: In a real integration test, we would:
	// 1. Start the agent and verify it detects all pods
	// 2. Verify market clearing allocates resources fairly
	// 3. Verify shadow prices are set correctly
	// 4. Verify allocations sum to capacity

	t.Log("Market clearing test setup complete")
}
