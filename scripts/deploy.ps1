#!/usr/bin/env pwsh
# MBCAS Deploy Script
# Deploys MBCAS components to the current Kubernetes cluster
# Uses k8s/ directory structure

param(
    [switch]$Delete  # Delete instead of apply
)

$ErrorActionPreference = "Stop"

function Write-Step { param($msg) Write-Host "`n==> $msg" -ForegroundColor Cyan }
function Write-Success { param($msg) Write-Host "[OK] $msg" -ForegroundColor Green }
function Write-Err { param($msg) Write-Host "[ERROR] $msg" -ForegroundColor Red }

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$ProjectRoot = Split-Path -Parent $ScriptDir
$K8sDir = "$ProjectRoot\k8s"

Write-Host "================================================================================" -ForegroundColor Magenta
Write-Host "  MBCAS Deploy" -ForegroundColor Magenta
Write-Host "================================================================================" -ForegroundColor Magenta
Write-Host "Using manifests from: $K8sDir"

if ($Delete) {
    Write-Step "Deleting MBCAS components..."
    
    # Delete in reverse order
    kubectl delete -f "$K8sDir\mbcas\controller-deployment.yaml" --ignore-not-found
    kubectl delete -f "$K8sDir\mbcas\agent-daemonset.yaml" --ignore-not-found
    kubectl delete -f "$K8sDir\mbcas\agent-rbac.yaml" --ignore-not-found
    kubectl delete -f "$K8sDir\mbcas\role_binding.yaml" --ignore-not-found
    kubectl delete -f "$K8sDir\mbcas\role.yaml" --ignore-not-found
    kubectl delete -f "$K8sDir\mbcas\service_account.yaml" --ignore-not-found
    kubectl delete -f "$K8sDir\mbcas\allocation.mbcas.io_podallocations.yaml" --ignore-not-found
    kubectl delete -f "$K8sDir\policy\limitrange-default.yaml" --ignore-not-found
    kubectl delete -f "$K8sDir\mbcas\namespace.yaml" --ignore-not-found
    
    Write-Success "MBCAS components deleted"
    exit 0
}

# Apply in correct order
Write-Step "Creating namespace..."
kubectl apply -f "$K8sDir\mbcas\namespace.yaml"
Write-Success "Namespace created"

Write-Step "Applying CRD..."
kubectl apply -f "$K8sDir\mbcas\allocation.mbcas.io_podallocations.yaml"
Write-Success "CRD applied"

Write-Step "Creating service account..."
kubectl apply -f "$K8sDir\mbcas\service_account.yaml"
Write-Success "Service account created"

Write-Step "Applying RBAC (roles and bindings)..."
kubectl apply -f "$K8sDir\mbcas\role.yaml"
kubectl apply -f "$K8sDir\mbcas\role_binding.yaml"
kubectl apply -f "$K8sDir\mbcas\agent-rbac.yaml"
Write-Success "RBAC applied"

Write-Step "Deploying Agent DaemonSet..."
kubectl apply -f "$K8sDir\mbcas\agent-daemonset.yaml"
Write-Success "Agent DaemonSet deployed"

Write-Step "Deploying Controller..."
kubectl apply -f "$K8sDir\mbcas\controller-deployment.yaml"
Write-Success "Controller deployed"

Write-Step "Applying LimitRange policy (optional)..."
if (Test-Path "$K8sDir\policy\limitrange-default.yaml") {
    kubectl apply -f "$K8sDir\policy\limitrange-default.yaml"
    Write-Success "LimitRange policy applied"
} else {
    Write-Host "Skipping - limitrange-default.yaml not found" -ForegroundColor Yellow
}

Write-Step "Waiting for components to be ready..."
Write-Host "Waiting for Agent DaemonSet..."
kubectl rollout status daemonset/mbcas-agent -n mbcas-system --timeout=120s

Write-Host "Waiting for Controller Deployment..."
kubectl rollout status deployment/mbcas-controller -n mbcas-system --timeout=120s

Write-Step "Verifying deployment..."
kubectl get pods -n mbcas-system -o wide

Write-Host "`n================================================================================" -ForegroundColor Green
Write-Host "  MBCAS Deployed Successfully!" -ForegroundColor Green
Write-Host "================================================================================" -ForegroundColor Green
Write-Host ""
Write-Host "Useful commands:"
Write-Host "  kubectl get pods -n mbcas-system"
Write-Host "  kubectl logs -n mbcas-system -l app.kubernetes.io/component=agent -f"
Write-Host "  kubectl logs -n mbcas-system -l app.kubernetes.io/component=controller -f"
Write-Host "  kubectl get podallocations -A"
Write-Host ""