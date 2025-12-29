# Minikube setup script for MBCAS (PowerShell)
# Creates Minikube cluster with InPlacePodVerticalScaling feature gate enabled

$ErrorActionPreference = "Stop"
$CLUSTER_NAME = "mbcas"

function Write-Info {
    Write-Host "[INFO] $args" -ForegroundColor Green
}

function Write-Warn {
    Write-Host "[WARN] $args" -ForegroundColor Yellow
}

function Write-Error {
    Write-Host "[ERROR] $args" -ForegroundColor Red
    exit 1
}

Write-Host "=========================================="
Write-Host "MBCAS Minikube Setup"
Write-Host "=========================================="
Write-Host ""

# Check if minikube is installed
try {
    $null = Get-Command minikube -ErrorAction Stop
} catch {
    Write-Error "minikube is not installed. Please install it first: https://minikube.sigs.k8s.io/docs/start/"
}

# Check if minikube cluster exists
$status = minikube status -p $CLUSTER_NAME 2>&1
if ($LASTEXITCODE -eq 0) {
    Write-Warn "Minikube cluster '$CLUSTER_NAME' already exists"
    $response = Read-Host "Delete existing cluster? (y/N)"
    if ($response -eq "y" -or $response -eq "Y") {
        Write-Info "Deleting existing cluster..."
        minikube delete -p $CLUSTER_NAME
    } else {
        Write-Info "Using existing cluster"
        exit 0
    }
}

# Start Minikube with feature gate
Write-Info "Starting Minikube cluster with InPlacePodVerticalScaling feature gate..."

minikube start -p $CLUSTER_NAME `
    --feature-gates=InPlacePodVerticalScaling=true `
    --kubernetes-version=latest `
    --driver=docker `
    --memory=4096 `
    --cpus=2

if ($LASTEXITCODE -ne 0) {
    Write-Error "Failed to start Minikube cluster"
}

# Wait for cluster to be ready
Write-Info "Waiting for cluster to be ready..."
kubectl wait --for=condition=Ready nodes --all --timeout=120s

# Verify feature gate
Write-Info "Verifying InPlacePodVerticalScaling feature gate..."

# Check Kubernetes version
$serverVersion = kubectl version -o json 2>&1 | ConvertFrom-Json
if ($serverVersion) {
    $k8sVersion = $serverVersion.serverVersion.gitVersion
    Write-Info "Kubernetes server version: $k8sVersion"
    
    if ($k8sVersion -match 'v?(\d+)\.(\d+)') {
        $major = [int]$matches[1]
        $minor = [int]$matches[2]
        if ($major -lt 1 -or ($major -eq 1 -and $minor -lt 27)) {
            Write-Error "Kubernetes version $k8sVersion is too old. Requires 1.27+ for InPlacePodVerticalScaling"
        } else {
            Write-Info "[OK] Kubernetes version supports InPlacePodVerticalScaling (1.27+)"
        }
    }
}

# Set up Docker environment for Minikube
Write-Info "Setting up Docker environment for Minikube..."
$dockerEnvOutput = minikube -p $CLUSTER_NAME docker-env
foreach ($line in $dockerEnvOutput) {
    if ($line -match '^\$Env:(\w+)\s*=\s*"(.+)"') {
        $varName = $matches[1]
        $varValue = $matches[2]
        Set-Item -Path "env:$varName" -Value $varValue
    } elseif ($line -match '^export\s+(\w+)=["''](.+)["'']') {
        $varName = $matches[1]
        $varValue = $matches[2]
        Set-Item -Path "env:$varName" -Value $varValue
    }
}

Write-Info "[OK] Minikube cluster is ready!"
Write-Host ""
Write-Info "Docker environment is configured for this session"
Write-Info ""
Write-Info "To stop the cluster:"
Write-Info "  minikube stop -p $CLUSTER_NAME"
Write-Info ""
Write-Info "To delete the cluster:"
Write-Info "  minikube delete -p $CLUSTER_NAME"

