# VPA vs MBCAS Comparison Test Script
# Runs comprehensive tests comparing VPA and MBCAS performance
param(
    [int]$TestDurationMinutes = 15,
    [switch]$SkipBuild = $false,
    [switch]$SkipCleanup = $false,
    [switch]$SkipPrerequisites = $false,
    [switch]$AutoDeploy = $false,
    [ValidateSet("both", "vpa-only", "mbcas-only")]
    [string]$TestMode = "both",
    [string]$VPAAutoscalerPath = "C:\Users\achra\Desktop\autoscaler\vertical-pod-autoscaler",
    [string]$MinikubeProfile = "mbcas",
    [int]$MinikubeCPUs = 4,
    [string]$MinikubeMemory = "4096"
)

$ErrorActionPreference = "Stop"
$separator = "================================================================================"

Write-Host $separator -ForegroundColor Cyan
Write-Host "  VPA vs MBCAS Comparison Test Suite" -ForegroundColor Cyan
Write-Host $separator -ForegroundColor Cyan
Write-Host ""

# Validate TestMode parameter and provide helpful error message
if ($TestMode -notin @("both", "vpa-only", "mbcas-only")) {
    Write-Host "  [ERROR] Invalid TestMode: $TestMode. Must be 'both', 'vpa-only', or 'mbcas-only'" -ForegroundColor Red
    Write-Host "  Usage: .\scripts\run-vpa-mbcas-comparison.ps1 -TestMode mbcas-only" -ForegroundColor Yellow
    Write-Host "  Note: Use -TestMode mbcas-only (not -mbcas-only)" -ForegroundColor Yellow
    throw "Invalid TestMode parameter"
}

Write-Host "  Test Mode: $TestMode" -ForegroundColor Cyan
Write-Host ""

# Resolve paths
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$ProjectRoot = Split-Path -Parent $ScriptDir
$TestDir = Join-Path $ProjectRoot "k8s\vpa-mbcas-test"
$ResultsDir = Join-Path $ProjectRoot "results\vpa-mbcas-comparison"
$Timestamp = Get-Date -Format "yyyyMMdd-HHmmss"
$TestResultsDir = Join-Path $ResultsDir $Timestamp

# Create results directory
New-Item -ItemType Directory -Force -Path $TestResultsDir | Out-Null

# ------------------------------------------------------------------------------
# Helper Functions
# ------------------------------------------------------------------------------

function Write-Step {
    param([string]$Message, [int]$Step, [int]$Total)
    Write-Host "[$Step/$Total] $Message" -ForegroundColor Yellow
}

function Wait-ForPods {
    param(
        [string]$Namespace,
        [string]$Selector,
        [int]$TimeoutSeconds = 300
    )
    
    Write-Host "  Waiting for pods with selector '$Selector'..." -ForegroundColor Gray
    $elapsed = 0
    while ($elapsed -lt $TimeoutSeconds) {
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
                Write-Host "  [OK] All pods ready" -ForegroundColor Green
                return $true
            }
        }
        Start-Sleep -Seconds 5
        $elapsed += 5
    }
    Write-Host "  [WARNING] Timeout waiting for pods" -ForegroundColor Red
    return $false
}

function Collect-PodMetrics {
    param(
        [string]$Namespace,
        [string]$TestType,
        [string]$OutputFile
    )
    
    $metrics = @{
        timestamp = (Get-Date -Format "o")
        testType = $TestType
        namespace = $Namespace
        pods = @()
    }
    
    $pods = kubectl get pods -n $Namespace -o json 2>$null | ConvertFrom-Json
    foreach ($pod in $pods.items) {
        if ($pod.metadata.labels.test -eq $TestType) {
            $podMetrics = @{
                name = $pod.metadata.name
                phase = $pod.status.phase
                restartCount = $pod.status.containerStatuses[0].restartCount
                resources = @{
                    requests = @{
                        cpu = $pod.spec.containers[0].resources.requests.cpu
                        memory = $pod.spec.containers[0].resources.requests.memory
                    }
                    limits = @{
                        cpu = $pod.spec.containers[0].resources.limits.cpu
                        memory = $pod.spec.containers[0].resources.limits.memory
                    }
                }
            }
            
            # Get CPU/memory usage from kubectl top
            try {
                $top = kubectl top pod $pod.metadata.name -n $Namespace --no-headers 2>$null
                if ($top -and ($top -is [array]) -and $top.Count -gt 0) {
                    $parts = $top[0] -split '\s+'
                    if ($parts.Length -ge 3) {
                        $podMetrics.usage = @{
                            cpu = $parts[1]
                            memory = $parts[2]
                        }
                    }
                }
                elseif ($top -and ($top -is [string]) -and $top.Trim()) {
                    $parts = $top.Trim() -split '\s+'
                    if ($parts.Length -ge 3) {
                        $podMetrics.usage = @{
                            cpu = $parts[1]
                            memory = $parts[2]
                        }
                    }
                }
            }
            catch {
                # Metrics not available yet
            }
            
            # Get container status for throttling info
            if ($pod.status.containerStatuses) {
                foreach ($containerStatus in $pod.status.containerStatuses) {
                    if ($containerStatus.name -eq $pod.spec.containers[0].name) {
                        $podMetrics.containerStatus = @{
                            ready = $containerStatus.ready
                            restartCount = $containerStatus.restartCount
                            state = $containerStatus.state
                        }
                    }
                }
            }
            
            # Try to get latency from pod annotations or labels (if exposed)
            if ($pod.metadata.annotations) {
                $latencyP50 = $pod.metadata.annotations["mbcas.io/latency-p50-ms"]
                $latencyP95 = $pod.metadata.annotations["mbcas.io/latency-p95-ms"]
                $latencyP99 = $pod.metadata.annotations["mbcas.io/latency-p99-ms"]
                if ($latencyP50 -or $latencyP95 -or $latencyP99) {
                    $podMetrics.latency = @{}
                    if ($latencyP50) { $podMetrics.latency.p50 = $latencyP50 }
                    if ($latencyP95) { $podMetrics.latency.p95 = $latencyP95 }
                    if ($latencyP99) { $podMetrics.latency.p99 = $latencyP99 }
                }
            }
            
            $metrics.pods += $podMetrics
        }
    }
    
    $metrics | ConvertTo-Json -Depth 10 | Out-File -FilePath $OutputFile -Encoding UTF8
    return $metrics
}

function Collect-VPAMetrics {
    param(
        [string]$Namespace,
        [string]$OutputFile
    )
    
    $vpaMetrics = @{
        timestamp = (Get-Date -Format "o")
        namespace = $Namespace
        vpas = @()
    }
    
    $vpas = kubectl get vpa -n $Namespace -o json 2>$null | ConvertFrom-Json
    foreach ($vpa in $vpas.items) {
        $vpaData = @{
            name = $vpa.metadata.name
            targetRef = $vpa.spec.targetRef
            updateMode = $vpa.spec.updatePolicy.updateMode
            recommendation = $vpa.status.recommendation
            conditions = $vpa.status.conditions
        }
        $vpaMetrics.vpas += $vpaData
    }
    
    $vpaMetrics | ConvertTo-Json -Depth 10 | Out-File -FilePath $OutputFile -Encoding UTF8
    return $vpaMetrics
}

function Collect-LatencyMetrics {
    param(
        [string]$Namespace,
        [string]$TestType,
        [string]$OutputFile
    )
    
    $latencyMetrics = @{
        timestamp = (Get-Date -Format "o")
        testType = $TestType
        namespace = $Namespace
        gateways = @()
    }
    
    # Try to get latency from gateway pod logs or annotations
    try {
        $gatewayPod = kubectl get pods -n $Namespace -l "test=$TestType,app=gateway" -o json 2>$null | ConvertFrom-Json
        if ($gatewayPod -and $gatewayPod.items -and $gatewayPod.items.Count -gt 0) {
            $pod = $gatewayPod.items[0]
            
            # Check annotations for latency metrics (if exposed by MBCAS agent)
            if ($pod.metadata.annotations) {
                $latencyData = @{
                    podName = $pod.metadata.name
                    p50 = $pod.metadata.annotations["mbcas.io/latency-p50-ms"]
                    p95 = $pod.metadata.annotations["mbcas.io/latency-p95-ms"]
                    p99 = $pod.metadata.annotations["mbcas.io/latency-p99-ms"]
                }
                if ($latencyData.p50 -or $latencyData.p95 -or $latencyData.p99) {
                    $latencyMetrics.gateways += $latencyData
                }
            }
            
            # Try to extract latency from recent logs (last 30 seconds)
            # This is a fallback if annotations aren't available
            try {
                $logs = kubectl logs $pod.metadata.name -n $Namespace --tail=100 --since=30s 2>$null
                # Look for latency patterns in logs (e.g., "done in 123ms")
                if ($logs) {
                    $logLines = $logs -split "`n"
                    $latencies = @()
                    foreach ($line in $logLines) {
                        if ($line -match "done in (\d+(?:\.\d+)?)(ms|µs|s)") {
                            $value = [double]$matches[1]
                            $unit = $matches[2]
                            # Convert to milliseconds
                            if ($unit -eq "s") { $value = $value * 1000 }
                            elseif ($unit -eq "µs") { $value = $value / 1000 }
                            $latencies += $value
                        }
                    }
                    if ($latencies.Count -gt 0) {
                        $latencies = $latencies | Sort-Object
                        $p50Idx = [math]::Floor($latencies.Count * 0.5)
                        $p95Idx = [math]::Floor($latencies.Count * 0.95)
                        $p99Idx = [math]::Floor($latencies.Count * 0.99)
                        
                        if (-not $latencyMetrics.gateways) {
                            $latencyMetrics.gateways += @{ podName = $pod.metadata.name }
                        }
                        $latencyMetrics.gateways[0].p50 = $latencies[$p50Idx]
                        $latencyMetrics.gateways[0].p95 = $latencies[$p95Idx]
                        $latencyMetrics.gateways[0].p99 = $latencies[$p99Idx]
                        $latencyMetrics.gateways[0].sampleCount = $latencies.Count
                    }
                }
            }
            catch {
                # Log parsing failed, continue
            }
        }
    }
    catch {
        # Gateway pod not found or query failed
    }
    
    $latencyMetrics | ConvertTo-Json -Depth 10 | Out-File -FilePath $OutputFile -Encoding UTF8
    return $latencyMetrics
}

function Collect-MBCASMetrics {
    param(
        [string]$Namespace,
        [string]$OutputFile
    )
    
    $mbcasMetrics = @{
        timestamp = (Get-Date -Format "o")
        namespace = $Namespace
        podAllocations = @()
    }
    
    $allocations = kubectl get podallocations -n $Namespace -o json 2>$null | ConvertFrom-Json
    if ($allocations -and $allocations.items) {
        foreach ($alloc in $allocations.items) {
            $allocData = @{
                name = $alloc.metadata.name
                podName = $alloc.spec.podName
                desiredCPU = $alloc.spec.desiredCPURequest
                desiredCPULimit = $alloc.spec.desiredCPULimit
                status = @{
                    appliedCPU = $alloc.status.appliedCPURequest
                    appliedCPULimit = $alloc.status.appliedCPULimit
                    phase = $alloc.status.phase
                    reason = $alloc.status.reason
                    shadowPrice = $alloc.status.shadowPriceCPU
                }
            }
            $mbcasMetrics.podAllocations += $allocData
        }
    }
    
    $mbcasMetrics | ConvertTo-Json -Depth 10 | Out-File -FilePath $OutputFile -Encoding UTF8
    return $mbcasMetrics
}

function Collect-SystemMetrics {
    param([string]$OutputFile)
    
    $systemMetrics = @{
        timestamp = (Get-Date -Format "o")
        nodes = @()
        pods = @()
    }
    
    # Node metrics
    try {
        $nodes = kubectl get nodes -o json 2>$null | ConvertFrom-Json
        if ($nodes -and $nodes.items) {
            foreach ($node in $nodes.items) {
                try {
                    # Retry kubectl top with better error handling
                    $nodeTop = $null
                    $retryCount = 0
                    $maxRetries = 3
                    while ($retryCount -lt $maxRetries -and -not $nodeTop) {
                        $nodeTop = kubectl top node $node.metadata.name --no-headers 2>&1
                        if ($LASTEXITCODE -ne 0) {
                            $nodeTop = $null
                            $retryCount++
                            if ($retryCount -lt $maxRetries) {
                                Start-Sleep -Milliseconds 500
                            }
                        }
                    }
                    
                    if ($nodeTop) {
                        $topStr = if ($nodeTop -is [array]) { $nodeTop[0] } else { $nodeTop.ToString() }
                        if ($topStr -and $topStr.Trim() -and -not ($topStr -match "error|Error|ERROR")) {
                            $parts = $topStr.Trim() -split '\s+'
                            if ($parts.Length -ge 3) {
                                $systemMetrics.nodes += @{
                                    name = $node.metadata.name
                                    cpu = $parts[1]
                                    memory = $parts[2]
                                }
                            }
                        }
                    }
                    else {
                        Write-Host "      [WARNING] Failed to get metrics for node $($node.metadata.name) after $maxRetries retries" -ForegroundColor Yellow
                    }
                }
                catch {
                    Write-Host "      [WARNING] Exception getting node metrics for $($node.metadata.name): $_" -ForegroundColor Yellow
                }
            }
        }
    }
    catch {
        Write-Host "      [WARNING] Failed to query nodes: $_" -ForegroundColor Yellow
    }
    
    # All pods in test namespace
    try {
        $pods = kubectl get pods -n vpa-mbcas-test -o json 2>$null | ConvertFrom-Json
        if ($pods -and $pods.items) {
            foreach ($pod in $pods.items) {
                try {
                    # Retry kubectl top with better error handling
                    $podTop = $null
                    $retryCount = 0
                    $maxRetries = 3
                    while ($retryCount -lt $maxRetries -and -not $podTop) {
                        $podTop = kubectl top pod $pod.metadata.name -n vpa-mbcas-test --no-headers 2>&1
                        if ($LASTEXITCODE -ne 0) {
                            $podTop = $null
                            $retryCount++
                            if ($retryCount -lt $maxRetries) {
                                Start-Sleep -Milliseconds 500
                            }
                        }
                    }
                    
                    if ($podTop) {
                        $topStr = if ($podTop -is [array]) { $podTop[0] } else { $podTop.ToString() }
                        if ($topStr -and $topStr.Trim() -and -not ($topStr -match "error|Error|ERROR")) {
                            $parts = $topStr.Trim() -split '\s+'
                            if ($parts.Length -ge 3) {
                                $systemMetrics.pods += @{
                                    name = $pod.metadata.name
                                    namespace = $pod.metadata.namespace
                                    cpu = $parts[1]
                                    memory = $parts[2]
                                }
                            }
                        }
                    }
                }
                catch {
                    # Metrics not available for this pod, continue
                }
            }
        }
        
        # Diagnostic: if no pod metrics collected, warn about metrics-server
        if ($systemMetrics.pods.Count -eq 0 -and $pods -and $pods.items -and $pods.items.Count -gt 0) {
            Write-Host "      [WARNING] No pod usage metrics collected. Check metrics-server:" -ForegroundColor Yellow
            Write-Host "        kubectl get pods -n kube-system | findstr metrics-server" -ForegroundColor Gray
            Write-Host "        kubectl top node" -ForegroundColor Gray
        }
    }
    catch {
        Write-Host "      [WARNING] Failed to query pods: $_" -ForegroundColor Yellow
    }
    
    $systemMetrics | ConvertTo-Json -Depth 10 | Out-File -FilePath $OutputFile -Encoding UTF8
    return $systemMetrics
}

# ------------------------------------------------------------------------------
# Main Test Flow
# ------------------------------------------------------------------------------

Write-Step "Checking prerequisites" 1 8

if (-not (Get-Command kubectl -ErrorAction SilentlyContinue)) {
    throw "kubectl is not installed or not in PATH"
}

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    throw "docker is not installed or not in PATH"
}

# Check and start Minikube if needed (before checking cluster connectivity)
$isMinikube = $false
$minikubeStarted = $false

if (Get-Command minikube -ErrorAction SilentlyContinue) {
    Write-Host "  Checking Minikube status..." -ForegroundColor Gray
    
    # First, quickly check if we can connect to a cluster (fastest check)
    $anyProfileRunning = $false
    try {
        $nodes = kubectl get nodes -o json 2>$null | ConvertFrom-Json
        if ($nodes -and $nodes.items) {
            foreach ($node in $nodes.items) {
                if ($node.metadata.name -match "minikube") {
                    $isMinikube = $true
                    $anyProfileRunning = $true
                    Write-Host "    [OK] Minikube cluster is already running" -ForegroundColor Green
                    break
                }
            }
        }
    }
    catch {
        # No cluster connection yet
    }
    
    # If cluster check didn't find Minikube, check profile status
    if (-not $anyProfileRunning) {
        try {
            $profileStatus = minikube status -p $MinikubeProfile 2>&1 | Out-String
            if ($LASTEXITCODE -eq 0 -and $profileStatus -match "Running|host: Running") {
                $anyProfileRunning = $true
                $isMinikube = $true
                Write-Host "    [OK] Minikube profile '$MinikubeProfile' is running" -ForegroundColor Green
            }
        }
        catch {
            # Profile doesn't exist or not running
        }
    }
    
    # Only start if not running
    if (-not $anyProfileRunning) {
        Write-Host "    [INFO] Minikube not running, starting..." -ForegroundColor Yellow
        
        # Check if profile exists
        $profileExists = $false
        try {
            $profileList = minikube profile list 2>&1 | Out-String
            if ($LASTEXITCODE -eq 0) {
                $profileExists = $profileList -match $MinikubeProfile
            }
        }
        catch {
            # No profiles exist yet
        }
        
        if ($profileExists) {
            Write-Host "      Starting existing profile '$MinikubeProfile'..." -ForegroundColor Gray
        }
        else {
            Write-Host "      Creating and starting profile '$MinikubeProfile'..." -ForegroundColor Gray
        }
        
        # Start Minikube with required configuration
        # Ignore registry connection warnings (they're normal and don't affect functionality)
        Write-Host "      Starting Minikube (this may take a few minutes, ignoring registry warnings)..." -ForegroundColor Gray
        try {
            $ErrorActionPreference = 'SilentlyContinue'
            $startResult = & minikube start `
            -p $MinikubeProfile `
            --driver=docker `
            --cpus=$MinikubeCPUs `
            --memory=$MinikubeMemory `
            --kubernetes-version=latest `
            --extra-config=apiserver.feature-gates=InPlacePodVerticalScaling=true `
            --addons=metrics-server `
            2>$null
            $ErrorActionPreference = 'Continue'
        }
        catch {
            # Ignore registry connection errors - they don't affect functionality
            $ErrorActionPreference = 'Continue'
        }
            
        # Check if minikube actually started successfully (ignore registry connection warnings)
        # Wait a moment for minikube to stabilize
        Start-Sleep -Seconds 3
        $minikubeStatus = minikube status -p $MinikubeProfile 2>&1 | Out-String
        
        if ($minikubeStatus -match "Running|host: Running") {
            Write-Host "      [OK] Minikube started successfully" -ForegroundColor Green
            $isMinikube = $true
            $minikubeStarted = $true
            
            # Verify metrics-server
            Start-Sleep -Seconds 5
            $addonsList = minikube addons list -p $MinikubeProfile
            $metricsEnabled = ($addonsList | Select-String 'metrics-server.*enabled') -ne $null
            if (-not $metricsEnabled) {
                Write-Host "      Enabling metrics-server addon..." -ForegroundColor Gray
                minikube addons enable metrics-server -p $MinikubeProfile | Out-Null
            }
        }
        else {
            Write-Host "      [ERROR] Failed to start Minikube" -ForegroundColor Red
            Write-Host "        Troubleshooting:" -ForegroundColor Yellow
            Write-Host "          - Check Docker is running" -ForegroundColor Gray
            Write-Host "          - Try: minikube delete -p $MinikubeProfile" -ForegroundColor Gray
            throw "Minikube startup failed"
        }
    }
}
else {
    Write-Host "  [INFO] minikube command not found, assuming non-Minikube cluster" -ForegroundColor Gray
}

# Check Kubernetes cluster connectivity (after Minikube is started if needed)
Write-Host "  Checking Kubernetes cluster connectivity..." -ForegroundColor Gray
try {
    $clusterInfo = kubectl cluster-info 2>&1 | Out-String
    if ($LASTEXITCODE -eq 0) {
        Write-Host "    [OK] Cluster accessible" -ForegroundColor Green
    }
    else {
        Write-Host "    [WARNING] Cannot connect to Kubernetes cluster (will continue anyway)" -ForegroundColor Yellow
    }
}
catch {
    Write-Host "    [WARNING] Cluster connectivity check failed (will continue anyway): $_" -ForegroundColor Yellow
}

# Track which systems were auto-deployed for cleanup
$vpaAutoDeployed = $false
$mbcasAutoDeployed = $false

# Note: VPA and MBCAS checks/deployments are now done right before each phase runs
# This ensures we only deploy what's needed, when it's needed

# Check VPA components (only if needed for test mode) - DEPRECATED, moved to phase execution
if ($false -and ($TestMode -eq "both" -or $TestMode -eq "vpa-only")) {
    Write-Host "  Checking VPA components..." -ForegroundColor Gray
    try {
        $vpaCRD = kubectl get crd verticalpodautoscalers.autoscaling.k8s.io 2>&1 | Out-Null
        if ($LASTEXITCODE -eq 0) {
            Write-Host "    [OK] VPA CRD found" -ForegroundColor Green
            
            # Check for VPA pods
            $vpaPods = kubectl get pods -A -l app=vpa-recommender 2>&1 | Out-Null
            if ($LASTEXITCODE -eq 0) {
                Write-Host "    [OK] VPA components appear to be installed" -ForegroundColor Green
            }
            else {
                Write-Host "    [WARNING] VPA CRD exists but VPA pods not found" -ForegroundColor Yellow
                Write-Host "      VPA may not function correctly" -ForegroundColor Yellow
            }
        }
        else {
            Write-Host "    [ERROR] VPA CRD not found" -ForegroundColor Red
            
            if ($AutoDeploy) {
                Write-Host "      Auto-deploying VPA..." -ForegroundColor Yellow
                try {
                    if (-not (Test-Path $VPAAutoscalerPath)) {
                        throw "VPA autoscaler path not found: $VPAAutoscalerPath"
                    }
                    
                    if (-not (Get-Command wsl -ErrorAction SilentlyContinue)) {
                        throw "WSL not found. VPA deployment requires WSL."
                    }
                    
                    # Convert Windows path to WSL path
                    # Docker Desktop uses /tmp/docker-desktop-root/run/desktop/mnt/host/c
                    # Also try /mnt/host/c and /mnt/c
                    $wslPathDockerFull = $VPAAutoscalerPath -replace '\\', '/' -replace 'C:', '/tmp/docker-desktop-root/run/desktop/mnt/host/c' -replace ':', ''
                    $wslPathDocker = $VPAAutoscalerPath -replace '\\', '/' -replace 'C:', '/mnt/host/c' -replace ':', ''
                    $wslPathStandard = $VPAAutoscalerPath -replace '\\', '/' -replace 'C:', '/mnt/c' -replace ':', ''
                    
                    # Test which path works
                    $testResult = & wsl bash -c "if test -d '$wslPathDockerFull'; then echo 'docker-full'; elif test -d '$wslPathDocker'; then echo 'docker'; elif test -d '$wslPathStandard'; then echo 'standard'; else echo 'none'; fi" 2>&1
                    if ($testResult -match "docker-full") {
                        $wslPath = $wslPathDockerFull
                    }
                    elseif ($testResult -match "docker") {
                        $wslPath = $wslPathDocker
                    }
                    elseif ($testResult -match "standard") {
                        $wslPath = $wslPathStandard
                    }
                    else {
                        # Default to Docker Desktop full path (most common for Docker Desktop)
                        $wslPath = $wslPathDockerFull
                    }
                    
                    # Run gencerts.sh first
                    Write-Host "        Generating VPA certificates..." -ForegroundColor Gray
                    $gencertsScript = Join-Path $VPAAutoscalerPath "pkg\admission-controller\gencerts.sh"
                    if (-not (Test-Path $gencertsScript)) {
                        throw "gencerts.sh not found at $gencertsScript"
                    }
                    
                    $gencertsWslPath = "$wslPath/pkg/admission-controller/gencerts.sh"
                    
                    # Verify script exists in WSL
                    Write-Host "          Verifying script exists..." -ForegroundColor Gray
                    $scriptExists = & wsl bash -c "test -f '$gencertsWslPath' && echo 'exists' || echo 'not found'" 2>&1
                    if ($scriptExists -notmatch "exists") {
                        throw "gencerts.sh not found at WSL path: $gencertsWslPath (Windows path: $gencertsScript)"
                    }
                    
                    # Prepare script - follow exact sequence: chmod, then dos2unix, then run with bash
                    Write-Host "          Preparing gencerts.sh (chmod +x)..." -ForegroundColor Gray
                    & wsl bash -c "chmod +x '$gencertsWslPath'" 2>&1 | Out-Null
                    
                    Write-Host "          Fixing line endings (dos2unix)..." -ForegroundColor Gray
                    & wsl bash -c "dos2unix '$gencertsWslPath' 2>/dev/null || sed -i 's/\r$//' '$gencertsWslPath' || true" 2>&1 | Out-Null
                    
                    # Run gencerts.sh using WSL with explicit bash (bypasses shebang line-ending issues)
                    Write-Host "          Running gencerts.sh with bash..." -ForegroundColor Gray
                    $gencertsOutput = & wsl bash -c "cd '$wslPath/pkg/admission-controller' && bash gencerts.sh" 2>&1
                    
                    if ($LASTEXITCODE -ne 0) {
                        Write-Host "          gencerts.sh output: $gencertsOutput" -ForegroundColor Yellow
                        throw "gencerts.sh failed with exit code $LASTEXITCODE. Check if the script exists and is executable."
                    }
                    
                    # Run vpa-up.sh
                    Write-Host "        Deploying VPA components..." -ForegroundColor Gray
                    $vpaUpScript = Join-Path $VPAAutoscalerPath "hack\vpa-up.sh"
                    if (-not (Test-Path $vpaUpScript)) {
                        throw "vpa-up.sh not found at $vpaUpScript"
                    }
                    
                    $vpaUpWslPath = "$wslPath/hack/vpa-up.sh"
                    
                    # Verify script exists in WSL
                    Write-Host "          Verifying script exists..." -ForegroundColor Gray
                    $scriptExists = & wsl bash -c "test -f '$vpaUpWslPath' && echo 'exists' || echo 'not found'" 2>&1
                    if ($scriptExists -notmatch "exists") {
                        throw "vpa-up.sh not found at WSL path: $vpaUpWslPath (Windows path: $vpaUpScript)"
                    }
                    
                    # Prepare script - follow exact sequence: chmod, then dos2unix, then run with bash
                    Write-Host "          Preparing vpa-up.sh (chmod +x)..." -ForegroundColor Gray
                    & wsl bash -c "chmod +x '$vpaUpWslPath'" 2>&1 | Out-Null
                    
                    Write-Host "          Fixing line endings (dos2unix)..." -ForegroundColor Gray
                    & wsl bash -c "dos2unix '$vpaUpWslPath' 2>/dev/null || sed -i 's/\r$//' '$vpaUpWslPath' || true" 2>&1 | Out-Null
                    
                    # Run vpa-up.sh using WSL with explicit bash (bypasses shebang line-ending issues)
                    Write-Host "          Running vpa-up.sh with bash..." -ForegroundColor Gray
                    $vpaUpOutput = & wsl bash -c "cd '$wslPath/hack' && bash vpa-up.sh" 2>&1
                    
                    if ($LASTEXITCODE -ne 0) {
                        Write-Host "          vpa-up.sh output: $vpaUpOutput" -ForegroundColor Yellow
                        throw "vpa-up.sh failed with exit code $LASTEXITCODE. Check if the script exists and is executable."
                    }
                    
                    # Wait for VPA to be ready
                    Write-Host "        Waiting for VPA components to be ready..." -ForegroundColor Gray
                    Start-Sleep -Seconds 10
                    
                    # Verify VPA is deployed
                    $vpaCheck = kubectl get crd verticalpodautoscalers.autoscaling.k8s.io 2>&1 | Out-Null
                    if ($LASTEXITCODE -eq 0) {
                        Write-Host "      [OK] VPA deployed successfully" -ForegroundColor Green
                        $vpaInstalled = $true
                        $vpaRunning = $true
                        $vpaAutoDeployed = $true
                    }
                    else {
                        throw "VPA CRD not found after deployment"
                    }
                }
                catch {
                    Write-Host "      [ERROR] Failed to auto-deploy VPA: $_" -ForegroundColor Red
                    if (-not $SkipPrerequisites) {
                        throw "VPA auto-deployment failed. Install VPA manually or use -SkipPrerequisites to continue anyway."
                    }
                }
            }
            else {
                Write-Host "      VPA must be installed before running this test" -ForegroundColor Yellow
                Write-Host "      Install VPA: https://github.com/kubernetes/autoscaler/tree/master/vertical-pod-autoscaler" -ForegroundColor Gray
                Write-Host "      Or use -AutoDeploy to deploy automatically" -ForegroundColor Gray
                if (-not $SkipPrerequisites) {
                    throw "VPA is not installed. Install VPA, use -AutoDeploy, or use -SkipPrerequisites to continue anyway."
                }
            }
        }
    }
    catch {
        Write-Host "    [WARNING] Could not verify VPA installation" -ForegroundColor Yellow
        if (-not $SkipPrerequisites) {
            Write-Host "      Use -SkipPrerequisites to continue anyway" -ForegroundColor Gray
        }
    }
}
else {
    Write-Host "  Skipping VPA check (not needed for $TestMode mode)" -ForegroundColor Gray
}

# Check MBCAS components (only if needed for test mode) - DEPRECATED, moved to phase execution
if ($false -and ($TestMode -eq "both" -or $TestMode -eq "mbcas-only")) {
    Write-Host "  Checking MBCAS components..." -ForegroundColor Gray
    try {
        $mbcasCRD = kubectl get crd podallocations.allocation.mbcas.io 2>&1 | Out-Null
        if ($LASTEXITCODE -eq 0) {
            Write-Host "    [OK] MBCAS CRD found" -ForegroundColor Green
            
            # Check for MBCAS pods
            $mbcasController = kubectl get pods -n mbcas-system -l app.kubernetes.io/component=controller 2>&1 | Out-Null
            $mbcasAgent = kubectl get pods -n mbcas-system -l app.kubernetes.io/component=agent 2>&1 | Out-Null
            if ($LASTEXITCODE -eq 0) {
                Write-Host "    [OK] MBCAS components appear to be deployed" -ForegroundColor Green
            }
            else {
                Write-Host "    [WARNING] MBCAS CRD exists but MBCAS pods not found" -ForegroundColor Yellow
                Write-Host "      MBCAS may not function correctly" -ForegroundColor Yellow
            }
        }
        else {
            Write-Host "    [ERROR] MBCAS CRD not found" -ForegroundColor Red
            
            if ($AutoDeploy) {
                Write-Host "      Auto-deploying MBCAS..." -ForegroundColor Yellow
                try {
                # Build MBCAS images
                Write-Host "        Building MBCAS Docker images..." -ForegroundColor Gray
                Push-Location $ProjectRoot
                docker build -f Dockerfile.controller -t mbcas-controller:latest . 2>&1 | Out-Null
                if ($LASTEXITCODE -ne 0) {
                    throw "Failed to build mbcas-controller image"
                }
                docker build -f Dockerfile.agent -t mbcas-agent:latest . 2>&1 | Out-Null
                if ($LASTEXITCODE -ne 0) {
                    throw "Failed to build mbcas-agent image"
                }
                Pop-Location
                
                # Load images into minikube
                if ($isMinikube -and (Get-Command minikube -ErrorAction SilentlyContinue)) {
                    minikube image load mbcas-controller:latest -p $MinikubeProfile 2>&1 | Out-Null
                    minikube image load mbcas-agent:latest -p $MinikubeProfile 2>&1 | Out-Null
                }
                
                # Apply Kubernetes manifests
                Write-Host "        Applying MBCAS Kubernetes manifests..." -ForegroundColor Gray
                $k8sDir = Join-Path (Join-Path $ProjectRoot "k8s") "mbcas"
                
                kubectl apply -f (Join-Path $k8sDir "allocation.mbcas.io_podallocations.yaml") 2>&1 | Out-Null
                Start-Sleep -Seconds 3
                
                kubectl apply -f (Join-Path $k8sDir "namespace.yaml") 2>&1 | Out-Null
                kubectl apply -f (Join-Path $k8sDir "service_account.yaml") 2>&1 | Out-Null
                kubectl apply -f (Join-Path $k8sDir "role.yaml") 2>&1 | Out-Null
                kubectl apply -f (Join-Path $k8sDir "role_binding.yaml") 2>&1 | Out-Null
                kubectl apply -f (Join-Path $k8sDir "agent-rbac.yaml") 2>&1 | Out-Null
                
                $configMapPath = Join-Path (Join-Path (Join-Path $ProjectRoot "config") "agent") "configmap.yaml"
                if (Test-Path $configMapPath) {
                    kubectl apply -f $configMapPath 2>&1 | Out-Null
                }
                
                kubectl apply -f (Join-Path $k8sDir "controller-deployment.yaml") 2>&1 | Out-Null
                kubectl apply -f (Join-Path $k8sDir "agent-daemonset.yaml") 2>&1 | Out-Null
                
                # Wait for MBCAS to be ready
                Write-Host "        Waiting for MBCAS components to be ready..." -ForegroundColor Gray
                kubectl wait --for=condition=available --timeout=120s deployment/mbcas-controller -n mbcas-system 2>&1 | Out-Null
                kubectl wait --for=condition=ready --timeout=120s daemonset/mbcas-agent -n mbcas-system 2>&1 | Out-Null
                
                    Write-Host "      [OK] MBCAS deployed successfully" -ForegroundColor Green
                    $mbcasInstalled = $true
                    $mbcasRunning = $true
                    $mbcasAutoDeployed = $true
                }
                catch {
                    Write-Host "      [ERROR] Failed to auto-deploy MBCAS: $_" -ForegroundColor Red
                    if (-not $SkipPrerequisites) {
                        throw "MBCAS auto-deployment failed. Deploy manually: .\scripts\setup-minikube-mbcas.ps1"
                    }
                }
            }
            else {
                Write-Host "      MBCAS must be deployed before running this test" -ForegroundColor Yellow
                Write-Host "      Deploy MBCAS: .\scripts\setup-minikube-mbcas.ps1" -ForegroundColor Gray
                Write-Host "      Or use -AutoDeploy to deploy automatically" -ForegroundColor Gray
                if (-not $SkipPrerequisites) {
                    throw "MBCAS is not deployed. Deploy MBCAS, use -AutoDeploy, or use -SkipPrerequisites to continue anyway."
                }
            }
        }
    }
    catch {
        Write-Host "    [WARNING] Could not verify MBCAS deployment" -ForegroundColor Yellow
        if (-not $SkipPrerequisites) {
            Write-Host "      Use -SkipPrerequisites to continue anyway" -ForegroundColor Gray
        }
    }
}
else {
    Write-Host "  Skipping MBCAS check (not needed for $TestMode mode)" -ForegroundColor Gray
}

# Check metrics-server
Write-Host "  Checking metrics-server..." -ForegroundColor Gray
try {
    $metricsServer = kubectl get deployment metrics-server -n kube-system 2>&1 | Out-Null
    if ($LASTEXITCODE -eq 0) {
        Write-Host "    [OK] metrics-server found" -ForegroundColor Green
    }
    else {
        Write-Host "    [WARNING] metrics-server not found" -ForegroundColor Yellow
        Write-Host "      Resource usage metrics may not be available" -ForegroundColor Yellow
    }
}
catch {
    Write-Host "    [WARNING] Could not verify metrics-server" -ForegroundColor Yellow
}

Write-Host "  [OK] Prerequisites check complete" -ForegroundColor Green
Write-Host ""

# ------------------------------------------------------------------------------
# Build eval-service
# ------------------------------------------------------------------------------
Write-Step "Building eval-service image" 2 8

if (-not $SkipBuild) {
    $evalServiceDir = Join-Path $ProjectRoot "apps\eval-service"
    Push-Location $evalServiceDir
    
    try {
        # Suppress Docker build output (progress messages go to stderr)
        # Use *>$null to suppress all output streams
        docker build -t eval-service:latest . *>$null
        if ($LASTEXITCODE -ne 0) {
            throw "Failed to build eval-service image (exit code: $LASTEXITCODE)"
        }
        if ($LASTEXITCODE -ne 0) {
            throw "Failed to build eval-service image"
        }
        
        # Load into minikube (if using minikube)
        if ($isMinikube -and (Get-Command minikube -ErrorAction SilentlyContinue)) {
            minikube image load eval-service:latest -p $MinikubeProfile 2>&1 | Out-Null
            Write-Host "  [OK] eval-service image built and loaded into Minikube" -ForegroundColor Green
        }
        else {
            Write-Host "  [OK] eval-service image built" -ForegroundColor Green
            if ($isMinikube) {
                Write-Host "    [WARNING] Could not load image into Minikube (minikube command not found)" -ForegroundColor Yellow
                Write-Host "      You may need to manually load the image or configure image pull" -ForegroundColor Gray
            }
        }
    }
    finally {
        Pop-Location
    }
}
else {
    Write-Host "  [SKIP] Skipping build (using existing image)" -ForegroundColor Yellow
}
Write-Host ""

# ------------------------------------------------------------------------------
# Create namespace
# ------------------------------------------------------------------------------
Write-Step "Creating test namespace" 3 8

kubectl apply -f (Join-Path $TestDir "namespace.yaml") 2>&1 | Out-Null
Start-Sleep -Seconds 2
Write-Host "  [OK] Namespace created" -ForegroundColor Green
Write-Host ""

# ------------------------------------------------------------------------------
# Function to run a single test phase
# ------------------------------------------------------------------------------
function Run-TestPhase {
    param(
        [string]$PhaseName,
        [string]$TestType,
        [string]$WorkloadFile,
        [string]$ConfigFile = $null,
        [int]$StabilizationSeconds = 30,
        [switch]$NeedsVPA = $false,
        [switch]$NeedsMBCAS = $false
    )
    
    $phaseResults = @{
        phase = $PhaseName
        testType = $TestType
        collections = @()
        initial = $null
        final = $null
    }
    
    Write-Host ""
    Write-Host $separator -ForegroundColor Cyan
    Write-Host "  PHASE: $PhaseName" -ForegroundColor Cyan
    Write-Host $separator -ForegroundColor Cyan
    Write-Host ""
    
    # Deploy workloads
    Write-Host "  Deploying $PhaseName workloads..." -ForegroundColor Yellow
    kubectl apply -f (Join-Path $TestDir $WorkloadFile) 2>&1 | Out-Null
    
    if ($ConfigFile) {
        kubectl apply -f (Join-Path $TestDir $ConfigFile) 2>&1 | Out-Null
    }
    
    Wait-ForPods -Namespace "vpa-mbcas-test" -Selector "test=$TestType" -TimeoutSeconds 180
    Write-Host "  [OK] $PhaseName workloads deployed" -ForegroundColor Green
    
    # Wait for system to stabilize
    if ($StabilizationSeconds -gt 0) {
        Write-Host "  Waiting for system to stabilize (${StabilizationSeconds}s)..." -ForegroundColor Gray
        Start-Sleep -Seconds $StabilizationSeconds
    }
    
    # Collect initial metrics
    Write-Host "  Collecting initial metrics..." -ForegroundColor Gray
    $phaseResults.initial = Collect-PodMetrics -Namespace "vpa-mbcas-test" -TestType $TestType -OutputFile (Join-Path $TestResultsDir "$TestType-initial.json")
    
    if ($NeedsVPA) {
        $vpaRecs = Collect-VPAMetrics -Namespace "vpa-mbcas-test" -OutputFile (Join-Path $TestResultsDir "$TestType-vpa-recommendations-initial.json")
        $phaseResults.initial.vpaRecommendations = $vpaRecs
    }
    
    if ($NeedsMBCAS) {
        $mbcasAllocs = Collect-MBCASMetrics -Namespace "vpa-mbcas-test" -OutputFile (Join-Path $TestResultsDir "$TestType-mbcas-allocations-initial.json")
        $phaseResults.initial.mbcasAllocations = $mbcasAllocs
    }
    
    # Deploy stress generator (phase-specific)
    Write-Host "  Deploying stress generator for $PhaseName..." -ForegroundColor Gray
    
    # Create phase-specific stress generator YAML from template
    $templateFile = Join-Path $TestDir "stress-generator-template.yaml"
    if (-not (Test-Path $templateFile)) {
        Write-Host "    [ERROR] Stress generator template not found: $templateFile" -ForegroundColor Red
        throw "Stress generator template is required. Cannot use fallback that targets all gateways (would under-drive load for single-test-type runs)."
    }
    
    $templateContent = Get-Content $templateFile -Raw
    $yamlContent = $templateContent -replace '\{\{TEST_TYPE\}\}', $TestType -replace '\{\{PHASE_NAME\}\}', $PhaseName
    $stressGeneratorFile = Join-Path $TestResultsDir "stress-generator-$TestType.yaml"
    $yamlContent | Out-File -FilePath $stressGeneratorFile -Encoding UTF8
    kubectl apply -f $stressGeneratorFile 2>&1 | Out-Null
    Start-Sleep -Seconds 10  # Wait for stress generator to start
    
    # Run test and collect metrics
    Write-Host "  Running stress test ($TestDurationMinutes minutes)..." -ForegroundColor Yellow
    $collectionInterval = 30 # seconds
    $totalCollections = [math]::Floor($TestDurationMinutes * 60 / $collectionInterval)
    
    for ($i = 0; $i -lt $totalCollections; $i++) {
        $collectionTime = Get-Date -Format "o"
        Write-Host "    Collection $($i+1)/$totalCollections..." -ForegroundColor Gray
        
        $collection = @{
            timestamp = $collectionTime
            iteration = $i + 1
            pods = Collect-PodMetrics -Namespace "vpa-mbcas-test" -TestType $TestType -OutputFile (Join-Path $TestResultsDir "$TestType-collection-$($i+1).json")
            system = Collect-SystemMetrics -OutputFile (Join-Path $TestResultsDir "$TestType-system-$($i+1).json")
        }
        
        if ($NeedsVPA) {
            $collection.vpaRecommendations = Collect-VPAMetrics -Namespace "vpa-mbcas-test" -OutputFile (Join-Path $TestResultsDir "$TestType-vpa-recommendations-$($i+1).json")
        }
        
        if ($NeedsMBCAS) {
            $collection.mbcasAllocations = Collect-MBCASMetrics -Namespace "vpa-mbcas-test" -OutputFile (Join-Path $TestResultsDir "$TestType-mbcas-allocations-$($i+1).json")
        }
        
        $phaseResults.collections += $collection
        
        if ($i -lt ($totalCollections - 1)) {
            Start-Sleep -Seconds $collectionInterval
        }
    }
    
    # Collect final metrics
    Write-Host "  Collecting final metrics..." -ForegroundColor Gray
    $phaseResults.final = Collect-PodMetrics -Namespace "vpa-mbcas-test" -TestType $TestType -OutputFile (Join-Path $TestResultsDir "$TestType-final.json")
    $phaseResults.final.latency = Collect-LatencyMetrics -Namespace "vpa-mbcas-test" -TestType $TestType -OutputFile (Join-Path $TestResultsDir "$TestType-latency-final.json")
    
    if ($NeedsVPA) {
        $vpaRecs = Collect-VPAMetrics -Namespace "vpa-mbcas-test" -OutputFile (Join-Path $TestResultsDir "$TestType-vpa-recommendations-final.json")
        $phaseResults.final.vpaRecommendations = $vpaRecs
    }
    
    if ($NeedsMBCAS) {
        $mbcasAllocs = Collect-MBCASMetrics -Namespace "vpa-mbcas-test" -OutputFile (Join-Path $TestResultsDir "$TestType-mbcas-allocations-final.json")
        $phaseResults.final.mbcasAllocations = $mbcasAllocs
    }
    
    # Cleanup workloads and stress generator
    Write-Host "  Cleaning up $PhaseName workloads..." -ForegroundColor Yellow
    kubectl delete -f (Join-Path $TestDir $WorkloadFile) --ignore-not-found=true 2>&1 | Out-Null
    if ($ConfigFile) {
        kubectl delete -f (Join-Path $TestDir $ConfigFile) --ignore-not-found=true 2>&1 | Out-Null
    }
    kubectl delete pod stress-generator-$TestType -n vpa-mbcas-test --ignore-not-found=true 2>&1 | Out-Null
    
    # Wait for cleanup
    Start-Sleep -Seconds 10
    Write-Host "  [OK] $PhaseName phase complete and cleaned up" -ForegroundColor Green
    
    return $phaseResults
}

# ------------------------------------------------------------------------------
# Run tests sequentially (VPA vs MBCAS)
# ------------------------------------------------------------------------------
$allPhaseResults = @{}

# Phase 1: VPA
if ($TestMode -eq "both" -or $TestMode -eq "vpa-only") {
    Write-Step "Checking and deploying VPA (if needed)" 4 8
    Write-Host "  TestMode: $TestMode (VPA phase will run)" -ForegroundColor Gray
    # Check VPA right before we need it
    $vpaCRD = kubectl get crd verticalpodautoscalers.autoscaling.k8s.io 2>&1 | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Write-Host "  VPA not found, deploying..." -ForegroundColor Yellow
        if ($AutoDeploy) {
            # Deploy VPA (reuse the deployment logic from prerequisites, but only when needed)
            & {
                $script:VPAAutoscalerPath = $VPAAutoscalerPath
                $script:SkipPrerequisites = $SkipPrerequisites
                $script:MinikubeProfile = $MinikubeProfile
                $script:isMinikube = $true
                
                if (-not (Test-Path $VPAAutoscalerPath)) {
                    throw "VPA autoscaler path not found: $VPAAutoscalerPath"
                }
                
                if (-not (Get-Command wsl -ErrorAction SilentlyContinue)) {
                    throw "WSL not found. VPA deployment requires WSL."
                }
                
                # Convert Windows path to WSL path
                # Docker Desktop uses /tmp/docker-desktop-root/run/desktop/mnt/host/c
                # Also try /mnt/host/c and /mnt/c
                $wslPathDockerFull = $VPAAutoscalerPath -replace '\\', '/' -replace 'C:', '/tmp/docker-desktop-root/run/desktop/mnt/host/c' -replace ':', ''
                $wslPathDocker = $VPAAutoscalerPath -replace '\\', '/' -replace 'C:', '/mnt/host/c' -replace ':', ''
                $wslPathStandard = $VPAAutoscalerPath -replace '\\', '/' -replace 'C:', '/mnt/c' -replace ':', ''
                
                # Initialize wslPath to default (will be overridden if test succeeds)
                $wslPath = $wslPathDockerFull
                
                # Test which path works
                $testResult = & wsl bash -c "if test -d '$wslPathDockerFull'; then echo 'docker-full'; elif test -d '$wslPathDocker'; then echo 'docker'; elif test -d '$wslPathStandard'; then echo 'standard'; else echo 'none'; fi" 2>&1
                if ($testResult -match "docker-full") {
                    $wslPath = $wslPathDockerFull
                    Write-Host "      Using Docker Desktop full path: $wslPath" -ForegroundColor Gray
                }
                elseif ($testResult -match "docker") {
                    $wslPath = $wslPathDocker
                    Write-Host "      Using Docker Desktop path: $wslPath" -ForegroundColor Gray
                }
                elseif ($testResult -match "standard") {
                    $wslPath = $wslPathStandard
                    Write-Host "      Using standard WSL path: $wslPath" -ForegroundColor Gray
                }
                else {
                    # Try to find the actual path by checking common locations
                    Write-Host "      Attempting to find correct WSL path..." -ForegroundColor Yellow
                    $foundPath = & wsl bash -c "for base in '/tmp/docker-desktop-root/run/desktop/mnt/host/c' '/mnt/host/c' '/mnt/c'; do test -d \"`$base/Users/achra/Desktop/autoscaler/vertical-pod-autoscaler\" && echo \"`$base\" && break; done" 2>&1
                    $foundPathStr = ($foundPath | Out-String).Trim()
                    if ($foundPathStr -and $foundPathStr -notmatch "error|not found|:" -and $foundPathStr.StartsWith('/')) {
                        $basePath = $foundPathStr
                        $wslPath = $VPAAutoscalerPath -replace '\\', '/' -replace 'C:', $basePath -replace ':', ''
                        Write-Host "      Found WSL path: $wslPath" -ForegroundColor Green
                    }
                    else {
                        # Default to Docker Desktop full path (most common for Docker Desktop)
                        $wslPath = $wslPathDockerFull
                        Write-Host "      Defaulting to Docker Desktop full path: $wslPath" -ForegroundColor Yellow
                    }
                }
                
                # Verify wslPath is set
                if (-not $wslPath -or $wslPath -eq "") {
                    Write-Host "      [ERROR] wslPath is empty. Test results: $testResult" -ForegroundColor Red
                    Write-Host "      docker-full: $wslPathDockerFull" -ForegroundColor Yellow
                    Write-Host "      docker: $wslPathDocker" -ForegroundColor Yellow
                    Write-Host "      standard: $wslPathStandard" -ForegroundColor Yellow
                    throw "Failed to determine WSL path for VPA autoscaler. Tried: docker-full=$wslPathDockerFull, docker=$wslPathDocker, standard=$wslPathStandard"
                }
                
                Write-Host "    Generating VPA certificates..." -ForegroundColor Gray
                Write-Host "      Using WSL path: $wslPath" -ForegroundColor Gray
                $gencertsScript = Join-Path $VPAAutoscalerPath "pkg\admission-controller\gencerts.sh"
                if (-not (Test-Path $gencertsScript)) {
                    throw "gencerts.sh not found at Windows path: $gencertsScript"
                }
                
                $gencertsWslPath = "$wslPath/pkg/admission-controller/gencerts.sh"
                
                # Verify script exists in WSL
                Write-Host "      Verifying script exists in WSL..." -ForegroundColor Gray
                $scriptExists = & wsl bash -c "test -f '$gencertsWslPath' && echo 'exists' || echo 'not found'" 2>&1
                if ($scriptExists -notmatch "exists") {
                    throw "gencerts.sh not found at WSL path: $gencertsWslPath (Windows path: $gencertsScript). WSL path used: $wslPath"
                }
                
                # Prepare script - follow exact sequence: chmod, then dos2unix, then run with bash
                Write-Host "      Preparing script (chmod +x)..." -ForegroundColor Gray
                & wsl bash -c "chmod +x '$gencertsWslPath'" 2>&1 | Out-Null
                
                Write-Host "      Fixing line endings (dos2unix)..." -ForegroundColor Gray
                & wsl bash -c "dos2unix '$gencertsWslPath' 2>/dev/null || sed -i 's/\r$//' '$gencertsWslPath' || true" 2>&1 | Out-Null
                
                # Run gencerts.sh using WSL with explicit bash (bypasses shebang line-ending issues)
                # Use Windows kubectl.exe from WSL to avoid kubeconfig path issues
                Write-Host "      Running gencerts.sh with bash..." -ForegroundColor Gray
                $kubectlExePath = (Get-Command kubectl -ErrorAction SilentlyContinue).Source
                if ($kubectlExePath) {
                    # Find the correct WSL path for kubectl.exe
                    # Try different mount points
                    $kubectlPaths = @(
                        ($kubectlExePath -replace '\\', '/') -replace 'C:', '/tmp/docker-desktop-root/run/desktop/mnt/host/c' -replace ':', '',
                        ($kubectlExePath -replace '\\', '/') -replace 'C:', '/mnt/host/c' -replace ':', '',
                        ($kubectlExePath -replace '\\', '/') -replace 'C:', '/mnt/c' -replace ':', ''
                    )
                    $kubectlWslPath = $null
                    foreach ($path in $kubectlPaths) {
                        $test = & wsl bash -c "test -f '$path' && echo 'exists'" 2>&1
                        if ($test -match "exists") {
                            $kubectlWslPath = $path
                            break
                        }
                    }
                    if ($kubectlWslPath) {
                        # Export KUBECTL env var so gencerts.sh uses Windows kubectl
                        $gencertsOutput = & wsl bash -c "export KUBECTL='$kubectlWslPath' && cd '$wslPath/pkg/admission-controller' && bash gencerts.sh" 2>&1
                    }
                    else {
                        # Fallback: try with PATH kubectl
                        $gencertsOutput = & wsl bash -c "cd '$wslPath/pkg/admission-controller' && bash gencerts.sh" 2>&1
                    }
                }
                else {
                    # Fallback: try with PATH kubectl
                    $gencertsOutput = & wsl bash -c "cd '$wslPath/pkg/admission-controller' && bash gencerts.sh" 2>&1
                }
                
                if ($LASTEXITCODE -ne 0) {
                    Write-Host "      gencerts.sh output: $gencertsOutput" -ForegroundColor Yellow
                    throw "gencerts.sh failed with exit code $LASTEXITCODE. Check if the script exists and dependencies are available."
                }
                
                Write-Host "    Deploying VPA components..." -ForegroundColor Gray
                $vpaUpScript = Join-Path $VPAAutoscalerPath "hack\vpa-up.sh"
                $vpaUpWslPath = "$wslPath/hack/vpa-up.sh"
                & wsl bash -c "chmod +x '$vpaUpWslPath'; (dos2unix '$vpaUpWslPath' 2>/dev/null || sed -i 's/\r$//' '$vpaUpWslPath' || true)" 2>&1 | Out-Null
                $vpaUpOutput = & wsl bash -c "cd '$wslPath/hack' && bash vpa-up.sh" 2>&1
                if ($LASTEXITCODE -ne 0) {
                    throw "vpa-up.sh failed with exit code $LASTEXITCODE"
                }
                
                Start-Sleep -Seconds 10
                $vpaCheck = kubectl get crd verticalpodautoscalers.autoscaling.k8s.io 2>&1 | Out-Null
                if ($LASTEXITCODE -eq 0) {
                    Write-Host "  [OK] VPA deployed" -ForegroundColor Green
                    $script:vpaAutoDeployed = $true
                }
                else {
                    throw "VPA CRD not found after deployment"
                }
            }
        }
        else {
            throw "VPA is not installed and -AutoDeploy was not specified. Install VPA or use -AutoDeploy."
        }
    }
    else {
        Write-Host "  [OK] VPA already installed" -ForegroundColor Green
    }
    Write-Host ""
    
    Write-Step "Running VPA test phase" 5 8
    $allPhaseResults.vpa = Run-TestPhase -PhaseName "VPA" -TestType "vpa" -WorkloadFile "workloads-vpa.yaml" -ConfigFile "vpa-configs.yaml" -StabilizationSeconds 60 -NeedsVPA
}
else {
    Write-Step "Skipping VPA test phase (mbcas-only mode)" 4 8
    Write-Host "  [SKIP] VPA test phase skipped" -ForegroundColor Yellow
    Write-Host ""
}

# Phase 2: MBCAS
if ($TestMode -eq "both" -or $TestMode -eq "mbcas-only") {
    Write-Step "Checking and deploying MBCAS (if needed)" 6 8
    # Check MBCAS right before we need it
    $mbcasCRD = kubectl get crd podallocations.allocation.mbcas.io 2>&1 | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Write-Host "  MBCAS not found, deploying..." -ForegroundColor Yellow
        if ($AutoDeploy) {
            # Deploy MBCAS (reuse the deployment logic from prerequisites, but only when needed)
            & {
                Write-Host "    Building MBCAS Docker images..." -ForegroundColor Gray
                Push-Location $ProjectRoot
                docker build -f Dockerfile.controller -t mbcas-controller:latest . 2>&1 | Out-Null
                if ($LASTEXITCODE -ne 0) {
                    throw "Failed to build mbcas-controller image"
                }
                docker build -f Dockerfile.agent -t mbcas-agent:latest . 2>&1 | Out-Null
                if ($LASTEXITCODE -ne 0) {
                    throw "Failed to build mbcas-agent image"
                }
                Pop-Location
                
                if ($isMinikube -and (Get-Command minikube -ErrorAction SilentlyContinue)) {
                    minikube image load mbcas-controller:latest -p $MinikubeProfile 2>&1 | Out-Null
                    minikube image load mbcas-agent:latest -p $MinikubeProfile 2>&1 | Out-Null
                }
                
                Write-Host "    Applying MBCAS Kubernetes manifests..." -ForegroundColor Gray
                $k8sDir = Join-Path (Join-Path $ProjectRoot "k8s") "mbcas"
                kubectl apply -f (Join-Path $k8sDir "allocation.mbcas.io_podallocations.yaml") 2>&1 | Out-Null
                Start-Sleep -Seconds 3
                kubectl apply -f (Join-Path $k8sDir "namespace.yaml") 2>&1 | Out-Null
                kubectl apply -f (Join-Path $k8sDir "service_account.yaml") 2>&1 | Out-Null
                kubectl apply -f (Join-Path $k8sDir "role.yaml") 2>&1 | Out-Null
                kubectl apply -f (Join-Path $k8sDir "role_binding.yaml") 2>&1 | Out-Null
                kubectl apply -f (Join-Path $k8sDir "agent-rbac.yaml") 2>&1 | Out-Null
                
                $configMapPath = Join-Path (Join-Path (Join-Path $ProjectRoot "config") "agent") "configmap.yaml"
                if (Test-Path $configMapPath) {
                    kubectl apply -f $configMapPath 2>&1 | Out-Null
                }
                
                kubectl apply -f (Join-Path $k8sDir "controller-deployment.yaml") 2>&1 | Out-Null
                kubectl apply -f (Join-Path $k8sDir "agent-daemonset.yaml") 2>&1 | Out-Null
                
                Write-Host "    Waiting for MBCAS components to be ready..." -ForegroundColor Gray
                kubectl wait --for=condition=available --timeout=120s deployment/mbcas-controller -n mbcas-system 2>&1 | Out-Null
                kubectl wait --for=condition=ready --timeout=120s daemonset/mbcas-agent -n mbcas-system 2>&1 | Out-Null
                
                Write-Host "  [OK] MBCAS deployed" -ForegroundColor Green
                $script:mbcasAutoDeployed = $true
            }
        }
        else {
            throw "MBCAS is not deployed and -AutoDeploy was not specified. Deploy MBCAS or use -AutoDeploy."
        }
    }
    else {
        Write-Host "  [OK] MBCAS already deployed" -ForegroundColor Green
    }
    Write-Host ""
    
    Write-Step "Running MBCAS test phase" 7 8
    $allPhaseResults.mbcas = Run-TestPhase -PhaseName "MBCAS" -TestType "mbcas" -WorkloadFile "workloads-mbcas.yaml" -StabilizationSeconds 30 -NeedsMBCAS
}
else {
    Write-Step "Skipping MBCAS test phase (vpa-only mode)" 6 8
    Write-Host "  [SKIP] MBCAS test phase skipped" -ForegroundColor Yellow
    Write-Host ""
}

# ------------------------------------------------------------------------------
# Generate comparison report
# ------------------------------------------------------------------------------
Write-Step "Generating comparison report" 8 8

# Calculate statistics
$collectionInterval = 30
$totalCollections = [math]::Floor($TestDurationMinutes * 60 / $collectionInterval)

$report = @{
    testMetadata = @{
        timestamp = $Timestamp
        testDurationMinutes = $TestDurationMinutes
        collectionIntervalSeconds = $collectionInterval
        totalCollections = $totalCollections
        testMode = $TestMode
    }
    summary = @{}
    phases = $allPhaseResults
}

if ($allPhaseResults.vpa) {
    $report.summary.vpa = @{
        totalPods = ($allPhaseResults.vpa.final.pods | Measure-Object).Count
        totalRestarts = ($allPhaseResults.vpa.final.pods | ForEach-Object { $_.restartCount } | Measure-Object -Sum).Sum
        vpaCount = ($allPhaseResults.vpa.final.vpaRecommendations.vpas | Measure-Object).Count
    }
}

if ($allPhaseResults.mbcas) {
    $report.summary.mbcas = @{
        totalPods = ($allPhaseResults.mbcas.final.pods | Measure-Object).Count
        totalRestarts = ($allPhaseResults.mbcas.final.pods | ForEach-Object { $_.restartCount } | Measure-Object -Sum).Sum
        allocationCount = ($allPhaseResults.mbcas.final.mbcasAllocations.podAllocations | Measure-Object).Count
    }
}

# Save comprehensive report
$reportPath = Join-Path $TestResultsDir "comparison-report.json"
$report | ConvertTo-Json -Depth 20 | Out-File -FilePath $reportPath -Encoding UTF8

Write-Host "  [OK] Comparison report generated: $reportPath" -ForegroundColor Green
Write-Host ""

# ------------------------------------------------------------------------------
# Print summary
# ------------------------------------------------------------------------------
Write-Host $separator -ForegroundColor Cyan
Write-Host "  Test Summary" -ForegroundColor Cyan
Write-Host $separator -ForegroundColor Cyan
Write-Host ""
Write-Host "Test Mode: $TestMode" -ForegroundColor Cyan
Write-Host ""

if ($allPhaseResults.vpa) {
    Write-Host "VPA:" -ForegroundColor Yellow
    Write-Host "  Pods: $($report.summary.vpa.totalPods)" -ForegroundColor White
    Write-Host "  Restarts: $($report.summary.vpa.totalRestarts)" -ForegroundColor White
    Write-Host "  VPA Resources: $($report.summary.vpa.vpaCount)" -ForegroundColor White
    Write-Host ""
}

if ($allPhaseResults.mbcas) {
    Write-Host "MBCAS:" -ForegroundColor Yellow
    Write-Host "  Pods: $($report.summary.mbcas.totalPods)" -ForegroundColor White
    Write-Host "  Restarts: $($report.summary.mbcas.totalRestarts)" -ForegroundColor White
    Write-Host "  PodAllocations: $($report.summary.mbcas.allocationCount)" -ForegroundColor White
    Write-Host ""
}
Write-Host "Results saved to: $TestResultsDir" -ForegroundColor Green
Write-Host ""

# ------------------------------------------------------------------------------
# Cleanup
# ------------------------------------------------------------------------------
Write-Step "Final cleanup" 9 9

# Cleanup auto-deployed systems if requested
if (-not $SkipCleanup) {
    if ($vpaAutoDeployed) {
        Write-Host "  Cleaning up auto-deployed VPA..." -ForegroundColor Gray
        try {
            $vpaDownScript = Join-Path $VPAAutoscalerPath "hack\vpa-down.sh"
            if (Test-Path $vpaDownScript) {
                if (Get-Command wsl -ErrorAction SilentlyContinue) {
                    # Convert Windows path to WSL path
                    # Docker Desktop uses /tmp/docker-desktop-root/run/desktop/mnt/host/c
                    # Also try /mnt/host/c and /mnt/c
                    $wslPathDockerFull = $VPAAutoscalerPath -replace '\\', '/' -replace 'C:', '/tmp/docker-desktop-root/run/desktop/mnt/host/c' -replace ':', ''
                    $wslPathDocker = $VPAAutoscalerPath -replace '\\', '/' -replace 'C:', '/mnt/host/c' -replace ':', ''
                    $wslPathStandard = $VPAAutoscalerPath -replace '\\', '/' -replace 'C:', '/mnt/c' -replace ':', ''
                    
                    # Test which path works
                    $testResult = & wsl bash -c "if test -d '$wslPathDockerFull'; then echo 'docker-full'; elif test -d '$wslPathDocker'; then echo 'docker'; elif test -d '$wslPathStandard'; then echo 'standard'; else echo 'none'; fi" 2>&1
                    if ($testResult -match "docker-full") {
                        $wslPath = $wslPathDockerFull
                    }
                    elseif ($testResult -match "docker") {
                        $wslPath = $wslPathDocker
                    }
                    elseif ($testResult -match "standard") {
                        $wslPath = $wslPathStandard
                    }
                    else {
                        # Default to Docker Desktop full path (most common for Docker Desktop)
                        $wslPath = $wslPathDockerFull
                    }
                    $vpaDownWslPath = "$wslPath/hack/vpa-down.sh"
                    
                    # Prepare script - follow exact sequence: chmod, then dos2unix, then run with bash
                    Write-Host "    Preparing vpa-down.sh (chmod +x)..." -ForegroundColor Gray
                    & wsl bash -c "chmod +x '$vpaDownWslPath'" 2>&1 | Out-Null
                    
                    Write-Host "    Fixing line endings (dos2unix)..." -ForegroundColor Gray
                    & wsl bash -c "dos2unix '$vpaDownWslPath' 2>/dev/null || sed -i 's/\r$//' '$vpaDownWslPath' || true" 2>&1 | Out-Null
                    
                    # Run vpa-down.sh using WSL with explicit bash (bypasses shebang line-ending issues)
                    # Use Windows kubectl.exe from WSL to avoid kubeconfig path issues
                    Write-Host "    Running vpa-down.sh with bash..." -ForegroundColor Gray
                    $kubectlExePath = (Get-Command kubectl -ErrorAction SilentlyContinue).Source
                    if ($kubectlExePath) {
                        # Find the correct WSL path for kubectl.exe
                        $kubectlPaths = @(
                            ($kubectlExePath -replace '\\', '/') -replace 'C:', '/tmp/docker-desktop-root/run/desktop/mnt/host/c' -replace ':', '',
                            ($kubectlExePath -replace '\\', '/') -replace 'C:', '/mnt/host/c' -replace ':', '',
                            ($kubectlExePath -replace '\\', '/') -replace 'C:', '/mnt/c' -replace ':', ''
                        )
                        $kubectlWslPath = $null
                        foreach ($path in $kubectlPaths) {
                            $test = & wsl bash -c "test -f '$path' && echo 'exists'" 2>&1
                            if ($test -match "exists") {
                                $kubectlWslPath = $path
                                break
                            }
                        }
                        if ($kubectlWslPath) {
                            # Export KUBECTL env var so vpa-down.sh uses Windows kubectl
                            & wsl bash -c "export KUBECTL='$kubectlWslPath' && cd '$wslPath/hack' && bash vpa-down.sh" 2>&1 | Out-Null
                        }
                        else {
                            # Fallback: try with PATH kubectl
                            & wsl bash -c "cd '$wslPath/hack' && bash vpa-down.sh" 2>&1 | Out-Null
                        }
                    }
                    else {
                        # Fallback: try with PATH kubectl
                        & wsl bash -c "cd '$wslPath/hack' && bash vpa-down.sh" 2>&1 | Out-Null
                    }
                    Write-Host "    [OK] VPA cleaned up" -ForegroundColor Green
                }
                else {
                    Write-Host "    [WARNING] WSL not found, skipping VPA cleanup" -ForegroundColor Yellow
                }
            }
            else {
                Write-Host "    [WARNING] vpa-down.sh not found, skipping VPA cleanup" -ForegroundColor Yellow
            }
        }
        catch {
            Write-Host "    [WARNING] Could not cleanup VPA automatically: $_" -ForegroundColor Yellow
        }
    }
    
    if ($mbcasAutoDeployed) {
        Write-Host "  Cleaning up auto-deployed MBCAS..." -ForegroundColor Gray
        try {
            # Delete MBCAS components
            kubectl delete deployment mbcas-controller -n mbcas-system --ignore-not-found=true 2>&1 | Out-Null
            kubectl delete daemonset mbcas-agent -n mbcas-system --ignore-not-found=true 2>&1 | Out-Null
            kubectl delete namespace mbcas-system --ignore-not-found=true 2>&1 | Out-Null
            kubectl delete crd podallocations.allocation.mbcas.io --ignore-not-found=true 2>&1 | Out-Null
            Write-Host "    [OK] MBCAS cleaned up" -ForegroundColor Green
        }
        catch {
            Write-Host "    [WARNING] Could not cleanup MBCAS automatically: $_" -ForegroundColor Yellow
        }
    }
    
    # Cleanup test namespace
    kubectl delete namespace vpa-mbcas-test --ignore-not-found=true 2>&1 | Out-Null
    Write-Host "  [OK] Test namespace cleaned up" -ForegroundColor Green
    
    # Stop Minikube if we started it
    if ($minikubeStarted) {
        Write-Host "  Stopping Minikube (was auto-started)..." -ForegroundColor Gray
        try {
            minikube stop -p $MinikubeProfile 2>&1 | Out-Null
            Write-Host "    [OK] Minikube stopped" -ForegroundColor Green
        }
        catch {
            Write-Host "    [WARNING] Could not stop Minikube: $_" -ForegroundColor Yellow
        }
    }
}
else {
    Write-Host "  [SKIP] Skipping cleanup (resources remain in cluster)" -ForegroundColor Yellow
    Write-Host "    Inspect resources: kubectl get all -n vpa-mbcas-test" -ForegroundColor Gray
    if ($vpaAutoDeployed) {
        Write-Host "    VPA was auto-deployed and will remain" -ForegroundColor Gray
    }
    if ($mbcasAutoDeployed) {
        Write-Host "    MBCAS was auto-deployed and will remain" -ForegroundColor Gray
    }
    if ($minikubeStarted) {
        Write-Host "    Minikube was auto-started and will remain running" -ForegroundColor Gray
    }
}
Write-Host ""

Write-Step "Test execution complete" 9 9

Write-Host ""
Write-Host $separator -ForegroundColor Cyan
Write-Host "  All Steps Complete!" -ForegroundColor Green
Write-Host $separator -ForegroundColor Cyan
Write-Host ""
Write-Host "Results saved to: $TestResultsDir" -ForegroundColor Cyan
Write-Host ""