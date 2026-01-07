#!/usr/bin/env pwsh
# MBCAS Competitor Test: Native Kubernetes (FORCED CONTENTION)
# Spawns massive load to ensure Node Saturation and force CFS throttling.

param(
    [string]$Namespace = "demo-native-forced",
    [int]$PodCount = 8,
    [int]$WorkersPerPod = 8,  # Increased to 8 (Total 64 threads) to fill 20 cores
    [string]$OutputDir = ".\test-results",
    [switch]$Cleanup
)

$ErrorActionPreference = "Stop"

# --- Helper Functions ---
function Write-Step { param($msg) Write-Host "`n==> $msg" -ForegroundColor Cyan }
function Write-Success { param($msg) Write-Host "[PASS] $msg" -ForegroundColor Green }
function Write-Info { param($msg) Write-Host "[INFO] $msg" -ForegroundColor Yellow }

$timestamp = Get-Date -Format "yyyy-MM-dd_HH-mm-ss"
$testId = "native-forced_$timestamp"
$logFile = "$OutputDir\${testId}_log.txt"
$csvFile = "$OutputDir\${testId}_metrics.csv"
$reportFile = "$OutputDir\${testId}_report.md"

New-Item -ItemType Directory -Force -Path $OutputDir | Out-Null
Start-Transcript -Path $logFile -Append | Out-Null

Write-Host "================================================================================" -ForegroundColor Magenta
Write-Host "  COMPETITOR TEST: Native Kubernetes (Forced Saturation)" -ForegroundColor Magenta
Write-Host "  Test ID: $testId" -ForegroundColor Magenta
Write-Host "================================================================================" -ForegroundColor Magenta

# 1. Check Node Capacity
$nodeCap = (kubectl get nodes -o jsonpath='{.items[0].status.capacity.cpu}').ToString()
# Convert "20" or "20000m" to int
$capacityMilli = 0
if ($nodeCap.EndsWith("m")) { $capacityMilli = [int]$nodeCap.Trim("m") }
else { $capacityMilli = [int]$nodeCap * 1000 }

Write-Info "Node Capacity: ${capacityMilli}m ($($capacityMilli/1000) Cores)"
$totalDemand = $PodCount * $WorkersPerPod * 1000
Write-Info "Target Demand: ${totalDemand}m ($($totalDemand/1000) Cores)"

if ($totalDemand -lt $capacityMilli) {
    Write-Host "WARNING: Demand ($totalDemand) < Capacity ($capacityMilli). This test may not throttle!" -ForegroundColor Red
} else {
    Write-Host "Saturation confirmed: Demand is $([math]::Round($totalDemand/$capacityMilli*100, 1))% of Capacity" -ForegroundColor Green
}

# 2. Setup Namespace
Write-Step "Setting up namespace '$Namespace'..."
kubectl create namespace $Namespace --dry-run=client -o yaml | kubectl apply -f -

# 3. Deploy Pods (Use Bare Pods to match MBCAS test structure)
Write-Step "Deploying $PodCount Native pods..."
Write-Info "Config: Requests=100m, Limits=4000m (Open Limits)"

for ($i=1; $i -le $PodCount; $i++) {
    $name = "worker-$i"
    
    $podYaml = @"
apiVersion: v1
kind: Pod
metadata:
  name: $name
  namespace: $Namespace
  labels:
    app: worker
    mbcas.io/managed: "false"  # Explicitly disable MBCAS
spec:
  restartPolicy: Never
  containers:
  - name: stress
    image: polinux/stress-ng
    # Aggressive load: 8 workers, 10 min timeout
    args: ["--cpu", "$WorkersPerPod", "--timeout", "600s"]
    resources:
      requests:
        cpu: "100m"   # Small guarantee
      limits:
        cpu: "4000m"  # High limit to allow bursting
"@
    $podYaml | kubectl apply -f - | Out-Null
}

Write-Success "Pods created"
Write-Step "Waiting for pods to be ready..."
kubectl wait --for=condition=Ready pod --all -n $Namespace --timeout=120s

# 4. Monitoring Phase
Write-Step "Monitoring CPU Usage (via Metrics Server)..."
Write-Info "Sampling every 15s for 3 minutes..."

# Init CSV
"timestamp,elapsed_sec,pod,usage_milli,requests_milli" | Out-File -FilePath $csvFile -Encoding UTF8

$startTime = Get-Date
$durationSecs = 180 # 3 minutes

for ($i=0; $i -le $durationSecs; $i+=15) {
    Start-Sleep -Seconds 15 
    $now = Get-Date
    $elapsed = ($now - $startTime).TotalSeconds
    
    $metricsRaw = $null
    try {
        $p = Start-Process kubectl -ArgumentList "top pods -n $Namespace --no-headers" -NoNewWindow -PassThru -RedirectStandardOutput "tmp_metrics.txt" -RedirectStandardError "tmp_err.txt" -Wait
        if ($p.ExitCode -eq 0) { $metricsRaw = Get-Content "tmp_metrics.txt" -Raw }
    } catch { }

    if ($metricsRaw) {
        $totalUsage = 0
        $podUsages = @()
        
        $lines = $metricsRaw -split "`n" | Where-Object { $_ -match "\S" }
        foreach ($line in $lines) {
            $parts = $line -split "\s+"
            if ($parts.Count -ge 2) {
                $pName = $parts[0]
                $cpuStr = $parts[1]
                
                $usageVal = 0
                if ($cpuStr.EndsWith("m")) { $usageVal = [int]$cpuStr.Trim("m") }
                elseif ($cpuStr -match "^\d+$") { $usageVal = [int]$cpuStr * 1000 }
                
                $totalUsage += $usageVal
                $podUsages += $usageVal
                
                "$($now.ToString("o")),$([math]::Round($elapsed,1)),$pName,$usageVal,100" | Out-File -FilePath $csvFile -Append -Encoding UTF8
            }
        }
        
        $avg = 0
        if ($podUsages.Count -gt 0) { $avg = [math]::Round($totalUsage / $podUsages.Count) }
        
        $utilization = [math]::Round(($totalUsage / $capacityMilli) * 100, 1)
        Write-Host "  [T+${i}s] Usage: ${totalUsage}m (${utilization}%) | Avg/Pod: ${avg}m" -ForegroundColor Gray
    }
}

Remove-Item "tmp_metrics.txt" -ErrorAction SilentlyContinue
Remove-Item "tmp_err.txt" -ErrorAction SilentlyContinue

# 5. Calculate Fairness
Write-Step "Calculating Metrics..."

$n = $podUsages.Count
$sum = 0
$sumSq = 0
$podUsages | ForEach-Object { $sum += $_; $sumSq += ($_ * $_) }

$jains = 0
if ($sumSq -gt 0) { $jains = ($sum * $sum) / ($n * $sumSq) }

Write-Host "`n---------------------------------------------------"
Write-Host "  NATIVE K8s (FORCED) RESULTS" -ForegroundColor Magenta
Write-Host "---------------------------------------------------"
Write-Host "  Saturation Level:      $([math]::Round(($totalUsage / $capacityMilli) * 100, 1))%" -ForegroundColor Cyan
Write-Host "  Jain's Fairness Index: $([math]::Round($jains, 4))" -ForegroundColor Yellow
Write-Host "  Total Usage:           ${totalUsage}m / ${capacityMilli}m"

$report = @"
# Native Kubernetes (Forced Saturation) Report
**Test ID:** $testId
**Saturation:** $([math]::Round(($totalUsage / $capacityMilli) * 100, 1))%
**Jain's Fairness Index:** $([math]::Round($jains, 4))

## Config
- **Capacity:** ${capacityMilli}m
- **Load:** 8 pods x 8 workers
- **Requests:** 100m (Equal)
"@
$report | Out-File -FilePath $reportFile -Encoding UTF8

if ($Cleanup) {
    Write-Step "Cleaning up..."
    kubectl delete namespace $Namespace --ignore-not-found
}

Stop-Transcript