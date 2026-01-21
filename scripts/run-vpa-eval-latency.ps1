param(
    [int]$DurationMinutes = 5,
    [string]$Namespace = "vpa-eval",
    [string]$Context = "minikube-vpa",
    [string]$OutDir = "./results/vpa"
)

$ErrorActionPreference = "Stop"
kubectl config use-context $Context | Out-Null

New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
$csv = Join-Path $OutDir "metrics.csv"
$json = Join-Path $OutDir "metrics.json"

"timestamp,cpu_usage_m,vpa_recommendation_m,latency_p50_ms,latency_p95_ms,restarts" | Set-Content $csv
$samples = @()

kubectl create namespace $Namespace --dry-run=client -o yaml | kubectl apply -f - | Out-Null

@"
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cpu-service
  namespace: $Namespace
spec:
  replicas: 1
  selector:
    matchLabels: { app: cpu-service }
  template:
    metadata:
      labels: { app: cpu-service }
    spec:
      containers:
      - name: app
        image: ghcr.io/jmalloc/echo-server
        ports:
        - containerPort: 8080
        resources:
          requests: { cpu: "100m" }
          limits:   { cpu: "2000m" }
---
apiVersion: autoscaling.k8s.io/v1
kind: VerticalPodAutoscaler
metadata:
  name: cpu-service-vpa
  namespace: $Namespace
spec:
  targetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: cpu-service
  updatePolicy:
    updateMode: Auto
---
apiVersion: v1
kind: Pod
metadata:
  name: load-generator
  namespace: $Namespace
spec:
  containers:
  - name: fortio
    image: fortio/fortio
    command: ["sleep","infinity"]
"@ | kubectl apply -f - | Out-Null

Write-Host "Waiting for deployment and pods..." -ForegroundColor Yellow
kubectl rollout status deployment/cpu-service -n $Namespace | Out-Null
kubectl wait --for=condition=ready pod/load-generator -n $Namespace --timeout=120s | Out-Null

function Generate-Load-And-MeasureLatency {
    # Run fortio inside the load-generator pod targeting the cpu-service
    # -c 50: 50 concurrent connections
    # -t 10s: Run for 10 seconds (sustained load)
    # -json -: Output JSON to stdout
    $outJson = kubectl exec -n $Namespace load-generator -- `
        fortio load -quiet -json - -c 50 -t 10s http://cpu-service:8080

    if ($LASTEXITCODE -ne 0) {
        Write-Warning "Fortio failed to generate load."
        return @{ latency_p50_ms = 0; latency_p95_ms = 0 }
    }

    try {
        $out = $outJson | ConvertFrom-Json
        return @{
            latency_p50_ms = [math]::Round($out.DurationHistogram.Percentiles.P50 * 1000, 2)
            latency_p95_ms = [math]::Round($out.DurationHistogram.Percentiles.P95 * 1000, 2)
        }
    }
    catch {
        Write-Warning "Failed to parse Fortio output"
        return @{ latency_p50_ms = 0; latency_p95_ms = 0 }
    }
}

$end = (Get-Date).AddMinutes($DurationMinutes)
Write-Host "Starting VPA load test for $DurationMinutes minutes..." -ForegroundColor Green

while ((Get-Date) -lt $end) {
    $ts = Get-Date -Format "o"

    # 1. Run Load & Measure Latency (takes ~10s)
    $stats = Generate-Load-And-MeasureLatency

    # 2. Get CPU Usage
    $cpu = kubectl top pod -n $Namespace -l app=cpu-service --no-headers 2>$null |
    ForEach-Object { ($_ -split '\s+')[1].Replace("m", "") }
    if (-not $cpu) { $cpu = 0 }

    # 3. Get VPA Recommendation
    $vpa = kubectl get vpa cpu-service-vpa -n $Namespace -o json 2>$null | ConvertFrom-Json
    $rec = 0
    if ($vpa.status.recommendation.containerRecommendations) {
        $rec = $vpa.status.recommendation.containerRecommendations[0].target.cpu.Replace("m", "")
    }

    # 4. Get Restart Count (Key differentiator!)
    $restarts = kubectl get pod -n $Namespace -l app=cpu-service -o jsonpath="{.items[0].status.containerStatuses[0].restartCount}" 2>$null
    if (-not $restarts) { $restarts = 0 }

    # Log to CSV
    "$ts,$cpu,$rec,$($stats.latency_p50_ms),$($stats.latency_p95_ms),$restarts" | Add-Content $csv

    # Console Output
    Write-Host "[$ts] CPU: ${cpu}m | Rec: ${rec}m | Restarts: $restarts | P50: $($stats.latency_p50_ms)ms" -ForegroundColor Cyan

    $samples += @{
        timestamp            = $ts
        cpu_usage_m          = [int]$cpu
        vpa_recommendation_m = [int]$rec
        latency_p50_ms       = $stats.latency_p50_ms
        latency_p95_ms       = $stats.latency_p95_ms
        restarts             = [int]$restarts
    }
    
    # No sleep needed because Fortio runs for 10s
}

$samples | ConvertTo-Json -Depth 4 | Set-Content $json
Write-Host "VPA results saved to $OutDir" -ForegroundColor Green
