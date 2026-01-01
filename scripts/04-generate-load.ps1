# MBCAS Demo Script 04 - Generate Load
# Generates CPU load on UrbanMoveMS services to trigger MBCAS scaling
#
# Prerequisites: 03-deploy-urbanmove.ps1 completed
# Usage: .\scripts\04-generate-load.ps1 [-Duration 120] [-Intensity 2] [-Service bus]
#        .\scripts\04-generate-load.ps1 -Stop

param(
    [int]$Duration = 120,                           # Load duration in seconds
    [int]$Intensity = 32,                            # Number of CPU workers (1-4)
    [string]$Service = "bus-service",               # Target service: bus, ticket, trajet, gateway
    [string]$RunId = "",                           # Optional run identifier for recordings
    [string]$RecordFile = "",                      # Optional explicit path for JSON recording
    [switch]$Stop,                                  # Stop any running load
    [switch]$Background                             # Run in background
)

$ErrorActionPreference = "Stop"
$LOAD_POD_NAME = "load-generator"
$NAMESPACE = "urbanmove"
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$ArtifactDir = Join-Path $ScriptDir "artifacts"

if (-not (Test-Path $ArtifactDir)) { New-Item -ItemType Directory -Path $ArtifactDir -Force | Out-Null }
if (-not $RunId) { $RunId = (New-Guid).Guid }
if (-not $RecordFile) { $RecordFile = Join-Path $ArtifactDir ("load-run-$RunId.json") }

# Helper functions
function Write-Info { Write-Host "[INFO] $args" -ForegroundColor Green }
function Write-Warn { Write-Host "[WARN] $args" -ForegroundColor Yellow }
function Write-Err { Write-Host "[ERROR] $args" -ForegroundColor Red; exit 1 }

function Initialize-Recording {
    if (-not (Test-Path $RecordFile)) {
        $doc = @{
            runId = $RunId
            kind = "load-run"
            started = (Get-Date).ToString("o")
            namespace = $NAMESPACE
            service = $Service
            durationSeconds = $Duration
            intensity = $Intensity
            background = [bool]$Background
            events = @()
        }
        $doc | ConvertTo-Json -Depth 6 | Set-Content -Path $RecordFile -Encoding UTF8
    }
}

function Append-Event {
    param(
        [string]$Type,
        [hashtable]$Data
    )

    if (-not (Test-Path $RecordFile)) { Initialize-Recording }

    $doc = Get-Content -Path $RecordFile -Raw | ConvertFrom-Json
    $doc.events += @{
        timestamp = (Get-Date).ToString("o")
        type = $Type
        data = $Data
    }
    $doc | ConvertTo-Json -Depth 6 | Set-Content -Path $RecordFile -Encoding UTF8
}

function Stop-LoadGenerator {
    Write-Info "Stopping load generators..."
    
    kubectl delete pod $LOAD_POD_NAME -n $NAMESPACE --ignore-not-found=true 2>&1 | Out-Null
    
    # Stop stress in all service pods (use io.kompose.service label)
    foreach ($svc in @("bus-service", "ticket-service", "trajet-service", "gateway")) {
        $pods = kubectl get pods -n $NAMESPACE -l "io.kompose.service=$svc" -o jsonpath='{.items[*].metadata.name}' 2>$null
        if ($pods) {
            foreach ($pod in ($pods -split " ")) {
                kubectl exec -n $NAMESPACE $pod -- pkill stress 2>$null
                kubectl exec -n $NAMESPACE $pod -- pkill -9 dd 2>$null
                kubectl exec -n $NAMESPACE $pod -- pkill -9 md5sum 2>$null
            }
        }
    }
    
    Write-Info "[OK] Load stopped"
        Append-Event -Type "load-stopped" -Data @{ reason = "manual stop" }
}

function Start-LoadOnService {
    Write-Info "Starting CPU stress on $Service pods..."
    
    # Use io.kompose.service label (from docker-compose conversion)
    $pods = kubectl get pods -n $NAMESPACE -l "io.kompose.service=$Service" -o jsonpath='{.items[*].metadata.name}'
    if (-not $pods) {
        Write-Err "$Service pods not found in $NAMESPACE namespace. Run 03-deploy-urbanmove.ps1 first."
    }
    
    $podArray = $pods -split " "
    Write-Info "Found $($podArray.Count) pod(s): $pods"
    Write-Info "Intensity: $Intensity CPU workers"
    Write-Info "Duration: $Duration seconds"
    Write-Host ""
    
    # Run stress on first pod using dd (works in any container)
    $targetPod = $podArray[0]
    
    # Use dd to generate CPU load (works in any Linux container)
    # This reads from /dev/zero and writes to /dev/null, burning CPU
    $cmd = "for i in `$(seq 1 $Intensity); do (dd if=/dev/zero bs=1M count=1000 2>/dev/null | md5sum > /dev/null &); done; sleep $Duration; pkill -9 dd"
    
    Write-Info "Target pod: $targetPod"
    Write-Info "Executing: CPU burn for $Duration seconds with $Intensity workers"

        Append-Event -Type "load-started" -Data @{ targetPod = $targetPod; command = "dd|md5sum" }
    
    if ($Background) {
        # Run in background (non-blocking)
        Start-Job -ScriptBlock {
            param($ns, $pod, $command)
            kubectl exec -n $ns $pod -- sh -c $command 2>&1
        } -ArgumentList $NAMESPACE, $targetPod, $cmd | Out-Null
        
        Write-Info "Load running in background on $Service for $Duration seconds"
        Write-Info "Monitor with: .\scripts\05-monitor-scaling.ps1"
    } else {
        # Run in foreground (blocking)
        Write-Host ""
        Write-Host "CPU stress running on $Service... (Ctrl+C to stop early)" -ForegroundColor Yellow
        Write-Host ""
        
        kubectl exec -n $NAMESPACE $targetPod -- sh -c $cmd
        Append-Event -Type "load-completed" -Data @{ targetPod = $targetPod }
    }
}

function Start-LoadGenerator {
    Write-Info "Starting dedicated load generator pod for $Service..."
    
    # Remove old generator if exists
    kubectl delete pod $LOAD_POD_NAME -n $NAMESPACE --ignore-not-found=true 2>&1 | Out-Null
    
    # Create load generator pod
    $loadManifest = @"
apiVersion: v1
kind: Pod
metadata:
  name: $LOAD_POD_NAME
  namespace: $NAMESPACE
  labels:
    app: load-generator
    mbcas.io/managed: "true"
spec:
  restartPolicy: Never
  containers:
  - name: stress
    image: progrium/stress:latest
    command: ["stress", "--cpu", "$Intensity", "--timeout", "${Duration}s"]
    resources:
      requests:
        cpu: "100m"
        memory: "64Mi"
      limits:
        cpu: "4000m"
        memory: "128Mi"
"@
    
    $loadManifest | kubectl apply -f -
    
    if ($LASTEXITCODE -ne 0) {
        Write-Err "Failed to create load generator"
    }
    
    Write-Info "[OK] Load generator started"
    Write-Info "Service: $Service (indirect load via workload)"
    Write-Info "Duration: $Duration seconds"
    Write-Info "Intensity: $Intensity CPU workers"
        Append-Event -Type "generator-started" -Data @{ pod = $LOAD_POD_NAME }
    Write-Host ""
    Write-Host "Monitor with: .\scripts\05-monitor-scaling.ps1" -ForegroundColor Cyan
}

function Show-LoadStatus {
    Write-Host ""
    Write-Host "========================================" -ForegroundColor Cyan
    Write-Host "  Load Generation Status" -ForegroundColor Cyan
    Write-Host "========================================" -ForegroundColor Cyan
    Write-Host ""
    
    Write-Host "Running pods in urbanmove namespace:" -ForegroundColor White
    $ErrorActionPreference = "SilentlyContinue"
    kubectl get pods -n $NAMESPACE -o wide 2>&1 | Select-String -Pattern "bus-service|ticket-service|trajet-service|gateway|load-generator"
    $ErrorActionPreference = "Stop"
    
    Write-Host ""
    Write-Host "CPU usage (if metrics available):" -ForegroundColor White
    $ErrorActionPreference = "SilentlyContinue"
    kubectl top pods -n $NAMESPACE 2>&1 | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Write-Warn "Metrics not available yet. Wait ~60s after cluster start."
    } else {
        kubectl top pods -n $NAMESPACE
    }
    $ErrorActionPreference = "Stop"
}

# Main execution
Write-Host ""
Write-Host "MBCAS Demo - Step 4: Generate Load on UrbanMoveMS" -ForegroundColor Cyan
Write-Host "=================================================" -ForegroundColor Cyan
Write-Host ""

Initialize-Recording

if ($Stop) {
    Stop-LoadGenerator
    Show-LoadStatus
    exit 0
}

# Validate service parameter
$validServices = @("bus-service", "ticket-service", "trajet-service", "gateway")
if ($validServices -notcontains $Service) {
    Write-Warn "Invalid service. Valid options: $($validServices -join ', ')"
    Write-Warn "Using default: bus-service"
    $Service = "bus-service"
}

# Validate parameters
if ($Intensity -lt 1 -or $Intensity -gt 4) {
    Write-Warn "Intensity should be 1-4. Using 2."
    $Intensity = 2
}

if ($Duration -lt 10) {
    Write-Warn "Duration too short. Using 30 seconds minimum."
    $Duration = 30
}

Write-Info "Load Configuration:"
Write-Info "  Service: $Service"
Write-Info "  Duration: $Duration seconds"
Write-Info "  Intensity: $Intensity CPU workers"
Write-Info "  RunId: $RunId"
Write-Info "  Record: $RecordFile"
Write-Host ""

Append-Event -Type "config" -Data @{ service = $Service; durationSeconds = $Duration; intensity = $Intensity }

# Load directly on service pod
Start-LoadOnService

Show-LoadStatus

Write-Host ""
Write-Host "MBCAS should detect increased CPU throttling and scale up the $Service pod." -ForegroundColor Green
Write-Host "Watch with: .\scripts\05-monitor-scaling.ps1 -Service $Service" -ForegroundColor Cyan
Write-Host ""
