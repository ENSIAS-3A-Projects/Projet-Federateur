# MBCAS Demo Script 05 - Monitor Scaling
# Continuously monitors CPU usage and MBCAS scaling decisions on UrbanMoveMS
#
# Prerequisites: 03-deploy-urbanmove.ps1 and 04-generate-load.ps1 running
# Usage: .\scripts\05-monitor-scaling.ps1 [-Interval 5] [-Count 0] [-Service bus-service]

param(
    [int]$Interval = 5,                        # Refresh interval in seconds
    [int]$Count = 0,                           # Number of iterations (0 = infinite)
    [string]$Service = ""                      # Optional: filter to specific service
)

$ErrorActionPreference = "Stop"
$NAMESPACE = "urbanmove"

# Helper functions
function Write-Info { Write-Host "[INFO] $args" -ForegroundColor Green }
function Write-Warn { Write-Host "[WARN] $args" -ForegroundColor Yellow }

function Get-PodResources {
    # Get pod resource configuration
    $podFilter = if ($Service) { "-l io.kompose.service=$Service" } else { "" }
    
    $pods = kubectl get pods -n $NAMESPACE $podFilter -o json 2>$null | ConvertFrom-Json
    
    $resources = @()
    foreach ($pod in $pods.items) {
        if ($pod.status.phase -ne "Running") { continue }
        
        $container = $pod.spec.containers[0]
        $podName = $pod.metadata.name
        $shortName = $podName.Substring(0, [Math]::Min(30, $podName.Length))
        
        $resources += [PSCustomObject]@{
            Name = $shortName
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
    
    try {
        $topOutput = kubectl top pods -n $NAMESPACE --no-headers 2>$null
        if ($LASTEXITCODE -eq 0 -and $topOutput) {
            foreach ($line in $topOutput) {
                $parts = $line -split '\s+'
                if ($parts.Count -ge 2) {
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
    
    try {
        $paList = kubectl get podallocations -n $NAMESPACE -o json 2>$null | ConvertFrom-Json
        if ($paList.items) {
            foreach ($pa in $paList.items) {
                $key = $pa.spec.podName
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
    
    Clear-Host
    
    $timestamp = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
    $title = "MBCAS Scaling Monitor - UrbanMoveMS"
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
    
    # Display pod table
    $nsDisplay = if ($Service) { "$NAMESPACE ($Service)" } else { $NAMESPACE }
    Write-Host "PODS IN NAMESPACE: $nsDisplay" -ForegroundColor Yellow
    Write-Host ("-" * 110)
    Write-Host ("{0,-30} {1,-8} {2,-10} {3,-10} {4,-10} {5,-12} {6,-15}" -f "POD", "MANAGED", "CPU_REQ", "CPU_LIM", "CPU_USAGE", "DESIRED", "PHASE")
    Write-Host ("-" * 110)
    
    foreach ($pod in $resources) {
        $usage = if ($metrics[$pod.Name]) { $metrics[$pod.Name] } else { "-" }
        $allocation = $allocations[$pod.Name]
        $desired = if ($allocation) { $allocation.Desired } else { "-" }
        $phase = if ($allocation) { $allocation.Phase } else { "N/A" }
        
        # Color coding
        $color = "White"
        if ($phase -eq "Applied") { $color = "Green" }
        elseif ($phase -eq "Pending") { $color = "Yellow" }
        elseif ($phase -eq "Failed") { $color = "Red" }
        
        Write-Host ("{0,-30} {1,-8} {2,-10} {3,-10} {4,-10} {5,-12} {6,-15}" -f $pod.Name, $pod.Managed, $pod.CPU_Req, $pod.CPU_Lim, $usage, $desired, $phase) -ForegroundColor $color
    }
    
    Write-Host ""
    
    # Display MBCAS decisions
    if ($allocations.Count -gt 0) {
        Write-Host "MBCAS ALLOCATION DECISIONS" -ForegroundColor Yellow
        Write-Host ("-" * 90)
        Write-Host ("{0,-30} {1,-12} {2,-12} {3,-10} {4,-20}" -f "POD", "DESIRED", "APPLIED", "PHASE", "REASON")
        Write-Host ("-" * 90)
        
        foreach ($key in $allocations.Keys) {
            $a = $allocations[$key]
            $color = switch ($a.Phase) {
                "Applied" { "Green" }
                "Pending" { "Yellow" }
                "Failed" { "Red" }
                default { "White" }
            }
            
            $shortKey = $key.Substring(0, [Math]::Min(30, $key.Length))
            Write-Host ("{0,-30} {1,-12} {2,-12} {3,-10} {4,-20}" -f $shortKey, $a.Desired, $a.Applied, $a.Phase, $a.Reason) -ForegroundColor $color
        }
    } else {
        Write-Host "No PodAllocations yet - MBCAS may still be initializing" -ForegroundColor Gray
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
}

# Main execution
Write-Host "Starting MBCAS monitoring..." -ForegroundColor Cyan
Write-Host "Namespace: $NAMESPACE"
if ($Service) { Write-Host "Service filter: $Service" }
Write-Host "Interval: ${Interval}s"
if ($Count -gt 0) {
    Write-Host "Iterations: $Count"
} else {
    Write-Host "Iterations: continuous (Ctrl+C to stop)"
}
Write-Host ""
Start-Sleep -Seconds 1

$i = 0
while ($true) {
    $i++
    Show-Dashboard -iteration $i
    
    if ($Count -gt 0 -and $i -ge $Count) {
        Write-Host ""
        Write-Host "Monitoring complete ($i iterations)" -ForegroundColor Green
        break
    }
    
    Start-Sleep -Seconds $Interval
}
