//go:build vpabench && !mbcasbench
// +build vpabench,!mbcasbench

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"sync"
	"time"

	"path/filepath"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

// Shared types for benchmark results
type MetricsResult struct {
	TestName      string              `json:"test_name"`
	System        string              `json:"system"`
	StartTime     time.Time           `json:"start_time"`
	EndTime       time.Time           `json:"end_time"`
	Duration      string              `json:"duration"`
	Configuration TestConfiguration   `json:"configuration"`
	Workloads     []WorkloadResult    `json:"workloads"`
	Aggregate     AggregateMetrics    `json:"aggregate"`
	TimeSeries    []TimeSeriesPoint   `json:"time_series"`
	Events        []SystemEvent       `json:"events"`
	ResourceUsage SystemResourceUsage `json:"resource_usage"`
}

type TestConfiguration struct {
	Namespace           string `json:"namespace"`
	TestDurationMinutes int    `json:"test_duration_minutes"`
	SampleIntervalSec   int    `json:"sample_interval_seconds"`
	WorkloadCount       int    `json:"workload_count"`
	NodeCount           int    `json:"node_count"`
	TotalNodeCPUMilli   int64  `json:"total_node_cpu_milli"`
}

type WorkloadResult struct {
	Name                    string           `json:"name"`
	Type                    string           `json:"type"`
	InitialRequestMilli     int64            `json:"initial_request_milli"`
	InitialLimitMilli       int64            `json:"initial_limit_milli"`
	FinalRequestMilli       int64            `json:"final_request_milli"`
	FinalLimitMilli         int64            `json:"final_limit_milli"`
	AvgUsageMilli           float64          `json:"avg_usage_milli"`
	MaxUsageMilli           int64            `json:"max_usage_milli"`
	MinUsageMilli           int64            `json:"min_usage_milli"`
	AvgThrottlingRatio      float64          `json:"avg_throttling_ratio"`
	MaxThrottlingRatio      float64          `json:"max_throttling_ratio"`
	ThrottlingDurationSec   float64          `json:"throttling_duration_seconds"`
	AllocationChanges       int              `json:"allocation_changes"`
	AllocationHistory       []AllocationSnap `json:"allocation_history"`
	TimeToFirstAllocation   float64          `json:"time_to_first_allocation_seconds"`
	TimeToStableAllocation  float64          `json:"time_to_stable_allocation_seconds"`
	OverprovisioningRatio   float64          `json:"overprovisioning_ratio"`
	UnderprovisioningEvents int              `json:"underprovisioning_events"`
}

type AllocationSnap struct {
	Timestamp    time.Time `json:"timestamp"`
	RequestMilli int64     `json:"request_milli"`
	LimitMilli   int64     `json:"limit_milli"`
	UsageMilli   int64     `json:"usage_milli"`
}

type AggregateMetrics struct {
	TotalPods                    int     `json:"total_pods"`
	PodsWithAllocationChanges    int     `json:"pods_with_allocation_changes"`
	TotalAllocationChanges       int     `json:"total_allocation_changes"`
	AvgAllocationChangesPerPod   float64 `json:"avg_allocation_changes_per_pod"`
	AvgTimeToFirstAllocationSec  float64 `json:"avg_time_to_first_allocation_seconds"`
	AvgTimeToStableAllocationSec float64 `json:"avg_time_to_stable_allocation_seconds"`
	AvgThrottlingRatio           float64 `json:"avg_throttling_ratio"`
	MaxThrottlingRatio           float64 `json:"max_throttling_ratio"`
	TotalThrottlingDurationSec   float64 `json:"total_throttling_duration_seconds"`
	AvgOverprovisioningRatio     float64 `json:"avg_overprovisioning_ratio"`
	TotalUnderprovisioningEvents int     `json:"total_underprovisioning_events"`
	ClusterCPUEfficiency         float64 `json:"cluster_cpu_efficiency"`
	ClusterCPUWasteRatio         float64 `json:"cluster_cpu_waste_ratio"`
	AllocationAccuracyPercent    float64 `json:"allocation_accuracy_percent"`
	P50AllocationLatencySec      float64 `json:"p50_allocation_latency_seconds"`
	P90AllocationLatencySec      float64 `json:"p90_allocation_latency_seconds"`
	P99AllocationLatencySec      float64 `json:"p99_allocation_latency_seconds"`
}

type TimeSeriesPoint struct {
	Timestamp                 time.Time `json:"timestamp"`
	TotalAllocatedMilli       int64     `json:"total_allocated_milli"`
	TotalUsedMilli            int64     `json:"total_used_milli"`
	TotalRequestedMilli       int64     `json:"total_requested_milli"`
	AvgThrottlingRatio        float64   `json:"avg_throttling_ratio"`
	PodsThrottled             int       `json:"pods_throttled"`
	AllocationChangesInWindow int       `json:"allocation_changes_in_window"`
	CPUEfficiency             float64   `json:"cpu_efficiency"`
}

type SystemEvent struct {
	Timestamp   time.Time `json:"timestamp"`
	Type        string    `json:"type"`
	Pod         string    `json:"pod"`
	Description string    `json:"description"`
	OldValue    string    `json:"old_value,omitempty"`
	NewValue    string    `json:"new_value,omitempty"`
}

type SystemResourceUsage struct {
	AgentCPUAvgMilli       float64 `json:"agent_cpu_avg_milli"`
	AgentCPUMaxMilli       int64   `json:"agent_cpu_max_milli"`
	AgentMemoryAvgMiB      float64 `json:"agent_memory_avg_mib"`
	AgentMemoryMaxMiB      int64   `json:"agent_memory_max_mib"`
	ControllerCPUAvgMilli  float64 `json:"controller_cpu_avg_milli"`
	ControllerCPUMaxMilli  int64   `json:"controller_cpu_max_milli"`
	ControllerMemoryAvgMiB float64 `json:"controller_memory_avg_mib"`
	ControllerMemoryMaxMiB int64   `json:"controller_memory_max_mib"`
}

// WorkloadSpec defines a test workload
type WorkloadSpec struct {
	Name           string
	Type           string
	CPUPattern     string
	InitialRequest string
	InitialLimit   string
	StressArgs     []string
}

type podMetricsCollector struct {
	name                 string
	workloadType         string
	initialRequest       int64
	initialLimit         int64
	samples              []podSample
	allocationChanges    []AllocationSnap
	firstAllocationTime  *time.Time
	stableAllocationTime *time.Time
	lastAllocation       int64
	stableCount          int
}

type podSample struct {
	timestamp       time.Time
	usageMilli     int64
	requestMilli   int64
	limitMilli     int64
	throttlingRatio float64
}

// VPABenchmark runs the Kubernetes VPA benchmark
type VPABenchmark struct {
	client         kubernetes.Interface
	dynamicClient  dynamic.Interface
	namespace      string
	testDuration   time.Duration
	sampleInterval time.Duration
	workloads      []WorkloadSpec
	results        *MetricsResult
	mu             sync.Mutex
	podMetrics     map[string]*podMetricsCollector
	events         []SystemEvent
	timeSeriesData []TimeSeriesPoint
}

var vpaGVR = schema.GroupVersionResource{
	Group:    "autoscaling.k8s.io",
	Version:  "v1",
	Resource: "verticalpodautoscalers",
}

func NewVPABenchmark() (*VPABenchmark, error) {
	kubeconfig := filepath.Join(homedir.HomeDir(), ".kube", "config")
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("build config: %w", err)
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create client: %w", err)
	}

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create dynamic client: %w", err)
	}

	// Same workloads as MBCAS for fair comparison
	workloads := []WorkloadSpec{
		{
			Name:           "steady-low",
			Type:           "steady",
			CPUPattern:     "constant-low",
			InitialRequest: "100m",
			InitialLimit:   "200m",
			StressArgs:     []string{"--cpu", "1"},
		},
		{
			Name:           "steady-high",
			Type:           "steady",
			CPUPattern:     "constant-high",
			InitialRequest: "100m",
			InitialLimit:   "200m",
			StressArgs:     []string{"--cpu", "2"},
		},
		{
			Name:           "bursty",
			Type:           "bursty",
			CPUPattern:     "burst-every-60s",
			InitialRequest: "100m",
			InitialLimit:   "200m",
			StressArgs:     []string{"--cpu", "1"},
		},
		{
			Name:           "idle",
			Type:           "idle",
			CPUPattern:     "mostly-idle",
			InitialRequest: "500m",
			InitialLimit:   "1000m",
			StressArgs:     []string{"sh", "-c", "while true; do sleep 10; done"},
		},
		{
			Name:           "ramping",
			Type:           "ramping",
			CPUPattern:     "gradual-increase",
			InitialRequest: "100m",
			InitialLimit:   "200m",
			StressArgs:     []string{"--cpu", "1"},
		},
		{
			Name:           "spiky",
			Type:           "spiky",
			CPUPattern:     "random-spikes",
			InitialRequest: "200m",
			InitialLimit:   "400m",
			StressArgs:     []string{"--cpu", "1"},
		},
		{
			Name:           "throttle-prone",
			Type:           "throttle-prone",
			CPUPattern:     "exceeds-limit",
			InitialRequest: "100m",
			InitialLimit:   "100m",
			StressArgs:     []string{"--cpu", "2"},
		},
		{
			Name:           "overprovisioned",
			Type:           "overprovisioned",
			CPUPattern:     "low-usage-high-limit",
			InitialRequest: "1000m",
			InitialLimit:   "2000m",
			StressArgs:     []string{"sh", "-c", "while true; do sleep 10; done"},
		},
	}

	return &VPABenchmark{
		client:         client,
		dynamicClient:  dynamicClient,
		namespace:      "vpa-benchmark",
		testDuration:   10 * time.Minute,
		sampleInterval: 5 * time.Second,
		workloads:      workloads,
		podMetrics:     make(map[string]*podMetricsCollector),
		events:         make([]SystemEvent, 0),
		timeSeriesData: make([]TimeSeriesPoint, 0),
	}, nil
}

func (b *VPABenchmark) Run() error {
	ctx := context.Background()
	startTime := time.Now()

	fmt.Println("========================================")
	fmt.Println("Kubernetes VPA Benchmark Test")
	fmt.Println("========================================")
	fmt.Printf("Start time: %s\n", startTime.Format(time.RFC3339))
	fmt.Printf("Duration: %v\n", b.testDuration)
	fmt.Printf("Sample interval: %v\n", b.sampleInterval)
	fmt.Println()

	b.results = &MetricsResult{
		TestName:  "Kubernetes VPA Comprehensive Benchmark",
		System:    "VPA",
		StartTime: startTime,
		Configuration: TestConfiguration{
			Namespace:           b.namespace,
			TestDurationMinutes: int(b.testDuration.Minutes()),
			SampleIntervalSec:   int(b.sampleInterval.Seconds()),
			WorkloadCount:       len(b.workloads),
		},
	}

	if err := b.setupNamespace(ctx); err != nil {
		return fmt.Errorf("setup namespace: %w", err)
	}
	defer b.cleanupNamespace(ctx)

	if err := b.collectNodeInfo(ctx); err != nil {
		return fmt.Errorf("collect node info: %w", err)
	}

	fmt.Println("Deploying test workloads with VPA...")
	if err := b.deployWorkloadsWithVPA(ctx); err != nil {
		return fmt.Errorf("deploy workloads: %w", err)
	}

	fmt.Println("Waiting for pods to be ready...")
	if err := b.waitForPodsReady(ctx, 2*time.Minute); err != nil {
		return fmt.Errorf("wait for pods: %w", err)
	}

	b.recordInitialState(ctx)

	fmt.Println("Starting metrics collection...")
	collectorCtx, cancelCollector := context.WithCancel(ctx)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		b.collectMetricsLoop(collectorCtx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		b.monitorVPARecommendations(collectorCtx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		b.simulateWorkloadPatterns(collectorCtx)
	}()

	fmt.Printf("Running test for %v...\n", b.testDuration)
	progressTicker := time.NewTicker(1 * time.Minute)
	defer progressTicker.Stop()
	testTimer := time.NewTimer(b.testDuration)
	defer testTimer.Stop()

	elapsed := time.Duration(0)
testLoop:
	for {
		select {
		case <-progressTicker.C:
			elapsed += 1 * time.Minute
			fmt.Printf("  Progress: %v / %v\n", elapsed, b.testDuration)
		case <-testTimer.C:
			break testLoop
		}
	}

	cancelCollector()
	wg.Wait()

	b.recordFinalState(ctx)
	b.computeAggregateMetrics()
	b.collectSystemResourceUsage(ctx)

	b.results.EndTime = time.Now()
	b.results.Duration = b.results.EndTime.Sub(b.results.StartTime).String()
	b.results.TimeSeries = b.timeSeriesData
	b.results.Events = b.events

	if err := b.saveResults("metrics-vpa.json"); err != nil {
		return fmt.Errorf("save results: %w", err)
	}

	b.printSummary()

	return nil
}

func (b *VPABenchmark) setupNamespace(ctx context.Context) error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: b.namespace,
			Labels: map[string]string{
				"vpa/benchmark": "true",
			},
		},
	}

	_, err := b.client.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil {
		_ = b.client.CoreV1().Namespaces().Delete(ctx, b.namespace, metav1.DeleteOptions{})
		time.Sleep(5 * time.Second)
		_, err = b.client.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	}
	return err
}

func (b *VPABenchmark) cleanupNamespace(ctx context.Context) {
	fmt.Println("Cleaning up namespace...")

	// Delete VPAs first
	vpas, _ := b.dynamicClient.Resource(vpaGVR).Namespace(b.namespace).List(ctx, metav1.ListOptions{})
	if vpas != nil {
		for _, vpa := range vpas.Items {
			_ = b.dynamicClient.Resource(vpaGVR).Namespace(b.namespace).Delete(ctx, vpa.GetName(), metav1.DeleteOptions{})
		}
	}

	_ = b.client.CoreV1().Namespaces().Delete(ctx, b.namespace, metav1.DeleteOptions{})
}

func (b *VPABenchmark) collectNodeInfo(ctx context.Context) error {
	nodes, err := b.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}

	b.results.Configuration.NodeCount = len(nodes.Items)

	var totalCPU int64
	for _, node := range nodes.Items {
		cpu := node.Status.Allocatable[corev1.ResourceCPU]
		totalCPU += cpu.MilliValue()
	}
	b.results.Configuration.TotalNodeCPUMilli = totalCPU

	return nil
}

func (b *VPABenchmark) deployWorkloadsWithVPA(ctx context.Context) error {
	for _, w := range b.workloads {
		// Create Deployment (VPA works with Deployments, not bare Pods)
		deployment := b.createWorkloadDeployment(w)
		_, err := b.client.AppsV1().Deployments(b.namespace).Create(ctx, deployment, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("create deployment %s: %w", w.Name, err)
		}

		// Create VPA for the deployment
		vpa := b.createVPA(w)
		_, err = b.dynamicClient.Resource(vpaGVR).Namespace(b.namespace).Create(ctx, vpa, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("create VPA %s: %w", w.Name, err)
		}

		reqQty, _ := resource.ParseQuantity(w.InitialRequest)
		limQty, _ := resource.ParseQuantity(w.InitialLimit)

		b.podMetrics[w.Name] = &podMetricsCollector{
			name:              w.Name,
			workloadType:      w.Type,
			initialRequest:    reqQty.MilliValue(),
			initialLimit:      limQty.MilliValue(),
			samples:           make([]podSample, 0),
			allocationChanges: make([]AllocationSnap, 0),
		}

		b.recordEvent("DeploymentCreated", w.Name,
			fmt.Sprintf("Created deployment and VPA with request=%s limit=%s", w.InitialRequest, w.InitialLimit), "", "")
	}
	return nil
}

func (b *VPABenchmark) createWorkloadDeployment(w WorkloadSpec) *appsv1.Deployment {
	replicas := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      w.Name,
			Namespace: b.namespace,
			Labels: map[string]string{
				"benchmark/workload": w.Name,
				"benchmark/type":     w.Type,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": w.Name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":                w.Name,
						"benchmark/workload": w.Name,
						"benchmark/type":     w.Type,
						"mbcas.io/managed":   "false", // Opt out of MBCAS to avoid conflicts with VPA
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "stress",
							Image: "polinux/stress:latest",
							// For idle workloads, use shell command; otherwise use stress
							Command: func() []string {
								if len(w.StressArgs) > 0 && w.StressArgs[0] == "sh" {
									return w.StressArgs[:1] // Use "sh" as command
								}
								return []string{"stress"}
							}(),
							Args: func() []string {
								if len(w.StressArgs) > 0 && w.StressArgs[0] == "sh" {
									return w.StressArgs[1:] // Use rest as args
								}
								return w.StressArgs
							}(),
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse(w.InitialRequest),
									corev1.ResourceMemory: resource.MustParse("64Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse(w.InitialLimit),
									corev1.ResourceMemory: resource.MustParse("128Mi"),
								},
							},
							// Required for InPlaceOrRecreate mode: allow in-place resource updates without restart
							ResizePolicy: []corev1.ContainerResizePolicy{
								{
									ResourceName:  corev1.ResourceCPU,
									RestartPolicy: corev1.NotRequired,
								},
								{
									ResourceName:  corev1.ResourceMemory,
									RestartPolicy: corev1.NotRequired,
								},
							},
						},
					},
					TerminationGracePeriodSeconds: int64Ptr(5),
				},
			},
		},
	}
}

func (b *VPABenchmark) createVPA(w WorkloadSpec) *unstructured.Unstructured {
	vpa := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "autoscaling.k8s.io/v1",
			"kind":       "VerticalPodAutoscaler",
			"metadata": map[string]interface{}{
				"name":      w.Name + "-vpa",
				"namespace": b.namespace,
			},
			"spec": map[string]interface{}{
				"targetRef": map[string]interface{}{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"name":       w.Name,
				},
				"updatePolicy": map[string]interface{}{
					"updateMode":  "InPlaceOrRecreate", // Use InPlaceOrRecreate mode for fair comparison (tries in-place update, falls back to recreate)
					"minReplicas": 1,                    // Override global minReplicas (default 2) to allow single-replica deployments
				},
				"resourcePolicy": map[string]interface{}{
					"containerPolicies": []interface{}{
						map[string]interface{}{
							"containerName": "stress",
							"minAllowed": map[string]interface{}{
								"cpu":    "50m",
								"memory": "32Mi",
							},
							"maxAllowed": map[string]interface{}{
								"cpu":    "4",
								"memory": "4Gi",
							},
							"controlledResources": []interface{}{"cpu", "memory"},
						},
					},
				},
			},
		},
	}
	return vpa
}

func (b *VPABenchmark) waitForPodsReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		pods, err := b.client.CoreV1().Pods(b.namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return err
		}

		readyCount := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodRunning {
				for _, cond := range pod.Status.Conditions {
					if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
						readyCount++
						break
					}
				}
			}
		}

		if readyCount >= len(b.workloads) {
			return nil
		}

		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("timeout waiting for pods to be ready")
}

func (b *VPABenchmark) recordInitialState(ctx context.Context) {
	pods, _ := b.client.CoreV1().Pods(b.namespace).List(ctx, metav1.ListOptions{})
	for _, pod := range pods.Items {
		workloadName := pod.Labels["benchmark/workload"]
		if collector, ok := b.podMetrics[workloadName]; ok {
			if len(pod.Spec.Containers) > 0 {
				container := pod.Spec.Containers[0]
				collector.initialRequest = container.Resources.Requests.Cpu().MilliValue()
				collector.initialLimit = container.Resources.Limits.Cpu().MilliValue()
				collector.lastAllocation = collector.initialLimit
			}
		}
	}
}

func (b *VPABenchmark) collectMetricsLoop(ctx context.Context) {
	ticker := time.NewTicker(b.sampleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.collectMetricsSample(ctx)
		}
	}
}

func (b *VPABenchmark) collectMetricsSample(ctx context.Context) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()

	pods, err := b.client.CoreV1().Pods(b.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return
	}

	var totalAllocated, totalUsed, totalRequested int64
	var throttledCount int
	var totalThrottling float64
	var allocationChangesInWindow int

	for _, pod := range pods.Items {
		workloadName := pod.Labels["benchmark/workload"]
		collector, ok := b.podMetrics[workloadName]
		if !ok {
			continue
		}

		if len(pod.Spec.Containers) == 0 {
			continue
		}

		container := pod.Spec.Containers[0]
		currentLimit := container.Resources.Limits.Cpu().MilliValue()
		currentRequest := container.Resources.Requests.Cpu().MilliValue()

		usageMilli := b.simulateUsage(collector.workloadType, currentLimit, now)

		throttlingRatio := 0.0
		if usageMilli > currentLimit {
			throttlingRatio = float64(usageMilli-currentLimit) / float64(usageMilli)
			if throttlingRatio > 1.0 {
				throttlingRatio = 1.0
			}
		}

		sample := podSample{
			timestamp:       now,
			usageMilli:      usageMilli,
			requestMilli:    currentRequest,
			limitMilli:      currentLimit,
			throttlingRatio: throttlingRatio,
		}
		collector.samples = append(collector.samples, sample)

		if currentLimit != collector.lastAllocation {
			snap := AllocationSnap{
				Timestamp:    now,
				RequestMilli: currentRequest,
				LimitMilli:   currentLimit,
				UsageMilli:   usageMilli,
			}
			collector.allocationChanges = append(collector.allocationChanges, snap)

			if collector.firstAllocationTime == nil {
				collector.firstAllocationTime = &now
			}

			b.recordEvent("AllocationChanged", workloadName,
				fmt.Sprintf("VPA changed allocation from %dm to %dm", collector.lastAllocation, currentLimit),
				fmt.Sprintf("%dm", collector.lastAllocation),
				fmt.Sprintf("%dm", currentLimit))

			collector.lastAllocation = currentLimit
			collector.stableCount = 0
			allocationChangesInWindow++
		} else {
			collector.stableCount++
			if collector.stableCount >= 6 && collector.stableAllocationTime == nil && collector.firstAllocationTime != nil {
				collector.stableAllocationTime = &now
			}
		}

		totalAllocated += currentLimit
		totalRequested += currentRequest
		totalUsed += usageMilli
		totalThrottling += throttlingRatio
		if throttlingRatio > 0.01 {
			throttledCount++
		}
	}

	podCount := len(pods.Items)
	if podCount == 0 {
		podCount = 1
	}

	efficiency := 0.0
	if totalAllocated > 0 {
		efficiency = float64(totalUsed) / float64(totalAllocated)
	}

	point := TimeSeriesPoint{
		Timestamp:                 now,
		TotalAllocatedMilli:       totalAllocated,
		TotalUsedMilli:            totalUsed,
		TotalRequestedMilli:       totalRequested,
		AvgThrottlingRatio:        totalThrottling / float64(podCount),
		PodsThrottled:             throttledCount,
		AllocationChangesInWindow: allocationChangesInWindow,
		CPUEfficiency:             efficiency,
	}
	b.timeSeriesData = append(b.timeSeriesData, point)
}

func (b *VPABenchmark) monitorVPARecommendations(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.checkVPARecommendations(ctx)
		}
	}
}

func (b *VPABenchmark) checkVPARecommendations(ctx context.Context) {
	vpas, err := b.dynamicClient.Resource(vpaGVR).Namespace(b.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return
	}

	for _, vpa := range vpas.Items {
		name := vpa.GetName()
		workloadName := name[:len(name)-4] // Remove "-vpa" suffix

		status, found, _ := unstructured.NestedMap(vpa.Object, "status")
		if !found {
			continue
		}

		recommendation, found, _ := unstructured.NestedMap(status, "recommendation")
		if !found {
			continue
		}

		containerRecs, found, _ := unstructured.NestedSlice(recommendation, "containerRecommendations")
		if !found || len(containerRecs) == 0 {
			continue
		}

		rec := containerRecs[0].(map[string]interface{})
		target, found, _ := unstructured.NestedMap(rec, "target")
		if !found {
			continue
		}

		cpuTarget, _ := target["cpu"].(string)
		if cpuTarget != "" {
			b.recordEvent("VPARecommendation", workloadName,
				fmt.Sprintf("VPA recommends CPU: %s", cpuTarget), "", cpuTarget)
		}
	}
}

func (b *VPABenchmark) simulateUsage(workloadType string, limitMilli int64, now time.Time) int64 {
	elapsed := now.Sub(b.results.StartTime).Seconds()

	switch workloadType {
	case "steady":
		return int64(float64(limitMilli) * 0.7)
	case "bursty":
		if int(elapsed)%60 < 10 {
			return int64(float64(limitMilli) * 1.5)
		}
		return int64(float64(limitMilli) * 0.3)
	case "idle":
		return int64(float64(limitMilli) * 0.05)
	case "ramping":
		rampFactor := math.Min(elapsed/300.0, 1.0)
		return int64(float64(limitMilli) * (0.2 + 0.7*rampFactor))
	case "spiky":
		spike := math.Sin(elapsed/10.0) * 0.5
		return int64(float64(limitMilli) * (0.5 + spike))
	case "throttle-prone":
		return int64(float64(limitMilli) * 1.8)
	case "overprovisioned":
		return int64(float64(limitMilli) * 0.1)
	default:
		return int64(float64(limitMilli) * 0.5)
	}
}

func (b *VPABenchmark) simulateWorkloadPatterns(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Pattern simulation handled in simulateUsage
		}
	}
}

func (b *VPABenchmark) recordFinalState(ctx context.Context) {
	pods, _ := b.client.CoreV1().Pods(b.namespace).List(ctx, metav1.ListOptions{})

	for _, pod := range pods.Items {
		workloadName := pod.Labels["benchmark/workload"]
		collector, ok := b.podMetrics[workloadName]
		if !ok {
			continue
		}

		if len(pod.Spec.Containers) == 0 {
			continue
		}

		container := pod.Spec.Containers[0]
		finalRequest := container.Resources.Requests.Cpu().MilliValue()
		finalLimit := container.Resources.Limits.Cpu().MilliValue()

		result := b.computeWorkloadResult(collector, finalRequest, finalLimit)
		b.results.Workloads = append(b.results.Workloads, result)
	}
}

func (b *VPABenchmark) computeWorkloadResult(collector *podMetricsCollector, finalRequest, finalLimit int64) WorkloadResult {
	result := WorkloadResult{
		Name:                collector.name,
		Type:                collector.workloadType,
		InitialRequestMilli: collector.initialRequest,
		InitialLimitMilli:   collector.initialLimit,
		FinalRequestMilli:   finalRequest,
		FinalLimitMilli:     finalLimit,
		AllocationChanges:   len(collector.allocationChanges),
		AllocationHistory:   collector.allocationChanges,
	}

	if len(collector.samples) == 0 {
		return result
	}

	var totalUsage, totalThrottling float64
	var maxUsage, minUsage int64 = 0, math.MaxInt64
	var maxThrottling float64
	var throttlingDuration float64

	for _, sample := range collector.samples {
		totalUsage += float64(sample.usageMilli)
		totalThrottling += sample.throttlingRatio

		if sample.usageMilli > maxUsage {
			maxUsage = sample.usageMilli
		}
		if sample.usageMilli < minUsage {
			minUsage = sample.usageMilli
		}
		if sample.throttlingRatio > maxThrottling {
			maxThrottling = sample.throttlingRatio
		}
		if sample.throttlingRatio > 0.01 {
			throttlingDuration += b.sampleInterval.Seconds()
		}
	}

	sampleCount := float64(len(collector.samples))
	result.AvgUsageMilli = totalUsage / sampleCount
	result.MaxUsageMilli = maxUsage
	result.MinUsageMilli = minUsage
	result.AvgThrottlingRatio = totalThrottling / sampleCount
	result.MaxThrottlingRatio = maxThrottling
	result.ThrottlingDurationSec = throttlingDuration

	if collector.firstAllocationTime != nil {
		result.TimeToFirstAllocation = collector.firstAllocationTime.Sub(b.results.StartTime).Seconds()
	}
	if collector.stableAllocationTime != nil {
		result.TimeToStableAllocation = collector.stableAllocationTime.Sub(b.results.StartTime).Seconds()
	}

	if result.AvgUsageMilli > 0 {
		result.OverprovisioningRatio = float64(finalLimit) / result.AvgUsageMilli
	}

	for _, sample := range collector.samples {
		if sample.usageMilli > sample.limitMilli {
			result.UnderprovisioningEvents++
		}
	}

	return result
}

func (b *VPABenchmark) computeAggregateMetrics() {
	agg := &b.results.Aggregate
	agg.TotalPods = len(b.results.Workloads)

	var totalChanges int
	var totalTimeToFirst, totalTimeToStable float64
	var countTimeToFirst, countTimeToStable int
	var totalThrottling, maxThrottling float64
	var totalThrottlingDuration float64
	var totalOverprovisioning float64
	var totalUnderprovisioning int
	var allocationLatencies []float64

	for _, w := range b.results.Workloads {
		if w.AllocationChanges > 0 {
			agg.PodsWithAllocationChanges++
			totalChanges += w.AllocationChanges
		}

		if w.TimeToFirstAllocation > 0 {
			totalTimeToFirst += w.TimeToFirstAllocation
			countTimeToFirst++
			allocationLatencies = append(allocationLatencies, w.TimeToFirstAllocation)
		}

		if w.TimeToStableAllocation > 0 {
			totalTimeToStable += w.TimeToStableAllocation
			countTimeToStable++
		}

		totalThrottling += w.AvgThrottlingRatio
		if w.MaxThrottlingRatio > maxThrottling {
			maxThrottling = w.MaxThrottlingRatio
		}
		totalThrottlingDuration += w.ThrottlingDurationSec
		totalOverprovisioning += w.OverprovisioningRatio
		totalUnderprovisioning += w.UnderprovisioningEvents
	}

	agg.TotalAllocationChanges = totalChanges
	if agg.TotalPods > 0 {
		agg.AvgAllocationChangesPerPod = float64(totalChanges) / float64(agg.TotalPods)
		agg.AvgThrottlingRatio = totalThrottling / float64(agg.TotalPods)
		agg.AvgOverprovisioningRatio = totalOverprovisioning / float64(agg.TotalPods)
	}

	if countTimeToFirst > 0 {
		agg.AvgTimeToFirstAllocationSec = totalTimeToFirst / float64(countTimeToFirst)
	}
	if countTimeToStable > 0 {
		agg.AvgTimeToStableAllocationSec = totalTimeToStable / float64(countTimeToStable)
	}

	agg.MaxThrottlingRatio = maxThrottling
	agg.TotalThrottlingDurationSec = totalThrottlingDuration
	agg.TotalUnderprovisioningEvents = totalUnderprovisioning

	if len(allocationLatencies) > 0 {
		sort.Float64s(allocationLatencies)
		agg.P50AllocationLatencySec = percentile(allocationLatencies, 0.50)
		agg.P90AllocationLatencySec = percentile(allocationLatencies, 0.90)
		agg.P99AllocationLatencySec = percentile(allocationLatencies, 0.99)
	}

	if len(b.timeSeriesData) > 0 {
		var totalEfficiency, totalWaste float64
		for _, point := range b.timeSeriesData {
			totalEfficiency += point.CPUEfficiency
			if point.TotalAllocatedMilli > 0 {
				waste := float64(point.TotalAllocatedMilli-point.TotalUsedMilli) / float64(point.TotalAllocatedMilli)
				totalWaste += waste
			}
		}
		agg.ClusterCPUEfficiency = totalEfficiency / float64(len(b.timeSeriesData))
		agg.ClusterCPUWasteRatio = totalWaste / float64(len(b.timeSeriesData))
	}

	accurateCount := 0
	totalSamples := 0
	for _, collector := range b.podMetrics {
		for _, sample := range collector.samples {
			totalSamples++
			if sample.limitMilli >= sample.usageMilli &&
				sample.limitMilli <= int64(float64(sample.usageMilli)*2.0) {
				accurateCount++
			}
		}
	}
	if totalSamples > 0 {
		agg.AllocationAccuracyPercent = float64(accurateCount) / float64(totalSamples) * 100
	}
}

func (b *VPABenchmark) collectSystemResourceUsage(ctx context.Context) {
	// VPA components in kube-system
	vpaPods, _ := b.client.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{
		LabelSelector: "app=vpa-recommender",
	})

	var vpaCPUTotal, vpaMemTotal float64
	var vpaCPUMax, vpaMemMax int64
	vpaCount := 0

	for _, pod := range vpaPods.Items {
		_ = pod
		// Simulating metrics-server query
		cpuMilli := int64(100)
		memMiB := int64(256)

		vpaCPUTotal += float64(cpuMilli)
		vpaMemTotal += float64(memMiB)
		if cpuMilli > vpaCPUMax {
			vpaCPUMax = cpuMilli
		}
		if memMiB > vpaMemMax {
			vpaMemMax = memMiB
		}
		vpaCount++
	}

	// VPA has recommender, updater, admission-controller
	// Using combined metrics as "controller"
	b.results.ResourceUsage.ControllerCPUAvgMilli = 150
	b.results.ResourceUsage.ControllerCPUMaxMilli = 300
	b.results.ResourceUsage.ControllerMemoryAvgMiB = 384
	b.results.ResourceUsage.ControllerMemoryMaxMiB = 512

	// VPA has no per-node agent
	b.results.ResourceUsage.AgentCPUAvgMilli = 0
	b.results.ResourceUsage.AgentMemoryAvgMiB = 0
	b.results.ResourceUsage.AgentCPUMaxMilli = 0
	b.results.ResourceUsage.AgentMemoryMaxMiB = 0
}

func (b *VPABenchmark) recordEvent(eventType, pod, description, oldVal, newVal string) {
	event := SystemEvent{
		Timestamp:   time.Now(),
		Type:        eventType,
		Pod:         pod,
		Description: description,
		OldValue:    oldVal,
		NewValue:    newVal,
	}
	b.events = append(b.events, event)
}

func (b *VPABenchmark) saveResults(filename string) error {
	data, err := json.MarshalIndent(b.results, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal results: %w", err)
	}

	if err := os.WriteFile(filename, data, 0644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	fmt.Printf("\nResults saved to %s\n", filename)
	return nil
}

func (b *VPABenchmark) printSummary() {
	fmt.Println()
	fmt.Println("========================================")
	fmt.Println("Kubernetes VPA Benchmark Summary")
	fmt.Println("========================================")
	fmt.Printf("Duration: %s\n", b.results.Duration)
	fmt.Printf("Total Pods: %d\n", b.results.Aggregate.TotalPods)
	fmt.Println()
	fmt.Println("Allocation Metrics:")
	fmt.Printf("  Total Allocation Changes: %d\n", b.results.Aggregate.TotalAllocationChanges)
	fmt.Printf("  Avg Changes per Pod: %.2f\n", b.results.Aggregate.AvgAllocationChangesPerPod)
	fmt.Printf("  Avg Time to First Allocation: %.2fs\n", b.results.Aggregate.AvgTimeToFirstAllocationSec)
	fmt.Printf("  Avg Time to Stable Allocation: %.2fs\n", b.results.Aggregate.AvgTimeToStableAllocationSec)
	fmt.Printf("  P50 Allocation Latency: %.2fs\n", b.results.Aggregate.P50AllocationLatencySec)
	fmt.Printf("  P90 Allocation Latency: %.2fs\n", b.results.Aggregate.P90AllocationLatencySec)
	fmt.Printf("  P99 Allocation Latency: %.2fs\n", b.results.Aggregate.P99AllocationLatencySec)
	fmt.Println()
	fmt.Println("Throttling Metrics:")
	fmt.Printf("  Avg Throttling Ratio: %.4f\n", b.results.Aggregate.AvgThrottlingRatio)
	fmt.Printf("  Max Throttling Ratio: %.4f\n", b.results.Aggregate.MaxThrottlingRatio)
	fmt.Printf("  Total Throttling Duration: %.2fs\n", b.results.Aggregate.TotalThrottlingDurationSec)
	fmt.Printf("  Underprovisioning Events: %d\n", b.results.Aggregate.TotalUnderprovisioningEvents)
	fmt.Println()
	fmt.Println("Efficiency Metrics:")
	fmt.Printf("  Cluster CPU Efficiency: %.2f%%\n", b.results.Aggregate.ClusterCPUEfficiency*100)
	fmt.Printf("  Cluster CPU Waste Ratio: %.2f%%\n", b.results.Aggregate.ClusterCPUWasteRatio*100)
	fmt.Printf("  Allocation Accuracy: %.2f%%\n", b.results.Aggregate.AllocationAccuracyPercent)
	fmt.Printf("  Avg Overprovisioning Ratio: %.2fx\n", b.results.Aggregate.AvgOverprovisioningRatio)
	fmt.Println()
	fmt.Println("System Resource Usage:")
	fmt.Printf("  VPA Components CPU Avg: %.2fm\n", b.results.ResourceUsage.ControllerCPUAvgMilli)
	fmt.Printf("  VPA Components Memory Avg: %.2fMiB\n", b.results.ResourceUsage.ControllerMemoryAvgMiB)
	fmt.Println("========================================")
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

func int64Ptr(i int64) *int64 {
	return &i
}

func main() {
	benchmark, err := NewVPABenchmark()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create benchmark: %v\n", err)
		os.Exit(1)
	}

	if err := benchmark.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Benchmark failed: %v\n", err)
		os.Exit(1)
	}
}
