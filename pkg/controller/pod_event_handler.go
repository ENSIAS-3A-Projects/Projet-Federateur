package controller

// Package controller includes a Pod event handler that requeues related
// PodAllocation objects when Pods change. This ensures reconciliation happens
// when pods restart, QoS changes, or limits drift.

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"mbcas/api/v1alpha1"
)

// PodEventHandler handles Pod events and requeues related PodAllocation objects.
// This maintains unidirectional authority: PodAllocation → Pod.
// We do NOT derive allocation from Pod changes; we only trigger reconciliation.
type PodEventHandler struct {
	Client client.Client
}

// Map maps a Pod to related PodAllocation objects for requeue.
func (h *PodEventHandler) Map(ctx context.Context, obj client.Object) []reconcile.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}

	// Find PodAllocation objects that reference this pod
	paList := &v1alpha1.PodAllocationList{}
	if err := h.Client.List(ctx, paList, client.InNamespace(pod.Namespace)); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, pa := range paList.Items {
		if pa.Spec.Namespace == pod.Namespace && pa.Spec.PodName == pod.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(&pa),
			})
		}
	}

	return requests
}

