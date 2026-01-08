# start_minikube.ps1
# Launches Minikube with all feature gates required for vertical scaling
# This script ensures the cluster is ready for MBCAS (CoAllocator) deployment

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

Write-Color "Starting Minikube with Vertical Scaling Feature Gates..." "Cyan"
Write-Color "This enables InPlacePodVerticalScaling for pod resource resizing" "Gray"

# Feature gates required for vertical scaling
$featureGates = @(
    "InPlacePodVerticalScaling=true"
)

$featureGatesString = $featureGates -join ","

# Check if minikube is already running
Write-Step 1 4 "Checking Minikube status..."
$minikubeRunning = $false
try {
    $status = minikube status --format='{{.Host}}' 2>$null
    if ($status -eq "Running") { 
        $minikubeRunning = $true
        Write-Color "Minikube is already running." "Green"
    }
}
catch {
    $minikubeRunning = $false
}

# Start or restart minikube with feature gates
if (-not $minikubeRunning) {
    Write-Color "Starting Minikube with feature gates: $featureGatesString" "Green"
    
    # Stop any existing minikube instance (only if it's actually running)
    # Check status more carefully to avoid errors when already stopped
    try {
        $currentStatus = minikube status --format='{{.Host}}' 2>&1 | Out-String
        if ($currentStatus -match "Running") {
            Write-Color "Stopping existing Minikube instance..." "Gray"
            minikube stop 2>&1 | Out-Null
            Start-Sleep -Seconds 2
        }
    }
    catch {
        # Minikube is already stopped or doesn't exist, which is fine
        Write-Color "Minikube is not running, proceeding to start..." "Gray"
    }
    
    # Start minikube with feature gates
    minikube start `
        --driver=docker `
        --cpus=4 `
        --memory=4096 `
        --wait=all `
        --feature-gates=$featureGatesString `
        --extra-config=kubelet.feature-gates=$featureGatesString
    
    if ($LASTEXITCODE -ne 0) {
        Write-Color "ERROR: Failed to start Minikube" "Red"
        exit 1
    }
    
    Write-Color "OK: Minikube started successfully" "Green"
}
else {
    # Check if feature gates are enabled
    Write-Step 2 4 "Verifying feature gates..."
    
    $kubeletConfig = kubectl get configmap kubelet-config -n kube-system -o jsonpath='{.data.kubelet}' 2>$null
    if ($kubeletConfig -notmatch "InPlacePodVerticalScaling") {
        Write-Color "WARNING: Feature gates may not be enabled. Restarting Minikube..." "Magenta"
        try {
            minikube stop 2>&1 | Out-Null
            Start-Sleep -Seconds 2
        }
        catch {
            # Already stopped, continue
        }
        minikube start `
            --driver=docker `
            --cpus=4 `
            --memory=4096 `
            --wait=all `
            --feature-gates=$featureGatesString `
            --extra-config=kubelet.feature-gates=$featureGatesString
    }
    else {
        Write-Color "Feature gates are already enabled." "Green"
    }
}

# Enable metrics-server (required for resource monitoring)
Write-Step 3 4 "Enabling metrics-server addon..."
$metricsServer = minikube addons list --output=json | ConvertFrom-Json | Where-Object { $_.Name -eq "metrics-server" }
if ($metricsServer.Status -ne "enabled") {
    minikube addons enable metrics-server
    Write-Color "Metrics-server enabled." "Green"
}
else {
    Write-Color "Metrics-server is already enabled." "Green"
}

# Verify pod/resize subresource is available
Write-Step 4 4 "Verifying pod/resize subresource..."
Start-Sleep -Seconds 5  # Wait for API server to be ready

# Check Kubernetes version
$k8sVersion = kubectl version --client=false -o json | ConvertFrom-Json | Select-Object -ExpandProperty serverVersion | Select-Object -ExpandProperty gitVersion
Write-Color "Kubernetes version: $k8sVersion" "Gray"

# Verify pod/resize subresource
$resources = kubectl api-resources --api-group="" 2>$null | Select-String "pods/resize"
if ($resources) {
    Write-Color "OK: pod/resize subresource is available" "Green"
}
else {
    Write-Color "WARNING: pod/resize subresource not found." "Magenta"
    Write-Color "   This may be normal if the API server hasn't fully started yet." "Gray"
    Write-Color "   The feature gate is enabled and will be active once pods are created." "Gray"
}

# Display cluster info
Write-Color "`nCluster Information:" "Cyan"
Write-Color "-----------------------------------------" "Gray"
kubectl cluster-info
Write-Color ""
kubectl get nodes
Write-Color ""

# Verify feature gates
Write-Color "🔧 Feature Gates Status:" "Cyan"
Write-Color "─────────────────────────────────────────" "Gray"
$apiserver = kubectl get pod -n kube-system -l component=kube-apiserver -o jsonpath='{.items[0].spec.containers[0].command}' 2>$null
if ($apiserver -match "InPlacePodVerticalScaling") {
    Write-Color "OK: InPlacePodVerticalScaling: ENABLED" "Green"
}
else {
    Write-Color "WARNING: InPlacePodVerticalScaling: Status unknown" "Magenta"
}

Write-Color "`nOK: Minikube is ready for MBCAS deployment!" "Green"
Write-Color "   Run: .\scripts\build_and_deploy.ps1" "Gray"

