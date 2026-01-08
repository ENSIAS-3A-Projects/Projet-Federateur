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

// BurnCPU burns CPU for approximately specified duration
// This is a busy loop, not sleep!
func burnCPU(duration time.Duration) {
	start := time.Now()
	for time.Since(start) < duration {
		// Busy loop
		_ = 1 + 1
	}
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
	start := time.Now()

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

	duration := time.Since(start)
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "Gateway done in %v", duration)
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

	if role == "noise" {
		go noiseLoop()
		// Noise pod also exposes health check but main job is background loop
		http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	} else if role == "gateway" {
		http.HandleFunc("/", handleGateway)
	} else {
		// Worker A or B
		http.HandleFunc("/", handleWorker)
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
