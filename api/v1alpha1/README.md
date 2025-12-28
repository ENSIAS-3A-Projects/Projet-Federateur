# PodAllocation API v1alpha1

This package defines the `PodAllocation` Custom Resource Definition (CRD) for the MBCAS project.

## Overview

`PodAllocation` is the single authoritative control object for CPU allocation decisions in the MBCAS system. It represents the desired CPU limit for a pod and tracks the status of applying that limit.

## Resource Structure

### Spec

- `namespace` (required): The namespace of the target pod.
- `podName` (required): The name of the target pod.
- `desiredCPULimit` (required): The desired CPU limit for the pod, expressed as a Kubernetes resource quantity string (e.g., "500m", "1", "2.5").

### Status

- `appliedCPULimit`: The CPU limit that was last successfully applied to the pod.
- `phase`: Current phase of the allocation (Pending, Applied, or Failed).
  
  **Phase Invariants:**
  - `Applied`: `appliedCPULimit == desiredCPULimit` (allocation successfully applied)
  - `Pending`: Reconcile in progress (controller is working to apply desired state)
  - `Failed`: Controller must retry later (temporary failure, will be retried)
  
- `reason`: Human-readable reason for the current phase.
- `lastAppliedTime`: Timestamp when the CPU limit was last successfully applied.
- `lastAttemptTime`: Timestamp of the last attempt to apply the CPU limit.

## Usage

One `PodAllocation` object should be created per pod. The `spec.namespace` and `spec.podName` fields explicitly identify the target pod, avoiding reliance on naming conventions. The system (node agent or controller) will update the `spec.desiredCPULimit` field, and the actuation controller will reconcile the desired state with the actual pod resources.

## Scope

The `PodAllocation` CRD is **Namespaced** (not Cluster-scoped) to:
- Align with Pod lifecycle (pods are namespaced)
- Avoid RBAC complexity
- Prevent cross-tenant leakage

## CRD Installation

The CRD can be installed using:

```bash
kubectl apply -f config/crd/bases/allocation.mbcas.io_podallocations.yaml
```

Or using kustomize:

```bash
kubectl apply -k config/crd/
```

