# MBCAS Demo Script 02 - Deploy MBCAS
# Builds and deploys MBCAS controller and agent
#
# Prerequisites: 01-start-cluster.ps1 completed
# Usage: .\scripts\02-deploy-mbcas.ps1

$ErrorActionPreference = "Stop"
$CLUSTER_NAME = "mbcas"
$SCRIPT_DIR = Split-Path -Parent $MyInvocation.MyCommand.Path
$PROJECT_ROOT = Split-Path -Parent $SCRIPT_DIR

# Helper functions
function Write-Info { Write-Host "[INFO] $args" -ForegroundColor Green }
function Write-Warn { Write-Host "[WARN] $args" -ForegroundColor Yellow }
function Write-Err { Write-Host "[ERROR] $args" -ForegroundColor Red; exit 1 }

function Set-MinikubeDockerEnv {
    Write-Info "Configuring Docker environment for Minikube..."
    
    $dockerEnvOutput = minikube -p $CLUSTER_NAME docker-env
    foreach ($line in $dockerEnvOutput) {
        if ($line -match '^\$Env:(\w+)\s*=\s*"(.+)"') {
            Set-Item -Path "env:$($matches[1])" -Value $matches[2]
        }
    }
}

function Build-Images {
    Write-Info "Building MBCAS Docker images..."
    
    Push-Location $PROJECT_ROOT
    
    try {
        # Verify we're in the right directory
        if (-not (Test-Path "go.mod")) {
            Write-Err "Must run from MBCAS project root (where go.mod is)"
        }
        
        # Build controller image
        Write-Info "Building controller image..."
        docker build -f Dockerfile.controller -t mbcas-controller:latest .
        if ($LASTEXITCODE -ne 0) {
            Write-Err "Failed to build controller image"
        }
        
        # Build agent image
        Write-Info "Building agent image..."
        docker build -f Dockerfile.agent -t mbcas-agent:latest .
        if ($LASTEXITCODE -ne 0) {
            Write-Err "Failed to build agent image"
        }
        
        Write-Info "[OK] Images built successfully"
        
        # List built images
        Write-Info "Built images:"
        docker images | Select-String "mbcas"
    }
    finally {
        Pop-Location
    }
}

function Deploy-MBCAS {
    Write-Info "Deploying MBCAS to cluster..."
    
    # Create namespace (with exclusion label)
    kubectl apply -f "$PROJECT_ROOT\k8s\mbcas\namespace.yaml"
    
    # Apply exclusion label to mbcas-system
    kubectl label namespace mbcas-system mbcas.io/managed=false --overwrite
    
    # Deploy CRD
    Write-Info "Deploying PodAllocation CRD..."
    kubectl apply -f "$PROJECT_ROOT\k8s\mbcas\allocation.mbcas.io_podallocations.yaml"
    
    # Wait for CRD to be established
    kubectl wait --for=condition=Established crd/podallocations.allocation.mbcas.io --timeout=30s
    if ($LASTEXITCODE -ne 0) {
        Write-Err "CRD not established"
    }
    
    # Deploy RBAC
    Write-Info "Deploying RBAC..."
    kubectl apply -f "$PROJECT_ROOT\k8s\mbcas\role.yaml"
    kubectl apply -f "$PROJECT_ROOT\k8s\mbcas\role_binding.yaml"
    kubectl apply -f "$PROJECT_ROOT\k8s\mbcas\service_account.yaml"
    kubectl apply -f "$PROJECT_ROOT\k8s\mbcas\agent-rbac.yaml"
    
    # Deploy controller
    Write-Info "Deploying controller..."
    kubectl apply -f "$PROJECT_ROOT\k8s\mbcas\controller-deployment.yaml"
    
    # Deploy agent
    Write-Info "Deploying agent DaemonSet..."
    kubectl apply -f "$PROJECT_ROOT\k8s\mbcas\agent-daemonset.yaml"
    
    Write-Info "[OK] MBCAS manifests applied"
}

function Wait-ForReady {
    Write-Info "Waiting for MBCAS components to be ready..."
    
    # Wait for controller
    Write-Info "Waiting for controller deployment..."
    kubectl wait --for=condition=Available deployment/mbcas-controller -n mbcas-system --timeout=120s
    if ($LASTEXITCODE -ne 0) {
        Write-Warn "Controller not ready - checking logs:"
        kubectl logs deployment/mbcas-controller -n mbcas-system --tail=20
        Write-Err "Controller deployment failed"
    }
    
    # Wait for agent
    Write-Info "Waiting for agent DaemonSet..."
    kubectl rollout status daemonset/mbcas-agent -n mbcas-system --timeout=120s
    if ($LASTEXITCODE -ne 0) {
        Write-Warn "Agent not ready - checking logs:"
        kubectl logs daemonset/mbcas-agent -n mbcas-system --tail=20
        Write-Err "Agent DaemonSet failed"
    }
    
    Write-Info "[OK] All MBCAS components ready"
}

function Show-Status {
    Write-Host ""
    Write-Host "========================================" -ForegroundColor Cyan
    Write-Host "  MBCAS Deployment Status" -ForegroundColor Cyan
    Write-Host "========================================" -ForegroundColor Cyan
    Write-Host ""
    
    Write-Host "Pods in mbcas-system:" -ForegroundColor White
    kubectl get pods -n mbcas-system -o wide
    
    Write-Host ""
    Write-Host "CRD Status:" -ForegroundColor White
    kubectl get crd podallocations.allocation.mbcas.io
    
    Write-Host ""
    Write-Host "Current PodAllocations (should be empty):" -ForegroundColor White
    kubectl get podallocations -A
    
    Write-Host ""
    Write-Host "Next step: .\scripts\03-deploy-demo-app.ps1" -ForegroundColor Cyan
    Write-Host ""
}

# Main execution
Write-Host ""
Write-Host "MBCAS Demo - Step 2: Deploy MBCAS" -ForegroundColor Cyan
Write-Host "==================================" -ForegroundColor Cyan
Write-Host ""

Set-MinikubeDockerEnv
Build-Images
Deploy-MBCAS
Wait-ForReady
Show-Status
