// File: cmd/compare-results/main.go
package main

import (
	"encoding/json"
	"fmt"
	"os"
)

type MetricsResult struct {
	TestName      string             `json:"test_name"`
	System        string             `json:"system"`
	Duration      string             `json:"duration"`
	Configuration TestConfiguration  `json:"configuration"`
	Aggregate     AggregateMetrics   `json:"aggregate"`
	ResourceUsage SystemResourceUsage `json:"resource_usage"`
}

type TestConfiguration struct {
	TestDurationMinutes int   `json:"test_duration_minutes"`
	WorkloadCount       int   `json:"workload_count"`
	NodeCount           int   `json:"node_count"`
	TotalNodeCPUMilli   int64 `json:"total_node_cpu_milli"`
}

type AggregateMetrics struct {
	TotalPods                    int     `json:"total_pods"`
	TotalAllocationChanges       int     `json:"total_allocation_changes"`
	AvgAllocationChangesPerPod   float64 `json:"avg_allocation_changes_per_pod"`
	AvgTimeToFirstAllocationSec  float64 `json:"avg_time_to_first_allocation_seconds"`
	AvgTimeToStableAllocationSec float64 `json:"avg_time_to_stable_allocation_seconds"`
	AvgThrottlingRatio           float64 `json:"avg_throttling_ratio"`
	MaxThrottlingRatio           float64 `json:"max_throttling_ratio"`
	TotalThrottlingDurationSec   float64 `json:"total_throttling_duration_seconds"`
	TotalUnderprovisioningEvents int     `json:"total_underprovisioning_events"`
	ClusterCPUEfficiency         float64 `json:"cluster_cpu_efficiency"`
	ClusterCPUWasteRatio         float64 `json:"cluster_cpu_waste_ratio"`
	AllocationAccuracyPercent    float64 `json:"allocation_accuracy_percent"`
	AvgOverprovisioningRatio     float64 `json:"avg_overprovisioning_ratio"`
	P50AllocationLatencySec      float64 `json:"p50_allocation_latency_seconds"`
	P90AllocationLatencySec      float64 `json:"p90_allocation_latency_seconds"`
	P99AllocationLatencySec      float64 `json:"p99_allocation_latency_seconds"`
}

type SystemResourceUsage struct {
	AgentCPUAvgMilli       float64 `json:"agent_cpu_avg_milli"`
	AgentMemoryAvgMiB      float64 `json:"agent_memory_avg_mib"`
	ControllerCPUAvgMilli  float64 `json:"controller_cpu_avg_milli"`
	ControllerMemoryAvgMiB float64 `json:"controller_memory_avg_mib"`
}

type ComparisonResult struct {
	Metric      string  `json:"metric"`
	MBCAS       float64 `json:"mbcas"`
	VPA         float64 `json:"vpa"`
	Difference  float64 `json:"difference"`
	Winner      string  `json:"winner"`
	Improvement string  `json:"improvement"`
}

func main() {
	mbcasData, err := os.ReadFile("metrics-mbcas.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read MBCAS metrics: %v\n", err)
		os.Exit(1)
	}

	vpaData, err := os.ReadFile("metrics-vpa.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read VPA metrics: %v\n", err)
		os.Exit(1)
	}

	var mbcas, vpa MetricsResult
	if err := json.Unmarshal(mbcasData, &mbcas); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse MBCAS metrics: %v\n", err)
		os.Exit(1)
	}
	if err := json.Unmarshal(vpaData, &vpa); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse VPA metrics: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("================================================================================")
	fmt.Println("                    MBCAS vs VPA Comparison Report")
	fmt.Println("================================================================================")
	fmt.Println()

	fmt.Println("Test Configuration:")
	fmt.Printf("  Duration: MBCAS=%s, VPA=%s\n", mbcas.Duration, vpa.Duration)
	fmt.Printf("  Workloads: MBCAS=%d, VPA=%d\n", mbcas.Configuration.WorkloadCount, vpa.Configuration.WorkloadCount)
	fmt.Printf("  Nodes: %d\n", mbcas.Configuration.NodeCount)
	fmt.Printf("  Total Node CPU: %dm\n", mbcas.Configuration.TotalNodeCPUMilli)
	fmt.Println()

	comparisons := []ComparisonResult{}

	// Responsiveness metrics (lower is better)
	comparisons = append(comparisons, compare(
		"Time to First Allocation (s)",
		mbcas.Aggregate.AvgTimeToFirstAllocationSec,
		vpa.Aggregate.AvgTimeToFirstAllocationSec,
		true,
	))

	comparisons = append(comparisons, compare(
		"Time to Stable Allocation (s)",
		mbcas.Aggregate.AvgTimeToStableAllocationSec,
		vpa.Aggregate.AvgTimeToStableAllocationSec,
		true,
	))

	comparisons = append(comparisons, compare(
		"P50 Allocation Latency (s)",
		mbcas.Aggregate.P50AllocationLatencySec,
		vpa.Aggregate.P50AllocationLatencySec,
		true,
	))

	comparisons = append(comparisons, compare(
		"P90 Allocation Latency (s)",
		mbcas.Aggregate.P90AllocationLatencySec,
		vpa.Aggregate.P90AllocationLatencySec,
		true,
	))

	comparisons = append(comparisons, compare(
		"P99 Allocation Latency (s)",
		mbcas.Aggregate.P99AllocationLatencySec,
		vpa.Aggregate.P99AllocationLatencySec,
		true,
	))

	// Throttling metrics (lower is better)
	comparisons = append(comparisons, compare(
		"Avg Throttling Ratio",
		mbcas.Aggregate.AvgThrottlingRatio,
		vpa.Aggregate.AvgThrottlingRatio,
		true,
	))

	comparisons = append(comparisons, compare(
		"Max Throttling Ratio",
		mbcas.Aggregate.MaxThrottlingRatio,
		vpa.Aggregate.MaxThrottlingRatio,
		true,
	))

	comparisons = append(comparisons, compare(
		"Total Throttling Duration (s)",
		mbcas.Aggregate.TotalThrottlingDurationSec,
		vpa.Aggregate.TotalThrottlingDurationSec,
		true,
	))

	comparisons = append(comparisons, compare(
		"Underprovisioning Events",
		float64(mbcas.Aggregate.TotalUnderprovisioningEvents),
		float64(vpa.Aggregate.TotalUnderprovisioningEvents),
		true,
	))

	// Efficiency metrics (higher is better for efficiency, lower for waste)
	comparisons = append(comparisons, compare(
		"CPU Efficiency (%)",
		mbcas.Aggregate.ClusterCPUEfficiency*100,
		vpa.Aggregate.ClusterCPUEfficiency*100,
		false,
	))

	comparisons = append(comparisons, compare(
		"CPU Waste Ratio (%)",
		mbcas.Aggregate.ClusterCPUWasteRatio*100,
		vpa.Aggregate.ClusterCPUWasteRatio*100,
		true,
	))

	comparisons = append(comparisons, compare(
		"Allocation Accuracy (%)",
		mbcas.Aggregate.AllocationAccuracyPercent,
		vpa.Aggregate.AllocationAccuracyPercent,
		false,
	))

	comparisons = append(comparisons, compare(
		"Avg Overprovisioning Ratio",
		mbcas.Aggregate.AvgOverprovisioningRatio,
		vpa.Aggregate.AvgOverprovisioningRatio,
		true,
	))

	// Stability metrics
	comparisons = append(comparisons, compare(
		"Allocation Changes per Pod",
		mbcas.Aggregate.AvgAllocationChangesPerPod,
		vpa.Aggregate.AvgAllocationChangesPerPod,
		true,
	))

	// System overhead
	totalMBCASCPU := mbcas.ResourceUsage.AgentCPUAvgMilli + mbcas.ResourceUsage.ControllerCPUAvgMilli
	totalVPACPU := vpa.ResourceUsage.ControllerCPUAvgMilli
	comparisons = append(comparisons, compare(
		"System CPU Overhead (m)",
		totalMBCASCPU,
		totalVPACPU,
		true,
	))

	totalMBCASMem := mbcas.ResourceUsage.AgentMemoryAvgMiB + mbcas.ResourceUsage.ControllerMemoryAvgMiB
	totalVPAMem := vpa.ResourceUsage.ControllerMemoryAvgMiB
	comparisons = append(comparisons, compare(
		"System Memory Overhead (MiB)",
		totalMBCASMem,
		totalVPAMem,
		true,
	))

	// Print comparison table
	fmt.Println("Comparison Results:")
	fmt.Println("--------------------------------------------------------------------------------")
	fmt.Printf("%-35s %12s %12s %12s %8s\n", "Metric", "MBCAS", "VPA", "Diff", "Winner")
	fmt.Println("--------------------------------------------------------------------------------")

	mbcasWins := 0
	vpaWins := 0
	ties := 0

	for _, c := range comparisons {
		winnerStr := c.Winner
		if c.Winner == "MBCAS" {
			winnerStr = "MBCAS ✓"
			mbcasWins++
		} else if c.Winner == "VPA" {
			winnerStr = "VPA ✓"
			vpaWins++
		} else {
			ties++
		}
		fmt.Printf("%-35s %12.4f %12.4f %12.4f %8s\n",
			c.Metric, c.MBCAS, c.VPA, c.Difference, winnerStr)
	}

	fmt.Println("--------------------------------------------------------------------------------")
	fmt.Println()

	// Summary
	fmt.Println("Summary:")
	fmt.Printf("  MBCAS Wins: %d\n", mbcasWins)
	fmt.Printf("  VPA Wins: %d\n", vpaWins)
	fmt.Printf("  Ties: %d\n", ties)
	fmt.Println()

	// Key findings
	fmt.Println("Key Findings:")

	// Responsiveness
	if mbcas.Aggregate.AvgTimeToFirstAllocationSec < vpa.Aggregate.AvgTimeToFirstAllocationSec {
		improvement := (vpa.Aggregate.AvgTimeToFirstAllocationSec - mbcas.Aggregate.AvgTimeToFirstAllocationSec) / vpa.Aggregate.AvgTimeToFirstAllocationSec * 100
		fmt.Printf("  • MBCAS responds %.1f%% faster to workload changes\n", improvement)
	} else if vpa.Aggregate.AvgTimeToFirstAllocationSec < mbcas.Aggregate.AvgTimeToFirstAllocationSec {
		improvement := (mbcas.Aggregate.AvgTimeToFirstAllocationSec - vpa.Aggregate.AvgTimeToFirstAllocationSec) / mbcas.Aggregate.AvgTimeToFirstAllocationSec * 100
		fmt.Printf("  • VPA responds %.1f%% faster to workload changes\n", improvement)
	}

	// Throttling
	if mbcas.Aggregate.AvgThrottlingRatio < vpa.Aggregate.AvgThrottlingRatio {
		improvement := (vpa.Aggregate.AvgThrottlingRatio - mbcas.Aggregate.AvgThrottlingRatio) / vpa.Aggregate.AvgThrottlingRatio * 100
		fmt.Printf("  • MBCAS reduces throttling by %.1f%%\n", improvement)
	} else if vpa.Aggregate.AvgThrottlingRatio < mbcas.Aggregate.AvgThrottlingRatio {
		improvement := (mbcas.Aggregate.AvgThrottlingRatio - vpa.Aggregate.AvgThrottlingRatio) / mbcas.Aggregate.AvgThrottlingRatio * 100
		fmt.Printf("  • VPA reduces throttling by %.1f%%\n", improvement)
	}

	// Efficiency
	if mbcas.Aggregate.ClusterCPUEfficiency > vpa.Aggregate.ClusterCPUEfficiency {
		improvement := (mbcas.Aggregate.ClusterCPUEfficiency - vpa.Aggregate.ClusterCPUEfficiency) * 100
		fmt.Printf("  • MBCAS achieves %.1f%% higher CPU efficiency\n", improvement)
	} else if vpa.Aggregate.ClusterCPUEfficiency > mbcas.Aggregate.ClusterCPUEfficiency {
		improvement := (vpa.Aggregate.ClusterCPUEfficiency - mbcas.Aggregate.ClusterCPUEfficiency) * 100
		fmt.Printf("  • VPA achieves %.1f%% higher CPU efficiency\n", improvement)
	}

	// Overhead
	if totalMBCASCPU < totalVPACPU {
		fmt.Printf("  • MBCAS uses %.0fm less CPU overhead\n", totalVPACPU-totalMBCASCPU)
	} else if totalVPACPU < totalMBCASCPU {
		fmt.Printf("  • VPA uses %.0fm less CPU overhead\n", totalMBCASCPU-totalVPACPU)
	}

	fmt.Println()
	fmt.Println("================================================================================")

	// Save comparison to JSON
	comparisonOutput := map[string]interface{}{
		"comparisons":  comparisons,
		"mbcas_wins":   mbcasWins,
		"vpa_wins":     vpaWins,
		"ties":         ties,
		"mbcas_system": mbcas.System,
		"vpa_system":   vpa.System,
	}

	comparisonData, _ := json.MarshalIndent(comparisonOutput, "", "  ")
	_ = os.WriteFile("metrics-comparison.json", comparisonData, 0644)
	fmt.Println("Comparison saved to metrics-comparison.json")
}

func compare(metric string, mbcas, vpa float64, lowerIsBetter bool) ComparisonResult {
	diff := mbcas - vpa
	var winner string
	var improvement string

	if mbcas == vpa || (mbcas == 0 && vpa == 0) {
		winner = "Tie"
		improvement = "0%"
	} else if lowerIsBetter {
		if mbcas < vpa {
			winner = "MBCAS"
			if vpa != 0 {
				improvement = fmt.Sprintf("%.1f%% better", (vpa-mbcas)/vpa*100)
			}
		} else {
			winner = "VPA"
			if mbcas != 0 {
				improvement = fmt.Sprintf("%.1f%% better", (mbcas-vpa)/mbcas*100)
			}
		}
	} else {
		if mbcas > vpa {
			winner = "MBCAS"
			if vpa != 0 {
				improvement = fmt.Sprintf("%.1f%% better", (mbcas-vpa)/vpa*100)
			}
		} else {
			winner = "VPA"
			if mbcas != 0 {
				improvement = fmt.Sprintf("%.1f%% better", (vpa-mbcas)/mbcas*100)
			}
		}
	}

	return ComparisonResult{
		Metric:      metric,
		MBCAS:       mbcas,
		VPA:         vpa,
		Difference:  diff,
		Winner:      winner,
		Improvement: improvement,
	}
}
