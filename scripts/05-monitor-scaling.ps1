# MBCAS Demo Script 05 - Monitor Scaling
# Continuously monitors CPU usage and MBCAS scaling decisions on UrbanMoveMS
#
# Prerequisites: 03-deploy-urbanmove.ps1 and 04-generate-load.ps1 running
# Usage: .\scripts\05-monitor-scaling.ps1 [-Interval 5] [-Count 0] [-Service bus-service]

param(
    [int]$Interval = 5,                        # Refresh interval in seconds
    [int]$Count = 0,                           # Number of iterations (0 = infinite)
    [string]$Service = "",                     # Optional: filter to specific service
    [string]$Namespace = "",                   # Optional: namespace to watch (empty = all)
    [string]$RunId = "",                      # Optional run identifier to align with load script
    [string]$OutputFile = ""                  # Path to JSON recording file
)

$ErrorActionPreference = "Stop"
$NAMESPACE = $Namespace
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$ArtifactDir = Join-Path $ScriptDir "artifacts"

if (-not (Test-Path $ArtifactDir)) { New-Item -ItemType Directory -Path $ArtifactDir -Force | Out-Null }
if (-not $RunId) { $RunId = (New-Guid).Guid }
if (-not $OutputFile) { $OutputFile = Join-Path $ArtifactDir ("monitor-$RunId.json") }

# Helper functions
function Write-Info { Write-Host "[INFO] $args" -ForegroundColor Green }
function Write-Warn { Write-Host "[WARN] $args" -ForegroundColor Yellow }

function Initialize-Recording {
    if (-not (Test-Path $OutputFile)) {
        $doc = @{
            runId = $RunId
            kind = "monitor-run"
            started = (Get-Date).ToString("o")
            namespace = if ($NAMESPACE) { $NAMESPACE } else { "all" }
            service = if ($Service) { $Service } else { "all" }
            intervalSeconds = $Interval
            samples = @()
        }
        $doc | ConvertTo-Json -Depth 8 | Set-Content -Path $OutputFile -Encoding UTF8
    }
}

function Append-Sample {
    param([hashtable]$Sample)

    if (-not (Test-Path $OutputFile)) { Initialize-Recording }
    $doc = Get-Content -Path $OutputFile -Raw | ConvertFrom-Json
    $doc.samples += $Sample
    $doc | ConvertTo-Json -Depth 8 | Set-Content -Path $OutputFile -Encoding UTF8
}

function Get-PodResources {
    # Get pod resource configuration
    $podFilter = if ($Service) { "-l io.kompose.service=$Service" } else { "" }
    $nsArg = if ($NAMESPACE) { "-n $NAMESPACE" } else { "-A" }

    $pods = kubectl get pods $nsArg $podFilter -o json 2>$null | ConvertFrom-Json
    
    $resources = @()
    foreach ($pod in $pods.items) {
        if ($pod.status.phase -ne "Running") { continue }
        
        $container = $pod.spec.containers[0]
        $podName = $pod.metadata.name
        $podNs = $pod.metadata.namespace
        $shortName = $podName.Substring(0, [Math]::Min(30, $podName.Length))
        
        $resources += [PSCustomObject]@{
            Namespace = $podNs
            Name = $podName
            DisplayName = $shortName
            Status = $pod.status.phase
            CPU_Req = if ($container.resources.requests.cpu) { $container.resources.requests.cpu } else { "-" }
            CPU_Lim = if ($container.resources.limits.cpu) { $container.resources.limits.cpu } else { "-" }
            Managed = if ($pod.metadata.labels.'mbcas.io/managed' -eq "true") { "Yes" } else { "No" }
        }
    }
    
    return $resources
}

function Get-PodMetrics {
    # Get actual CPU usage from metrics-server
    $metrics = @{}
    $nsArg = if ($NAMESPACE) { "-n $NAMESPACE" } else { "-A" }
    
    try {
        $topOutput = kubectl top pods $nsArg --no-headers 2>$null
        if ($LASTEXITCODE -eq 0 -and $topOutput) {
            foreach ($line in $topOutput) {
                $parts = $line -split '\s+'
                if (-not $NAMESPACE) {
                    # Format when -A: NAMESPACE NAME CPU(M) MEM(M)
                    if ($parts.Count -ge 3) {
                        $metrics["$($parts[0])/$($parts[1])"] = $parts[2]
                    }
                } elseif ($parts.Count -ge 2) {
                    # Namespaced call: NAME CPU(M)
                    $metrics[$parts[0]] = $parts[1]
                }
            }
        }
    } catch {
        # Metrics not available
    }
    
    return $metrics
}

function Get-PodAllocations {
    # Get MBCAS PodAllocation decisions
    $allocations = @{}
    $nsArg = if ($NAMESPACE) { "-n $NAMESPACE" } else { "-A" }
    
    try {
        $paList = kubectl get podallocations $nsArg -o json 2>$null | ConvertFrom-Json
        if ($paList.items) {
            foreach ($pa in $paList.items) {
                $key = if ($NAMESPACE) { $pa.spec.podName } else { "$($pa.metadata.namespace)/$($pa.spec.podName)" }
                $allocations[$key] = [PSCustomObject]@{
                    Desired = $pa.spec.desiredCPULimit
                    Applied = if ($pa.status.appliedCPULimit) { $pa.status.appliedCPULimit } else { "-" }
                    Phase = if ($pa.status.phase) { $pa.status.phase } else { "Pending" }
                    Reason = if ($pa.status.reason) { $pa.status.reason } else { "-" }
                }
            }
        }
    } catch {
        # PodAllocations not available
    }
    
    return $allocations
}

function Show-Dashboard {
    param([int]$iteration)

    Write-Host ""

    $timestamp = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
    $title = "MBCAS Scaling Monitor"
    if ($Service) { $title += " ($Service)" }
    
    Write-Host "========================================" -ForegroundColor Cyan
    Write-Host "  $title" -ForegroundColor Cyan
    Write-Host "  $timestamp (refresh: ${Interval}s)" -ForegroundColor Gray
    Write-Host "========================================" -ForegroundColor Cyan
    Write-Host ""
    
    # Get data
    $resources = Get-PodResources
    $metrics = Get-PodMetrics
    $allocations = Get-PodAllocations
    $podSamples = @()
    
    # Display pod table
    $nsDisplay = if ($NAMESPACE) { if ($Service) { "$NAMESPACE ($Service)" } else { $NAMESPACE } } else { if ($Service) { "all namespaces ($Service)" } else { "all namespaces" } }
    Write-Host "PODS IN NAMESPACE: $nsDisplay" -ForegroundColor Yellow
    Write-Host ("-" * 130)
    Write-Host ("{0,-18} {1,-30} {2,-8} {3,-10} {4,-10} {5,-10} {6,-12} {7,-15}" -f "NAMESPACE", "POD", "MANAGED", "CPU_REQ", "CPU_LIM", "CPU_USAGE", "DESIRED", "PHASE")
    Write-Host ("-" * 130)
    
    foreach ($pod in $resources) {
        $usageKey = if ($NAMESPACE) { $pod.Name } else { "$($pod.Namespace)/$($pod.Name)" }
        $usage = if ($metrics[$usageKey]) { $metrics[$usageKey] } else { "-" }
        $allocKey = if ($NAMESPACE) { $pod.Name } else { "$($pod.Namespace)/$($pod.Name)" }
        $allocation = $allocations[$allocKey]
        $desired = if ($allocation) { $allocation.Desired } else { "-" }
        $phase = if ($allocation) { $allocation.Phase } else { "N/A" }
        $reason = if ($allocation) { $allocation.Reason } else { "" }
        
        # Color coding
        $color = "White"
        if ($phase -eq "Applied") { $color = "Green" }
        elseif ($phase -eq "Pending") { $color = "Yellow" }
        elseif ($phase -eq "Failed") { $color = "Red" }
        
        Write-Host ("{0,-18} {1,-30} {2,-8} {3,-10} {4,-10} {5,-10} {6,-12} {7,-15}" -f $pod.Namespace, $pod.DisplayName, $pod.Managed, $pod.CPU_Req, $pod.CPU_Lim, $usage, $desired, $phase) -ForegroundColor $color

        $podSamples += @{
            namespace = $pod.Namespace
            name = $pod.Name
            managed = $pod.Managed
            cpuRequest = $pod.CPU_Req
            cpuLimit = $pod.CPU_Lim
            cpuUsage = $usage
            desired = $desired
            phase = $phase
            reason = $reason
        }
    }
    
    Write-Host ""
    
    # Display MBCAS decisions
    if ($allocations.Count -gt 0) {
        Write-Host "MBCAS ALLOCATION DECISIONS" -ForegroundColor Yellow
        Write-Host ("-" * 110)
        Write-Host ("{0,-18} {1,-30} {2,-12} {3,-12} {4,-10} {5,-20}" -f "NAMESPACE", "POD", "DESIRED", "APPLIED", "PHASE", "REASON")
        Write-Host ("-" * 110)

        $allocSamples = @()
        
        foreach ($key in $allocations.Keys) {
            $a = $allocations[$key]
            $color = switch ($a.Phase) {
                "Applied" { "Green" }
                "Pending" { "Yellow" }
                "Failed" { "Red" }
                default { "White" }
            }
            
            $shortKey = $key.Substring(0, [Math]::Min(30, $key.Length))
            $nsPart = if ($NAMESPACE) { $NAMESPACE } else { ($key -split '/')[0] }
            $podPart = if ($NAMESPACE) { $shortKey } else { ($key -split '/',2)[1].Substring(0, [Math]::Min(30, ($key -split '/',2)[1].Length)) }

            Write-Host ("{0,-18} {1,-30} {2,-12} {3,-12} {4,-10} {5,-20}" -f $nsPart, $podPart, $a.Desired, $a.Applied, $a.Phase, $a.Reason) -ForegroundColor $color

            $allocSamples += @{
                namespace = $nsPart
                pod = $podPart
                desired = $a.Desired
                applied = $a.Applied
                phase = $a.Phase
                reason = $a.Reason
            }
        }
    } else {
        Write-Host "No PodAllocations yet - MBCAS may still be initializing" -ForegroundColor Gray
        $allocSamples = @()
    }
    
    Write-Host ""
    
    # Controller/Agent status
    Write-Host "MBCAS COMPONENTS (mbcas-system namespace)" -ForegroundColor Yellow
    $ErrorActionPreference = "SilentlyContinue"
    $controller = kubectl get pods -n mbcas-system -l "app.kubernetes.io/component=controller" -o jsonpath='{.items[0].status.phase}' 2>$null
    $agent = kubectl get pods -n mbcas-system -l "app.kubernetes.io/component=agent" -o jsonpath='{.items[0].status.phase}' 2>$null
    $ErrorActionPreference = "Stop"
    
    $controllerColor = if ($controller -eq "Running") { "Green" } else { "Red" }
    $agentColor = if ($agent -eq "Running") { "Green" } else { "Red" }
    
    Write-Host ("  Controller: {0}" -f $(if ($controller) { $controller } else { "Not running" })) -ForegroundColor $controllerColor
    Write-Host ("  Agent: {0}" -f $(if ($agent) { $agent } else { "Not running" })) -ForegroundColor $agentColor
    
    Write-Host ""
    Write-Host "Press Ctrl+C to stop monitoring" -ForegroundColor Gray
    
    if ($iteration -gt 0) {
        Write-Host "Iteration: $iteration / $( if ($Count -gt 0) { $Count } else { '∞' } )" -ForegroundColor Gray
    }

    return @{
        iteration = $iteration
        timestamp = (Get-Date).ToString("o")
        pods = $podSamples
        allocations = $allocSamples
    }
}

# Main execution
Write-Host "Starting MBCAS monitoring..." -ForegroundColor Cyan
Write-Host "Namespace: $(if ($NAMESPACE) { $NAMESPACE } else { 'all namespaces' })"
if ($Service) { Write-Host "Service filter: $Service" }
Write-Host "Interval: ${Interval}s"
if ($Count -gt 0) {
    Write-Host "Iterations: $Count"
} else {
    Write-Host "Iterations: continuous (Ctrl+C to stop)"
}
Write-Host "RunId: $RunId"
Write-Host "Recording: $OutputFile"
Write-Host ""
Initialize-Recording
Start-Sleep -Seconds 1

$i = 0
while ($true) {
    $i++
    $sample = Show-Dashboard -iteration $i
    Append-Sample -Sample $sample
    
    if ($Count -gt 0 -and $i -ge $Count) {
        Write-Host ""
        Write-Host "Monitoring complete ($i iterations)" -ForegroundColor Green
        break
    }
    
    Start-Sleep -Seconds $Interval
}
