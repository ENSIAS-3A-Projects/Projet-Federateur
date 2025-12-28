
# MBCAS — Market-Based CPU Allocation System

## Overview

MBCAS is a Kubernetes-native system for **dynamic CPU allocation** in microservices architectures.
It replaces configuration-driven autoscaling mechanisms with a **market-based allocation mechanism** that reallocates CPU resources according to **real execution pressure observed by the Linux kernel**.

Instead of relying on user-defined metrics, annotations, or external monitoring systems, MBCAS treats CPU allocation as a **continuous resource allocation game** among co-located services, using kernel pressure signals as implicit bids and enforcing decisions via in-place pod resizing.

---

## Academic Context (Projet Fédérateur)

This project is developed within the scope of a **Projet Fédérateur (PF)** focused on:

> *The implementation and optimization of collaborative strategies in microservices architectures using game theory and agent-based models to improve resource allocation, coordination, and overall system performance.*

MBCAS directly addresses this objective by modeling each microservice (pod) as an **agent** competing for a finite shared resource (CPU) at the node level. Coordination emerges implicitly through runtime behavior rather than explicit communication or configuration.

The system implements a **Fisher-market-inspired mechanism** that approximates **proportional fairness**, equivalent to maximizing **Nash Social Welfare**, under real system constraints.

---

## Motivation

Kubernetes autoscaling mechanisms (HPA, VPA) have several limitations:

* Depend on **user configuration** and tuning
* Rely on **indirect metrics** (CPU usage, request ratios)
* Require **external metrics pipelines** (e.g., Prometheus)
* React slowly to contention
* Cannot observe **kernel-level execution stalls**

As a result, clusters often suffer from:

* CPU overprovisioning
* Throttling under load
* Oscillations caused by delayed feedback
* High operational complexity

MBCAS explores an alternative approach: **let the operating system reveal true demand**, and use that information to coordinate resource allocation automatically.

---

## Core Idea

MBCAS treats CPU allocation as a **market-clearing problem**:

* CPU is a finite divisible good
* Pods are agents competing for CPU
* Kernel pressure (CPU throttling, PSI) represents implicit demand
* A controller redistributes CPU proportionally to observed demand
* Allocations are enforced using Kubernetes in-place resizing

No user annotations, no application instrumentation, and no external metrics systems are required.

---

## High-Level Architecture

MBCAS consists of three main components:

### 1. Node Agent (DaemonSet)

A lightweight agent runs on every node and:

* Reads kernel-level signals:

  * CPU throttling from cgroups
  * CPU pressure via PSI
* Aggregates signals per pod
* Computes a normalized demand value in the range `[0,1]`
* Allocates node CPU proportionally across pods
* Writes desired allocations to the Kubernetes API

The agent has:

* No network services
* No custom protocols
* No external dependencies

---

### 2. PodAllocation Custom Resource (CRD)

`PodAllocation` is the system’s **authoritative declaration** of CPU decisions.

* One object per pod
* Declares desired CPU limit
* Records application status and timestamps
* Owned exclusively by the system (not users)

CRDs are used only to store **decisions and state**, not raw metrics or time series.

---

### 3. Controller and Actuator

A Kubernetes controller:

* Watches `PodAllocation` resources
* Compares desired vs actual pod resources
* Applies changes using the `pods/resize` subresource
* Enforces safety rules (cooldowns, step limits)
* Updates status and emits events

The actuator logic is isolated and reusable, ensuring clean separation between **decision-making** and **enforcement**.

---

## Market-Based Allocation Model

MBCAS implements a **Fisher-market-inspired allocation rule**:

* Each pod implicitly “bids” through observed pressure
* CPU is allocated proportionally to demand
* Total allocation is constrained by node capacity

Formally, the mechanism approximates:

```
maximize   Σ log(cpu_i)
subject to Σ cpu_i ≤ C
```

This corresponds to **proportional fairness** and **Nash Social Welfare maximization**, providing:

* Pareto efficiency
* Fairness under contention
* No starvation
* Stable convergence

Crucially, bids are **revealed through behavior**, making the mechanism naturally incentive-compatible.

---

## Relationship to Kubernetes Autoscalers

MBCAS does not extend HPA or VPA.

Instead:

* Kubernetes remains responsible for **placement**
* MBCAS takes responsibility for **continuous CPU allocation**

Conceptually, MBCAS behaves like a **kernel-aware vertical autoscaler**, but without:

* user-defined targets
* historical profiling
* external metrics dependencies

---

## Stability and Safety Considerations

The system explicitly addresses known risks:

* **Oscillation control** via smoothing, hysteresis, and rate limits
* **Pod lifecycle shocks** handled through warm-up periods and CPU reserves
* **QoS preservation** by mutating limits only and respecting Guaranteed pods
* **Fairness policies** layered over raw pressure signals
* **Ineffective scaling detection** when CPU does not improve progress

These measures ensure stable behavior under real-world workloads.

---

## Design Principles

* System-owned (no user configuration)
* Kernel-informed (pressure over utilization)
* Kubernetes-native (CRDs and controllers)
* Minimal operational overhead
* Market-based and fairness-aware
* Academically grounded and practically deployable

---

## Project Status

Current focus:

* Core actuator implementation
* Architecture and mechanism definition
* Transition to a fully Kubernetes-native control loop

Planned future work:

* Formal stability analysis
* Comparative evaluation against HPA/VPA
* Scalability experiments
* Sensitivity analysis of allocation policies

---

## Summary

MBCAS explores a shift from **declared intent** to **observed reality** in microservices resource management.
By grounding coordination in kernel-level pressure and enforcing decisions through Kubernetes-native mechanisms, the project demonstrates how **market-based, agent-oriented models** can be applied to real distributed systems to improve efficiency, fairness, and stability.


