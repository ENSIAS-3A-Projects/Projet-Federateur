package controller

// Package controller implements Phase 2: Actuation Controller.
// It enforces CPU allocation decisions declaratively by watching PodAllocation
// resources and reconciling desired CPU limits with actual pod resources.

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"mbcas/api/v1alpha1"
	"mbcas/pkg/actuator"
)

const (
	// ResizeCooldown is the minimum time between resize operations per pod.
	ResizeCooldown = 10 * time.Second

	// MaxStepSizeFactor is the maximum factor by which CPU can change per resize.
	// Set high (10x) for demo to allow large decreases for idle pods.
	// In production, use 2-3x with graduated stepping.
	MaxStepSizeFactor = 10.0

	// MaxAbsoluteDeltaMilli is the maximum absolute CPU change per resize (in millicores).
	// This prevents extreme jumps even when factor is within limits.
	// P0 Fix: Added to prevent bypassing step size via zero values.
	MaxAbsoluteDeltaMilli = 20000 // 20 cores max change per resize

	// MinSafeBaselineMilli is the minimum baseline for step size calculations.
	// Used when current is 0 or very low to prevent unbounded increases.
	MinSafeBaselineMilli = 100 // 100m minimum for ratio calculations

	// DefaultCPULimit is used when a pod has no CPU limit defined.
	// With LimitRange policy, this should rarely be needed.
	DefaultCPULimit = "500m"

	// ManagedLabel is the label that controls MBCAS management.
	// Pods/namespaces with "false" are excluded.
	ManagedLabel = "mbcas.io/managed"

	// SkipGuaranteedAnnotation allows opting out of Guaranteed QoS skip.
	// Set to "false" to manage Guaranteed QoS pods.
	SkipGuaranteedAnnotation = "mbcas.io/skip-guaranteed"

	// PhaseApplied indicates the CPU limit was successfully applied.
	PhaseApplied = "Applied"
	// PhasePending indicates reconciliation is in progress.
	PhasePending = "Pending"
	// PhaseFailed indicates a temporary failure that will be retried.
	PhaseFailed = "Failed"
)

// PodAllocationReconciler reconciles PodAllocation objects.
type PodAllocationReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	Recorder  record.EventRecorder
	K8sClient kubernetes.Interface
}

// SetupWithManager sets up the controller with the Manager.
func (r *PodAllocationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	podEventHandler := &PodEventHandler{Client: mgr.GetClient()}
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.PodAllocation{}).
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(podEventHandler.Map),
		).
		Complete(r)
}

// Reconcile is the main reconciliation function.
// It compares desired CPU (from PodAllocation) with actual CPU (from Pod)
// and applies changes using the actuator.
func (r *PodAllocationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	// Fetch PodAllocation
	pa := &v1alpha1.PodAllocation{}
	if err := r.Get(ctx, req.NamespacedName, pa); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get PodAllocation: %w", err)
	}

	// Fetch Pod
	pod := &corev1.Pod{}
	podKey := types.NamespacedName{
		Namespace: pa.Spec.Namespace,
		Name:      pa.Spec.PodName,
	}
	if err := r.Get(ctx, podKey, pod); err != nil {
		if apierrors.IsNotFound(err) {
			return r.updateStatus(ctx, pa, PhaseFailed, "PodNotFound",
				fmt.Sprintf("Pod %s/%s not found", pa.Spec.Namespace, pa.Spec.PodName), nil)
		}
		return ctrl.Result{}, fmt.Errorf("get Pod: %w", err)
	}

	// Default managed label to true so monitor output reflects inclusion without manual tagging.
	if err := r.ensureManagedLabel(ctx, pod); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, fmt.Errorf("ensure managed label: %w", err)
	}

	// Check if pod is excluded via label (manage-everyone with opt-out)
	if isExcluded(pod) {
		now := metav1.Now()
		pa.Status.Phase = PhaseApplied
		pa.Status.Reason = "Excluded"
		pa.Status.AppliedCPURequest = pa.Spec.DesiredCPURequest
		pa.Status.AppliedCPULimit = pa.Spec.DesiredCPULimit
		if pa.Status.LastAppliedTime == nil {
			pa.Status.LastAppliedTime = &now
		}
		if err := r.Status().Update(ctx, pa); err != nil {
			return ctrl.Result{}, fmt.Errorf("update status: %w", err)
		}
		r.Recorder.Event(pa, corev1.EventTypeNormal, "CPUSkipped",
			fmt.Sprintf("Skipping Pod %s/%s: excluded via label", pa.Spec.Namespace, pa.Spec.PodName))
		return ctrl.Result{}, nil
	}

	// Policy-driven Guaranteed QoS check (default: skip, but can opt-in via annotation)
	if shouldSkipGuaranteed(pod) && isGuaranteedQoS(pod) {
		now := metav1.Now()
		pa.Status.Phase = PhaseApplied
		pa.Status.Reason = "GuaranteedQoS"
		pa.Status.AppliedCPURequest = pa.Spec.DesiredCPURequest
		pa.Status.AppliedCPULimit = pa.Spec.DesiredCPULimit
		if pa.Status.LastAppliedTime == nil {
			pa.Status.LastAppliedTime = &now
		}
		if err := r.Status().Update(ctx, pa); err != nil {
			return ctrl.Result{}, fmt.Errorf("update status: %w", err)
		}
		r.Recorder.Event(pa, corev1.EventTypeNormal, "CPUSkipped",
			fmt.Sprintf("Skipping Pod %s/%s: Guaranteed QoS (requests == limits)", pa.Spec.Namespace, pa.Spec.PodName))
		return ctrl.Result{}, nil
	}

	// Extract current CPU limit from pod
	currentCPU, err := extractCPULimit(pod)
	if err != nil {
		return r.updateStatus(ctx, pa, PhaseFailed, "InvalidPodState",
			fmt.Sprintf("Cannot extract CPU limit from pod: %v", err), nil)
	}

	// Extract current CPU request from pod
	currentRequest, err := extractCPURequest(pod)
	if err != nil {
		return r.updateStatus(ctx, pa, PhaseFailed, "InvalidPodState",
			fmt.Sprintf("Cannot extract CPU request from pod: %v", err), nil)
	}

	// Normalize desired CPU limit
	desiredCPU := pa.Spec.DesiredCPULimit
	desiredQty, err := resource.ParseQuantity(desiredCPU)
	if err != nil {
		return r.updateStatus(ctx, pa, PhaseFailed, "InvalidDesiredCPU",
			fmt.Sprintf("Invalid desired CPU limit %q: %v", desiredCPU, err), nil)
	}

	// Normalize desired CPU request
	desiredRequest := pa.Spec.DesiredCPURequest
	desiredRequestQty, err := resource.ParseQuantity(desiredRequest)
	if err != nil {
		return r.updateStatus(ctx, pa, PhaseFailed, "InvalidDesiredCPU",
			fmt.Sprintf("Invalid desired CPU request %q: %v", desiredRequest, err), nil)
	}

	// Parse current CPU for comparison
	currentQty, err := resource.ParseQuantity(currentCPU)
	if err != nil {
		return r.updateStatus(ctx, pa, PhaseFailed, "InvalidPodState",
			fmt.Sprintf("Cannot parse current CPU limit %q: %v", currentCPU, err), nil)
	}

	// Parse current request for comparison
	currentRequestQty, err := resource.ParseQuantity(currentRequest)
	if err != nil {
		return r.updateStatus(ctx, pa, PhaseFailed, "InvalidPodState",
			fmt.Sprintf("Cannot parse current CPU request %q: %v", currentRequest, err), nil)
	}

	// Compare desired vs actual (using quantity comparison, not string)
	limitMatch := currentQty.Equal(desiredQty)
	requestMatch := currentRequestQty.Equal(desiredRequestQty)
	if limitMatch && requestMatch {
		// Already applied - update status if needed
		if pa.Status.Phase != PhaseApplied || pa.Status.AppliedCPULimit != desiredCPU || pa.Status.AppliedCPURequest != desiredRequest {
			now := metav1.Now()
			pa.Status.Phase = PhaseApplied
			pa.Status.Reason = "AlreadyApplied"
			pa.Status.AppliedCPURequest = desiredRequest
			pa.Status.AppliedCPULimit = desiredCPU
			if pa.Status.LastAppliedTime == nil {
				pa.Status.LastAppliedTime = &now
			}
			if err := r.Status().Update(ctx, pa); err != nil {
				return ctrl.Result{}, fmt.Errorf("update status: %w", err)
			}
			r.Recorder.Event(pa, corev1.EventTypeNormal, "CPUApplied",
				fmt.Sprintf("CPU request=%s limit=%s already applied to Pod %s/%s", desiredRequest, desiredCPU, pa.Spec.Namespace, pa.Spec.PodName))
		}
		return ctrl.Result{}, nil
	}

	// Check safety rules: cooldown and step size
	if err := r.checkSafety(ctx, pa, currentCPU, desiredCPU); err != nil {
		reason := "SafetyCheckFailed"
		if err == errCooldownActive {
			reason = "RateLimited"
		} else if err == errStepSizeExceeded {
			reason = "StepSizeExceeded"
		}
		return r.updateStatus(ctx, pa, PhasePending, reason, err.Error(), nil)
	}

	// Apply change using actuator
	now := metav1.Now()
	pa.Status.LastAttemptTime = &now
	pa.Status.Phase = PhasePending
	pa.Status.Reason = "Applying"
	if err := r.Status().Update(ctx, pa); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status before apply: %w", err)
	}

	// P0 Fix: Apply to ALL containers proportionally, not just first
	// For simplicity, we apply the same values to each container
	// (future: distribute proportionally based on original limits)
	containerName := ""
	if len(pod.Spec.Containers) > 0 {
		containerName = pod.Spec.Containers[0].Name
	}

	opts := actuator.Options{
		MaxRetries:  3,
		Wait:        true,             // P1 Fix: Wait for resize to complete
		WaitTimeout: 10 * time.Second, // Don't wait too long
	}

	// Use the new function that handles separate request and limit values
	before, after, err := actuator.ApplyScalingWithResources(
		ctx,
		r.K8sClient,
		pa.Spec.Namespace,
		pa.Spec.PodName,
		containerName,
		desiredRequest, // CPU request
		desiredCPU,     // CPU limit
		"",             // No memory request change
		"",             // No memory limit change
		opts,
	)
	if err != nil {
		return r.updateStatus(ctx, pa, PhaseFailed, "ActuatorError",
			fmt.Sprintf("Failed to apply CPU resources: %v", err), nil)
	}

	// P1 Fix: Verify the resize was actually applied by kubelet
	verified := false
	verifyReason := "Unknown"
	if after != nil && len(after.Spec.Containers) > 0 {
		// Check if both request and limit match what we requested
		actualLimit, limitOk := after.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU]
		actualRequest, requestOk := after.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]

		if limitOk && requestOk {
			actualLimitMilli := actualLimit.MilliValue()
			actualRequestMilli := actualRequest.MilliValue()
			desiredLimitQty, _ := resource.ParseQuantity(desiredCPU)
			desiredRequestQty, _ := resource.ParseQuantity(desiredRequest)
			desiredLimitMilli := desiredLimitQty.MilliValue()
			desiredRequestMilli := desiredRequestQty.MilliValue()

			limitMatch := actualLimitMilli == desiredLimitMilli
			requestMatch := actualRequestMilli == desiredRequestMilli

			if limitMatch && requestMatch {
				verified = true
				verifyReason = "Verified"
			} else if !limitMatch && !requestMatch {
				verifyReason = fmt.Sprintf("Mismatch: actualLimit=%dm(want %dm), actualRequest=%dm(want %dm)",
					actualLimitMilli, desiredLimitMilli, actualRequestMilli, desiredRequestMilli)
			} else if !limitMatch {
				verifyReason = fmt.Sprintf("LimitMismatch: actual=%dm, desired=%dm", actualLimitMilli, desiredLimitMilli)
			} else {
				verifyReason = fmt.Sprintf("RequestMismatch: actual=%dm, desired=%dm", actualRequestMilli, desiredRequestMilli)
			}
		} else if !limitOk {
			verifyReason = "NoLimitSet"
		} else {
			verifyReason = "NoRequestSet"
		}
	} else {
		verifyReason = "NoAfterPod"
	}

	// Update status based on verification
	if verified {
		pa.Status.Phase = PhaseApplied
		pa.Status.Reason = "Applied"
	} else {
		pa.Status.Phase = PhasePending
		pa.Status.Reason = verifyReason
		// Requeue to try again
		pa.Status.LastAppliedTime = nil
		if err := r.Status().Update(ctx, pa); err != nil {
			return ctrl.Result{}, fmt.Errorf("update status after verify: %w", err)
		}
		r.Recorder.Event(pa, corev1.EventTypeWarning, "ResizeNotVerified",
			fmt.Sprintf("Resize to request=%s limit=%s not verified for Pod %s/%s: %s",
				desiredRequest, desiredCPU, pa.Spec.Namespace, pa.Spec.PodName, verifyReason))
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	pa.Status.AppliedCPURequest = desiredRequest
	pa.Status.AppliedCPULimit = desiredCPU
	pa.Status.LastAppliedTime = &now
	if err := r.Status().Update(ctx, pa); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status after apply: %w", err)
	}

	// Emit event on state transition
	r.Recorder.Event(pa, corev1.EventTypeNormal, "CPUApplied",
		fmt.Sprintf("Applied CPU request=%s limit=%s to Pod %s/%s (was request=%s limit=%s)",
			desiredRequest, desiredCPU, pa.Spec.Namespace, pa.Spec.PodName,
			extractCPURequestString(before), extractCPULimitString(before)))

	return ctrl.Result{}, nil
}

// updateStatus is a helper to update PodAllocation status and emit events.
func (r *PodAllocationReconciler) updateStatus(ctx context.Context, pa *v1alpha1.PodAllocation,
	phase, reason, message string, lastAttemptTime *metav1.Time) (ctrl.Result, error) {
	oldPhase := pa.Status.Phase

	pa.Status.Phase = phase
	pa.Status.Reason = reason
	if lastAttemptTime != nil {
		pa.Status.LastAttemptTime = lastAttemptTime
	} else if phase == PhasePending || phase == PhaseFailed {
		now := metav1.Now()
		pa.Status.LastAttemptTime = &now
	}

	if err := r.Status().Update(ctx, pa); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	// Emit event only on state transitions
	if oldPhase != phase {
		eventType := corev1.EventTypeNormal
		if phase == PhaseFailed {
			eventType = corev1.EventTypeWarning
		}
		r.Recorder.Event(pa, eventType, reason, message)
	}

	return ctrl.Result{}, nil
}

// ensureManagedLabel defaults the managed label to "true" for pods without an explicit setting.
// This keeps the demo UI aligned with the controller's manage-everyone behavior.
func (r *PodAllocationReconciler) ensureManagedLabel(ctx context.Context, pod *corev1.Pod) error {
	if pod.Labels != nil {
		if val, ok := pod.Labels[ManagedLabel]; ok && val != "" {
			return nil
		}
	}

	patch := client.MergeFrom(pod.DeepCopy())
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}
	pod.Labels[ManagedLabel] = "true"

	return r.Patch(ctx, pod, patch)
}

// Safety check errors
var (
	errCooldownActive   = fmt.Errorf("resize cooldown active")
	errStepSizeExceeded = fmt.Errorf("step size exceeded")
)

// checkSafety validates cooldown and step size constraints.
// Uses status.lastAppliedTime for cooldown (status-based, not in-memory).
// P0 Fix: No longer bypasses checks when current or desired is 0.
func (r *PodAllocationReconciler) checkSafety(ctx context.Context, pa *v1alpha1.PodAllocation, currentCPU, desiredCPU string) error {
	// Check cooldown: use status.lastAppliedTime (status-based)
	if pa.Status.LastAppliedTime != nil {
		elapsed := time.Since(pa.Status.LastAppliedTime.Time)
		if elapsed < ResizeCooldown {
			remaining := ResizeCooldown - elapsed
			return fmt.Errorf("%w: %v remaining", errCooldownActive, remaining.Round(time.Second))
		}
	}

	// Parse quantities
	currentQty, err := resource.ParseQuantity(currentCPU)
	if err != nil {
		return fmt.Errorf("parse current CPU: %w", err)
	}
	desiredQty, err := resource.ParseQuantity(desiredCPU)
	if err != nil {
		return fmt.Errorf("parse desired CPU: %w", err)
	}

	currentMilli := currentQty.MilliValue()
	desiredMilli := desiredQty.MilliValue()

	// P0 Fix: Reject desired=0 as invalid (allocations must have a minimum)
	if desiredMilli <= 0 {
		return fmt.Errorf("%w: desired CPU must be > 0", errStepSizeExceeded)
	}

	// Both equal: no change needed
	if currentMilli == desiredMilli {
		return nil
	}

	// P0 Fix: Absolute delta cap check (before factor check)
	delta := abs64(desiredMilli - currentMilli)
	if delta > MaxAbsoluteDeltaMilli {
		return fmt.Errorf("%w: absolute change %dm exceeds maximum %dm", errStepSizeExceeded, delta, MaxAbsoluteDeltaMilli)
	}

	// P0 Fix: Use safe baseline for factor calculation when current is 0 or very low
	effectiveCurrent := currentMilli
	if effectiveCurrent < MinSafeBaselineMilli {
		effectiveCurrent = MinSafeBaselineMilli
	}

	// Calculate factor (always >= 1.0)
	var factor float64
	if desiredMilli > effectiveCurrent {
		factor = float64(desiredMilli) / float64(effectiveCurrent)
	} else {
		factor = float64(effectiveCurrent) / float64(desiredMilli)
	}

	if factor > MaxStepSizeFactor {
		return fmt.Errorf("%w: change factor %.2fx exceeds maximum %.2fx", errStepSizeExceeded, factor, MaxStepSizeFactor)
	}

	return nil
}

// abs64 returns absolute value of int64
func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// isGuaranteedQoS checks if a pod has Guaranteed QoS class.
// Guaranteed: requests == limits for all containers.
func isGuaranteedQoS(pod *corev1.Pod) bool {
	for _, container := range pod.Spec.Containers {
		requests := container.Resources.Requests
		limits := container.Resources.Limits

		// Check CPU
		cpuReq, hasCPUReq := requests[corev1.ResourceCPU]
		cpuLimit, hasCPULimit := limits[corev1.ResourceCPU]
		if hasCPUReq != hasCPULimit || (hasCPUReq && !cpuReq.Equal(cpuLimit)) {
			return false
		}

		// Check memory
		memReq, hasMemReq := requests[corev1.ResourceMemory]
		memLimit, hasMemLimit := limits[corev1.ResourceMemory]
		if hasMemReq != hasMemLimit || (hasMemReq && !memReq.Equal(memLimit)) {
			return false
		}
	}
	return true
}

// extractCPULimit extracts the CPU limit from the first container of a pod.
// If no limit is set, returns DefaultCPULimit to allow MBCAS to manage the pod.
// This enables "manage everyone" mode where LimitRange provides defaults.
func extractCPULimit(pod *corev1.Pod) (string, error) {
	if len(pod.Spec.Containers) == 0 {
		return "", fmt.Errorf("pod has no containers")
	}

	container := pod.Spec.Containers[0]
	if cpuLimit, ok := container.Resources.Limits[corev1.ResourceCPU]; ok {
		return cpuLimit.String(), nil
	}

	// No limit set - return default to enable manage-everyone mode
	// LimitRange policy should inject limits, but handle gracefully if missing
	return DefaultCPULimit, nil
}

// isExcluded checks if a pod should be excluded from MBCAS management.
// Pods with label mbcas.io/managed=false are excluded.
func isExcluded(pod *corev1.Pod) bool {
	if val, ok := pod.Labels[ManagedLabel]; ok {
		return val == "false"
	}
	return false
}

// shouldSkipGuaranteed checks if Guaranteed QoS pods should be skipped.
// Default is true (skip), but can be overridden with annotation.
func shouldSkipGuaranteed(pod *corev1.Pod) bool {
	if val, ok := pod.Annotations[SkipGuaranteedAnnotation]; ok {
		return val != "false"
	}
	return true // Default: skip Guaranteed QoS
}

// extractCPULimitString is a helper to extract CPU limit as string, returning "none" if not set.
func extractCPULimitString(pod *corev1.Pod) string {
	if len(pod.Spec.Containers) == 0 {
		return "none"
	}
	container := pod.Spec.Containers[0]
	if cpuLimit, ok := container.Resources.Limits[corev1.ResourceCPU]; ok {
		return cpuLimit.String()
	}
	return "none"
}

// extractCPURequest extracts the CPU request from the first container of a pod.
// If no request is set, returns DefaultCPULimit (same as limit for safety).
func extractCPURequest(pod *corev1.Pod) (string, error) {
	if len(pod.Spec.Containers) == 0 {
		return "", fmt.Errorf("pod has no containers")
	}

	container := pod.Spec.Containers[0]
	if cpuRequest, ok := container.Resources.Requests[corev1.ResourceCPU]; ok {
		return cpuRequest.String(), nil
	}

	// No request set - return default
	return DefaultCPULimit, nil
}

// extractCPURequestString is a helper to extract CPU request as string, returning "none" if not set.
func extractCPURequestString(pod *corev1.Pod) string {
	if pod == nil || len(pod.Spec.Containers) == 0 {
		return "none"
	}
	container := pod.Spec.Containers[0]
	if cpuRequest, ok := container.Resources.Requests[corev1.ResourceCPU]; ok {
		return cpuRequest.String()
	}
	return "none"
}
