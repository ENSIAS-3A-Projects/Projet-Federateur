# MBCAS Metrics Test Script
# Deploys MBCAS-managed workloads, generates load, and collects detailed metrics
param(
    [int]$DurationMinutes = 10,
    [string]$Namespace = "mbcas-eval-test",
    [string]$MinikubeProfile = "mbcas",
    [switch]$SkipSetup = $false,
    [switch]$SkipCleanup = $false
)

$ErrorActionPreference = "Stop"
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$ProjectRoot = Split-Path -Parent $ScriptDir
$ResultsDir = Join-Path $ProjectRoot "results\mbcas-test"
$Timestamp = Get-Date -Format "yyyyMMdd-HHmmss"
$TestResultsDir = Join-Path $ResultsDir $Timestamp

# Create results directory
New-Item -ItemType Directory -Force -Path $TestResultsDir | Out-Null
Write-Host "Results directory: $TestResultsDir" -ForegroundColor Cyan

# ------------------------------------------------------------------------------
# Configuration
# ------------------------------------------------------------------------------
$Phases = @(
    @{ Name = "Baseline"; Duration = 20; Load = "gateway:1" },
    @{ Name = "Supreme Overload (A)"; Duration = 40; Load = "worker-a:20" },
    @{ Name = "Saturation (A+B)"; Duration = 30; Load = "worker-a:5,worker-b:5" },
    @{ Name = "Overload (Add Noise)"; Duration = 30; Load = "worker-a:5,worker-b:5,noise:1" },
    @{ Name = "Cool Down"; Duration = 20; Load = "gateway:1" }
)

# ------------------------------------------------------------------------------
# Helper Functions
# ------------------------------------------------------------------------------

function Set-LoadPattern {
    param([string]$Namespace, [string]$Pattern, [string]$Mode)
    
    Write-Host "  Applying load pattern: $Pattern" -ForegroundColor Magenta
    
    # Parse pattern: "worker-a:5,worker-b:2"
    $targets = $Pattern.Split(",")
    $commands = @()
    
    foreach ($t in $targets) {
        $parts = $t.Split(":")
        $name = $parts[0]
        $intensity = $parts[1]
        
        $targetUrl = "http://$name-$Mode.$Namespace.svc.cluster.local:8080"
        if ($name -eq "gateway") {
            $targetUrl = "http://gateway-$Mode.$Namespace.svc.cluster.local:8080"
        }
        
        # Build curl loop for this target
        # intensity determines delay: 1 -> sleep 0.5, 5 -> sleep 0.1
        $delay = 0.5 / [double]$intensity
        $commands += "while true; do curl -s -o /dev/null -w `"${name}: %{time_total}\n`" $targetUrl; sleep $delay; done &"
    }
    
    # Combine into one script
    $fullScript = "echo `"Starting $Pattern`";" + ($commands -join " ") + " wait"
    
    # Update load-generator pod with new command
    kubectl delete pod load-generator -n $Namespace --ignore-not-found --grace-period=0 | Out-Null
    
    $yaml = @"
apiVersion: v1
kind: Pod
metadata:
  name: load-generator
  namespace: $Namespace
spec:
  containers:
  - name: load-gen
    image: eval-service:latest
    imagePullPolicy: Never
    command: ["/bin/sh", "-c"]
    args:
    - |
      $fullScript
"@
    $yaml | kubectl apply -f - | Out-Null
}

function Wait-ForPods {
    param([string]$Namespace, [string]$Selector)
    Write-Host "  Waiting for pods ($Selector)..." -ForegroundColor Gray
    $timeout = 300
    $elapsed = 0
    while ($elapsed -lt $timeout) {
        $pods = kubectl get pods -n $Namespace -l $Selector -o json 2>$null | ConvertFrom-Json
        if ($pods.items.Count -gt 0) {
            $allReady = $true
            foreach ($pod in $pods.items) {
                if ($pod.status.phase -ne "Running" -or $pod.status.containerStatuses[0].ready -ne $true) {
                    $allReady = $false
                    break
                }
            }
            if ($allReady) {
                Write-Host "  [OK] Pods ready" -ForegroundColor Green
                return
            }
        }
        Start-Sleep -Seconds 5
        $elapsed += 5
    }
    throw "Timeout waiting for pods ($Selector)"
}

function Measure-WorkloadMetrics {
    param([string]$Namespace, [string]$OutputFile, [string]$PhaseName)
    
    $metrics = @{
        timestamp   = (Get-Date -Format "o")
        phase       = $PhaseName
        pods        = @()
        allocations = @()
        latency     = @()
    }
    
    # 0. Latency metrics from load generator
    try {
        $logs = kubectl logs -n $Namespace load-generator --tail=50 2>$null
        if ($logs) {
            foreach ($line in $logs) {
                if ($line -match "([\w-]+): ([\d.]+)") {
                    $metrics.latency += @{ target = $Matches[1]; ms = [double]$Matches[2] * 1000 }
                }
            }
        }
    }
    catch { }
    
    # 1. Pod Resource Usage (kubectl top)
    try {
        $top = $null
        for ($i = 0; $i -lt 5; $i++) {
            $top = kubectl top pods -n $Namespace --no-headers 2>$null
            if ($top) { break }
            Start-Sleep -Seconds 5
        }

        if ($top) {
            foreach ($line in $top) {
                if ($line -match "^\s*$") { continue }
                if ($line -match "error:|metrics not available") { continue }
                
                $parts = $line.Split(" ", [StringSplitOptions]::RemoveEmptyEntries)
                if ($parts.Length -ge 3) {
                    $metrics.pods += @{
                        name   = $parts[0]
                        cpu    = $parts[1]
                        memory = $parts[2]
                    }
                }
            }
        }
    }
    catch {
        Write-Host "    (Metrics not available yet)" -ForegroundColor DarkGray
    }
    
    # 2. MBCAS Allocations
    try {
        $allocations = kubectl get podallocations -n $Namespace -o json 2>$null | ConvertFrom-Json
        if ($allocations -and $allocations.items) {
            foreach ($alloc in $allocations.items) {
                $metrics.allocations += @{
                    name        = $alloc.metadata.name
                    pod         = $alloc.spec.podName
                    allocCPU    = $alloc.status.appliedCPURequest
                    allocLimit  = $alloc.status.appliedCPULimit
                    shadowPrice = $alloc.status.shadowPriceCPU
                }
            }
        }
    }
    catch { }
    
    # Convert to JSON for storage
    $metrics | ConvertTo-Json -Depth 10 -Compress | Out-File -FilePath $OutputFile -Encoding UTF8 -Append
}

# ------------------------------------------------------------------------------
# Main Execution
# ------------------------------------------------------------------------------

# 1. Setup
if (-not $SkipSetup) {
    Write-Host "=== Setting up MBCAS Test Environment ===" -ForegroundColor Cyan
    
    # Create Namespace
    kubectl create namespace $Namespace --dry-run=client -o yaml | kubectl apply -f -
    
    # Build and Load Images
    Write-Host "Building and loading images..." -ForegroundColor Cyan
    Push-Location (Join-Path $ProjectRoot "apps\eval-service")
    docker build -t eval-service:latest .
    if ($LASTEXITCODE -ne 0) { throw "Failed to build eval-service" }
    Pop-Location
    
    if (Get-Command minikube -ErrorAction SilentlyContinue) {
        Write-Host "Loading image into Minikube (Profile: $MinikubeProfile)..."
        minikube image load eval-service:latest -p $MinikubeProfile
    }

    # Apply Workloads
    Write-Host "Deploying workloads..."
    $workloadsPath = Join-Path $ProjectRoot "k8s\vpa-mbcas-test\workloads-mbcas.yaml"
    (Get-Content $workloadsPath) -replace "namespace: vpa-mbcas-test", "namespace: $Namespace" | kubectl apply -f -
    
    # Wait for readiness
    Wait-ForPods -Namespace $Namespace -Selector "test=mbcas"
}

# 2. Execution Loop
Write-Host "=== Starting Phasic Test Loop ===" -ForegroundColor Cyan
$metricsFile = Join-Path $TestResultsDir "metrics.json"
"[" | Out-File -FilePath $metricsFile -Encoding UTF8

$first = $true
foreach ($phase in $Phases) {
    Write-Host "`n>>> Phase: $($phase.Name) ($($phase.Duration)s)" -ForegroundColor Cyan
    Set-LoadPattern -Namespace $Namespace -Pattern $phase.Load -Mode "mbcas"
    
    # Wait for load generator to be ready
    Start-Sleep -Seconds 5
    
    $phaseEndTime = (Get-Date).AddSeconds($phase.Duration)
    while ((Get-Date) -lt $phaseEndTime) {
        Write-Host "  Collecting metrics..." -NoNewline
        if (-not $first) {
            "," | Out-File -FilePath $metricsFile -Encoding UTF8 -Append
        }
        $first = $false
        Measure-WorkloadMetrics -Namespace $Namespace -OutputFile $metricsFile -PhaseName $phase.Name
        Write-Host " [Done]" -ForegroundColor Gray
        Start-Sleep -Seconds 10
    }
}

# Close JSON array
"]" | Out-File -FilePath $metricsFile -Encoding UTF8 -Append

Write-Host "Test complete. Results saved to $metricsFile" -ForegroundColor Green

# Close JSON array
"]" | Out-File -FilePath $metricsFile -Encoding UTF8 -Append

Write-Host "Test complete. Results saved to $metricsFile" -ForegroundColor Green

# 4. Cleanup
if (-not $SkipCleanup) {
    Write-Host "cleaning up..."
    kubectl delete namespace $Namespace --wait=$false
}
