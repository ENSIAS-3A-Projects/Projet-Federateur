#!/usr/bin/env pwsh
# ULTIMATE BENCHMARK: MBCAS vs Native Kubernetes
# Automates Cluster Start, Image Pulling, and Comparison.

$ErrorActionPreference = "Stop"
$ResultsDir = ".\test-results"
New-Item -ItemType Directory -Force -Path $ResultsDir | Out-Null

# --- Helper Functions ---
function Write-Header { param($msg) Write-Host "`n======================================================`n  $msg`n======================================================" -ForegroundColor Magenta }
function Write-Step { param($msg) Write-Host " -> $msg" -ForegroundColor Cyan }

function Ensure-Minikube {
    param([bool]$Clean = $false)
    
    if ($Clean) {
        Write-Step "Rebooting Cluster (Clean Slate)..."
        minikube delete | Out-Null
    }

    $status = minikube status -o json | ConvertFrom-Json -ErrorAction SilentlyContinue
    if ($status.Host -ne "Running") {
        Write-Step "Starting Minikube (4 CPUs, 4GB)..."
        minikube start --cpus=4 --memory=4096 --driver=docker --wait=all | Out-Null
    } else {
        Write-Step "Minikube is running."
    }
}

function Pre-Pull-Images {
    Write-Step "Pre-pulling test images (to prevent timeouts)..."
    minikube ssh "docker pull python:3.9-slim && docker pull polinux/stress-ng" | Out-Null
}

function Run-Performance-Test {
    param([string]$Mode, [string]$OutputFile)
    
    $Namespace = "bench-$($Mode.ToLower())"
    Write-Host "`n[TEST] Running $Mode Benchmark..." -ForegroundColor Yellow

    # 1. Setup
    kubectl create namespace $Namespace --dry-run=client -o yaml | kubectl apply -f - | Out-Null
    
    # 2. Server Config
    $serverConfig = @"
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-script
data:
  server.py: |
    from http.server import BaseHTTPRequestHandler, HTTPServer
    import time
    def fib(n):
        if n <= 1: return n
        return fib(n-1) + fib(n-2)
    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            start = time.time()
            val = fib(25) # CPU Burn (~20ms)
            duration = (time.time() - start) * 1000
            self.send_response(200)
            self.end_headers()
            self.wfile.write(f'Time: {duration:.2f}ms'.encode())
    server = HTTPServer(('0.0.0.0', 8080), Handler)
    server.serve_forever()
"@
    $serverConfig | kubectl apply -n $Namespace -f - | Out-Null

    # 3. Victim App
    $managed = if ($Mode -eq "MBCAS") { "true" } else { "false" }
    $appPod = @"
apiVersion: v1
kind: Pod
metadata:
  name: victim-app
  namespace: $Namespace
  labels:
    app: victim
    mbcas.io/managed: "$managed"
spec:
  containers:
  - name: python
    image: python:3.9-slim
    command: ["python", "/app/server.py"]
    volumeMounts:
    - name: script
      mountPath: /app
    resources:
      requests:
        cpu: "100m"
      limits:
        cpu: "2000m"
  volumes:
  - name: script
    configMap:
      name: app-script
"@
    $appPod | kubectl apply -f - | Out-Null

    # 4. Noise (Attackers)
    Write-Step "Deploying Noise (3x Pods)..."
    for ($i=1; $i -le 3; $i++) {
        $noisePod = @"
apiVersion: v1
kind: Pod
metadata:
  name: noise-$i
  namespace: $Namespace
  labels:
    app: noise
    mbcas.io/managed: "$managed"
spec:
  containers:
  - name: stress
    image: polinux/stress-ng
    args: ["--cpu", "2", "--timeout", "120s"]
    resources:
      requests:
        cpu: "100m"
      limits:
        cpu: "2000m"
"@
        $noisePod | kubectl apply -f - | Out-Null
    }

    # Wait for Victim
    Write-Step "Waiting for App to be Ready..."
    kubectl wait --for=condition=Ready pod/victim-app -n $Namespace --timeout=120s | Out-Null
    Start-Sleep 5

    # 5. Load Generator
    Write-Step "Launching Load Generator (30s)..."
    $loadPod = @"
apiVersion: v1
kind: Pod
metadata:
  name: load-gen
  namespace: $Namespace
spec:
  restartPolicy: Never
  containers:
  - name: load
    image: python:3.9-slim
    command: ["python", "-c"]
    args:
      - |
        import urllib.request, time
        url = "http://victim-app:8080"
        start = time.time()
        count = 0
        total_lat = 0
        
        # Run for 30 seconds
        while (time.time() - start) < 30:
            req_start = time.time()
            try:
                with urllib.request.urlopen(url, timeout=2) as f: f.read()
                total_lat += (time.time() - req_start) * 1000
                count += 1
            except: pass
            time.sleep(0.1) # Slight pause to prevent self-congestion
        
        avg = (total_lat / count) if count > 0 else 0
        rps = count / 30
        print(f"{rps:.2f}|{avg:.2f}")
"@
    $loadPod | kubectl apply -f - | Out-Null
    
    # Wait for completion (Increased timeout to 120s)
    kubectl wait --for=condition=Completed pod/load-gen -n $Namespace --timeout=120s | Out-Null
    
    # 6. Result
    $log = kubectl logs load-gen -n $Namespace
    $parts = $log.Split("|")
    $rps = [double]$parts[0]
    $lat = [double]$parts[1]
    
    Write-Host "  RESULT: $rps RPS | $lat ms Latency" -ForegroundColor Green
    
    @{ Mode=$Mode; RPS=$rps; Latency=$lat } | ConvertTo-Json | Out-File $OutputFile
    
    # Cleanup
    kubectl delete namespace $Namespace --timeout=30s | Out-Null
}

# ==============================================================================
# PHASE 1: NATIVE K8s (Standard Requests/Limits)
# ==============================================================================
Write-Header "PHASE 1: Native Kubernetes (Clean)"
Ensure-Minikube -Clean $true
Pre-Pull-Images
Run-Performance-Test -Mode "Native" -OutputFile "$ResultsDir\native.json"

# ==============================================================================
# PHASE 2: MBCAS (Your System)
# ==============================================================================
Write-Header "PHASE 2: MBCAS System"

# Note: We don't delete minikube here to save time, we just deploy MBCAS on top.
# If you want a 100% clean test, change -Clean $false to $true
Ensure-Minikube -Clean $false

Write-Step "Building & Deploying MBCAS..."
.\scripts\build.ps1 | Out-Null
.\scripts\deploy.ps1 | Out-Null

Write-Step "Waiting for Controller..."
kubectl wait --for=condition=Available deployment/mbcas-controller -n mbcas-system --timeout=120s | Out-Null

Run-Performance-Test -Mode "MBCAS" -OutputFile "$ResultsDir\mbcas.json"

# ==============================================================================
# FINAL SCORECARD
# ==============================================================================
Write-Header "FINAL SCORECARD"

$n = Get-Content "$ResultsDir\native.json" | ConvertFrom-Json
$m = Get-Content "$ResultsDir\mbcas.json" | ConvertFrom-Json

Write-Host "Metric       | Native (Standard) | MBCAS (Yours) | Winner"
Write-Host "-------------|-------------------|---------------|-------"

$rpsWin = if ($m.RPS -gt $n.RPS) { "MBCAS" } else { "Native" }
Write-Host "Throughput   | $($n.RPS.ToString("N1")) RPS        | $($m.RPS.ToString("N1")) RPS      | $rpsWin" -ForegroundColor $(if ($rpsWin -eq "MBCAS") { "Green" } else { "Yellow" })

$latWin = if ($m.Latency -lt $n.Latency) { "MBCAS" } else { "Native" }
Write-Host "Latency      | $($n.Latency.ToString("N1")) ms       | $($m.Latency.ToString("N1")) ms     | $latWin" -ForegroundColor $(if ($latWin -eq "MBCAS") { "Green" } else { "Yellow" })

Write-Host "`nInterpretation:"
Write-Host "  Native: Uses static limits. Noise pods can burst and slow down the Victim."
Write-Host "  MBCAS:  Dynamically throttles Noise pods to protect the Victim."