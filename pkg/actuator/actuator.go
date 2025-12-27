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
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// ResizePolicy controls which resource fields are updated during scaling.
type ResizePolicy string

const (
	// PolicyBoth sets both requests and limits to the same value (default, backward compatible).
	PolicyBoth ResizePolicy = "both"
	// PolicyLimits sets only limits, leaving requests unchanged.
	PolicyLimits ResizePolicy = "limits"
	// PolicyRequests sets only requests, leaving limits unchanged.
	PolicyRequests ResizePolicy = "requests"
)

// Options configures how scaling is applied.
type Options struct {
	// DryRun, when true, only validates and prints but does not call the API.
	DryRun bool

	// MaxRetries controls how many times we retry on conflict.
	// If zero, a sensible default is used.
	MaxRetries int

	// Policy controls whether to resize requests, limits, or both.
	// Defaults to PolicyBoth for backward compatibility.
	Policy ResizePolicy

	// Wait, when true, blocks until the resize operation completes.
	// WaitTimeout defaults to 30 seconds if not set.
	Wait bool

	// WaitTimeout is the maximum time to wait for resize completion.
	// Only used when Wait is true. If zero, defaults to 30 seconds.
	WaitTimeout time.Duration

	// PollInterval is the time between polls when waiting for resize completion.
	// Only used when Wait is true. If zero, defaults to 500ms.
	PollInterval time.Duration
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

	// Validate and normalize resource quantities before proceeding.
	normalizedCPU, err := parseQuantityOrEmpty(newCPU)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid CPU quantity: %w", err)
	}
	normalizedMem, err := parseQuantityOrEmpty(newMem)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid memory quantity: %w", err)
	}

	// Fetch pod to validate existence and determine default container.
	before, err = client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("fetch pod: %w", err)
	}

	targetContainer, err := resolveContainer(before, containerName)
	if err != nil {
		return before, nil, err
	}

	if opts.DryRun {
		// Caller can inspect "before" and decide; we do not touch the API.
		return before, nil, nil
	}

	maxRetries := opts.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}

	// Default policy to "both" if not specified (backward compatibility).
	policy := opts.Policy
	if policy == "" {
		policy = PolicyBoth
	}
	// Validate policy to prevent no-op patches.
	if err := validatePolicy(policy); err != nil {
		return before, nil, err
	}

	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		patchData, buildErr := buildResizePatch(targetContainer, normalizedCPU, normalizedMem, policy)
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
		if errors.As(lastErr, &statusErr) {
			reason := statusErr.Status().Reason
			if reason == metav1.StatusReasonConflict {
				// Simple retry with small backoff on conflict.
				time.Sleep(200 * time.Millisecond)
				continue
			}
			// Classify and enhance error message for non-conflict errors.
			enhancedErr := classifyAndEnhanceError(lastErr, statusErr, attempt+1, maxRetries)
			return before, nil, enhancedErr
		}

		// Non-status error: no point retrying.
		return before, nil, fmt.Errorf("apply resize patch: %w", lastErr)
	}

	if lastErr != nil {
		// This shouldn't happen, but handle it just in case.
		var statusErr *apierrors.StatusError
		if errors.As(lastErr, &statusErr) {
			return before, nil, classifyAndEnhanceError(lastErr, statusErr, maxRetries, maxRetries)
		}
		return before, nil, fmt.Errorf("apply resize patch: %w", lastErr)
	}

	// If wait is enabled, poll until resize completes or timeout.
	if opts.Wait {
		after, err = waitForResizeCompletion(ctx, client, namespace, podName, opts.WaitTimeout, opts.PollInterval)
		if err != nil {
			return before, nil, fmt.Errorf("wait for resize completion: %w", err)
		}
		return before, after, nil
	}

	// Without wait, just fetch the pod once to show current state.
	after, err = client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return before, nil, fmt.Errorf("verify updated pod: %w", err)
	}

	return before, after, nil
}

// buildResizePatch constructs the JSON patch body for the resize subresource.
func buildResizePatch(containerName, newCPU, newMem string, policy ResizePolicy) ([]byte, error) {
	if newCPU == "" && newMem == "" {
		return nil, fmt.Errorf("no resource changes requested")
	}

	patchObj := map[string]interface{}{
		"spec": map[string]interface{}{
			"containers": []map[string]interface{}{
				{
					"name": containerName,
					"resources": map[string]interface{}{},
				},
			},
		},
	}

	spec := patchObj["spec"].(map[string]interface{})
	containers := spec["containers"].([]map[string]interface{})
	resources := containers[0]["resources"].(map[string]interface{})

	// Only include limits/requests in patch if policy requires them.
	if policy == PolicyBoth || policy == PolicyLimits {
		resources["limits"] = map[string]interface{}{}
	}
	if policy == PolicyBoth || policy == PolicyRequests {
		resources["requests"] = map[string]interface{}{}
	}

	setResource := func(resType, val string) {
		if val == "" {
			return
		}
		if policy == PolicyBoth || policy == PolicyLimits {
			limits := resources["limits"].(map[string]interface{})
			limits[resType] = val
		}
		if policy == PolicyBoth || policy == PolicyRequests {
			requests := resources["requests"].(map[string]interface{})
			requests[resType] = val
		}
	}

	setResource("cpu", newCPU)
	setResource("memory", newMem)

	return json.Marshal(patchObj)
}

// validatePolicy ensures the policy is one of the supported values.
func validatePolicy(policy ResizePolicy) error {
	if policy != PolicyBoth && policy != PolicyLimits && policy != PolicyRequests {
		return fmt.Errorf("invalid resize policy %q: must be one of %q, %q, or %q", policy, PolicyBoth, PolicyLimits, PolicyRequests)
	}
	return nil
}

// classifyAndEnhanceError classifies API errors and provides actionable hints.
func classifyAndEnhanceError(err error, statusErr *apierrors.StatusError, attempt, maxAttempts int) error {
	reason := statusErr.Status().Reason
	code := statusErr.Status().Code

	var hint string
	switch reason {
	case metav1.StatusReasonForbidden:
		hint = "check RBAC: you need 'patch' permission on 'pods/resize' subresource"
	case metav1.StatusReasonNotFound:
		if statusErr.Status().Details != nil && statusErr.Status().Details.Kind == "pods" {
			hint = "pod not found: verify namespace and pod name"
		} else {
			hint = "resource not found: verify the resource exists"
		}
	case metav1.StatusReasonInvalid, metav1.StatusReasonBadRequest:
		// Check if it's about the resize subresource not being available
		message := statusErr.Status().Message
		if code == 404 || (message != "" && (strings.Contains(strings.ToLower(message), "pods/resize") || strings.Contains(strings.ToLower(message), "subresource"))) {
			hint = "enable InPlacePodVerticalScaling feature gate on your cluster"
		} else {
			hint = "verify quantities/policy and runtime support for in-place resize"
		}
	case metav1.StatusReasonConflict:
		if attempt >= maxAttempts {
			hint = fmt.Sprintf("conflict after %d attempts: pod may be updated by another process", attempt)
		} else {
			hint = "retrying due to conflict"
		}
	default:
		hint = "check cluster logs and verify feature gate support"
	}

	if attempt >= maxAttempts && reason == metav1.StatusReasonConflict {
		return fmt.Errorf("apply resize patch (attempt %d/%d): %w\nHint: %s", attempt, maxAttempts, err, hint)
	}

	return fmt.Errorf("apply resize patch: %w\nHint: %s", err, hint)
}

// PlanScaling computes the planned patch and returns the before pod and patch data
// without applying any changes. This is useful for dry-run scenarios.
//
// It performs the same validation as ApplyScaling but returns the patch bytes
// instead of applying them.
func PlanScaling(
	ctx context.Context,
	client kubernetes.Interface,
	namespace, podName, containerName, newCPU, newMem string,
	opts Options,
) (before *corev1.Pod, patch []byte, err error) {
	if podName == "" {
		return nil, nil, fmt.Errorf("pod name is required")
	}
	if newCPU == "" && newMem == "" {
		return nil, nil, fmt.Errorf("no resource changes requested")
	}

	// Validate and normalize resource quantities before proceeding.
	normalizedCPU, err := parseQuantityOrEmpty(newCPU)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid CPU quantity: %w", err)
	}
	normalizedMem, err := parseQuantityOrEmpty(newMem)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid memory quantity: %w", err)
	}

	// Fetch pod to validate existence and determine default container.
	before, err = client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("fetch pod: %w", err)
	}

	targetContainer, err := resolveContainer(before, containerName)
	if err != nil {
		return before, nil, err
	}

	// Default policy to "both" if not specified (backward compatibility).
	policy := opts.Policy
	if policy == "" {
		policy = PolicyBoth
	}
	// Validate policy to prevent no-op patches.
	if err := validatePolicy(policy); err != nil {
		return before, nil, err
	}

	// Build the patch without applying it.
	patch, err = buildResizePatch(targetContainer, normalizedCPU, normalizedMem, policy)
	if err != nil {
		return before, nil, err
	}

	return before, patch, nil
}

// isResizeInProgress checks if the pod has any resize operations in progress.
//
// In Kubernetes 1.27+ with InPlacePodVerticalScaling feature gate enabled,
// pod.Status.Resize is of type []string (see k8s.io/api/core/v1.PodStatus.Resize).
// It contains the names of containers that are currently being resized.
// When a resize completes, the container name is removed from this slice.
// An empty slice or nil means no resize is in progress.
//
// Note: The exact semantics may vary across Kubernetes versions. This implementation
// assumes the slice-based model where non-empty means resizing is in progress.
// For per-container status details, check pod.Status.ContainerStatuses[].Resources.
func isResizeInProgress(pod *corev1.Pod) bool {
	return len(pod.Status.Resize) > 0
}

// waitForResizeCompletion polls the pod until resize completes or timeout is reached.
func waitForResizeCompletion(
	ctx context.Context,
	client kubernetes.Interface,
	namespace, podName string,
	timeout, pollInterval time.Duration,
) (*corev1.Pod, error) {
	// Set defaults if not specified.
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if pollInterval == 0 {
		pollInterval = 500 * time.Millisecond
	}

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		// Check if we've exceeded the deadline.
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("resize did not complete within %s", timeout)
		}

		// Fetch current pod state.
		pod, err := client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("wait: get pod: %w", err)
		}

		// Check if resize is complete.
		if !isResizeInProgress(pod) {
			return pod, nil
		}

		// Wait for next poll interval or context cancellation.
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("wait cancelled: %w", ctx.Err())
		case <-ticker.C:
			// Continue polling.
		}
	}
}

// parseQuantityOrEmpty validates and normalizes a Kubernetes resource quantity string.
//
// If the input is empty, it returns an empty string.
// Otherwise, it parses the quantity using resource.ParseQuantity and returns
// the normalized string representation (e.g., "0.5" becomes "500m").
// Returns an error if the quantity string is invalid.
func parseQuantityOrEmpty(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return "", fmt.Errorf("invalid quantity %q: %w", s, err)
	}
	return q.String(), nil
}

// resolveContainer validates and resolves the target container name.
//
// If containerName is empty, it returns the name of the first container.
// If containerName is provided, it validates that the container exists in the pod.
// Returns an error if the pod has no containers or if the specified container is not found.
func resolveContainer(pod *corev1.Pod, containerName string) (string, error) {
	if len(pod.Spec.Containers) == 0 {
		return "", fmt.Errorf("pod has no containers")
	}
	if containerName == "" {
		return pod.Spec.Containers[0].Name, nil
	}
	for _, c := range pod.Spec.Containers {
		if c.Name == containerName {
			return containerName, nil
		}
	}
	return "", fmt.Errorf("container %q not found in pod", containerName)
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
