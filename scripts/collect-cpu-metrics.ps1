#!/usr/bin/env pwsh
# CPU Metrics Collection Script
# Collects CPU usage from running consumer pods over a specified period
# Outputs results to a JSON file

param(
    [int]$DurationSeconds = 30,
    [int]$IntervalSeconds = 2,
    [string]$Namespace = "default",
    [string]$LabelSelector = "",  # e.g., "app=consumer" or "mbcas.io/managed=true"
    [string]$OutputFile = "cpu-metrics-$(Get-Date -Format 'yyyyMMdd-HHmmss').json",
    [switch]$AllNamespaces
)

$ErrorActionPreference = "Stop"

function Write-Step { param($msg) Write-Host "`n==> $msg" -ForegroundColor Cyan }
function Write-Success { param($msg) Write-Host "[OK] $msg" -ForegroundColor Green }
function Write-Warn { param($msg) Write-Host "[WARN] $msg" -ForegroundColor Yellow }

Write-Host "================================================================================" -ForegroundColor Magenta
Write-Host "  CPU Metrics Collection" -ForegroundColor Magenta
Write-Host "================================================================================" -ForegroundColor Magenta
Write-Host ""
Write-Host "Configuration:"
Write-Host "  Duration:       ${DurationSeconds}s"
Write-Host "  Interval:       ${IntervalSeconds}s"
Write-Host "  Namespace:      $(if ($AllNamespaces) { 'ALL' } else { $Namespace })"
Write-Host "  Label Selector: $(if ($LabelSelector) { $LabelSelector } else { '(none)' })"
Write-Host "  Output File:    $OutputFile"
Write-Host ""

# ============================================================================
# Initialize metrics collection
# ============================================================================
$metrics = @{
    metadata = @{
        startTime = (Get-Date).ToUniversalTime().ToString("o")
        endTime = $null
        durationSeconds = $DurationSeconds
        intervalSeconds = $IntervalSeconds
        namespace = if ($AllNamespaces) { "all" } else { $Namespace }
        labelSelector = $LabelSelector
        samplesCollected = 0
    }
    pods = @{}
    samples = @()
}

# ============================================================================
# Build kubectl command arguments
# ============================================================================
$kubectlArgs = @("top", "pods", "--no-headers")

if ($AllNamespaces) {
    $kubectlArgs += "-A"
} else {
    $kubectlArgs += @("-n", $Namespace)
}

if ($LabelSelector) {
    $kubectlArgs += @("-l", $LabelSelector)
}

# ============================================================================
# Discover pods first
# ============================================================================
Write-Step "Discovering running pods..."

$getPodArgs = @("get", "pods", "-o", "json")
if ($AllNamespaces) {
    $getPodArgs += "-A"
} else {
    $getPodArgs += @("-n", $Namespace)
}
if ($LabelSelector) {
    $getPodArgs += @("-l", $LabelSelector)
}

$podsJson = kubectl @getPodArgs 2>$null | ConvertFrom-Json

if (-not $podsJson.items -or $podsJson.items.Count -eq 0) {
    Write-Warn "No pods found matching criteria."
    exit 0
}

# Filter to running pods only
$runningPods = $podsJson.items | Where-Object { $_.status.phase -eq "Running" }

Write-Host "Found $($runningPods.Count) running pod(s):"
foreach ($pod in $runningPods) {
    $podKey = "$($pod.metadata.namespace)/$($pod.metadata.name)"
    Write-Host "  - $podKey"
    
    # Store pod metadata
    $metrics.pods[$podKey] = @{
        name = $pod.metadata.name
        namespace = $pod.metadata.namespace
        uid = $pod.metadata.uid
        nodeName = $pod.spec.nodeName
        labels = $pod.metadata.labels
        containers = @($pod.spec.containers | ForEach-Object {
            @{
                name = $_.name
                cpuRequest = if ($_.resources.requests.cpu) { $_.resources.requests.cpu } else { $null }
                cpuLimit = if ($_.resources.limits.cpu) { $_.resources.limits.cpu } else { $null }
            }
        })
        samples = @()
    }
}

# ============================================================================
# Collect metrics over time
# ============================================================================
Write-Step "Collecting CPU metrics for ${DurationSeconds}s (interval: ${IntervalSeconds}s)..."

$startTime = Get-Date
$endTime = $startTime.AddSeconds($DurationSeconds)
$sampleCount = 0

while ((Get-Date) -lt $endTime) {
    $sampleTime = (Get-Date).ToUniversalTime().ToString("o")
    $sampleCount++
    
    Write-Host "  Sample $sampleCount at $(Get-Date -Format 'HH:mm:ss')..." -NoNewline
    
    try {
        # Get current CPU usage via kubectl top
        $topOutput = kubectl @kubectlArgs 2>$null
        
        $sampleData = @{
            timestamp = $sampleTime
            readings = @{}
        }
        
        if ($topOutput) {
            foreach ($line in $topOutput) {
                # Parse kubectl top output
                # Format: NAME CPU MEMORY (or NAMESPACE NAME CPU MEMORY with -A)
                $parts = $line -split '\s+' | Where-Object { $_ }
                
                if ($AllNamespaces -and $parts.Count -ge 3) {
                    $ns = $parts[0]
                    $podName = $parts[1]
                    $cpuUsage = $parts[2]
                    $memUsage = if ($parts.Count -ge 4) { $parts[3] } else { $null }
                    $podKey = "$ns/$podName"
                } elseif ($parts.Count -ge 2) {
                    $podName = $parts[0]
                    $cpuUsage = $parts[1]
                    $memUsage = if ($parts.Count -ge 3) { $parts[2] } else { $null }
                    $podKey = "$Namespace/$podName"
                } else {
                    continue
                }
                
                # Parse CPU value (convert to millicores)
                $cpuMilli = 0
                if ($cpuUsage -match '^(\d+)m$') {
                    $cpuMilli = [int]$Matches[1]
                } elseif ($cpuUsage -match '^(\d+)$') {
                    $cpuMilli = [int]$Matches[1] * 1000
                } elseif ($cpuUsage -match '^(\d+)n$') {
                    $cpuMilli = [int]($Matches[1] / 1000000)
                }
                
                $sampleData.readings[$podKey] = @{
                    cpuRaw = $cpuUsage
                    cpuMillicores = $cpuMilli
                    memoryRaw = $memUsage
                }
                
                # Also append to per-pod samples
                if ($metrics.pods.ContainsKey($podKey)) {
                    $metrics.pods[$podKey].samples += @{
                        timestamp = $sampleTime
                        cpuMillicores = $cpuMilli
                        cpuRaw = $cpuUsage
                    }
                }
            }
        }
        
        $metrics.samples += $sampleData
        Write-Host " $($sampleData.readings.Count) pod(s)" -ForegroundColor Green
        
    } catch {
        Write-Host " ERROR: $($_.Exception.Message)" -ForegroundColor Red
    }
    
    # Wait for next interval (if not past end time)
    $remaining = ($endTime - (Get-Date)).TotalSeconds
    if ($remaining -gt 0) {
        $sleepTime = [Math]::Min($IntervalSeconds, $remaining)
        Start-Sleep -Seconds $sleepTime
    }
}

# ============================================================================
# Compute statistics
# ============================================================================
Write-Step "Computing statistics..."

foreach ($podKey in $metrics.pods.Keys) {
    $podData = $metrics.pods[$podKey]
    $cpuValues = $podData.samples | ForEach-Object { $_.cpuMillicores } | Where-Object { $_ -gt 0 }
    
    if ($cpuValues.Count -gt 0) {
        $podData.statistics = @{
            sampleCount = $cpuValues.Count
            cpuMin = ($cpuValues | Measure-Object -Minimum).Minimum
            cpuMax = ($cpuValues | Measure-Object -Maximum).Maximum
            cpuAvg = [Math]::Round(($cpuValues | Measure-Object -Average).Average, 2)
            cpuSum = ($cpuValues | Measure-Object -Sum).Sum
        }
        
        # Calculate percentiles
        $sorted = $cpuValues | Sort-Object
        $podData.statistics.cpuP50 = $sorted[[Math]::Floor($sorted.Count * 0.5)]
        $podData.statistics.cpuP90 = $sorted[[Math]::Floor($sorted.Count * 0.9)]
        $podData.statistics.cpuP99 = $sorted[[Math]::Floor($sorted.Count * 0.99)]
    } else {
        $podData.statistics = @{
            sampleCount = 0
            error = "No valid CPU readings"
        }
    }
}

# ============================================================================
# Finalize and save
# ============================================================================
$metrics.metadata.endTime = (Get-Date).ToUniversalTime().ToString("o")
$metrics.metadata.samplesCollected = $sampleCount

# Convert to JSON and save
$jsonOutput = $metrics | ConvertTo-Json -Depth 10
$jsonOutput | Out-File -FilePath $OutputFile -Encoding UTF8

Write-Success "Metrics saved to: $OutputFile"

# ============================================================================
# Print summary
# ============================================================================
Write-Host "`n================================================================================" -ForegroundColor Green
Write-Host "  Collection Summary" -ForegroundColor Green
Write-Host "================================================================================" -ForegroundColor Green
Write-Host ""
Write-Host "Total samples: $sampleCount"
Write-Host "Pods monitored: $($metrics.pods.Count)"
Write-Host ""

Write-Host "Per-Pod Statistics (CPU in millicores):" -ForegroundColor Cyan
Write-Host ("-" * 80)
Write-Host ("{0,-40} {1,8} {2,8} {3,8} {4,8}" -f "POD", "MIN", "AVG", "MAX", "P90")
Write-Host ("-" * 80)

foreach ($podKey in $metrics.pods.Keys | Sort-Object) {
    $stats = $metrics.pods[$podKey].statistics
    if ($stats.sampleCount -gt 0) {
        Write-Host ("{0,-40} {1,8} {2,8} {3,8} {4,8}" -f `
            $podKey.Substring([Math]::Max(0, $podKey.Length - 40)), `
            "$($stats.cpuMin)m", `
            "$($stats.cpuAvg)m", `
            "$($stats.cpuMax)m", `
            "$($stats.cpuP90)m")
    } else {
        Write-Host ("{0,-40} {1}" -f $podKey, "(no data)")
    }
}

Write-Host ""
Write-Host "Output file: $OutputFile"
Write-Host ""
