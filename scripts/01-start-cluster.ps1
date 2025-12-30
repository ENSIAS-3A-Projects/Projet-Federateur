# MBCAS Demo Script 01 - Start Cluster
# Creates Minikube cluster with InPlacePodVerticalScaling feature gate
# and applies cluster-wide CPU policies
#
# Prerequisites: minikube, kubectl, docker
# Usage: .\scripts\01-start-cluster.ps1

$ErrorActionPreference = "Stop"
$CLUSTER_NAME = "mbcas"
$SCRIPT_DIR = Split-Path -Parent $MyInvocation.MyCommand.Path
$PROJECT_ROOT = Split-Path -Parent $SCRIPT_DIR

# Helper functions
function Write-Info { Write-Host "[INFO] $args" -ForegroundColor Green }
function Write-Warn { Write-Host "[WARN] $args" -ForegroundColor Yellow }
function Write-Err { Write-Host "[ERROR] $args" -ForegroundColor Red; exit 1 }

function Test-Prerequisites {
    Write-Info "Checking prerequisites..."
    
    # Check minikube
    if (-not (Get-Command minikube -ErrorAction SilentlyContinue)) {
        Write-Err "minikube not found. Install from https://minikube.sigs.k8s.io/"
    }
    
    # Check kubectl
    if (-not (Get-Command kubectl -ErrorAction SilentlyContinue)) {
        Write-Err "kubectl not found. Install from https://kubernetes.io/docs/tasks/tools/"
    }
    
    # Check docker
    if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
        Write-Err "docker not found. Install Docker Desktop."
    }
    
    # Check docker is running
    $dockerInfo = docker info 2>&1
    if ($LASTEXITCODE -ne 0) {
        Write-Err "Docker is not running. Start Docker Desktop."
    }
    
    Write-Info "[OK] All prerequisites met"
}

function Start-MinikubeCluster {
    Write-Info "Checking Minikube cluster status..."
    
    $status = minikube status -p $CLUSTER_NAME 2>&1
    if ($LASTEXITCODE -eq 0) {
        Write-Warn "Minikube cluster '$CLUSTER_NAME' already exists"
        $response = Read-Host "Delete and recreate? (y/N)"
        if ($response -eq "y" -or $response -eq "Y") {
            Write-Info "Deleting existing cluster..."
            minikube delete -p $CLUSTER_NAME 2>&1 | Out-Null
        } else {
            Write-Info "Using existing cluster"
            Set-MinikubeDockerEnv
            return
        }
    }
    
    Write-Info "Creating Minikube cluster with InPlacePodVerticalScaling feature gate..."
    Write-Warn "Note: First-time startup downloads Kubernetes image. With your network speed (60 Mbps), this may take 20-30 minutes."
    Write-Warn "Using smaller K8s version (v1.27.0) to reduce download size (~500 MB instead of 2 GB)"
    Write-Host ""
    
    # Start Minikube with required feature gate for in-place CPU resizing
    # Using v1.27.0 (older but smaller image) to reduce download time on slow connections
    minikube start -p $CLUSTER_NAME `
        --feature-gates=InPlacePodVerticalScaling=true `
        --kubernetes-version=v1.35.0 `
        --driver=docker `
        --memory=4096 `
        --cpus=4 `
        --wait=all `
        --wait-timeout=3600s
    
    if ($LASTEXITCODE -ne 0) {
        Write-Err "Failed to start Minikube cluster"
    }
    
    Set-MinikubeDockerEnv
    
    Write-Info "Waiting for cluster to be ready..."
    kubectl wait --for=condition=Ready nodes --all --timeout=120s
    
    if ($LASTEXITCODE -ne 0) {
        Write-Err "Cluster nodes not ready"
    }
    
    Write-Info "[OK] Minikube cluster started"
}

function Set-MinikubeDockerEnv {
    Write-Info "Configuring Docker environment for Minikube..."
    
    $dockerEnvOutput = minikube -p $CLUSTER_NAME docker-env
    foreach ($line in $dockerEnvOutput) {
        if ($line -match '^\$Env:(\w+)\s*=\s*"(.+)"') {
            Set-Item -Path "env:$($matches[1])" -Value $matches[2]
        }
    }
}

function Install-MetricsServer {
    Write-Info "Installing metrics-server (required for resource monitoring)..."
    
    # Check if already installed (suppress error output)
    $ErrorActionPreference = "SilentlyContinue"
    kubectl get deployment metrics-server -n kube-system 2>&1 | Out-Null
    $metricsExists = $LASTEXITCODE -eq 0
    $ErrorActionPreference = "Stop"
    
    if ($metricsExists) {
        Write-Info "metrics-server already installed"
        return
    }
    
    # Install metrics-server
    minikube addons enable metrics-server -p $CLUSTER_NAME 2>&1 | Out-Null
    
    Write-Info "Waiting for metrics-server to be ready..."
    Start-Sleep -Seconds 10
    $ErrorActionPreference = "SilentlyContinue"
    kubectl wait --for=condition=Available deployment/metrics-server -n kube-system --timeout=120s 2>&1 | Out-Null
    $ErrorActionPreference = "Stop"
    
    Write-Info "[OK] metrics-server installed"
}

function Apply-ClusterPolicies {
    Write-Info "Applying cluster-wide CPU policies..."
    
    # Create demo namespace with MBCAS management enabled
    kubectl apply -f "$PROJECT_ROOT\k8s\policy\namespace-demo.yaml"
    
    # Apply namespace exclusions (kube-system, mbcas-system)
    kubectl apply -f "$PROJECT_ROOT\k8s\policy\namespace-exclusions.yaml"
    
    # Apply LimitRange to demo namespace (ensures all pods have CPU limits)
    kubectl apply -f "$PROJECT_ROOT\k8s\policy\limitrange-default.yaml" -n demo
    
    Write-Info "[OK] Cluster policies applied"
    
    # Verify LimitRange
    Write-Info "Verifying LimitRange in demo namespace:"
    kubectl get limitrange -n demo
}

function Verify-FeatureGate {
    Write-Info "Verifying InPlacePodVerticalScaling feature gate..."
    
    # Check Kubernetes version
    $serverVersion = kubectl version -o json 2>&1 | ConvertFrom-Json
    if ($serverVersion.serverVersion) {
        $k8sVersion = $serverVersion.serverVersion.gitVersion
        Write-Info "Kubernetes version: $k8sVersion"
        
        if ($k8sVersion -match 'v?(\d+)\.(\d+)') {
            $major = [int]$matches[1]
            $minor = [int]$matches[2]
            if ($major -lt 1 -or ($major -eq 1 -and $minor -lt 27)) {
                Write-Err "Kubernetes $k8sVersion is too old. Requires 1.27+ for InPlacePodVerticalScaling"
            }
        }
    }
    
    # Quick test: check if pods/resize subresource is available
    # This is the definitive test for the feature gate
    $resources = kubectl api-resources --api-group="" -o name 2>&1
    if ($resources -match "pods/resize") {
        Write-Info "[OK] pods/resize subresource available - InPlacePodVerticalScaling is enabled"
    } else {
        Write-Warn "pods/resize subresource not found - feature gate may not be enabled"
        Write-Warn "MBCAS may not work correctly. Verify minikube started with --feature-gates=InPlacePodVerticalScaling=true"
    }
}

function Show-Summary {
    Write-Host ""
    Write-Host "========================================" -ForegroundColor Cyan
    Write-Host "  MBCAS Cluster Ready" -ForegroundColor Cyan
    Write-Host "========================================" -ForegroundColor Cyan
    Write-Host ""
    Write-Host "Cluster: $CLUSTER_NAME" -ForegroundColor White
    Write-Host "Namespaces:"
    Write-Host "  - demo (MBCAS managed, LimitRange applied)" -ForegroundColor Green
    Write-Host "  - kube-system (excluded)" -ForegroundColor Yellow
    Write-Host "  - mbcas-system (excluded, for MBCAS itself)" -ForegroundColor Yellow
    Write-Host ""
    Write-Host "Next step: .\scripts\02-deploy-mbcas.ps1" -ForegroundColor Cyan
    Write-Host ""
}

# Main execution
Write-Host ""
Write-Host "MBCAS Demo - Step 1: Start Cluster" -ForegroundColor Cyan
Write-Host "===================================" -ForegroundColor Cyan
Write-Host ""

Test-Prerequisites
Start-MinikubeCluster
Install-MetricsServer
Apply-ClusterPolicies
Verify-FeatureGate
Show-Summary
