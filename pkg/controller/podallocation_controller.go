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
	ResizeCooldown = 30 * time.Second

	// MaxStepSizeFactor is the maximum factor by which CPU can change per resize.
	// Symmetric: both increases and decreases are limited to 1.5x.
	MaxStepSizeFactor = 1.5

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
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
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

	// Explicit Guaranteed QoS check: skip these pods
	if isGuaranteedQoS(pod) {
		now := metav1.Now()
		pa.Status.Phase = PhaseApplied
		pa.Status.Reason = "GuaranteedQoS"
		pa.Status.AppliedCPULimit = pa.Spec.DesiredCPULimit
		if pa.Status.LastAppliedTime == nil {
			pa.Status.LastAppliedTime = &now
		}
		if err := r.Status().Update(ctx, pa); err != nil {
			return ctrl.Result{}, fmt.Errorf("update status: %w", err)
		}
		r.Recorder.Event(pa, corev1.EventTypeNormal, "CPULimitSkipped",
			fmt.Sprintf("Skipping Pod %s/%s: Guaranteed QoS (requests == limits)", pa.Spec.Namespace, pa.Spec.PodName))
		return ctrl.Result{}, nil
	}

	// Extract current CPU limit from pod
	currentCPU, err := extractCPULimit(pod)
	if err != nil {
		return r.updateStatus(ctx, pa, PhaseFailed, "InvalidPodState",
			fmt.Sprintf("Cannot extract CPU limit from pod: %v", err), nil)
	}

	// Normalize desired CPU
	desiredCPU := pa.Spec.DesiredCPULimit
	desiredQty, err := resource.ParseQuantity(desiredCPU)
	if err != nil {
		return r.updateStatus(ctx, pa, PhaseFailed, "InvalidDesiredCPU",
			fmt.Sprintf("Invalid desired CPU quantity %q: %v", desiredCPU, err), nil)
	}

	// Parse current CPU for comparison
	currentQty, err := resource.ParseQuantity(currentCPU)
	if err != nil {
		return r.updateStatus(ctx, pa, PhaseFailed, "InvalidPodState",
			fmt.Sprintf("Cannot parse current CPU limit %q: %v", currentCPU, err), nil)
	}

	// Compare desired vs actual (using quantity comparison, not string)
	if currentQty.Equal(desiredQty) {
		// Already applied - update status if needed
		if pa.Status.Phase != PhaseApplied || pa.Status.AppliedCPULimit != desiredCPU {
			now := metav1.Now()
			pa.Status.Phase = PhaseApplied
			pa.Status.Reason = "AlreadyApplied"
			pa.Status.AppliedCPULimit = desiredCPU
			if pa.Status.LastAppliedTime == nil {
				pa.Status.LastAppliedTime = &now
			}
			if err := r.Status().Update(ctx, pa); err != nil {
				return ctrl.Result{}, fmt.Errorf("update status: %w", err)
			}
			r.Recorder.Event(pa, corev1.EventTypeNormal, "CPULimitApplied",
				fmt.Sprintf("CPU limit %s already applied to Pod %s/%s", desiredCPU, pa.Spec.Namespace, pa.Spec.PodName))
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

	// Determine target container (use first container if not specified)
	containerName := ""
	if len(pod.Spec.Containers) > 0 {
		containerName = pod.Spec.Containers[0].Name
	}

	opts := actuator.Options{
		Policy:     actuator.PolicyLimits, // Only mutate limits
		MaxRetries: 3,
		Wait:       false, // Don't wait - we'll reconcile again
	}

	before, _, err := actuator.ApplyScaling(
		ctx,
		r.K8sClient,
		pa.Spec.Namespace,
		pa.Spec.PodName,
		containerName,
		desiredCPU,
		"", // No memory change
		opts,
	)
	if err != nil {
		return r.updateStatus(ctx, pa, PhaseFailed, "ActuatorError",
			fmt.Sprintf("Failed to apply CPU limit: %v", err), nil)
	}

	// Success - update status
	pa.Status.Phase = PhaseApplied
	pa.Status.Reason = "Applied"
	pa.Status.AppliedCPULimit = desiredCPU
	pa.Status.LastAppliedTime = &now
	if err := r.Status().Update(ctx, pa); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status after apply: %w", err)
	}

	// Emit event on state transition
	r.Recorder.Event(pa, corev1.EventTypeNormal, "CPULimitApplied",
		fmt.Sprintf("Applied CPU limit %s to Pod %s/%s (was %s)", desiredCPU, pa.Spec.Namespace, pa.Spec.PodName, extractCPULimitString(before)))

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

// Safety check errors
var (
	errCooldownActive   = fmt.Errorf("resize cooldown active")
	errStepSizeExceeded = fmt.Errorf("step size exceeded")
)

// checkSafety validates cooldown and step size constraints.
// Uses status.lastAppliedTime for cooldown (status-based, not in-memory).
func (r *PodAllocationReconciler) checkSafety(ctx context.Context, pa *v1alpha1.PodAllocation, currentCPU, desiredCPU string) error {
	// Check cooldown: use status.lastAppliedTime (status-based)
	if pa.Status.LastAppliedTime != nil {
		elapsed := time.Since(pa.Status.LastAppliedTime.Time)
		if elapsed < ResizeCooldown {
			remaining := ResizeCooldown - elapsed
			return fmt.Errorf("%w: %v remaining", errCooldownActive, remaining.Round(time.Second))
		}
	}

	// Check step size: symmetric 1.5x factor
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

	// Defensive guard: handle zero values
	if currentMilli == 0 && desiredMilli == 0 {
		// Both zero: no change needed
		return nil
	}
	if currentMilli == 0 {
		// Current is zero, desired is not: allow any increase (no step size limit)
		return nil
	}
	if desiredMilli == 0 {
		// Desired is zero, current is not: this is a removal, allow it
		// (In practice, we probably don't want to set CPU to zero, but we'll allow it here)
		return nil
	}

	// Calculate factor (always >= 1.0)
	var factor float64
	if desiredMilli > currentMilli {
		factor = float64(desiredMilli) / float64(currentMilli)
	} else {
		factor = float64(currentMilli) / float64(desiredMilli)
	}

	if factor > MaxStepSizeFactor {
		return fmt.Errorf("%w: change factor %.2fx exceeds maximum %.2fx", errStepSizeExceeded, factor, MaxStepSizeFactor)
	}

	return nil
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
func extractCPULimit(pod *corev1.Pod) (string, error) {
	if len(pod.Spec.Containers) == 0 {
		return "", fmt.Errorf("pod has no containers")
	}

	container := pod.Spec.Containers[0]
	if cpuLimit, ok := container.Resources.Limits[corev1.ResourceCPU]; ok {
		return cpuLimit.String(), nil
	}

	return "", fmt.Errorf("container %s has no CPU limit", container.Name)
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

