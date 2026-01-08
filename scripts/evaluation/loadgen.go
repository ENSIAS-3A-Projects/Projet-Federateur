package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Configuration
var (
	targetURL     = flag.String("url", "http://localhost:80/gateway", "Target URL")
	outputDir     = flag.String("out", "results", "Output directory for metrics")
	durationScale = flag.Float64("scale", 1.0, "Duration scaling factor (e.g. 0.1 for 10% duration)")
)

type Phase struct {
	Name     string
	Duration time.Duration
	RPS      int
	Bursty   bool
}

type PhaseResult struct {
	PhaseName      string        `json:"phase_name"`
	Duration       time.Duration `json:"duration"`
	TotalRequests  int64         `json:"total_requests"`
	TotalErrors    int64         `json:"total_errors"`
	SuccessRate    float64       `json:"success_rate"`
	AvgLatency     float64       `json:"avg_latency_ms"`
	P50Latency     float64       `json:"p50_latency_ms"`
	P95Latency     float64       `json:"p95_latency_ms"`
	P99Latency     float64       `json:"p99_latency_ms"`
	MaxLatency     float64       `json:"max_latency_ms"`
	MinLatency     float64       `json:"min_latency_ms"`
	ThroughputRPS  float64       `json:"throughput_rps"`
	StartTime      time.Time     `json:"start_time"`
	EndTime        time.Time     `json:"end_time"`
}

type TestResult struct {
	TargetURL    string        `json:"target_url"`
	StartTime    time.Time     `json:"start_time"`
	EndTime      time.Time     `json:"end_time"`
	TotalPhases  int           `json:"total_phases"`
	PhaseResults []PhaseResult `json:"phase_results"`
	Overall      struct {
		TotalRequests int64   `json:"total_requests"`
		TotalErrors   int64   `json:"total_errors"`
		AvgLatency    float64 `json:"avg_latency_ms"`
		P95Latency    float64 `json:"p95_latency_ms"`
		P99Latency    float64 `json:"p99_latency_ms"`
	} `json:"overall"`
}

func main() {
	flag.Parse()

	// Create output directory
	if err := os.MkdirAll(*outputDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating output directory: %v\n", err)
		os.Exit(1)
	}

	phases := []Phase{
		{"Warmup", time.Duration(2 * float64(time.Minute) * *durationScale), 10, false},
		{"Saturation", time.Duration(5 * float64(time.Minute) * *durationScale), 50, false},
		{"Contention", time.Duration(5 * float64(time.Minute) * *durationScale), 100, false},
		{"Bursty", time.Duration(3 * float64(time.Minute) * *durationScale), 50, true}, // Base 50, spikes to 100
	}

	testResult := TestResult{
		TargetURL:    *targetURL,
		StartTime:    time.Now().UTC(),
		TotalPhases:  len(phases),
		PhaseResults: make([]PhaseResult, 0, len(phases)),
	}

	fmt.Printf("Starting load test against %s\n", *targetURL)
	fmt.Println("Phase schedule:")
	for _, p := range phases {
		fmt.Printf("- %s: %s (RPS: %d, Bursty: %v)\n", p.Name, p.Duration, p.RPS, p.Bursty)
	}

	// Run all phases
	for _, p := range phases {
		result := runPhase(p)
		testResult.PhaseResults = append(testResult.PhaseResults, result)
		testResult.Overall.TotalRequests += result.TotalRequests
		testResult.Overall.TotalErrors += result.TotalErrors
	}

	testResult.EndTime = time.Now().UTC()

	// Calculate overall statistics
	allLatencies := make([]float64, 0)
	for _, pr := range testResult.PhaseResults {
		allLatencies = append(allLatencies, pr.P95Latency)
	}
	if len(allLatencies) > 0 {
		sort.Float64s(allLatencies)
		testResult.Overall.P95Latency = percentile(allLatencies, 0.95)
		testResult.Overall.P99Latency = percentile(allLatencies, 0.99)
		testResult.Overall.AvgLatency = average(allLatencies)
	}

	// Export results
	exportResults(testResult)

	fmt.Println("\nLoad test complete.")
	fmt.Printf("Total requests: %d\n", testResult.Overall.TotalRequests)
	fmt.Printf("Total errors: %d\n", testResult.Overall.TotalErrors)
	fmt.Printf("Overall P95 latency: %.2f ms\n", testResult.Overall.P95Latency)
}

func runPhase(p Phase) PhaseResult {
	startTime := time.Now().UTC()
	fmt.Printf("\n>>> Starting Phase: %s (%s)\n", p.Name, p.Duration)

	stop := make(chan struct{})
	timer := time.NewTimer(p.Duration)

	var wg sync.WaitGroup
	var requests, errors int64
	var latencies []time.Duration
	var mu sync.Mutex

	// Rate limiter ticker
	interval := time.Duration(1e9 / p.RPS)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Burst logic
	burstTicker := time.NewTicker(20 * time.Second)
	defer burstTicker.Stop()
	isBurst := false

	go func() {
		for {
			select {
			case <-stop:
				return
			case <-burstTicker.C:
				if p.Bursty {
					isBurst = !isBurst
					if isBurst {
						fmt.Println("  [Bursty] Spike started!")
						ticker.Reset(time.Duration(1e9 / (p.RPS * 2))) // Double RPS
					} else {
						fmt.Println("  [Bursty] Spike ended.")
						ticker.Reset(interval)
					}
				}
			case <-ticker.C:
				wg.Add(1)
				go func() {
					defer wg.Done()
					start := time.Now()
					resp, err := http.Get(*targetURL)
					latency := time.Since(start)

					if err == nil && resp != nil && resp.StatusCode == 200 {
						atomic.AddInt64(&requests, 1)
						resp.Body.Close()
						mu.Lock()
						latencies = append(latencies, latency)
						mu.Unlock()
					} else {
						atomic.AddInt64(&errors, 1)
					}
				}()
			}
		}
	}()

	<-timer.C
	stop <- struct{}{}
	wg.Wait()
	endTime := time.Now().UTC()

	// Calculate stats
	mu.Lock()
	count := len(latencies)
	latenciesCopy := make([]time.Duration, count)
	copy(latenciesCopy, latencies)
	mu.Unlock()

	result := PhaseResult{
		PhaseName:     p.Name,
		Duration:      p.Duration,
		TotalRequests: requests,
		TotalErrors:   errors,
		StartTime:     startTime,
		EndTime:       endTime,
	}

	if count > 0 {
		// Sort latencies for percentile calculation
		sort.Slice(latenciesCopy, func(i, j int) bool {
			return latenciesCopy[i] < latenciesCopy[j]
		})

		// Calculate percentiles
		latenciesMs := make([]float64, count)
		var sum time.Duration
		for i, l := range latenciesCopy {
			latenciesMs[i] = float64(l.Nanoseconds()) / 1e6 // Convert to milliseconds
			sum += l
		}

		result.AvgLatency = float64(sum.Nanoseconds()) / float64(count) / 1e6
		result.P50Latency = percentile(latenciesMs, 0.50)
		result.P95Latency = percentile(latenciesMs, 0.95)
		result.P99Latency = percentile(latenciesMs, 0.99)
		result.MinLatency = latenciesMs[0]
		result.MaxLatency = latenciesMs[count-1]

		// Calculate throughput
		durationSeconds := endTime.Sub(startTime).Seconds()
		if durationSeconds > 0 {
			result.ThroughputRPS = float64(requests) / durationSeconds
		}
	}

	if requests > 0 {
		result.SuccessRate = float64(requests-errors) / float64(requests) * 100
	}

	fmt.Printf("<<< Finished Phase: %s\n", p.Name)
	fmt.Printf("    Requests: %d\n", result.TotalRequests)
	fmt.Printf("    Errors:   %d\n", result.TotalErrors)
	fmt.Printf("    Success Rate: %.2f%%\n", result.SuccessRate)
	fmt.Printf("    Avg Lat:  %.2f ms\n", result.AvgLatency)
	fmt.Printf("    P50 Lat:  %.2f ms\n", result.P50Latency)
	fmt.Printf("    P95 Lat:  %.2f ms\n", result.P95Latency)
	fmt.Printf("    P99 Lat:  %.2f ms\n", result.P99Latency)
	fmt.Printf("    Throughput: %.2f RPS\n", result.ThroughputRPS)

	return result
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	index := p * float64(len(sorted)-1)
	lower := int(index)
	upper := lower + 1
	if upper >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	weight := index - float64(lower)
	return sorted[lower]*(1-weight) + sorted[upper]*weight
}

func average(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

func exportResults(result TestResult) {
	// Export JSON
	jsonPath := fmt.Sprintf("%s/loadgen_results.json", *outputDir)
	jsonFile, err := os.Create(jsonPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating JSON file: %v\n", err)
		return
	}
	defer jsonFile.Close()

	encoder := json.NewEncoder(jsonFile)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
		return
	}
	fmt.Printf("\nOK: Results exported to %s\n", jsonPath)

	// Export CSV
	csvPath := fmt.Sprintf("%s/loadgen_results.csv", *outputDir)
	csvFile, err := os.Create(csvPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating CSV file: %v\n", err)
		return
	}
	defer csvFile.Close()

	writer := csv.NewWriter(csvFile)
	defer writer.Flush()

	// Write header
	writer.Write([]string{
		"Phase", "Duration_sec", "Total_Requests", "Total_Errors", "Success_Rate_%",
		"Avg_Latency_ms", "P50_Latency_ms", "P95_Latency_ms", "P99_Latency_ms",
		"Min_Latency_ms", "Max_Latency_ms", "Throughput_RPS", "Start_Time", "End_Time",
	})

	// Write phase results
	for _, pr := range result.PhaseResults {
		writer.Write([]string{
			pr.PhaseName,
			fmt.Sprintf("%.2f", pr.Duration.Seconds()),
			fmt.Sprintf("%d", pr.TotalRequests),
			fmt.Sprintf("%d", pr.TotalErrors),
			fmt.Sprintf("%.2f", pr.SuccessRate),
			fmt.Sprintf("%.2f", pr.AvgLatency),
			fmt.Sprintf("%.2f", pr.P50Latency),
			fmt.Sprintf("%.2f", pr.P95Latency),
			fmt.Sprintf("%.2f", pr.P99Latency),
			fmt.Sprintf("%.2f", pr.MinLatency),
			fmt.Sprintf("%.2f", pr.MaxLatency),
			fmt.Sprintf("%.2f", pr.ThroughputRPS),
			pr.StartTime.Format(time.RFC3339),
			pr.EndTime.Format(time.RFC3339),
		})
	}

	fmt.Printf("OK: Results exported to %s\n", csvPath)
}
