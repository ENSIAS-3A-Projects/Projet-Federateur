# MBCAS Demo Script 06 - Before/After Comparison
# Captures metrics before and after MBCAS enablement for comparison
#
# Usage: .\scripts\06-compare-metrics.ps1 [-Duration 120]

param(
    [int]$Duration = 120,
    [string]$OutputDir = ".\demo-results"
)

$ErrorActionPreference = "Stop"
$NAMESPACE = "urbanmove"

function Write-Info { Write-Host "[INFO] $args" -ForegroundColor Green }
function Write-Warn { Write-Host "[WARN] $args" -ForegroundColor Yellow }

# Create output directory
if (-not (Test-Path $OutputDir)) {
    New-Item -ItemType Directory -Path $OutputDir -Force | Out-Null
}

$timestamp = Get-Date -Format "yyyyMMdd-HHmmss"
$beforeFile = Join-Path $OutputDir "before-$timestamp.json"
$afterFile = Join-Path $OutputDir "after-$timestamp.json"
$reportFile = Join-Path $OutputDir "comparison-$timestamp.txt"

function Capture-Metrics {
    param([string]$Label)
    
    Write-Info "Capturing $Label metrics..."
    
    $metrics = @{
        timestamp = (Get-Date).ToString("o")
        label = $Label
        pods = @()
    }
    
    # Get pod resource usage
    $topOutput = kubectl top pods -n $NAMESPACE --no-headers 2>$null
    if ($topOutput) {
        foreach ($line in $topOutput) {
            $parts = $line -split '\\s+'
            if ($parts.Count -ge 3) {
                $metrics.pods += @{
                    name = $parts[0]
                    cpuUsage = $parts[1]
                    memUsage = $parts[2]
                }
            }
        }
    }
    
    # Get pod specs
    $podJson = kubectl get pods -n $NAMESPACE -o json 2>$null | ConvertFrom-Json
    foreach ($pod in $podJson.items) {
        $existing = $metrics.pods | Where-Object { $_.name -eq $pod.metadata.name }
        if ($existing) {
            $container = $pod.spec.containers[0]
            $existing.cpuRequest = $container.resources.requests.cpu
            $existing.cpuLimit = $container.resources.limits.cpu
        }
    }
    
    # Get throttling info from MBCAS if available
    $allocations = kubectl get podallocations -n $NAMESPACE -o json 2>$null | ConvertFrom-Json
    if ($allocations.items) {
        foreach ($pa in $allocations.items) {
            $existing = $metrics.pods | Where-Object { $_.name -eq $pa.spec.podName }
            if ($existing) {
                $existing.desiredCPU = $pa.spec.desiredCPULimit
                $existing.appliedCPU = $pa.status.appliedCPULimit
                $existing.phase = $pa.status.phase
            }
        }
    }
    
    return $metrics
}

function Calculate-Stats {
    param($Metrics)
    
    $stats = @{
        totalCpuUsage = 0
        totalCpuLimit = 0
        podsThrottled = 0
        avgUtilization = 0
    }
    
    foreach ($pod in $Metrics.pods) {
        $usage = [int]($pod.cpuUsage -replace 'm', '')
        $limit = [int]($pod.cpuLimit -replace 'm', '')
        
        $stats.totalCpuUsage += $usage
        $stats.totalCpuLimit += $limit
        
        if ($limit -gt 0 -and $usage -ge ($limit * 0.9)) {
            $stats.podsThrottled++
        }
    }
    
    if ($stats.totalCpuLimit -gt 0) {
        $stats.avgUtilization = [math]::Round($stats.totalCpuUsage / $stats.totalCpuLimit * 100, 1)
    }
    
    return $stats
}

function Generate-Report {
    param($Before, $After, $BeforeStats, $AfterStats)
    
    $report = @"
================================================================================
                     MBCAS DEMO - BEFORE/AFTER COMPARISON
================================================================================

Generated: $(Get-Date)
Namespace: $NAMESPACE
Test Duration: $Duration seconds

--------------------------------------------------------------------------------
                              SUMMARY METRICS
--------------------------------------------------------------------------------

                              BEFORE          AFTER           CHANGE
Total CPU Usage:              $($BeforeStats.totalCpuUsage)m            $($AfterStats.totalCpuUsage)m            $(($AfterStats.totalCpuUsage - $BeforeStats.totalCpuUsage))m
Total CPU Limit:              $($BeforeStats.totalCpuLimit)m            $($AfterStats.totalCpuLimit)m            $(($AfterStats.totalCpuLimit - $BeforeStats.totalCpuLimit))m
Pods Near Throttle (>90%):    $($BeforeStats.podsThrottled)               $($AfterStats.podsThrottled)               $(($AfterStats.podsThrottled - $BeforeStats.podsThrottled))
Avg Utilization:              $($BeforeStats.avgUtilization)%            $($AfterStats.avgUtilization)%            $(($AfterStats.avgUtilization - $BeforeStats.avgUtilization))%

--------------------------------------------------------------------------------
                              PER-POD DETAILS
--------------------------------------------------------------------------------

"@
    
    $report += "`nBEFORE (Static Limits):`n"
    $report += "-" * 80 + "`n"
    $report += "{0,-35} {1,10} {2,10} {3,10}`n" -f "POD", "USAGE", "LIMIT", "UTIL%"
    
    foreach ($pod in $Before.pods) {
        $usage = [int]($pod.cpuUsage -replace 'm', '')
        $limit = [int]($pod.cpuLimit -replace 'm', '')
        $util = if ($limit -gt 0) { [math]::Round($usage / $limit * 100, 1) } else { 0 }
        $report += "{0,-35} {1,10} {2,10} {3,10}`n" -f $pod.name.Substring(0, [Math]::Min(35, $pod.name.Length)), "$($usage)m", "$($limit)m", "$util%"
    }
    
    $report += "`nAFTER (MBCAS Dynamic Limits):`n"
    $report += "-" * 80 + "`n"
    $report += "{0,-35} {1,10} {2,10} {3,10} {4,10}`n" -f "POD", "USAGE", "LIMIT", "UTIL%", "MBCAS"
    
    foreach ($pod in $After.pods) {
        $usage = [int]($pod.cpuUsage -replace 'm', '')
        $limit = [int]($pod.cpuLimit -replace 'm', '')
        $util = if ($limit -gt 0) { [math]::Round($usage / $limit * 100, 1) } else { 0 }
        $mbcas = if ($pod.appliedCPU) { $pod.appliedCPU } else { "-" }
        $report += "{0,-35} {1,10} {2,10} {3,10} {4,10}`n" -f $pod.name.Substring(0, [Math]::Min(35, $pod.name.Length)), "$($usage)m", "$($limit)m", "$util%", $mbcas
    }
    
    # Build interpretation lines
    $interp1 = if ($AfterStats.podsThrottled -lt $BeforeStats.podsThrottled) {
        "[OK] IMPROVEMENT: Fewer pods at throttle threshold ($($BeforeStats.podsThrottled) -> $($AfterStats.podsThrottled))"
    } else {
        "[-] No change in throttling"
    }

    $interp2 = if ($AfterStats.totalCpuLimit -ne $BeforeStats.totalCpuLimit) {
        "[OK] MBCAS adjusted total limits ($($BeforeStats.totalCpuLimit)m -> $($AfterStats.totalCpuLimit)m)"
    } else {
        "[-] Limits unchanged"
    }

    $appliedCount = ($After.pods | Where-Object { $_.phase -eq "Applied" }).Count
    $interp3 = if ($appliedCount -gt 0) {
        "[OK] MBCAS actively managing $appliedCount pods"
    } else {
        "[-] MBCAS not yet applied allocations"
    }

    $report += @"

--------------------------------------------------------------------------------
                              INTERPRETATION
--------------------------------------------------------------------------------

$interp1

$interp2

$interp3

================================================================================
                                  END REPORT
================================================================================
"@
    
    return $report
}

# Main execution
Write-Host ""
Write-Host "MBCAS Before/After Comparison" -ForegroundColor Cyan
Write-Host "=============================" -ForegroundColor Cyan
Write-Host ""

# Phase 1: Disable MBCAS, capture baseline
Write-Info "Phase 1: Capturing BEFORE metrics (static Kubernetes limits)"
Write-Info "Scaling down MBCAS controller..."
kubectl scale deployment mbcas-controller -n mbcas-system --replicas=0 2>$null

Write-Info "Waiting for metrics to stabilize (30s)..."
Start-Sleep -Seconds 30

$beforeMetrics = Capture-Metrics -Label "before"
$beforeMetrics | ConvertTo-Json -Depth 5 | Set-Content $beforeFile
Write-Info "Saved to $beforeFile"

# Phase 2: Enable MBCAS
Write-Info "Phase 2: Enabling MBCAS..."
kubectl scale deployment mbcas-controller -n mbcas-system --replicas=1 2>$null

Write-Info "Waiting for MBCAS to converge ($Duration seconds)..."
Start-Sleep -Seconds $Duration

# Phase 3: Capture after metrics
Write-Info "Phase 3: Capturing AFTER metrics (MBCAS dynamic limits)"
$afterMetrics = Capture-Metrics -Label "after"
$afterMetrics | ConvertTo-Json -Depth 5 | Set-Content $afterFile
Write-Info "Saved to $afterFile"

# Generate comparison
$beforeStats = Calculate-Stats -Metrics $beforeMetrics
$afterStats = Calculate-Stats -Metrics $afterMetrics

$report = Generate-Report -Before $beforeMetrics -After $afterMetrics -BeforeStats $beforeStats -AfterStats $afterStats

$report | Set-Content $reportFile
Write-Host ""
Write-Host $report
Write-Host ""
Write-Info "Report saved to $reportFile"
