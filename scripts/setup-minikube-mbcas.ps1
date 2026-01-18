# MBCAS Minikube Setup Script
# Sets up a complete Minikube environment with MBCAS deployed
param(
    [string]$MinikubeProfile = "mbcas",
    [int]$CPUs = 4,
    [string]$Memory = "4096",
    [string]$KubernetesVersion = "latest"
)

$ErrorActionPreference = "Stop"




$separator = "================================================================================"
Write-Host $separator -ForegroundColor Cyan
Write-Host "  MBCAS Minikube Setup Script" -ForegroundColor Cyan
Write-Host $separator -ForegroundColor Cyan
Write-Host ""

# Resolve paths safely
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$ProjectRoot = Split-Path -Parent $ScriptDir

# ------------------------------------------------------------------------------
# [1/9] Prerequisites
# ------------------------------------------------------------------------------
Write-Host "[1/9] Checking prerequisites..." -ForegroundColor Yellow

if (-not (Get-Command minikube -ErrorAction SilentlyContinue)) {
    throw "minikube is not installed or not in PATH"
}

try {
    docker info | Out-Null
}
catch {
    throw "Docker is not running or not accessible"
}

Write-Host "  [OK] Prerequisites check passed" -ForegroundColor Green
Write-Host ""

# ------------------------------------------------------------------------------
# [2/9] Check Minikube Status
# ------------------------------------------------------------------------------
Write-Host "[2/9] Checking minikube status..." -ForegroundColor Yellow

$profileExists = $false
try {
    $profileList = minikube profile list 2>&1
    if ($LASTEXITCODE -eq 0) {
        $profileExists = $profileList -match $MinikubeProfile
    }
}
catch {
    # If minikube profile list fails (e.g. no profiles), assume profile doesn't exist
    $profileExists = $false
}

if ($profileExists) {
    try {
        $minikubeStatus = minikube status -p $MinikubeProfile 2>&1 | Out-String
        $isRunning = $minikubeStatus -match "Running|host: Running"
    }
    catch {
        $isRunning = $false
    }
    
    if ($isRunning) {
        Write-Host "  [OK] Minikube profile '$MinikubeProfile' is already running" -ForegroundColor Green
        Write-Host "  [SKIP] Skipping minikube launch" -ForegroundColor Yellow
        $skipMinikubeStart = $true
    }
    else {
        Write-Host "  Profile exists but not running, will start..." -ForegroundColor Gray
        $skipMinikubeStart = $false
    }
}
else {
    Write-Host "  Profile does not exist, will create and start..." -ForegroundColor Gray
    $skipMinikubeStart = $false
}

Write-Host ""

# ------------------------------------------------------------------------------
# [3/9] Start Minikube (if needed)
# ------------------------------------------------------------------------------
if (-not $skipMinikubeStart) {
    Write-Host "[3/9] Starting minikube with InPlacePodVerticalScaling feature gate..." -ForegroundColor Yellow
    
    Write-Host "  Starting minikube (this may take a few minutes)..." -ForegroundColor Gray
    try {
        $startResult = & minikube start -p $MinikubeProfile --driver=docker --cpus=$CPUs --memory=$Memory --kubernetes-version=$KubernetesVersion --extra-config=apiserver.feature-gates=InPlacePodVerticalScaling=true --addons=metrics-server 2>&1
    }
    catch {
        Write-Host "  [WARNING] Minikube start reported errors (likely registry connection, ignoring): $_" -ForegroundColor Yellow
    }
    
    # Check if minikube actually started successfully (ignore registry connection warnings)
    # Wait a moment for minikube to stabilize
    Start-Sleep -Seconds 3
    $minikubeStatus = minikube status -p $MinikubeProfile 2>&1 | Out-String
    if ($minikubeStatus -notmatch "Running|host: Running") {
        Write-Host "ERROR: Failed to start minikube" -ForegroundColor Red
        Write-Host $startResult -ForegroundColor Red
        Write-Host ""
        Write-Host "Troubleshooting tips:" -ForegroundColor Yellow
        Write-Host "  - Check your internet connection" -ForegroundColor Gray
        Write-Host "  - Ensure Docker is running" -ForegroundColor Gray
        Write-Host "  - Try: minikube delete -p $MinikubeProfile" -ForegroundColor Gray
        Write-Host "  - Then run this script again" -ForegroundColor Gray
        exit 1
    }
    
    Write-Host "  [OK] Minikube started successfully" -ForegroundColor Green
    Write-Host ""
}
else {
    Write-Host "[3/9] Minikube already running, skipping start..." -ForegroundColor Yellow
    Write-Host "  [OK] Using existing minikube instance" -ForegroundColor Green
    Write-Host ""
}

# ------------------------------------------------------------------------------
# [4/9] Metrics Server
# ------------------------------------------------------------------------------
Write-Host "[4/9] Verifying metrics-server addon..." -ForegroundColor Yellow
Start-Sleep -Seconds 5

$addonsList = minikube addons list -p $MinikubeProfile
$metricsEnabled = ($addonsList | Select-String 'metrics-server.*enabled') -ne $null

if (-not $metricsEnabled) {
    Write-Host "  Enabling metrics-server addon..." -ForegroundColor Gray
    minikube addons enable metrics-server -p $MinikubeProfile | Out-Null
}
Write-Host "  [OK] Metrics-server addon enabled" -ForegroundColor Green

Write-Host ""

# ------------------------------------------------------------------------------
# [5/9] Docker config
# ------------------------------------------------------------------------------
Write-Host "[5/9] Docker configuration..." -ForegroundColor Yellow
Write-Host "  [OK] Images will be loaded via minikube image load" -ForegroundColor Green
Write-Host ""

# ------------------------------------------------------------------------------
# [6/9] Build images
# ------------------------------------------------------------------------------
Write-Host "[6/9] Building MBCAS Docker images..." -ForegroundColor Yellow

Push-Location $ProjectRoot

Write-Host "  Building mbcas-controller:latest..." -ForegroundColor Gray
docker build -f Dockerfile.controller -t mbcas-controller:latest .

Write-Host "    [OK] Controller image built" -ForegroundColor Green

Write-Host "  Building mbcas-agent:latest..." -ForegroundColor Gray
docker build -f Dockerfile.agent -t mbcas-agent:latest .

Write-Host "    [OK] Agent image built" -ForegroundColor Green

Pop-Location
Write-Host ""

# ------------------------------------------------------------------------------
# [7/9] Load images
# ------------------------------------------------------------------------------
Write-Host "[7/9] Loading images into Minikube..." -ForegroundColor Yellow

minikube image load mbcas-controller:latest -p $MinikubeProfile
minikube image load mbcas-agent:latest      -p $MinikubeProfile

Write-Host "  [OK] Images loaded" -ForegroundColor Green
Write-Host ""

# ------------------------------------------------------------------------------
# [8/9] Kubernetes manifests
# ------------------------------------------------------------------------------
Write-Host "[8/9] Applying Kubernetes manifests..." -ForegroundColor Yellow

$k8sDir = Join-Path (Join-Path $ProjectRoot "k8s") "mbcas"

kubectl apply -f (Join-Path $k8sDir "allocation.mbcas.io_podallocations.yaml")
Start-Sleep -Seconds 3

kubectl apply -f (Join-Path $k8sDir "namespace.yaml")
kubectl apply -f (Join-Path $k8sDir "service_account.yaml")
kubectl apply -f (Join-Path $k8sDir "role.yaml")
kubectl apply -f (Join-Path $k8sDir "role_binding.yaml")
kubectl apply -f (Join-Path $k8sDir "agent-rbac.yaml")

$configMapPath = Join-Path (Join-Path (Join-Path $ProjectRoot "config") "agent") "configmap.yaml"
if (Test-Path $configMapPath) {
    kubectl apply -f $configMapPath
}

kubectl apply -f (Join-Path $k8sDir "controller-deployment.yaml")
kubectl apply -f (Join-Path $k8sDir "agent-daemonset.yaml")

Write-Host "  [OK] Manifests applied" -ForegroundColor Green
Write-Host ""

# ------------------------------------------------------------------------------
# [9/9] Readiness
# ------------------------------------------------------------------------------
$msg9 = '[9/9] Waiting for MBCAS components...'
Write-Host $msg9 -ForegroundColor Yellow

kubectl wait --for=condition=available --timeout=120s deployment/mbcas-controller -n mbcas-system
kubectl wait --for=condition=ready --timeout=120s daemonset/mbcas-agent -n mbcas-system

Write-Host
Write-Host $separator -ForegroundColor Cyan
$msg = '  Setup Complete'
Write-Host $msg -ForegroundColor Green
Write-Host $separator -ForegroundColor Cyan
Write-Host

kubectl get pods -n mbcas-system
