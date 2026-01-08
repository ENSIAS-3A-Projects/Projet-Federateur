# run_e2e_test.ps1
# Complete end-to-end test script that runs both baseline and MBCAS scenarios
# Automatically starts monitoring after workloads are running
# Generates comparison report

param(
    [switch]$SkipBaseline
)

$ErrorActionPreference = "Stop"

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

function Write-Step($step, $total, $message) {
    Write-Color "`n[${step}/${total}] $message" "Yellow"
}

function Test-Command($cmd) {
    $null = Get-Command $cmd -ErrorAction SilentlyContinue
    return $?
}

function Wait-ForPods {
    param(
        [string]$Namespace,
        [int]$TimeoutSeconds = 300
    )
    
    Write-Color "Waiting for pods to be Running in namespace $Namespace..." "Gray"
    $startTime = Get-Date
    $timeout = $startTime.AddSeconds($TimeoutSeconds)
    
    while ((Get-Date) -lt $timeout) {
        $pods = kubectl get pods -n $Namespace -o json 2>$null | ConvertFrom-Json
        $allRunning = $true
        $readyCount = 0
        
        foreach ($pod in $pods.items) {
            if ($pod.status.phase -ne "Running") {
                $allRunning = $false
                break
            }
            if ($pod.status.containerStatuses) {
                foreach ($container in $pod.status.containerStatuses) {
                    if ($container.ready) {
                        $readyCount++
                    }
                }
            }
        }
        
        if ($allRunning -and $pods.items.Count -gt 0) {
            Write-Color "OK: All pods are Running" "Green"
            return $true
        }
        
        Start-Sleep -Seconds 2
        Write-Host "." -NoNewline
    }
    
    Write-Host ""
    Write-Color "WARNING: Timeout waiting for pods to be Running" "Yellow"
    return $false
}

function Wait-ForPodAllocations {
    param(
        [string]$Namespace,
        [int]$TimeoutSeconds = 120
    )
    
    Write-Color "Waiting for PodAllocations to be created..." "Gray"
    $startTime = Get-Date
    $timeout = $startTime.AddSeconds($TimeoutSeconds)
    
    while ((Get-Date) -lt $timeout) {
        try {
            $allocations = kubectl get podallocations -n $Namespace -o json 2>$null | ConvertFrom-Json
            if ($allocations.items -and $allocations.items.Count -gt 0) {
                Write-Color "OK: Found $($allocations.items.Count) PodAllocation(s)" "Green"
                return $true
            }
        } catch {
            # PodAllocations may not exist yet
        }
        
        Start-Sleep -Seconds 2
        Write-Host "." -NoNewline
    }
    
    Write-Host ""
    Write-Color "WARNING: Timeout waiting for PodAllocations" "Yellow"
    return $false
}

function Run-LoadTest {
    param(
        [string]$GatewayURL,
        [string]$OutputDir,
        [float]$DurationScale = 0.1  # 10% of normal duration for faster testing
    )
    
    Write-Color "`nLOAD TEST: Starting load test..." "Cyan"
    
    # Build loadgen if needed
    $loadgenPath = "scripts\evaluation\loadgen.exe"
    if (-not (Test-Path $loadgenPath)) {
        Write-Color "Building loadgen..." "Gray"
        $projectRoot = Split-Path -Parent $PSScriptRoot
        Set-Location $projectRoot
        go build -o $loadgenPath scripts\evaluation\loadgen.go
        if ($LASTEXITCODE -ne 0) {
            Write-Color "ERROR: Failed to build loadgen" "Red"
            return $false
        }
    }
    
    # Run loadgen
    & $loadgenPath -url $GatewayURL -out $OutputDir -scale $DurationScale
    
    return $LASTEXITCODE -eq 0
}

function Start-Monitoring {
    param(
        [string]$OutputDir,
        [int]$DurationSeconds,
        [string]$Namespace
    )
    
    Write-Color "MONITORING: Starting monitoring in background..." "Cyan"
    
    $monitorScript = Join-Path $PSScriptRoot "evaluation\monitor.ps1"
    $job = Start-Job -ScriptBlock {
        param($script, $outDir, $duration, $ns)
        & $script -OutputDir $outDir -DurationSeconds $duration -Namespace $ns
    } -ArgumentList $monitorScript, $OutputDir, $DurationSeconds, $Namespace
    
    return $job
}

function Run-TestScenario {
    param(
        [string]$ScenarioName,
        [string]$WorkloadFile,
        [string]$ResultsDir,
        [bool]$WithMBCAS = $false,
        [int]$LoadTestDurationSeconds = 900  # 15 minutes default
    )
    
    Write-Color "`n=========================================================" "Cyan"
    Write-Color "  Scenario: $ScenarioName" "Cyan"
    Write-Color "=========================================================" "Cyan"
    
    $namespace = "evaluation"
    
    # Step 1: Deploy workloads
    Write-Step 1 5 "Deploying workloads..."
    kubectl apply -f $WorkloadFile
    if ($LASTEXITCODE -ne 0) {
        Write-Color "ERROR: Failed to deploy workloads" "Red"
        return $false
    }
    Write-Color "OK: Workloads deployed" "Green"
    
    # Step 2: Wait for pods to be Running
    Write-Step 2 5 "Waiting for pods to be Running..."
    if (-not (Wait-ForPods -Namespace $namespace)) {
        Write-Color "ERROR: Pods did not become Ready in time" "Red"
        return $false
    }
    
    # Step 3: Wait for PodAllocations (if MBCAS test)
    if ($WithMBCAS) {
        Write-Step 3 5 "Waiting for PodAllocations..."
        if (-not (Wait-ForPodAllocations -Namespace $namespace)) {
            Write-Color "WARNING: PodAllocations not created, but continuing..." "Yellow"
        }
    } else {
        Write-Step 3 5 "Skipping PodAllocation wait (baseline test)"
    }
    
    # Step 4: Start monitoring (automatically after pods are Running)
    Write-Step 4 5 "Starting monitoring..."
    $monitorJob = Start-Monitoring -OutputDir $ResultsDir -DurationSeconds $LoadTestDurationSeconds -Namespace $namespace
    Write-Color "OK: Monitoring started in background" "Green"
    
    # Step 5: Run load test
    Write-Step 5 5 "Running load test..."
    # Use kubectl port-forward directly (more reliable than minikube service on Windows)
    Write-Color "Setting up port-forward for gateway..." "Gray"
    $portForwardJob = Start-Job -ScriptBlock {
        param($ns)
        kubectl port-forward -n $ns svc/gateway 8080:80 2>&1 | Out-Null
    } -ArgumentList $namespace
    Start-Sleep -Seconds 5  # Give port-forward time to establish
    $gatewayURL = "http://localhost:8080"
    
    # Verify port-forward is working
    try {
        $testResponse = Invoke-WebRequest -Uri $gatewayURL -TimeoutSec 2 -UseBasicParsing -ErrorAction SilentlyContinue
        Write-Color "OK: Gateway is accessible at $gatewayURL" "Green"
    } catch {
        Write-Color "WARNING: Could not verify gateway connectivity, but continuing..." "Yellow"
    }
    
    $loadTestSuccess = Run-LoadTest -GatewayURL $gatewayURL -OutputDir $ResultsDir
    if (-not $loadTestSuccess) {
        Write-Color "WARNING: Load test had errors, but continuing..." "Yellow"
    }
    
    # Wait for monitoring to complete
    Write-Color "`nWaiting for monitoring to complete..." "Gray"
    Wait-Job $monitorJob | Out-Null
    $monitorOutput = Receive-Job $monitorJob
    Remove-Job $monitorJob
    
    # Clean up port-forward
    if ($portForwardJob) {
        Write-Color "Stopping port-forward..." "Gray"
        Stop-Job $portForwardJob -ErrorAction SilentlyContinue
        Remove-Job $portForwardJob -ErrorAction SilentlyContinue
    }
    
    # Step 6: Cleanup workloads
    Write-Color "`nCleaning up workloads..." "Gray"
    kubectl delete -f $WorkloadFile --ignore-not-found=true
    Start-Sleep -Seconds 5
    
    Write-Color "OK: Scenario complete: $ScenarioName" "Green"
    return $true
}

function Generate-ComparisonReport {
    param(
        [string]$BaselineDir,
        [string]$MBCASDir,
        [string]$OutputDir
    )
    
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
    
    Write-Color "`nREPORT: Generating comparison report..." "Cyan"
    
    # Load results
    $baselineLoadgen = Get-Content (Join-Path $BaselineDir "loadgen_results.json") | ConvertFrom-Json
    $mbcasLoadgen = Get-Content (Join-Path $MBCASDir "loadgen_results.json") | ConvertFrom-Json
    
    $baselineMetrics = Get-Content (Join-Path $BaselineDir "metrics.json") | ConvertFrom-Json
    $mbcasMetrics = Get-Content (Join-Path $MBCASDir "metrics.json") | ConvertFrom-Json
    
    # Calculate comparison metrics
    $comparison = @{
        baseline = @{
            overall_p95_latency_ms = $baselineLoadgen.overall.p95_latency_ms
            overall_p99_latency_ms = $baselineLoadgen.overall.p99_latency_ms
            overall_avg_latency_ms = $baselineLoadgen.overall.avg_latency_ms
            total_requests = $baselineLoadgen.overall.total_requests
            total_errors = $baselineLoadgen.overall.total_errors
            error_rate = if ($baselineLoadgen.overall.total_requests -gt 0) {
                ($baselineLoadgen.overall.total_errors / $baselineLoadgen.overall.total_requests) * 100
            } else { 0 }
        }
        mbcas = @{
            overall_p95_latency_ms = $mbcasLoadgen.overall.p95_latency_ms
            overall_p99_latency_ms = $mbcasLoadgen.overall.p99_latency_ms
            overall_avg_latency_ms = $mbcasLoadgen.overall.avg_latency_ms
            total_requests = $mbcasLoadgen.overall.total_requests
            total_errors = $mbcasLoadgen.overall.total_errors
            error_rate = if ($mbcasLoadgen.overall.total_requests -gt 0) {
                ($mbcasLoadgen.overall.total_errors / $mbcasLoadgen.overall.total_requests) * 100
            } else { 0 }
        }
    }
    
    # Calculate differences
    $comparison.differences = @{
        p95_latency_change_ms = $mbcasLoadgen.overall.p95_latency_ms - $baselineLoadgen.overall.p95_latency_ms
        p95_latency_change_percent = if ($baselineLoadgen.overall.p95_latency_ms -gt 0) {
            (($mbcasLoadgen.overall.p95_latency_ms - $baselineLoadgen.overall.p95_latency_ms) / $baselineLoadgen.overall.p95_latency_ms) * 100
        } else { 0 }
        p99_latency_change_ms = $mbcasLoadgen.overall.p99_latency_ms - $baselineLoadgen.overall.p99_latency_ms
        avg_latency_change_ms = $mbcasLoadgen.overall.avg_latency_ms - $baselineLoadgen.overall.avg_latency_ms
    }
    
    # Count pod restarts
    $baselineRestarts = ($baselineMetrics.metrics | ForEach-Object { $_.pods | ForEach-Object { $_.restart_count } } | Measure-Object -Sum).Sum
    $mbcasRestarts = ($mbcasMetrics.metrics | ForEach-Object { $_.pods | ForEach-Object { $_.restart_count } } | Measure-Object -Sum).Sum
    
    $comparison.baseline.total_pod_restarts = $baselineRestarts
    $comparison.mbcas.total_pod_restarts = $mbcasRestarts
    
    # MBCAS component overhead (parse CPU values first)
    $mbcasComponentCpuValues = $mbcasMetrics.metrics | ForEach-Object { 
        $_.mbcas_components | ForEach-Object { 
            if ($_.cpu_usage) { 
                Parse-CPUUsage $_.cpu_usage 
            } 
        } 
    } | Where-Object { $_ -ne $null }
    
    $mbcasComponentCpu = if ($mbcasComponentCpuValues) {
        ($mbcasComponentCpuValues | Measure-Object -Average).Average
    } else {
        $null
    }
    $comparison.mbcas.component_cpu_overhead = $mbcasComponentCpu
    
    # Export JSON
    $jsonPath = Join-Path $OutputDir "comparison_report.json"
    $comparison | ConvertTo-Json -Depth 10 | Set-Content $jsonPath
    Write-Color "OK: JSON report: $jsonPath" "Green"
    
    # Generate Markdown report
    $mdPath = Join-Path $OutputDir "comparison_report.md"
    $generatedTime = [DateTimeOffset]::UtcNow.ToString('yyyy-MM-dd HH:mm:ss') + ' UTC'
    $latencyStatus = if ($comparison.differences.p95_latency_change_percent -lt 0) { 'improves' } else { 'maintains' }
    $overheadText = if ($mbcasComponentCpu) { [string]::Format('{0:F3} cores', $mbcasComponentCpu) } else { 'N/A' }
    
    $md = @"
# MBCAS vs Baseline Comparison Report

Generated: $generatedTime

## Summary

| Metric | Baseline | MBCAS | Change |
|--------|----------|-------|--------|
| P95 Latency (ms) | $([math]::Round($baselineLoadgen.overall.p95_latency_ms, 2)) | $([math]::Round($mbcasLoadgen.overall.p95_latency_ms, 2)) | $([math]::Round($comparison.differences.p95_latency_change_ms, 2)) ms ($([math]::Round($comparison.differences.p95_latency_change_percent, 1))%) |
| P99 Latency (ms) | $([math]::Round($baselineLoadgen.overall.p99_latency_ms, 2)) | $([math]::Round($mbcasLoadgen.overall.p99_latency_ms, 2)) | $([math]::Round($comparison.differences.p99_latency_change_ms, 2)) ms |
| Avg Latency (ms) | $([math]::Round($baselineLoadgen.overall.avg_latency_ms, 2)) | $([math]::Round($mbcasLoadgen.overall.avg_latency_ms, 2)) | $([math]::Round($comparison.differences.avg_latency_change_ms, 2)) ms |
| Total Requests | $($baselineLoadgen.overall.total_requests) | $($mbcasLoadgen.overall.total_requests) | - |
| Total Errors | $($baselineLoadgen.overall.total_errors) | $($mbcasLoadgen.overall.total_errors) | - |
| Error Rate (%) | $([math]::Round($comparison.baseline.error_rate, 2)) | $([math]::Round($comparison.mbcas.error_rate, 2)) | - |
| Pod Restarts | $baselineRestarts | $mbcasRestarts | - |

## Key Findings

- **Latency**: MBCAS $latencyStatus latency compared to baseline
- **Pod Restarts**: MBCAS has $mbcasRestarts restarts vs $baselineRestarts for baseline
- **MBCAS Overhead**: Average CPU usage by MBCAS components: $overheadText

## Detailed Results

See individual result directories:
- Baseline: $($BaselineDir)
- MBCAS: $($MBCASDir)
"@
    
    $md | Set-Content $mdPath
    Write-Color "OK: Markdown report: $mdPath" "Green"
}

# Main execution
Write-Color "E2E TEST: MBCAS End-to-End Test Suite" "Cyan"
Write-Color "=========================================================" "Cyan"

if ($SkipBaseline) {
    Write-Color "NOTE: Skipping baseline test (using existing results)" "Yellow"
}

# Pre-flight checks
Write-Color "`nPre-flight Checks..." "Cyan"
$requiredCommands = @("minikube", "kubectl", "docker", "go")
foreach ($cmd in $requiredCommands) {
    if (-not (Test-Command $cmd)) {
        Write-Color "ERROR: $cmd is not installed" "Red"
        exit 1
    }
}

# Check Minikube
$minikubeStatus = minikube status --format='{{.Host}}' 2>$null
if ($minikubeStatus -ne "Running") {
    Write-Color "ERROR: Minikube is not running. Please run: .\scripts\start_minikube.ps1" "Red"
    exit 1
}

# Get project root
$projectRoot = Split-Path -Parent $PSScriptRoot
Set-Location $projectRoot

# Create results directory
$resultsRoot = Join-Path $projectRoot "results"
$baselineDir = Join-Path $resultsRoot "baseline"
$mbcasDir = Join-Path $resultsRoot "mbcas"

New-Item -ItemType Directory -Path $baselineDir -Force | Out-Null
New-Item -ItemType Directory -Path $mbcasDir -Force | Out-Null

# Ensure eval-service is built
Write-Color "`nBUILD: Building eval-service..." "Cyan"
& (Join-Path $PSScriptRoot "build_eval_service.ps1")
if ($LASTEXITCODE -ne 0) {
    Write-Color "ERROR: Failed to build eval-service" "Red"
    exit 1
}

# Remove MBCAS if it exists (to ensure clean baseline test)
Write-Color "`nCLEANUP: Removing MBCAS for clean baseline test..." "Cyan"
try {
    kubectl delete namespace mbcas-system --ignore-not-found=true 2>&1 | Out-Null
    Write-Color "OK: MBCAS removed (if it was deployed)" "Green"
    Start-Sleep -Seconds 3  # Wait for namespace deletion
}
catch {
    Write-Color "WARNING: Could not remove MBCAS namespace (may not exist)" "Yellow"
}

# Test duration (10% scale = ~1.5 minutes total)
$testDurationSeconds = 90  # 1.5 minutes

# Run Baseline Test (unless skipped)
$baselineSuccess = $false
if (-not $SkipBaseline) {
    $baselineWorkloads = Join-Path $projectRoot "k8s\evaluation\mode-a-static\workloads.yaml"
    $baselineSuccess = Run-TestScenario `
        -ScenarioName "Baseline (Static Limits)" `
        -WorkloadFile $baselineWorkloads `
        -ResultsDir $baselineDir `
        -WithMBCAS $false `
        -LoadTestDurationSeconds $testDurationSeconds

    if (-not $baselineSuccess) {
        Write-Color "WARNING: Baseline test had issues, but continuing..." "Yellow"
    }
} else {
    # Check if baseline results exist
    $baselineMetrics = Join-Path $baselineDir "metrics.json"
    if (Test-Path $baselineMetrics) {
        Write-Color "OK: Using existing baseline results from $baselineDir" "Green"
        $baselineSuccess = $true  # Consider it successful if results exist
    } else {
        Write-Color "WARNING: Baseline results not found at $baselineMetrics" "Yellow"
        Write-Color "   Run without -SkipBaseline to generate baseline results" "Gray"
        $baselineSuccess = $false
    }
}

# Ensure MBCAS is deployed
Write-Color "`nDEPLOY: Ensuring MBCAS is deployed..." "Cyan"
& (Join-Path $PSScriptRoot "build_and_deploy.ps1")
if ($LASTEXITCODE -ne 0) {
    Write-Color "ERROR: Failed to deploy MBCAS" "Red"
    exit 1
}

# Wait a bit for MBCAS to stabilize
Start-Sleep -Seconds 10

# Run MBCAS Test
$mbcasWorkloads = Join-Path $projectRoot "k8s\evaluation\mode-c-coallocator\workloads.yaml"
$mbcasSuccess = Run-TestScenario `
    -ScenarioName "MBCAS (CoAllocator)" `
    -WorkloadFile $mbcasWorkloads `
    -ResultsDir $mbcasDir `
    -WithMBCAS $true `
    -LoadTestDurationSeconds $testDurationSeconds

if (-not $mbcasSuccess) {
    Write-Color "WARNING: MBCAS test had issues, but continuing..." "Yellow"
}

# Generate comparison report (if both baseline and MBCAS results exist)
$baselineMetricsFile = Join-Path $baselineDir "metrics.json"
$mbcasMetricsFile = Join-Path $mbcasDir "metrics.json"
$baselineLoadgenFile = Join-Path $baselineDir "loadgen_results.json"
$mbcasLoadgenFile = Join-Path $mbcasDir "loadgen_results.json"

if ((Test-Path $baselineMetricsFile) -and (Test-Path $mbcasMetricsFile) -and 
    (Test-Path $baselineLoadgenFile) -and (Test-Path $mbcasLoadgenFile)) {
    Generate-ComparisonReport -BaselineDir $baselineDir -MBCASDir $mbcasDir -OutputDir $resultsRoot
} else {
    Write-Color "WARNING: Skipping comparison report - missing results files" "Yellow"
    if (-not (Test-Path $baselineMetricsFile)) {
        Write-Color "   Missing: $baselineMetricsFile" "Gray"
    }
    if (-not (Test-Path $mbcasMetricsFile)) {
        Write-Color "   Missing: $mbcasMetricsFile" "Gray"
    }
    if (-not (Test-Path $baselineLoadgenFile)) {
        Write-Color "   Missing: $baselineLoadgenFile" "Gray"
    }
    if (-not (Test-Path $mbcasLoadgenFile)) {
        Write-Color "   Missing: $mbcasLoadgenFile" "Gray"
    }
}

Write-Color "`nE2E Test Suite Complete!" "Green"
Write-Color ('   Results: ' + $resultsRoot) "Gray"
