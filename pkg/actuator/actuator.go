package actuator

// Package actuator implements the Phase 1 "Infrastructure" layer:
// a reusable Go library for in-place pod vertical scaling using the pod/resize subresource.
//
// It is intentionally free of CLI concerns so it can be called from both a CLI tool
// (see pkg/podtool) and, later, from a long-running controller.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// Options configures how scaling is applied.
type Options struct {
	// DryRun, when true, only validates and prints but does not call the API.
	DryRun bool

	// MaxRetries controls how many times we retry on conflict.
	// If zero, a sensible default is used.
	MaxRetries int
}

// ApplyScaling is the Phase 1 actuator library entrypoint.
//
// It performs an in-place update of CPU/memory requests & limits via the
// pod/resize subresource. It returns the pod before and after the update
// (the "after" pod is nil when DryRun is true).
//
// This is intentionally free of CLI concerns so it can be embedded in a
// controller later.
func ApplyScaling(
	ctx context.Context,
	client kubernetes.Interface,
	namespace, podName, containerName, newCPU, newMem string,
	opts Options,
) (before *corev1.Pod, after *corev1.Pod, err error) {
	if podName == "" {
		return nil, nil, fmt.Errorf("pod name is required")
	}
	if newCPU == "" && newMem == "" {
		return nil, nil, fmt.Errorf("no resource changes requested")
	}

	// Fetch pod to validate existence and determine default container.
	before, err = client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("fetch pod: %w", err)
	}

	targetContainer := containerName
	if targetContainer == "" {
		if len(before.Spec.Containers) == 0 {
			return before, nil, fmt.Errorf("pod has no containers")
		}
		targetContainer = before.Spec.Containers[0].Name
	}

	if opts.DryRun {
		// Caller can inspect "before" and decide; we do not touch the API.
		return before, nil, nil
	}

	maxRetries := opts.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}

	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		patchData, buildErr := buildResizePatch(targetContainer, newCPU, newMem)
		if buildErr != nil {
			return before, nil, buildErr
		}

		_, lastErr = client.CoreV1().Pods(namespace).Patch(
			ctx,
			podName,
			types.StrategicMergePatchType,
			patchData,
			metav1.PatchOptions{},
			"resize",
		)
		if lastErr == nil {
			break
		}

		var statusErr *apierrors.StatusError
		if errors.As(lastErr, &statusErr) && statusErr.Status().Reason == metav1.StatusReasonConflict {
			// Simple retry with small backoff on conflict.
			time.Sleep(200 * time.Millisecond)
			continue
		}

		// Non-conflict error: no point retrying.
		break
	}

	if lastErr != nil {
		return before, nil, fmt.Errorf("apply resize patch: %w", lastErr)
	}

	after, err = client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return before, nil, fmt.Errorf("verify updated pod: %w", err)
	}

	return before, after, nil
}

// buildResizePatch constructs the JSON patch body for the resize subresource.
func buildResizePatch(containerName, newCPU, newMem string) ([]byte, error) {
	if newCPU == "" && newMem == "" {
		return nil, fmt.Errorf("no resource changes requested")
	}

	patchObj := map[string]interface{}{
		"spec": map[string]interface{}{
			"containers": []map[string]interface{}{
				{
					"name": containerName,
					"resources": map[string]interface{}{
						"limits":   map[string]interface{}{},
						"requests": map[string]interface{}{},
					},
				},
			},
		},
	}

	setResources := func(resType, val string) {
		if val == "" {
			return
		}
		spec := patchObj["spec"].(map[string]interface{})
		containers := spec["containers"].([]map[string]interface{})
		resources := containers[0]["resources"].(map[string]interface{})
		limits := resources["limits"].(map[string]interface{})
		requests := resources["requests"].(map[string]interface{})

		limits[resType] = val
		requests[resType] = val
	}

	setResources("cpu", newCPU)
	setResources("memory", newMem)

	return json.Marshal(patchObj)
}

// CheckResizeSupport verifies that the cluster exposes the pod/resize subresource.
//
// This is a Phase 1 "feature gate" check to fail fast with a clear error if
// InPlacePodVerticalScaling is not enabled.
func CheckResizeSupport(ctx context.Context, client kubernetes.Interface) error {
	resources, err := client.Discovery().ServerResourcesForGroupVersion("v1")
	if err != nil {
		return fmt.Errorf("discover core/v1 resources: %w", err)
	}

	for _, r := range resources.APIResources {
		if r.Name == "pods/resize" {
			return nil
		}
	}

	return fmt.Errorf("cluster does not advertise pods/resize subresource; ensure InPlacePodVerticalScaling feature gate is enabled")
}
