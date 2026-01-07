#!/usr/bin/env pwsh
# MBCAS Forced Contention Test
# Spawns massive load (64 threads) to guarantee Node Saturation (>100% Demand).

param(
    [string]$Namespace = "demo-forced",
    [int]$PodCount = 8,
    [int]$WorkersPerPod = 8,  # 8 pods * 8 workers = 64 threads (vs 20 cores)
    [string]$OutputDir = ".\test-results",
    [switch]$Cleanup
)

$ErrorActionPreference = "Stop"

# --- Helper Functions ---
function Write-Step { param($msg) Write-Host "`n==> $msg" -ForegroundColor Cyan }
function Write-Success { param($msg) Write-Host "[PASS] $msg" -ForegroundColor Green }
function Write-Info { param($msg) Write-Host "[INFO] $msg" -ForegroundColor Yellow }

$timestamp = Get-Date -Format "yyyy-MM-dd_HH-mm-ss"
$testId = "mbcas-forced_$timestamp"
$logFile = "$OutputDir\${testId}_log.txt"
$csvFile = "$OutputDir\${testId}_allocations.csv"
$reportFile = "$OutputDir\${testId}_report.md"

New-Item -ItemType Directory -Force -Path $OutputDir | Out-Null
Start-Transcript -Path $logFile -Append | Out-Null

Write-Host "================================================================================" -ForegroundColor Magenta
Write-Host "  MBCAS FORCED CONTENTION TEST (High Saturation)" -ForegroundColor Magenta
Write-Host "  Test ID: $testId" -ForegroundColor Magenta
Write-Host "================================================================================" -ForegroundColor Magenta

# 1. Verify MBCAS is Running
$pods = kubectl get pods -n mbcas-system -o name
if (-not $pods) {
    Write-Host "ERROR: MBCAS is not running!" -ForegroundColor Red
    exit 1
}

# 2. Check Capacity & Demand
$nodeCap = (kubectl get nodes -o jsonpath='{.items[0].status.capacity.cpu}').ToString()
$capacityMilli = 0
if ($nodeCap.EndsWith("m")) { $capacityMilli = [int]$nodeCap.Trim("m") }
else { $capacityMilli = [int]$nodeCap * 1000 }

$totalDemand = $PodCount * $WorkersPerPod * 1000 # stress-ng 1 worker ~= 1000m
$contentionRatio = [math]::Round($totalDemand / $capacityMilli, 2)

Write-Info "Node Capacity: ${capacityMilli}m"
Write-Info "Test Demand:   ${totalDemand}m"
if ($contentionRatio -lt 1.0) {
    Write-Host "WARNING: Contention Ratio is only ${contentionRatio}x. This might not force throttling." -ForegroundColor Yellow
} else {
    Write-Host "Saturation Level: ${contentionRatio}x (Severe Contention)" -ForegroundColor Green
}

# 3. Deploy Test Pods
Write-Step "Setting up namespace '$Namespace'..."
$nsYaml = @"
apiVersion: v1
kind: Namespace
metadata:
  name: $Namespace
  labels:
    mbcas.io/managed: "true" # ENABLE MBCAS
"@
$nsYaml | kubectl apply -f - | Out-Null

Write-Step "Deploying $PodCount MBCAS-managed pods..."

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
spec:
  restartPolicy: Never
  containers:
  - name: stress
    image: polinux/stress-ng
    # Massive load: 8 workers
    args: ["--cpu", "$WorkersPerPod", "--timeout", "300s"]
    resources:
      requests:
        cpu: "100m"
      limits:
        cpu: "4000m" # Allow bursting if space exists
"@
    $podYaml | kubectl apply -f - | Out-Null
}

Write-Success "Pods deployed"
Write-Step "Waiting for pods to be ready..."
kubectl wait --for=condition=Ready pod --all -n $Namespace --timeout=120s

# 4. Monitoring Phase
Write-Step "Monitoring Allocations (3 minutes)..."
Write-Info "Capturing PodAllocations every 15s..."

"timestamp,elapsed_sec,pod,applied_cpu_milli" | Out-File -FilePath $csvFile -Encoding UTF8

$startTime = Get-Date
$durationSecs = 180

for ($i=0; $i -le $durationSecs; $i+=15) {
    Start-Sleep -Seconds 15
    $now = Get-Date
    $elapsed = ($now - $startTime).TotalSeconds
    
    $paJson = kubectl get podallocations -n $Namespace -o json 2>$null | ConvertFrom-Json
    
    $allocs = @()
    if ($paJson.items) {
        foreach ($pa in $paJson.items) {
            $pName = $pa.spec.podName
            $appliedStr = $pa.status.appliedCPULimit
            
            $appliedVal = 0
            if ($appliedStr -match "(\d+)m") { $appliedVal = [int]$Matches[1] }
            elseif ($appliedStr -match "^(\d+)$") { $appliedVal = [int]$Matches[1] * 1000 }
            
            $allocs += $appliedVal
            "$($now.ToString("o")),$([math]::Round($elapsed,1)),$pName,$appliedVal" | Out-File -FilePath $csvFile -Append -Encoding UTF8
        }
    }
    
    # Calculate Live Fairness
    $avg = 0
    if ($allocs.Count -gt 0) {
        $total = ($allocs | Measure-Object -Sum).Sum
        $avg = [math]::Round($total / $allocs.Count)
        $jains = 0
        $sumSq = ($allocs | ForEach-Object { $_ * $_ } | Measure-Object -Sum).Sum
        if ($sumSq -gt 0) { $jains = ($total * $total) / ($allocs.Count * $sumSq) }
        
        Write-Host "  [T+${i}s] Avg Alloc: ${avg}m | Fairness: $([math]::Round($jains, 4))" -ForegroundColor Gray
    }
}

# 5. Final Report
Write-Step "Generating Report..."

$allocs = @() # Get final snapshot
$paJson = kubectl get podallocations -n $Namespace -o json 2>$null | ConvertFrom-Json
if ($paJson.items) {
    foreach ($pa in $paJson.items) {
        $appliedStr = $pa.status.appliedCPULimit
        $val = 0
        if ($appliedStr -match "(\d+)m") { $val = [int]$Matches[1] }
        $allocs += $val
    }
}

$n = $allocs.Count
$sum = ($allocs | Measure-Object -Sum).Sum
$sumSq = ($allocs | ForEach-Object { $_ * $_ } | Measure-Object -Sum).Sum
$jains = 0
if ($sumSq -gt 0) { $jains = ($sum * $sum) / ($n * $sumSq) }

Write-Host "`n---------------------------------------------------"
Write-Host "  MBCAS (FORCED) RESULTS" -ForegroundColor Magenta
Write-Host "---------------------------------------------------"
Write-Host "  Contention Ratio:      ${contentionRatio}x" -ForegroundColor Cyan
Write-Host "  Jain's Fairness Index: $([math]::Round($jains, 4))" -ForegroundColor Yellow
Write-Host "  Total Allocated:       ${sum}m / ${capacityMilli}m"

$report = @"
# MBCAS Forced Contention Report
**Test ID:** $testId
**Contention Ratio:** ${contentionRatio}x
**Jain's Fairness Index:** $([math]::Round($jains, 4))

## Config
- **Capacity:** ${capacityMilli}m
- **Load:** 8 pods x 8 workers (${totalDemand}m)
- **Result:** Forced contention to test true fairness.
"@
$report | Out-File -FilePath $reportFile -Encoding UTF8

if ($Cleanup) {
    Write-Step "Cleaning up..."
    kubectl delete namespace $Namespace --ignore-not-found
}

Stop-Transcript