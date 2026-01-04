#!/usr/bin/env pwsh
# MBCAS Build Script
# Builds Docker images and loads them into Minikube

param(
    [string]$ClusterName = "mbcas",
    [string]$Tag = "latest",
    [switch]$SkipClusterCreate,  # Skip Minikube cluster creation
    [int]$CPUs = 4,
    [string]$Memory = "4096",
    [string]$Driver = "docker"
)

$ErrorActionPreference = "Stop"

function Write-Step { param($msg) Write-Host "`n==> $msg" -ForegroundColor Cyan }
function Write-Success { param($msg) Write-Host "[OK] $msg" -ForegroundColor Green }
function Write-Err { param($msg) Write-Host "[ERROR] $msg" -ForegroundColor Red }
function Write-Warn { param($msg) Write-Host "[WARN] $msg" -ForegroundColor Yellow }

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$ProjectRoot = Split-Path -Parent $ScriptDir

Write-Host "================================================================================" -ForegroundColor Magenta
Write-Host "  MBCAS Build & Load to Minikube" -ForegroundColor Magenta
Write-Host "================================================================================" -ForegroundColor Magenta
Write-Host ""
Write-Host "Configuration:"
Write-Host "  Project Root:       $ProjectRoot"
Write-Host "  Cluster Name:       $ClusterName"
Write-Host "  Tag:                $Tag"
Write-Host ""

# ============================================================================
# Step 1: Check Prerequisites
# ============================================================================
Write-Step "Checking prerequisites..."

# Check minikube
try {
    $minikubeVersion = minikube version --short 2>$null
    Write-Success "Minikube: $minikubeVersion"
} catch {
    Write-Err "Minikube not found. Install from: https://minikube.sigs.k8s.io/docs/start/"
    exit 1
}

# Check Docker
try {
    $dockerVersion = docker version --format '{{.Server.Version}}' 2>$null
    Write-Success "Docker: $dockerVersion"
} catch {
    Write-Err "Docker not found or not running. Please start Docker Desktop."
    exit 1
}

# ============================================================================
# Step 2: Create/Start Minikube Cluster
# ============================================================================
if (-not $SkipClusterCreate) {
    Write-Step "Setting up Minikube cluster '$ClusterName'..."
    
    # Check if cluster exists
    $clusterExists = $false
    try {
        $status = minikube status -p $ClusterName --format='{{.Host}}' 2>$null
        if ($status -eq "Running") {
            Write-Success "Cluster '$ClusterName' is already running"
            $clusterExists = $true
        } elseif ($status -eq "Stopped") {
            Write-Warn "Cluster '$ClusterName' exists but is stopped. Starting..."
            minikube start -p $ClusterName
            $clusterExists = $true
        }
    } catch {
        # Cluster doesn't exist
    }
    
    if (-not $clusterExists) {
        Write-Host "Creating new Minikube cluster with InPlacePodVerticalScaling..."
        
        $startArgs = @(
            "start"
            "-p", $ClusterName
            "--cpus=$CPUs"
            "--memory=$Memory"
            "--driver=$Driver"
            "--feature-gates=InPlacePodVerticalScaling=true"
            "--extra-config=kubelet.feature-gates=InPlacePodVerticalScaling=true"
            "--addons=metrics-server"
        )
        
        Write-Host "Command: minikube $($startArgs -join ' ')"
        & minikube @startArgs
        
        if ($LASTEXITCODE -ne 0) {
            Write-Err "Failed to create Minikube cluster"
            exit 1
        }
        Write-Success "Cluster created"
    }
    
    # Set kubectl context
    minikube -p $ClusterName update-context
    kubectl config use-context $ClusterName
} else {
    Write-Warn "Skipping cluster creation (-SkipClusterCreate)"
    
    # Verify cluster is running
    $status = minikube status -p $ClusterName --format='{{.Host}}' 2>$null
    if ($status -ne "Running") {
        Write-Err "Cluster '$ClusterName' is not running. Remove -SkipClusterCreate or start manually."
        exit 1
    }
}

# ============================================================================
# Step 3: Build Docker Images (locally)
# ============================================================================
Write-Step "Building Docker images locally..."

$agentImageName = "mbcas-agent:$Tag"
$controllerImageName = "mbcas-controller:$Tag"

# Build Agent
Write-Host "Building $agentImageName..."
docker build -t $agentImageName -f "$ProjectRoot\Dockerfile.agent" "$ProjectRoot"
if ($LASTEXITCODE -ne 0) {
    Write-Err "Failed to build mbcas-agent"
    exit 1
}
Write-Success "Built $agentImageName"

# Build Controller
Write-Host "Building $controllerImageName..."
docker build -t $controllerImageName -f "$ProjectRoot\Dockerfile.controller" "$ProjectRoot"
if ($LASTEXITCODE -ne 0) {
    Write-Err "Failed to build mbcas-controller"
    exit 1
}
Write-Success "Built $controllerImageName"

# ============================================================================
# Step 4: Load Images into Minikube
# ============================================================================
Write-Step "Loading images into Minikube..."

Write-Host "Loading $agentImageName..."
minikube -p $ClusterName image load $agentImageName
if ($LASTEXITCODE -ne 0) {
    Write-Err "Failed to load mbcas-agent into Minikube"
    exit 1
}
Write-Success "Loaded $agentImageName"

Write-Host "Loading $controllerImageName..."
minikube -p $ClusterName image load $controllerImageName
if ($LASTEXITCODE -ne 0) {
    Write-Err "Failed to load mbcas-controller into Minikube"
    exit 1
}
Write-Success "Loaded $controllerImageName"

# ============================================================================
# Step 5: Verify Images in Minikube
# ============================================================================
Write-Step "Verifying images in Minikube..."

$images = minikube -p $ClusterName image ls 2>$null | Select-String "mbcas"
if ($images) {
    Write-Host "Images available in Minikube:"
    $images | ForEach-Object { Write-Host "  $_" -ForegroundColor Green }
} else {
    Write-Warn "Could not verify images (this may be normal)"
}

# ============================================================================
# Summary
# ============================================================================
Write-Host "`n================================================================================" -ForegroundColor Green
Write-Host "  Build Complete!" -ForegroundColor Green
Write-Host "================================================================================" -ForegroundColor Green
Write-Host ""
Write-Host "Images built and loaded into Minikube cluster '$ClusterName':"
Write-Host "  - $agentImageName"
Write-Host "  - $controllerImageName"
Write-Host ""
Write-Host "Next steps:"
Write-Host "  1. Deploy MBCAS infrastructure:"
Write-Host "     .\scripts\setup-minikube.ps1 -SkipBuild -ClusterName $ClusterName"
Write-Host ""
Write-Host "  2. Or deploy manually:"
Write-Host "     kubectl apply -f config/crd/bases/"
Write-Host "     kubectl apply -f config/agent/"
Write-Host "     kubectl apply -f config/controller/"
Write-Host ""
Write-Host "  3. Restart deployments to pick up new images:"
Write-Host "     kubectl rollout restart -n mbcas-system daemonset/mbcas-agent"
Write-Host "     kubectl rollout restart -n mbcas-system deployment/mbcas-controller"
Write-Host ""
