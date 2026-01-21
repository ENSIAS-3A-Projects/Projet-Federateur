param(
    [int]$DurationMinutes = 5,
    [string]$Namespace = "mbcas-eval",
    [string]$Context = "minikube-mbcas",
    [string]$OutDir = "./results/mbcas"
)

$ErrorActionPreference = "Stop"
kubectl config use-context $Context | Out-Null

New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
$csv = Join-Path $OutDir "metrics.csv"
$json = Join-Path $OutDir "metrics.json"

"timestamp,cpu_usage_m,applied_cpu_limit_m,latency_p50_ms,latency_p95_ms" | Set-Content $csv
$samples = @()

kubectl create namespace $Namespace --dry-run=client -o yaml | kubectl apply -f - | Out-Null

@"
apiVersion: v1
kind: Pod
metadata:
  name: cpu-service
  namespace: $Namespace
  labels:
    mbcas.io/managed: "true"
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

Write-Host "Waiting for pods to be ready..." -ForegroundColor Yellow
kubectl wait --for=condition=ready pod --all -n $Namespace --timeout=120s | Out-Null

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
Write-Host "Starting load test for $DurationMinutes minutes..." -ForegroundColor Green

while ((Get-Date) -lt $end) {
    $ts = Get-Date -Format "o"

    # 1. Run Load & Measure Latency (takes ~10s)
    $stats = Generate-Load-And-MeasureLatency

    # 2. Get CPU Usage
    $cpu = kubectl top pod cpu-service -n $Namespace --no-headers 2>$null |
    ForEach-Object { ($_ -split '\s+')[1].Replace("m", "") }
    
    if (-not $cpu) { $cpu = 0 }

    # 3. Get MBCAS Allocation
    $alloc = kubectl get podallocations -n $Namespace -o json 2>$null |
    ConvertFrom-Json |
    Select-Object -ExpandProperty items |
    Where-Object { $_.spec.podName -eq "cpu-service" }

    if ($alloc) {
        $limit = $alloc.status.appliedCPULimit.Replace("m", "")
    }
    else {
        $limit = 0
    }

    # Log to CSV
    "$ts,$cpu,$limit,$($stats.latency_p50_ms),$($stats.latency_p95_ms)" | Add-Content $csv

    # Console Output
    Write-Host "[$ts] CPU: ${cpu}m | Limit: ${limit}m | P50: $($stats.latency_p50_ms)ms | P95: $($stats.latency_p95_ms)ms" -ForegroundColor Cyan

    $samples += @{
        timestamp           = $ts
        cpu_usage_m         = [int]$cpu
        applied_cpu_limit_m = [int]$limit
        latency_p50_ms      = $stats.latency_p50_ms
        latency_p95_ms      = $stats.latency_p95_ms
    }

    # No sleep needed because Fortio runs for 10s, acting as the interval
}

$samples | ConvertTo-Json -Depth 4 | Set-Content $json
Write-Host "MBCAS results saved to $OutDir" -ForegroundColor Green
