# MBCAS Demo Script - Cleanup
# Removes demo resources and optionally the Minikube cluster
#
# Usage: .\scripts\cleanup.ps1 [-All] [-Force]

param(
    [switch]$All,      # Also delete Minikube cluster
    [switch]$Force     # Skip confirmation prompts
)

$ErrorActionPreference = "Stop"
$CLUSTER_NAME = "mbcas"
$NAMESPACES = @("demo", "urbanmove")

# Helper functions
function Write-Info { Write-Host "[INFO] $args" -ForegroundColor Green }
function Write-Warn { Write-Host "[WARN] $args" -ForegroundColor Yellow }

function Remove-DemoResources {
    Write-Info "Removing demo and application resources..."
    
    # Delete namespaces (cascades to all resources)
    foreach ($ns in $NAMESPACES) {
        kubectl delete namespace $ns --ignore-not-found=true 2>&1 | Out-Null
    }
    
    Write-Info "[OK] Demo resources removed"
}

function Remove-MBCAS {
    Write-Info "Removing MBCAS components..."
    
    # Delete MBCAS deployments
    kubectl delete deployment mbcas-controller -n mbcas-system --ignore-not-found=true
    kubectl delete daemonset mbcas-agent -n mbcas-system --ignore-not-found=true
    
    # Delete RBAC
    kubectl delete clusterrolebinding mbcas-controller-rolebinding --ignore-not-found=true
    kubectl delete clusterrole mbcas-controller-role --ignore-not-found=true
    kubectl delete serviceaccount mbcas-controller -n mbcas-system --ignore-not-found=true
    kubectl delete serviceaccount mbcas-agent -n mbcas-system --ignore-not-found=true
    
    # Delete CRD (cascades to all PodAllocations)
    kubectl delete crd podallocations.allocation.mbcas.io --ignore-not-found=true
    
    # Delete namespace
    kubectl delete namespace mbcas-system --ignore-not-found=true
    
    Write-Info "[OK] MBCAS removed"
}

function Remove-Cluster {
    Write-Info "Deleting Minikube cluster '$CLUSTER_NAME'..."
    
    minikube delete -p $CLUSTER_NAME
    
    if ($LASTEXITCODE -eq 0) {
        Write-Info "[OK] Cluster deleted"
    } else {
        Write-Warn "Cluster deletion may have had issues"
    }
}

function Show-Status {
    Write-Host ""
    Write-Host "========================================" -ForegroundColor Cyan
    Write-Host "  Cleanup Status" -ForegroundColor Cyan
    Write-Host "========================================" -ForegroundColor Cyan
    Write-Host ""
    
    # Check what remains
    Write-Host "Remaining namespaces:" -ForegroundColor White
    kubectl get namespaces 2>$null
    
    Write-Host ""
    Write-Host "Minikube clusters:" -ForegroundColor White
    minikube profile list 2>$null
}

# Main execution
Write-Host ""
Write-Host "MBCAS Demo - Cleanup" -ForegroundColor Cyan
Write-Host "====================" -ForegroundColor Cyan
Write-Host ""

# Confirmation
if (-not $Force) {
    Write-Host "This will remove:" -ForegroundColor Yellow
    Write-Host "  - Demo namespace and all resources"
    Write-Host "  - MBCAS controller, agent, and CRD"
    if ($All) {
        Write-Host "  - Minikube cluster '$CLUSTER_NAME'" -ForegroundColor Red
    }
    Write-Host ""
    
    $confirm = Read-Host "Continue? (y/N)"
    if ($confirm -ne "y" -and $confirm -ne "Y") {
        Write-Host "Cancelled." -ForegroundColor Yellow
        exit 0
    }
}

# Check if cluster is running
$status = minikube status -p $CLUSTER_NAME 2>&1
if ($LASTEXITCODE -ne 0) {
    Write-Warn "Minikube cluster '$CLUSTER_NAME' is not running"
    if ($All) {
        # Try to delete anyway
        Remove-Cluster
    }
    exit 0
}

# Perform cleanup
Remove-DemoResources
Remove-MBCAS

if ($All) {
    Remove-Cluster
} else {
    Show-Status
    Write-Host ""
    Write-Host "To also delete the cluster: .\scripts\cleanup.ps1 -All" -ForegroundColor Cyan
}

Write-Host ""
Write-Host "[OK] Cleanup complete" -ForegroundColor Green
Write-Host ""
