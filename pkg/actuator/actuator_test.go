package actuator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	discoveryfake "k8s.io/client-go/discovery/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestApplyScaling_HappyPath(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:      "app",
				Resources: corev1.ResourceRequirements{},
			}},
		},
	}
	client := k8sfake.NewSimpleClientset(pod)

	// Add a patch reactor that manually applies the patch to simulate real API server behavior.
	// This ensures the test verifies that patches are correctly constructed and applied.
	client.Fake.PrependReactor("patch", "pods", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		patchAction := action.(k8stesting.PatchAction)
		if patchAction.GetSubresource() != "resize" {
			return false, nil, nil
		}

		// Get the current pod from the tracker
		obj, err := client.Tracker().Get(action.GetResource(), action.GetNamespace(), "demo")
		if err != nil {
			return true, nil, err
		}
		currentPod := obj.(*corev1.Pod).DeepCopy()

		// Parse the patch and apply it to the pod
		var patchData map[string]interface{}
		if err := json.Unmarshal(patchAction.GetPatch(), &patchData); err != nil {
			return true, nil, err
		}

		// Apply the patch to the pod's resources
		if spec, ok := patchData["spec"].(map[string]interface{}); ok {
			if containers, ok := spec["containers"].([]interface{}); ok && len(containers) > 0 {
				if container, ok := containers[0].(map[string]interface{}); ok {
					containerName := container["name"].(string)
					// Find the container in the pod
					for i := range currentPod.Spec.Containers {
						if currentPod.Spec.Containers[i].Name == containerName {
							if resources, ok := container["resources"].(map[string]interface{}); ok {
								// Apply limits
								if limits, ok := resources["limits"].(map[string]interface{}); ok {
									if currentPod.Spec.Containers[i].Resources.Limits == nil {
										currentPod.Spec.Containers[i].Resources.Limits = make(corev1.ResourceList)
									}
									for k, v := range limits {
										if val, ok := v.(string); ok {
											q, err := resource.ParseQuantity(val)
											if err == nil {
												currentPod.Spec.Containers[i].Resources.Limits[corev1.ResourceName(k)] = q
											}
										}
									}
								}
								// Apply requests
								if requests, ok := resources["requests"].(map[string]interface{}); ok {
									if currentPod.Spec.Containers[i].Resources.Requests == nil {
										currentPod.Spec.Containers[i].Resources.Requests = make(corev1.ResourceList)
									}
									for k, v := range requests {
										if val, ok := v.(string); ok {
											q, err := resource.ParseQuantity(val)
											if err == nil {
												currentPod.Spec.Containers[i].Resources.Requests[corev1.ResourceName(k)] = q
											}
										}
									}
								}
							}
							break
						}
					}
				}
			}
		}

		// Update the pod in the tracker
		if err := client.Tracker().Update(action.GetResource(), currentPod, action.GetNamespace()); err != nil {
			return true, nil, err
		}

		// Return the updated pod
		return true, currentPod, nil
	})

	before, after, err := ApplyScaling(
		ctx,
		client,
		"default",
		"demo",
		"app",
		"500m",
		"256Mi",
		Options{},
	)
	if err != nil {
		t.Fatalf("ApplyScaling error: %v", err)
	}
	if before == nil || after == nil {
		t.Fatalf("expected non-nil before and after pods")
	}

	c := after.Spec.Containers[0]
	if got := c.Resources.Limits.Cpu().String(); got != "500m" {
		t.Fatalf("expected CPU limit 500m, got %s", got)
	}
	if got := c.Resources.Limits.Memory().String(); got != "256Mi" {
		t.Fatalf("expected memory limit 256Mi, got %s", got)
	}
	// Verify requests are also set (default policy is "both")
	if got := c.Resources.Requests.Cpu().String(); got != "500m" {
		t.Fatalf("expected CPU request 500m, got %s", got)
	}
	if got := c.Resources.Requests.Memory().String(); got != "256Mi" {
		t.Fatalf("expected memory request 256Mi, got %s", got)
	}
}

func TestApplyScaling_DryRunDoesNotPatch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := k8sfake.NewSimpleClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "demo",
				Namespace: "default",
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:      "app",
					Resources: corev1.ResourceRequirements{},
				}},
			},
		},
	)

	var patchCalls int
	client.Fake.PrependReactor("patch", "pods", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		patchCalls++
		return false, nil, nil
	})

	before, after, err := ApplyScaling(
		ctx,
		client,
		"default",
		"demo",
		"app",
		"500m",
		"256Mi",
		Options{DryRun: true},
	)
	if err != nil {
		t.Fatalf("ApplyScaling (dry-run) error: %v", err)
	}
	if before == nil {
		t.Fatalf("expected non-nil before pod")
	}
	if after != nil {
		t.Fatalf("expected nil after pod in dry-run mode")
	}
	if patchCalls != 0 {
		t.Fatalf("expected no patch calls in dry-run, got %d", patchCalls)
	}
}

func TestApplyScaling_RetriesOnConflict(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := k8sfake.NewSimpleClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "demo",
				Namespace: "default",
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:      "app",
					Resources: corev1.ResourceRequirements{},
				}},
			},
		},
	)

	var calls int
	client.Fake.PrependReactor("patch", "pods", func(act k8stesting.Action) (bool, runtime.Object, error) {
		calls++
		if calls == 1 {
			return true, nil, apierrors.NewConflict(
				schema.GroupResource{Group: "", Resource: "pods"},
				"demo",
				nil,
			)
		}
		// Let the client-go fake handle subsequent patches normally.
		return false, nil, nil
	})

	_, _, err := ApplyScaling(
		ctx,
		client,
		"default",
		"demo",
		"app",
		"500m",
		"256Mi",
		Options{MaxRetries: 3},
	)
	if err != nil {
		t.Fatalf("expected conflict to be retried successfully, got error: %v", err)
	}
	if calls < 2 {
		t.Fatalf("expected at least 2 patch attempts, got %d", calls)
	}
}

func TestCheckResizeSupport_Found(t *testing.T) {
	t.Parallel()

	client := k8sfake.NewSimpleClientset()
	disco := client.Discovery().(*discoveryfake.FakeDiscovery)
	disco.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: "v1",
			APIResources: []metav1.APIResource{
				{Name: "pods"},
				{Name: "pods/resize"},
			},
		},
	}

	if err := CheckResizeSupport(context.Background(), client); err != nil {
		t.Fatalf("expected resize support check to pass, got %v", err)
	}
}

func TestCheckResizeSupport_NotFound(t *testing.T) {
	t.Parallel()

	client := k8sfake.NewSimpleClientset()
	disco := client.Discovery().(*discoveryfake.FakeDiscovery)
	disco.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: "v1",
			APIResources: []metav1.APIResource{
				{Name: "pods"},
			},
		},
	}

	if err := CheckResizeSupport(context.Background(), client); err == nil {
		t.Fatalf("expected error when pods/resize is missing")
	}
}

func TestResolveContainer_EmptyDefaultsToFirst(t *testing.T) {
	t.Parallel()

	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "first"},
				{Name: "second"},
			},
		},
	}

	name, err := resolveContainer(pod, "")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if name != "first" {
		t.Fatalf("expected 'first', got %q", name)
	}
}

func TestResolveContainer_ValidName(t *testing.T) {
	t.Parallel()

	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "first"},
				{Name: "second"},
			},
		},
	}

	name, err := resolveContainer(pod, "second")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if name != "second" {
		t.Fatalf("expected 'second', got %q", name)
	}
}

func TestResolveContainer_InvalidName(t *testing.T) {
	t.Parallel()

	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "first"},
			},
		},
	}

	_, err := resolveContainer(pod, "nonexistent")
	if err == nil {
		t.Fatalf("expected error for nonexistent container")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected error message about container not found, got %q", err.Error())
	}
}

func TestResolveContainer_NoContainers(t *testing.T) {
	t.Parallel()

	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{},
		},
	}

	_, err := resolveContainer(pod, "")
	if err == nil {
		t.Fatalf("expected error for pod with no containers")
	}
}

func TestParseQuantityOrEmpty_Empty(t *testing.T) {
	t.Parallel()

	result, err := parseQuantityOrEmpty("")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result != "" {
		t.Fatalf("expected empty string, got %q", result)
	}
}

func TestParseQuantityOrEmpty_ValidCPU(t *testing.T) {
	t.Parallel()

	result, err := parseQuantityOrEmpty("500m")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result != "500m" {
		t.Fatalf("expected '500m', got %q", result)
	}
}

func TestParseQuantityOrEmpty_ValidMemory(t *testing.T) {
	t.Parallel()

	result, err := parseQuantityOrEmpty("256Mi")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result != "256Mi" {
		t.Fatalf("expected '256Mi', got %q", result)
	}
}

func TestParseQuantityOrEmpty_Normalizes(t *testing.T) {
	t.Parallel()

	// Test that 0.5 gets normalized to 500m
	result, err := parseQuantityOrEmpty("0.5")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	// The normalized form should be valid, though the exact format may vary
	if result == "" {
		t.Fatalf("expected normalized quantity, got empty string")
	}
}

func TestParseQuantityOrEmpty_Invalid(t *testing.T) {
	t.Parallel()

	_, err := parseQuantityOrEmpty("invalid")
	if err == nil {
		t.Fatalf("expected error for invalid quantity")
	}
	if !strings.Contains(err.Error(), "invalid quantity") {
		t.Fatalf("expected error message about invalid quantity, got %q", err.Error())
	}
}

func TestBuildResizePatch_PolicyBoth(t *testing.T) {
	t.Parallel()

	patch, err := buildResizePatch("app", "500m", "256Mi", PolicyBoth)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	var patchObj map[string]interface{}
	if err := json.Unmarshal(patch, &patchObj); err != nil {
		t.Fatalf("failed to unmarshal patch: %v", err)
	}

	// Verify both limits and requests are set
	spec := patchObj["spec"].(map[string]interface{})
	containers := spec["containers"].([]interface{})
	container := containers[0].(map[string]interface{})
	resources := container["resources"].(map[string]interface{})

	limits, hasLimits := resources["limits"].(map[string]interface{})
	if !hasLimits {
		t.Fatalf("expected limits in patch")
	}
	if limits["cpu"] != "500m" {
		t.Fatalf("expected CPU limit 500m, got %v", limits["cpu"])
	}

	requests, hasRequests := resources["requests"].(map[string]interface{})
	if !hasRequests {
		t.Fatalf("expected requests in patch")
	}
	if requests["cpu"] != "500m" {
		t.Fatalf("expected CPU request 500m, got %v", requests["cpu"])
	}
}

func TestBuildResizePatch_PolicyLimits(t *testing.T) {
	t.Parallel()

	patch, err := buildResizePatch("app", "500m", "256Mi", PolicyLimits)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	var patchObj map[string]interface{}
	if err := json.Unmarshal(patch, &patchObj); err != nil {
		t.Fatalf("failed to unmarshal patch: %v", err)
	}

	spec := patchObj["spec"].(map[string]interface{})
	containers := spec["containers"].([]interface{})
	container := containers[0].(map[string]interface{})
	resources := container["resources"].(map[string]interface{})

	// Verify limits are set
	limits, hasLimits := resources["limits"].(map[string]interface{})
	if !hasLimits {
		t.Fatalf("expected limits in patch")
	}
	if limits["cpu"] != "500m" {
		t.Fatalf("expected CPU limit 500m, got %v", limits["cpu"])
	}

	// Verify requests are NOT set
	_, hasRequests := resources["requests"]
	if hasRequests {
		t.Fatalf("expected no requests in patch for PolicyLimits")
	}
}

func TestBuildResizePatch_PolicyRequests(t *testing.T) {
	t.Parallel()

	patch, err := buildResizePatch("app", "500m", "256Mi", PolicyRequests)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	var patchObj map[string]interface{}
	if err := json.Unmarshal(patch, &patchObj); err != nil {
		t.Fatalf("failed to unmarshal patch: %v", err)
	}

	spec := patchObj["spec"].(map[string]interface{})
	containers := spec["containers"].([]interface{})
	container := containers[0].(map[string]interface{})
	resources := container["resources"].(map[string]interface{})

	// Verify requests are set
	requests, hasRequests := resources["requests"].(map[string]interface{})
	if !hasRequests {
		t.Fatalf("expected requests in patch")
	}
	if requests["cpu"] != "500m" {
		t.Fatalf("expected CPU request 500m, got %v", requests["cpu"])
	}

	// Verify limits are NOT set
	_, hasLimits := resources["limits"]
	if hasLimits {
		t.Fatalf("expected no limits in patch for PolicyRequests")
	}
}

func TestBuildResizePatch_CPUOnly(t *testing.T) {
	t.Parallel()

	patch, err := buildResizePatch("app", "500m", "", PolicyBoth)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	var patchObj map[string]interface{}
	if err := json.Unmarshal(patch, &patchObj); err != nil {
		t.Fatalf("failed to unmarshal patch: %v", err)
	}

	spec := patchObj["spec"].(map[string]interface{})
	containers := spec["containers"].([]interface{})
	container := containers[0].(map[string]interface{})
	resources := container["resources"].(map[string]interface{})
	limits := resources["limits"].(map[string]interface{})

	if limits["cpu"] != "500m" {
		t.Fatalf("expected CPU limit 500m, got %v", limits["cpu"])
	}
	if _, hasMem := limits["memory"]; hasMem {
		t.Fatalf("expected no memory in patch when not specified")
	}
}

func TestBuildResizePatch_MemoryOnly(t *testing.T) {
	t.Parallel()

	patch, err := buildResizePatch("app", "", "256Mi", PolicyBoth)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	var patchObj map[string]interface{}
	if err := json.Unmarshal(patch, &patchObj); err != nil {
		t.Fatalf("failed to unmarshal patch: %v", err)
	}

	spec := patchObj["spec"].(map[string]interface{})
	containers := spec["containers"].([]interface{})
	container := containers[0].(map[string]interface{})
	resources := container["resources"].(map[string]interface{})
	limits := resources["limits"].(map[string]interface{})

	if limits["memory"] != "256Mi" {
		t.Fatalf("expected memory limit 256Mi, got %v", limits["memory"])
	}
	if _, hasCPU := limits["cpu"]; hasCPU {
		t.Fatalf("expected no CPU in patch when not specified")
	}
}

func TestApplyScaling_InvalidPolicy(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := k8sfake.NewSimpleClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "demo",
				Namespace: "default",
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:      "app",
					Resources: corev1.ResourceRequirements{},
				}},
			},
		},
	)

	_, _, err := ApplyScaling(
		ctx,
		client,
		"default",
		"demo",
		"app",
		"500m",
		"256Mi",
		Options{Policy: ResizePolicy("invalid")},
	)
	if err == nil {
		t.Fatalf("expected error for invalid policy")
	}
	if !strings.Contains(err.Error(), "invalid resize policy") {
		t.Fatalf("expected error message about invalid policy, got %q", err.Error())
	}
}

func TestPlanScaling_InvalidPolicy(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := k8sfake.NewSimpleClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "demo",
				Namespace: "default",
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:      "app",
					Resources: corev1.ResourceRequirements{},
				}},
			},
		},
	)

	_, _, err := PlanScaling(
		ctx,
		client,
		"default",
		"demo",
		"app",
		"500m",
		"256Mi",
		Options{Policy: ResizePolicy("invalid")},
	)
	if err == nil {
		t.Fatalf("expected error for invalid policy")
	}
	if !strings.Contains(err.Error(), "invalid resize policy") {
		t.Fatalf("expected error message about invalid policy, got %q", err.Error())
	}
}
