# MBCAS Metrics Test Script
# Deploys MBCAS-managed workloads, generates load, and collects detailed metrics
param(
    [int]$DurationMinutes = 5,
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
# Helper Functions
# ------------------------------------------------------------------------------

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
    param([string]$Namespace, [string]$OutputFile)
    
    $metrics = @{
        timestamp   = (Get-Date -Format "o")
        pods        = @()
        allocations = @()
        latency     = @{}
    }
    
    # 0. Latency metrics from load generator
    try {
        $log = kubectl logs -n $Namespace load-generator --tail=20 2>$null
        if ($log) {
            $latencies = $log | Select-String "time_total: ([\d.]+)" | ForEach-Object { [double]$_.Matches[0].Groups[1].Value * 1000 }
            if ($latencies) {
                $avg = ($latencies | Measure-Object -Average).Average
                $p99 = ($latencies | Sort-Object)[[math]::Max(0, [int]($latencies.Count * 0.99) - 1)]
                $metrics.latency = @{
                    avgMs = [math]::Round($avg, 2)
                    p99Ms = [math]::Round($p99, 2)
                }
            }
        }
    }
    catch { }
    
    # 1. Pod Resource Usage (kubectl top)
    try {
        # Temporarily relax error action to handle "metrics not available"
        $prevErrorAction = $ErrorActionPreference
        $ErrorActionPreference = "Continue"
        
        $top = kubectl top pods -n $Namespace --no-headers 2>&1
        
        $ErrorActionPreference = $prevErrorAction
        
        # Retry logic for metrics server (wait up to 30s for metrics-server to pick up new namespace)
        $top = $null
        for ($i = 0; $i -lt 5; $i++) {
            $top = kubectl top pods -n $Namespace --no-headers 2>$null
            if ($top) { break }
            Start-Sleep -Seconds 5
        }

        if ($top) {
            $topLines = $top -split "`n"
            foreach ($line in $topLines) {
                if ($line -match "^\s*$") { continue }
                # Handle potential error messages in output
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
    catch {
        # Ignore errors if no allocations yet
    }
    
    # Write clean JSON manually to avoid PowerShell serialization issues
    $json = "{"
    $json += "`"timestamp`":`"$($metrics.timestamp)`","
    
    # Latency (Ensure numbers are never null)
    $avgMs = if ($null -eq $metrics.latency.avgMs) { 0 } else { $metrics.latency.avgMs }
    $p99Ms = if ($null -eq $metrics.latency.p99Ms) { 0 } else { $metrics.latency.p99Ms }
    $json += "`"latency`":{`"avgMs`":$avgMs,`"p99Ms`":$p99Ms},"
    
    # Pods
    $json += "`"pods`":["
    $podList = @()
    foreach ($p in $metrics.pods) { $podList += "{`"name`":`"$($p.name)`",`"cpu`":`"$($p.cpu)`",`"memory`":`"$($p.memory)`"}" }
    $json += ($podList -join ",") + "],"
    
    # Allocations
    $json += "`"allocations`":["
    $allocList = @()
    foreach ($a in $metrics.allocations) { 
        $sp = if ($null -eq $a.shadowPrice) { 0 } else { $a.shadowPrice }
        $allocList += "{`"name`":`"$($a.name)`",`"pod`":`"$($a.pod)`",`"allocCPU`":`"$($a.allocCPU)`",`"allocLimit`":`"$($a.allocLimit)`",`"shadowPrice`":$sp}" 
    }
    $json += ($allocList -join ",") + "]"
    $json += "}"
    
    $json | Out-File -FilePath $OutputFile -Encoding UTF8 -Append
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
    
    # Build eval-service (Reuse existing logic)
    Push-Location (Join-Path $ProjectRoot "apps\eval-service")
    docker build -t eval-service:latest .
    if ($LASTEXITCODE -ne 0) { throw "Failed to build eval-service" }
    Pop-Location
    
    # Load into Minikube
    if (Get-Command minikube -ErrorAction SilentlyContinue) {
        Write-Host "Loading image into Minikube (Profile: $MinikubeProfile)..."
        minikube image load eval-service:latest -p $MinikubeProfile
    }

    # Apply Workloads
    Write-Host "Deploying workloads..."
    $workloadsPath = Join-Path $ProjectRoot "k8s\vpa-mbcas-test\workloads-mbcas.yaml"
    
    # Modify namespace in files (on the fly application)
    (Get-Content $workloadsPath) -replace "namespace: vpa-mbcas-test", "namespace: $Namespace" | kubectl apply -f -
    
    # Wait for readiness
    Wait-ForPods -Namespace $Namespace -Selector "test=mbcas"
}

# 2. Start Load Generator
Write-Host "=== Starting Load Generator ===" -ForegroundColor Cyan
$loadGenYaml = @"
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
      echo "Starting load generation..."
      while true; do
        curl -s -o /dev/null -w "time_total: %{time_total}\n" http://gateway-mbcas.$Namespace.svc.cluster.local:8080
        sleep 0.1
      done
"@

$loadGenYaml | kubectl apply -f -

# 3. Test Loop
Write-Host "=== Starting Test Loop ($DurationMinutes minutes) ===" -ForegroundColor Cyan
$startTime = Get-Date
$endTime = $startTime.AddMinutes($DurationMinutes)
$metricsFile = Join-Path $TestResultsDir "metrics.json"

# Initialize metrics file as an array
"[" | Out-File -FilePath $metricsFile -Encoding UTF8

$first = $true
while ((Get-Date) -lt $endTime) {
    $remaining = ($endTime - (Get-Date)).TotalSeconds
    Write-Host "Collecting metrics... (${remaining}s remaining)" -NoNewline
    
    if (-not $first) {
        "," | Out-File -FilePath $metricsFile -Encoding UTF8 -Append
    }
    $first = $false
    
    Measure-WorkloadMetrics -Namespace $Namespace -OutputFile $metricsFile
    
    Write-Host " [Done]" -ForegroundColor Green
    Start-Sleep -Seconds 10
}

# Close JSON array
"]" | Out-File -FilePath $metricsFile -Encoding UTF8 -Append

Write-Host "Test complete. Results saved to $metricsFile" -ForegroundColor Green

# 4. Cleanup
if (-not $SkipCleanup) {
    Write-Host "cleaning up..."
    kubectl delete namespace $Namespace --wait=$false
}
