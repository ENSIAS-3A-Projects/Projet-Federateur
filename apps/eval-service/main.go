package main

import (
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Prometheus metrics
var (
	requestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request duration in seconds",
			Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		},
		[]string{"handler", "method", "status"},
	)

	requestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests",
		},
		[]string{"handler", "method", "status"},
	)

	cpuBurnDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "cpu_burn_duration_seconds",
			Help:    "CPU burn duration per request",
			Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1},
		},
		[]string{"role"},
	)
)

// Configuration
var (
	role           = getEnv("ROLE", "worker-a")
	port           = getEnv("PORT", "8080")
	targetA        = getEnv("TARGET_A_URL", "http://worker-a")
	targetB        = getEnv("TARGET_B_URL", "http://worker-b")
	baseLoadMs     = getEnvInt("CPU_LOAD_MS", 20)
	spikeProb      = getEnvFloat("SPIKE_PROB", 0.0)
	spikeMult      = getEnvFloat("SPIKE_MULT", 1.0)
	noiseIntensity = getEnvFloat("NOISE_INTENSITY", 0.5) // % of core
)

// responseWriter wraps http.ResponseWriter to capture status code
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// instrumentedHandler wraps handlers with Prometheus instrumentation
func instrumentedHandler(handler string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Create response writer wrapper to capture status
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		h(rw, r)

		duration := time.Since(start).Seconds()
		status := strconv.Itoa(rw.statusCode)

		requestDuration.WithLabelValues(handler, r.Method, status).Observe(duration)
		requestsTotal.WithLabelValues(handler, r.Method, status).Inc()
	}
}

// BurnCPU burns CPU for approximately specified duration
// This is a busy loop, not sleep!
func burnCPU(duration time.Duration) {
	start := time.Now()
	for time.Since(start) < duration {
		// Busy loop
		_ = 1 + 1
	}
	cpuBurnDuration.WithLabelValues(role).Observe(duration.Seconds())
}

// NoiseLoop burns CPU continuously to simulate background noise
func noiseLoop() {
	log.Printf("Starting background noise loop with intensity %.2f", noiseIntensity)
	for {
		// Burn for 100ms * intensity
		burnDuration := time.Duration(float64(100*time.Millisecond) * noiseIntensity)
		sleepDuration := 100*time.Millisecond - burnDuration

		burnCPU(burnDuration)
		time.Sleep(sleepDuration)
	}
}

// Gateway Handler: Calls A then B
func handleGateway(w http.ResponseWriter, r *http.Request) {
	// Call Worker A
	if err := callService(targetA); err != nil {
		http.Error(w, fmt.Sprintf("Error calling A: %v", err), 500)
		return
	}

	// Call Worker B
	if err := callService(targetB); err != nil {
		http.Error(w, fmt.Sprintf("Error calling B: %v", err), 500)
		return
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "Gateway done")
}

// Worker Handler: Burns CPU
func handleWorker(w http.ResponseWriter, r *http.Request) {
	load := time.Duration(baseLoadMs) * time.Millisecond

	// Apply spikes if enabled (Worker B)
	if spikeProb > 0 && rand.Float64() < spikeProb {
		load = time.Duration(float64(load) * spikeMult)
		log.Printf("SPIKE! Burning for %v", load)
	}

	burnCPU(load)
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "Worked for %v", load)
}

func callService(url string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.ReadAll(resp.Body)
	return err
}

func main() {
	log.Printf("Starting Service. Role: %s, Port: %s", role, port)

	// Prometheus metrics endpoint
	http.Handle("/metrics", promhttp.Handler())

	// Health endpoint
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "OK")
	})

	if role == "noise" {
		go noiseLoop()
	} else if role == "gateway" {
		http.HandleFunc("/", instrumentedHandler("gateway", handleGateway))
	} else {
		// Worker A or B
		http.HandleFunc("/", instrumentedHandler("worker", handleWorker))
	}

	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// Helpers
func getEnv(key, fallback string) string {
	if val, ok := os.LookupEnv(key); ok {
		return val
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	str := getEnv(key, "")
	if val, err := strconv.Atoi(str); err == nil {
		return val
	}
	return fallback
}

func getEnvFloat(key string, fallback float64) float64 {
	str := getEnv(key, "")
	if val, err := strconv.ParseFloat(str, 64); err == nil {
		return val
	}
	return fallback
}
