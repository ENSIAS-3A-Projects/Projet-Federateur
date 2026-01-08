# monitor.ps1
# Automated monitoring module that collects metrics during load tests
# Collects: Prometheus metrics, Kubernetes metrics, PodAllocation status

param(
    [Parameter(Mandatory=$true)]
    [string]$OutputDir,
    
    [Parameter(Mandatory=$true)]
    [int]$DurationSeconds,
    
    [Parameter(Mandatory=$false)]
    [int]$IntervalSeconds = 5,
    
    [Parameter(Mandatory=$false)]
    [string]$Namespace = "evaluation",
    
    [Parameter(Mandatory=$false)]
    [int]$WarmupDelaySeconds = 0,
    
    [Parameter(Mandatory=$false)]
    [int]$MetricsServerReadySamples = 3
)

$ErrorActionPreference = "Stop"

# Debug logging configuration
$DebugLogPath = Join-Path $PSScriptRoot "..\.cursor\debug.log"
function Write-DebugLog {
    param(
        [string]$sessionId = "debug-session",
        [string]$runId = "run1",
        [string]$hypothesisId = "",
        [string]$location = "",
        [string]$message = "",
        [hashtable]$data = @{}
    )
    try {
        $logEntry = @{
            sessionId = $sessionId
            runId = $runId
            hypothesisId = $hypothesisId
            location = $location
            message = $message
            data = $data
            timestamp = [DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds()
        } | ConvertTo-Json -Compress -Depth 10
        Add-Content -Path $DebugLogPath -Value $logEntry -ErrorAction SilentlyContinue
    } catch {
        # Silently fail to avoid breaking the script
    }
}

function Write-Color($text, $color) {
    # Validate color and provide fallback
    $validColors = @("Black", "DarkBlue", "DarkGreen", "DarkCyan", "DarkRed", "DarkMagenta", 
                     "DarkYellow", "Gray", "DarkGray", "Blue", "Green", "Cyan", 
                     "Red", "Magenta", "Yellow", "White")
    
    if ($color -and $validColors -contains $color) {
        Write-Host $text -ForegroundColor $color
    } else {
        # Fallback to default color if invalid
        Write-Host $text
    }
}

# Create output directory
if (-not (Test-Path $OutputDir)) {
    New-Item -ItemType Directory -Path $OutputDir -Force | Out-Null
}

$metricsFile = Join-Path $OutputDir "metrics.json"
$csvFile = Join-Path $OutputDir "metrics.csv"
$podAllocationsFile = Join-Path $OutputDir "podallocations.json"

Write-Color "Starting monitoring for $DurationSeconds seconds..." "Cyan"
Write-Color "   Output directory: $OutputDir" "Gray"
Write-Color "   Collection interval: $IntervalSeconds seconds" "Gray"

# Use UTC for all timestamps to ensure integrity
$startTime = [DateTimeOffset]::UtcNow
$endTime = $startTime.AddSeconds($DurationSeconds)
$metrics = @()
$podAllocationHistory = @()

# Function to collect metrics snapshot
function Collect-Metrics {
    # Use true UTC time (not local time with Z suffix)
    $timestamp = [DateTimeOffset]::UtcNow.ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
    $snapshot = @{
        timestamp = $timestamp
        pods = @()
        podallocations = @()
        mbcas_components = @()
    }
    
    # Collect pod metrics
    try {
        $pods = kubectl get pods -n $Namespace -o json 2>$null | ConvertFrom-Json
        foreach ($pod in $pods.items) {
            $podMetrics = @{
                name = $pod.metadata.name
                namespace = $pod.metadata.namespace
                phase = $pod.status.phase
                restartCount = 0
                cpu_request = ""
                cpu_limit = ""
                cpu_usage = ""
                memory_usage = ""
            }
            
            # Get restart count
            if ($pod.status.containerStatuses) {
                foreach ($container in $pod.status.containerStatuses) {
                    $podMetrics.restartCount += $container.restartCount
                }
            }
            
            # Get resource requests/limits
            if ($pod.spec.containers) {
                foreach ($container in $pod.spec.containers) {
                    if ($container.resources.requests.cpu) {
                        $podMetrics.cpu_request = $container.resources.requests.cpu
                    }
                    if ($container.resources.limits.cpu) {
                        $podMetrics.cpu_limit = $container.resources.limits.cpu
                    }
                }
            }
            
            # Get actual usage from metrics-server (if available)
            # #region agent log
            Write-DebugLog -location "monitor.ps1:100" -message "Attempting to get pod usage metrics" -hypothesisId "A,B,C,D,E,F" -data @{
                podName = $pod.metadata.name
                namespace = $Namespace
                phase = $pod.status.phase
            }
            # #endregion
            
            # Try multiple methods to get metrics, with retries
            $maxRetries = 2
            $retryDelay = 1
            $usage = $null
            $exitCode = 0
            
            for ($retry = 0; $retry -le $maxRetries; $retry++) {
                try {
                    if ($retry -gt 0) {
                        Start-Sleep -Seconds $retryDelay
                    }
                    
                    # #region agent log
                    Write-DebugLog -location "monitor.ps1:101" -message "Before kubectl top command (attempt $($retry + 1))" -hypothesisId "A,B,C" -data @{
                        podName = $pod.metadata.name
                        namespace = $Namespace
                        command = "kubectl top pod $($pod.metadata.name) -n $Namespace --no-headers"
                        retry = $retry
                    }
                    # #endregion
                    
                    # Method 1: Try using Invoke-Expression with proper error handling
                    try {
                        $usageOutput = kubectl top pod $pod.metadata.name -n $Namespace --no-headers 2>&1
                        $exitCode = $LASTEXITCODE
                        
                        # Check if output contains error messages
                        if ($usageOutput -is [System.Array]) {
                            $usageOutput = $usageOutput -join "`n"
                        }
                        
                        # Check if it's an error (starts with "Error" or contains common error patterns)
                        if ($exitCode -eq 0 -and $usageOutput -and -not ($usageOutput -match "^(Error|WARNING|unable to)")) {
                            $usage = $usageOutput.Trim()
                        } elseif ($exitCode -ne 0) {
                            # #region agent log
                            Write-DebugLog -location "monitor.ps1:102a" -message "kubectl top failed with exit code $exitCode" -hypothesisId "A,B,C,E,F" -data @{
                                podName = $pod.metadata.name
                                exitCode = $exitCode
                                output = $usageOutput
                                retry = $retry
                            }
                            # #endregion
                        }
                    } catch {
                        # #region agent log
                        Write-DebugLog -location "monitor.ps1:102b" -message "Exception in kubectl top call" -hypothesisId "A,B,C,E,F" -data @{
                            podName = $pod.metadata.name
                            errorMessage = $_.Exception.Message
                            retry = $retry
                        }
                        # #endregion
                    }
                    
                    # If we got valid output, break out of retry loop
                    if ($usage -and $usage.Length -gt 0) {
                        break
                    }
                } catch {
                    # #region agent log
                    Write-DebugLog -location "monitor.ps1:109" -message "Exception caught in metrics collection (retry $retry)" -hypothesisId "A,B,C,E,F" -data @{
                        podName = $pod.metadata.name
                        errorMessage = $_.Exception.Message
                        errorType = $_.Exception.GetType().FullName
                        retry = $retry
                    }
                    # #endregion
                }
            }
            
            # Parse the usage output
            if ($usage) {
                # #region agent log
                Write-DebugLog -location "monitor.ps1:104" -message "Parsing kubectl top output" -hypothesisId "D" -data @{
                    podName = $pod.metadata.name
                    rawOutput = $usage
                    outputLength = $usage.Length
                }
                # #endregion
                
                # Split by whitespace and filter out empty strings
                $parts = $usage -split '\s+' | Where-Object { $_ -ne '' }
                
                # #region agent log
                Write-DebugLog -location "monitor.ps1:105" -message "After splitting output" -hypothesisId "D" -data @{
                    podName = $pod.metadata.name
                    partsCount = $parts.Length
                    parts = $parts
                }
                # #endregion
                
                # Expected format: POD_NAME CPU MEMORY (or just CPU MEMORY if --no-headers)
                # Parts[0] should be pod name (or first part), Parts[1] should be CPU, Parts[2] should be memory
                if ($parts.Length -ge 2) {
                    # Find CPU and memory - they could be in different positions
                    $cpuIndex = -1
                    $memoryIndex = -1
                    
                    for ($i = 0; $i -lt $parts.Length; $i++) {
                        $part = $parts[$i]
                        # CPU format: "123m" or "1" or "0.5"
                        if ($cpuIndex -eq -1 -and ($part -match '^[\d.]+m?$' -or $part -match '^\d+$')) {
                            $cpuIndex = $i
                        }
                        # Memory format: "123Mi" or "123Ki" or "123Gi"
                        elseif ($memoryIndex -eq -1 -and ($part -match '^\d+[KMGT]i?$' -or $part -match '^\d+$')) {
                            $memoryIndex = $i
                        }
                    }
                    
                    # If we found CPU at index 1 and memory at index 2 (standard format)
                    if ($cpuIndex -eq 1 -and $memoryIndex -eq 2) {
                        $podMetrics.cpu_usage = $parts[1].Trim()
                        $podMetrics.memory_usage = $parts[2].Trim()
                    }
                    # If we found them in other positions
                    elseif ($cpuIndex -ge 0 -and $memoryIndex -ge 0) {
                        $podMetrics.cpu_usage = $parts[$cpuIndex].Trim()
                        $podMetrics.memory_usage = $parts[$memoryIndex].Trim()
                    }
                    # Fallback: assume standard format
                    elseif ($parts.Length -ge 3) {
                        $podMetrics.cpu_usage = $parts[1].Trim()
                        $podMetrics.memory_usage = $parts[2].Trim()
                    }
                    # Only CPU available
                    elseif ($parts.Length -ge 2) {
                        $podMetrics.cpu_usage = $parts[1].Trim()
                    }
                    
                    # Validate that we got non-empty values
                    if ([string]::IsNullOrWhiteSpace($podMetrics.cpu_usage)) {
                        $podMetrics.cpu_usage = ""
                    }
                    if ([string]::IsNullOrWhiteSpace($podMetrics.memory_usage)) {
                        $podMetrics.memory_usage = ""
                    }
                    
                    # #region agent log
                    Write-DebugLog -location "monitor.ps1:106" -message "Parsed metrics result" -hypothesisId "D" -data @{
                        podName = $pod.metadata.name
                        cpu_usage = $podMetrics.cpu_usage
                        memory_usage = $podMetrics.memory_usage
                        cpuIndex = $cpuIndex
                        memoryIndex = $memoryIndex
                    }
                    # #endregion
                } else {
                    # #region agent log
                    Write-DebugLog -location "monitor.ps1:107" -message "Output parsing failed - insufficient parts" -hypothesisId "D" -data @{
                        podName = $pod.metadata.name
                        partsCount = $parts.Length
                        rawOutput = $usage
                    }
                    # #endregion
                }
            } else {
                # #region agent log
                Write-DebugLog -location "monitor.ps1:108" -message "kubectl top returned empty output after $($maxRetries + 1) attempts" -hypothesisId "A,B,C,E,F" -data @{
                    podName = $pod.metadata.name
                    exitCode = $exitCode
                }
                # #endregion
            }
            
            $snapshot.pods += $podMetrics
        }
    } catch {
        Write-Color "  WARNING: Error collecting pod metrics: $_" "Yellow"
    }
    
    # Collect PodAllocation CRDs
    try {
        $allocations = kubectl get podallocations -n $Namespace -o json 2>$null | ConvertFrom-Json
        foreach ($alloc in $allocations.items) {
            $allocSnapshot = @{
                name = $alloc.metadata.name
                namespace = $alloc.spec.namespace
                podName = $alloc.spec.podName
                desiredCPURequest = $alloc.spec.desiredCPURequest
                desiredCPULimit = $alloc.spec.desiredCPULimit
                appliedCPURequest = $alloc.status.appliedCPURequest
                appliedCPULimit = $alloc.status.appliedCPULimit
                phase = $alloc.status.phase
                lastAppliedTime = $alloc.status.lastAppliedTime
                lastAttemptTime = $alloc.status.lastAttemptTime
            }
            $snapshot.podallocations += $allocSnapshot
        }
        
        # Store in history
        $podAllocationHistory += @{
            timestamp = $timestamp
            allocations = $snapshot.podallocations
        }
    } catch {
        # PodAllocations may not exist in baseline test
    }
    
    # Collect MBCAS component metrics (if MBCAS is deployed)
    try {
        $agentPods = kubectl get pods -n mbcas-system -l app.kubernetes.io/component=agent -o json 2>$null | ConvertFrom-Json
        $controllerPods = kubectl get pods -n mbcas-system -l app.kubernetes.io/component=controller -o json 2>$null | ConvertFrom-Json
        
        foreach ($pod in $agentPods.items) {
            try {
                $usage = kubectl top pod $pod.metadata.name -n mbcas-system --no-headers 2>$null
                if ($usage) {
                    $parts = $usage -split '\s+'
                    $snapshot.mbcas_components += @{
                        name = $pod.metadata.name
                        component = "agent"
                        cpu_usage = if ($parts.Length -ge 2) { $parts[1] } else { "" }
                        memory_usage = if ($parts.Length -ge 3) { $parts[2] } else { "" }
                    }
                }
            } catch { }
        }
        
        foreach ($pod in $controllerPods.items) {
            try {
                $usage = kubectl top pod $pod.metadata.name -n mbcas-system --no-headers 2>$null
                if ($usage) {
                    $parts = $usage -split '\s+'
                    $snapshot.mbcas_components += @{
                        name = $pod.metadata.name
                        component = "controller"
                        cpu_usage = if ($parts.Length -ge 2) { $parts[1] } else { "" }
                        memory_usage = if ($parts.Length -ge 3) { $parts[2] } else { "" }
                    }
                }
            } catch { }
        }
    } catch {
        # MBCAS may not be deployed in baseline test
    }
    
    return $snapshot
}

# Function to check if metrics-server is ready by verifying non-empty CPU/memory usage
function Wait-ForMetricsServerReady {
    param(
        [string]$Namespace,
        [int]$RequiredSamples = 3,
        [int]$CheckIntervalSeconds = 5,
        [int]$MaxWaitSeconds = 120
    )
    
    Write-Color "Waiting for metrics-server to be ready..." "Cyan"
    Write-Color "   Checking for $RequiredSamples consecutive samples with non-empty CPU/memory usage" "Gray"
    
    $startWait = [DateTimeOffset]::UtcNow
    $consecutiveGoodSamples = 0
    $sampleCount = 0
    
    while ($true) {
        $elapsed = ([DateTimeOffset]::UtcNow - $startWait).TotalSeconds
        if ($elapsed -gt $MaxWaitSeconds) {
            Write-Color "  WARNING: Timeout waiting for metrics-server readiness after $MaxWaitSeconds seconds" "Yellow"
            return $false
        }
        
        $sampleCount++
        $allPodsHaveMetrics = $true
        
        try {
            $pods = kubectl get pods -n $Namespace -o json 2>$null | ConvertFrom-Json
            if (-not $pods -or -not $pods.items) {
                Write-Color "  Sample ${sampleCount}: No pods found, waiting..." "Gray"
                Start-Sleep -Seconds $CheckIntervalSeconds
                continue
            }
            
            $podsWithMetrics = 0
            $totalPods = $pods.items.Count
            
            foreach ($pod in $pods.items) {
                if ($pod.status.phase -ne "Running") {
                    continue
                }
                
                try {
                    $usageOutput = kubectl top pod $pod.metadata.name -n $Namespace --no-headers 2>&1
                    $topExitCode = $LASTEXITCODE
                    
                    if ($usageOutput -is [System.Array]) {
                        $usageOutput = $usageOutput -join "`n"
                    }
                    
                    $usage = $null
                    if ($topExitCode -eq 0 -and $usageOutput -and -not ($usageOutput -match "^(Error|WARNING|unable to)")) {
                        $usage = $usageOutput.Trim()
                    }
                    
                    if ($usage) {
                        $parts = ($usage -split '\s+') | Where-Object { $_ -ne '' }
                        if ($parts.Length -ge 2 -and -not [string]::IsNullOrWhiteSpace($parts[1])) {
                            # CPU usage is non-empty
                            if ($parts.Length -ge 3 -and -not [string]::IsNullOrWhiteSpace($parts[2])) {
                                # Memory usage is also non-empty
                                $podsWithMetrics++
                            }
                        }
                    }
                } catch {
                    # Pod might not have metrics yet
                }
            }
            
            if ($totalPods -gt 0 -and $podsWithMetrics -eq $totalPods) {
                $consecutiveGoodSamples++
                Write-Color "  Sample ${sampleCount}: All $totalPods pods have metrics ($consecutiveGoodSamples/$RequiredSamples consecutive)" "Green"
                
                if ($consecutiveGoodSamples -ge $RequiredSamples) {
                    Write-Color "OK: Metrics-server is ready!" "Green"
                    return $true
                }
            } else {
                $consecutiveGoodSamples = 0
                Write-Color "  Sample ${sampleCount}: Only $podsWithMetrics/$totalPods pods have metrics, resetting counter" "Yellow"
            }
        } catch {
            Write-Color "  Sample ${sampleCount}: Error checking metrics: $_" "Yellow"
            $consecutiveGoodSamples = 0
        }
        
        Start-Sleep -Seconds $CheckIntervalSeconds
    }
}

# Check metrics-server availability at startup
# #region agent log
Write-DebugLog -location "monitor.ps1:189" -message "Checking metrics-server availability" -hypothesisId "A,B" -data @{
    namespace = $Namespace
}
# #endregion
try {
    $metricsServerCheck = kubectl get deployment metrics-server -n kube-system -o json 2>&1
    if ($LASTEXITCODE -eq 0) {
        $metricsServerJson = $metricsServerCheck | ConvertFrom-Json
        # #region agent log
        Write-DebugLog -location "monitor.ps1:190" -message "Metrics-server deployment found" -hypothesisId "A" -data @{
            readyReplicas = $metricsServerJson.status.readyReplicas
            replicas = $metricsServerJson.status.replicas
        }
        # #endregion
    } else {
        # #region agent log
        Write-DebugLog -location "monitor.ps1:191" -message "Metrics-server deployment not found" -hypothesisId "A" -data @{
            error = $metricsServerCheck
        }
        # #endregion
    }
} catch {
    # #region agent log
    Write-DebugLog -location "monitor.ps1:192" -message "Error checking metrics-server" -hypothesisId "A,B" -data @{
        error = $_.Exception.Message
    }
    # #endregion
}

# Wait for metrics-server to be ready before starting collection
$metricsServerReady = Wait-ForMetricsServerReady -Namespace $Namespace -RequiredSamples $MetricsServerReadySamples
if (-not $metricsServerReady) {
    Write-Color "WARNING: Metrics-server readiness check failed or timed out, but continuing..." "Yellow"
}

# Apply warmup delay if specified
if ($WarmupDelaySeconds -gt 0) {
    Write-Color "Applying warmup delay of $WarmupDelaySeconds seconds..." "Cyan"
    Start-Sleep -Seconds $WarmupDelaySeconds
    Write-Color "OK: Warmup delay complete" "Green"
}

# Main collection loop
$collectionCount = 0
$podsWithMetricsCount = 0
$podsWithoutMetricsCount = 0

while ([DateTimeOffset]::UtcNow -lt $endTime) {
    $snapshot = Collect-Metrics
    $metrics += $snapshot
    
    # Track metrics collection success
    $podsInSnapshot = $snapshot.pods | Where-Object { $_.phase -eq "Running" }
    $podsWithMetrics = $podsInSnapshot | Where-Object { 
        -not [string]::IsNullOrWhiteSpace($_.cpu_usage) -and 
        -not [string]::IsNullOrWhiteSpace($_.memory_usage) 
    }
    $podsWithoutMetrics = $podsInSnapshot | Where-Object { 
        [string]::IsNullOrWhiteSpace($_.cpu_usage) -or 
        [string]::IsNullOrWhiteSpace($_.memory_usage) 
    }
    
    if ($podsWithMetrics) { $podsWithMetricsCount += $podsWithMetrics.Count }
    if ($podsWithoutMetrics) { $podsWithoutMetricsCount += $podsWithoutMetrics.Count }
    
    $collectionCount++
    
    $remaining = ($endTime - [DateTimeOffset]::UtcNow).TotalSeconds
    $statusChar = if ($podsWithoutMetrics.Count -gt 0) { "!" } else { "." }
    Write-Host $statusChar -NoNewline
    if ($collectionCount % 10 -eq 0) {
        $metricsStatus = if ($podsWithoutMetrics.Count -gt 0) {
            " ($podsWithoutMetrics.Count pods missing metrics)"
        } else {
            ""
        }
        Write-Host " ($collectionCount samples, $([math]::Round($remaining))s remaining$metricsStatus)"
    }
    
    Start-Sleep -Seconds $IntervalSeconds
}

Write-Host ""

# Export metrics to JSON
$exportData = @{
    start_time = $startTime.ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
    end_time = [DateTimeOffset]::UtcNow.ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
    duration_seconds = $DurationSeconds
    interval_seconds = $IntervalSeconds
    namespace = $Namespace
    total_samples = $collectionCount
    metrics = $metrics
    podallocation_history = $podAllocationHistory
}

$exportData | ConvertTo-Json -Depth 10 | Set-Content $metricsFile
Write-Color "OK: Metrics exported to $metricsFile" "Green"

# Export PodAllocation history separately
if ($podAllocationHistory.Count -gt 0) {
    $podAllocationHistory | ConvertTo-Json -Depth 10 | Set-Content $podAllocationsFile
    Write-Color "OK: PodAllocation history exported to $podAllocationsFile" "Green"
}

# Export CSV summary
try {
    $csvLines = @()
    $csvLines += "timestamp,pod_name,namespace,phase,restart_count,cpu_request,cpu_limit,cpu_usage,memory_usage"
    
    foreach ($snapshot in $metrics) {
        foreach ($pod in $snapshot.pods) {
            $csvLines += "$($snapshot.timestamp),$($pod.name),$($pod.namespace),$($pod.phase),$($pod.restartCount),$($pod.cpu_request),$($pod.cpu_limit),$($pod.cpu_usage),$($pod.memory_usage)"
        }
    }
    
    $csvLines | Set-Content $csvFile
    Write-Color "OK: CSV summary exported to $csvFile" "Green"
} catch {
    Write-Color "WARNING: Error exporting CSV: $_" "Yellow"
}

# Helper function to parse CPU usage (handles "718m", "1.5", etc.)
function Parse-CPUUsage {
    param([string]$cpuStr)
    if ([string]::IsNullOrWhiteSpace($cpuStr)) { return $null }
    
    # Remove whitespace
    $cpuStr = $cpuStr.Trim()
    
    # Handle millicores (e.g., "718m" -> 0.718)
    if ($cpuStr -match '^(\d+(?:\.\d+)?)m$') {
        $millicores = [double]$matches[1]
        return $millicores / 1000.0
    }
    
    # Handle cores (e.g., "1.5" -> 1.5)
    if ($cpuStr -match '^(\d+(?:\.\d+)?)$') {
        return [double]$matches[1]
    }
    
    return $null
}

# Calculate summary statistics
$totalPods = if ($metrics.Count -gt 0 -and $metrics[0].pods) { ($metrics[0].pods | Measure-Object).Count } else { 0 }
$totalRestarts = ($metrics | ForEach-Object { $_.pods | ForEach-Object { $_.restartCount } } | Measure-Object -Sum).Sum

# Check latest sample for metrics availability
$latestSample = if ($metrics.Count -gt 0) { $metrics[-1] } else { $null }
$latestSamplePodsWithMetrics = 0
$latestSamplePodsWithoutMetrics = 0

if ($latestSample) {
    $latestRunningPods = $latestSample.pods | Where-Object { $_.phase -eq "Running" }
    foreach ($pod in $latestRunningPods) {
        if (-not [string]::IsNullOrWhiteSpace($pod.cpu_usage) -and -not [string]::IsNullOrWhiteSpace($pod.memory_usage)) {
            $latestSamplePodsWithMetrics++
        } else {
            $latestSamplePodsWithoutMetrics++
        }
    }
}

# Parse and average CPU usage
$cpuValues = $metrics | ForEach-Object { 
    $_.pods | ForEach-Object { 
        if ($_.cpu_usage) { 
            Parse-CPUUsage $_.cpu_usage 
        } 
    } 
} | Where-Object { $_ -ne $null }

$avgCpuUsage = if ($cpuValues) {
    ($cpuValues | Measure-Object -Average).Average
} else {
    $null
}

Write-Color "`nMonitoring Summary:" "Cyan"
Write-Color "   Total samples: $collectionCount" "Gray"
Write-Color "   Total pods monitored: $totalPods" "Gray"
Write-Color "   Total pod restarts: $totalRestarts" "Gray"
if ($podsWithMetricsCount -gt 0 -or $podsWithoutMetricsCount -gt 0) {
    $totalPodsChecked = $podsWithMetricsCount + $podsWithoutMetricsCount
    $metricsSuccessRate = if ($totalPodsChecked -gt 0) {
        [math]::Round(($podsWithMetricsCount / $totalPodsChecked) * 100, 1)
    } else { 0 }
    Write-Color "   Metrics collection: $podsWithMetricsCount pods with metrics, $podsWithoutMetricsCount without ($metricsSuccessRate% success rate)" "Gray"
    if ($podsWithoutMetricsCount -gt 0) {
        Write-Color "   WARNING: Some pods had missing CPU/memory metrics. Check metrics-server availability." "Yellow"
    }
}
if ($avgCpuUsage) {
    Write-Color "   Average CPU usage: $([math]::Round($avgCpuUsage, 3)) cores" "Gray"
}

# Final diagnostics
if ($latestSamplePodsWithoutMetrics -gt 0) {
    Write-Color "`nWARNING: Latest sample has $latestSamplePodsWithoutMetrics pod(s) with missing CPU/memory metrics!" "Yellow"
    Write-Color "   This may indicate:" "Yellow"
    Write-Color "   - Metrics-server is not ready or not available" "Yellow"
    Write-Color "   - Pods are too new (metrics-server needs time to collect data)" "Yellow"
    Write-Color "   - Metrics-server permissions issue" "Yellow"
    Write-Color "   Troubleshooting:" "Yellow"
    Write-Color "   1. Check metrics-server: kubectl get deployment metrics-server -n kube-system" "Gray"
    Write-Color "   2. Test manually: kubectl top pods -n $Namespace" "Gray"
    Write-Color "   3. Check metrics-server logs: kubectl logs -n kube-system -l k8s-app=metrics-server" "Gray"
} elseif ($latestSamplePodsWithMetrics -gt 0) {
    Write-Color "`nOK: Latest sample has metrics for all $latestSamplePodsWithMetrics running pod(s)" "Green"
}

Write-Color "`nOK: Monitoring complete!" "Green"

