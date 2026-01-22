# MBCAS Minikube Setup Script
# Optimized for reliable image building and deployment
param(
    [string]$MinikubeProfile = "mbcas",
    [int]$CPUs = 4,
    [string]$Memory = "4096",
    [switch]$SkipImageBuild = $false
)

$ErrorActionPreference = "Stop"
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$ProjectRoot = Split-Path -Parent $ScriptDir

$separator = "=" * 80
Write-Host $separator -ForegroundColor Cyan
Write-Host "  MBCAS Minikube Setup (Optimized)" -ForegroundColor Cyan
Write-Host $separator -ForegroundColor Cyan
Write-Host ""

# ------------------------------------------------------------------------------
# [1/9] Prerequisites
# ------------------------------------------------------------------------------
Write-Host "[1/9] Checking prerequisites..." -ForegroundColor Yellow

if (-not (Get-Command minikube -ErrorAction SilentlyContinue)) {
    throw "minikube is not installed or not in PATH"
}

if (-not (Get-Command kubectl -ErrorAction SilentlyContinue)) {
    throw "kubectl is not installed or not in PATH"
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
# [2/9] Minikube Status
# ------------------------------------------------------------------------------
Write-Host "[2/9] Checking minikube status..." -ForegroundColor Yellow

$profileExists = $false
$isRunning = $false

try {
    $status = minikube status -p $MinikubeProfile 2>&1 | Out-String
    if ($status -match "host.*Running") {
        $profileExists = $true
        $isRunning = $true
        Write-Host "  [OK] Minikube profile '$MinikubeProfile' is running" -ForegroundColor Green
    }
    elseif ($status -match "Stopped|Paused") {
        $profileExists = $true
        $isRunning = $false
        Write-Host "  Profile exists but stopped" -ForegroundColor Gray
    }
}
catch {
    Write-Host "  Profile does not exist" -ForegroundColor Gray
}

Write-Host ""

# ------------------------------------------------------------------------------
# [3/9] Start/Create Minikube
# ------------------------------------------------------------------------------
if (-not $isRunning) {
    Write-Host "[3/9] Starting minikube..." -ForegroundColor Yellow
    
    if ($profileExists) {
        Write-Host "  Starting existing profile..." -ForegroundColor Gray
        minikube start -p $MinikubeProfile
    }
    else {
        Write-Host "  Creating new profile with InPlacePodVerticalScaling..." -ForegroundColor Gray
        minikube start -p $MinikubeProfile `
            --cpus=$CPUs `
            --memory=$Memory `
            --driver=docker `
            --feature-gates=InPlacePodVerticalScaling=true `
            --extra-config=apiserver.feature-gates=InPlacePodVerticalScaling=true `
            --extra-config=kubelet.feature-gates=InPlacePodVerticalScaling=true
    }
    
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to start minikube"
    }
    
    Write-Host "  [OK] Minikube started" -ForegroundColor Green
}
else {
    Write-Host "[3/9] Minikube already running" -ForegroundColor Yellow
    Write-Host "  [SKIP]" -ForegroundColor Green
}

Write-Host ""

# ------------------------------------------------------------------------------
# [4/9] Metrics Server
# ------------------------------------------------------------------------------
Write-Host "[4/9] Enabling metrics-server..." -ForegroundColor Yellow

minikube addons enable metrics-server -p $MinikubeProfile | Out-Null
Write-Host "  [OK] Metrics-server enabled" -ForegroundColor Green
Write-Host ""

# ------------------------------------------------------------------------------
# [5/9] Set Docker Context
# ------------------------------------------------------------------------------
Write-Host "[5/9] Configuring Docker environment..." -ForegroundColor Yellow

# Point Docker to Minikube's Docker daemon for efficient image loading
# Point Docker to Minikube's Docker daemon for efficient image loading
minikube docker-env -p $MinikubeProfile --shell=powershell | Invoke-Expression
if (-not $env:DOCKER_HOST) {
    throw "Failed to set Docker environment variables from minikube"
}

Write-Host "  [OK] Docker context set to Minikube" -ForegroundColor Green
Write-Host ""

# ------------------------------------------------------------------------------
# [6/9] Build Images (in Minikube Docker)
# ------------------------------------------------------------------------------
if (-not $SkipImageBuild) {
    Write-Host "[6/9] Building MBCAS images (inside Minikube Docker)..." -ForegroundColor Yellow
    
    Push-Location $ProjectRoot
    
    # Build controller
    Write-Host "  Building mbcas-controller:latest..." -ForegroundColor Gray
    docker build -f Dockerfile.controller -t mbcas-controller:latest . --quiet
    if ($LASTEXITCODE -ne 0) {
        Pop-Location
        throw "Failed to build controller image"
    }
    Write-Host "    [OK]" -ForegroundColor Green
    
    # Build agent
    Write-Host "  Building mbcas-agent:latest..." -ForegroundColor Gray
    docker build -f Dockerfile.agent -t mbcas-agent:latest . --quiet
    if ($LASTEXITCODE -ne 0) {
        Pop-Location
        throw "Failed to build agent image"
    }
    Write-Host "    [OK]" -ForegroundColor Green
    
    Pop-Location
    
    # Verify images exist in Minikube
    Write-Host "  Verifying images in Minikube..." -ForegroundColor Gray
    $images = docker images --format "{{.Repository}}:{{.Tag}}" | Select-String "mbcas"
    if ($images.Count -lt 2) {
        throw "Images not found in Minikube Docker daemon"
    }
    Write-Host "    [OK] Images available in Minikube" -ForegroundColor Green
    
}
else {
    Write-Host "[6/9] Skipping image build (--SkipImageBuild)" -ForegroundColor Yellow
    Write-Host "  [SKIP]" -ForegroundColor Green
}

Write-Host ""

# ------------------------------------------------------------------------------
# [7/9] Reset Docker Context
# ------------------------------------------------------------------------------
Write-Host "[7/9] Resetting Docker context..." -ForegroundColor Yellow

Remove-Item Env:\DOCKER_TLS_VERIFY -ErrorAction SilentlyContinue
Remove-Item Env:\DOCKER_HOST -ErrorAction SilentlyContinue
Remove-Item Env:\DOCKER_CERT_PATH -ErrorAction SilentlyContinue
Remove-Item Env:\MINIKUBE_ACTIVE_DOCKERD -ErrorAction SilentlyContinue

Write-Host "  [OK] Docker context reset" -ForegroundColor Green
Write-Host ""

# ------------------------------------------------------------------------------
# [8/9] Apply Kubernetes Manifests
# ------------------------------------------------------------------------------
Write-Host "[8/9] Applying Kubernetes manifests..." -ForegroundColor Yellow

$k8sDir = Join-Path $ProjectRoot "k8s\mbcas"
$configDir = Join-Path $ProjectRoot "config"

# CRD first
kubectl apply -f (Join-Path $k8sDir "allocation.mbcas.io_podallocations.yaml") | Out-Null
Start-Sleep -Seconds 2

# Namespace and RBAC
kubectl apply -f (Join-Path $k8sDir "namespace.yaml") | Out-Null
kubectl apply -f (Join-Path $k8sDir "service_account.yaml") | Out-Null
kubectl apply -f (Join-Path $k8sDir "role.yaml") | Out-Null
kubectl apply -f (Join-Path $k8sDir "role_binding.yaml") | Out-Null
kubectl apply -f (Join-Path $k8sDir "agent-rbac.yaml") | Out-Null

# ConfigMap
$configMapPath = Join-Path $configDir "agent\configmap.yaml"
if (Test-Path $configMapPath) {
    kubectl apply -f $configMapPath | Out-Null
}

# Deployments
kubectl apply -f (Join-Path $k8sDir "controller-deployment.yaml") | Out-Null
kubectl apply -f (Join-Path $k8sDir "agent-daemonset.yaml") | Out-Null

Write-Host "  [OK] Manifests applied" -ForegroundColor Green
Write-Host ""

# ------------------------------------------------------------------------------
# [9/9] Wait for Readiness
# ------------------------------------------------------------------------------
Write-Host "[9/9] Waiting for MBCAS components..." -ForegroundColor Yellow

Write-Host "  Waiting for controller..." -ForegroundColor Gray
kubectl wait --for=condition=available --timeout=120s deployment/mbcas-controller -n mbcas-system 2>$null
if ($LASTEXITCODE -ne 0) {
    Write-Host "    [WARNING] Controller not ready yet" -ForegroundColor Yellow
}
else {
    Write-Host "    [OK]" -ForegroundColor Green
}

Write-Host "  Waiting for agent..." -ForegroundColor Gray
kubectl wait --for=condition=ready --timeout=120s pod -l app.kubernetes.io/component=agent -n mbcas-system 2>$null
if ($LASTEXITCODE -ne 0) {
    Write-Host "    [WARNING] Agent not ready yet" -ForegroundColor Yellow
}
else {
    Write-Host "    [OK]" -ForegroundColor Green
}

Write-Host ""
Write-Host $separator -ForegroundColor Cyan
Write-Host "  Setup Complete!" -ForegroundColor Green
Write-Host $separator -ForegroundColor Cyan
Write-Host ""

# Show status
Write-Host "MBCAS Components:" -ForegroundColor Cyan
kubectl get pods -n mbcas-system

Write-Host ""
Write-Host "Next steps:" -ForegroundColor Yellow
Write-Host "  1. Run test: .\scripts\test-mbcas.ps1" -ForegroundColor Gray
Write-Host "  2. View logs: kubectl logs -n mbcas-system -l app.kubernetes.io/component=agent" -ForegroundColor Gray
Write-Host "  3. Check allocations: kubectl get podallocations -A" -ForegroundColor Gray
