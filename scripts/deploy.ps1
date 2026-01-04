#!/usr/bin/env pwsh
# MBCAS Deploy Script
# Deploys MBCAS components to the current Kubernetes cluster

param(
    [switch]$Delete  # Delete instead of apply
)

$ErrorActionPreference = "Stop"

function Write-Step { param($msg) Write-Host "`n==> $msg" -ForegroundColor Cyan }
function Write-Success { param($msg) Write-Host "[OK] $msg" -ForegroundColor Green }
function Write-Err { param($msg) Write-Host "[ERROR] $msg" -ForegroundColor Red }

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$ProjectRoot = Split-Path -Parent $ScriptDir

Write-Host "================================================================================" -ForegroundColor Magenta
Write-Host "  MBCAS Deploy" -ForegroundColor Magenta
Write-Host "================================================================================" -ForegroundColor Magenta

$action = if ($Delete) { "delete" } else { "apply" }

if ($Delete) {
    Write-Step "Deleting MBCAS components..."
    
    # Delete in reverse order
    kubectl delete -f "$ProjectRoot\config\controller\deployment.yaml" --ignore-not-found
    kubectl delete -f "$ProjectRoot\config\agent\daemonset.yaml" --ignore-not-found
    kubectl delete -f "$ProjectRoot\config\agent\rbac.yaml" --ignore-not-found
    kubectl delete -f "$ProjectRoot\config\rbac\service_account.yaml" --ignore-not-found
    kubectl delete -f "$ProjectRoot\config\crd\bases\allocation.mbcas.io_podallocations.yaml" --ignore-not-found
    kubectl delete -f "$ProjectRoot\config\namespace.yaml" --ignore-not-found
    
    Write-Success "MBCAS components deleted"
    exit 0
}

# Apply in correct order
Write-Step "Creating namespace..."
kubectl apply -f "$ProjectRoot\config\namespace.yaml"
Write-Success "Namespace created"

Write-Step "Applying CRD..."
kubectl apply -f "$ProjectRoot\config\crd\bases\allocation.mbcas.io_podallocations.yaml"
Write-Success "CRD applied"

Write-Step "Creating service accounts..."
# Create service accounts inline since they may not exist as separate files
$serviceAccounts = @"
apiVersion: v1
kind: ServiceAccount
metadata:
  name: mbcas-agent
  namespace: mbcas-system
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: mbcas-controller
  namespace: mbcas-system
"@
$serviceAccounts | kubectl apply -f -
Write-Success "Service accounts created"

Write-Step "Applying Agent RBAC..."
kubectl apply -f "$ProjectRoot\config\agent\rbac.yaml"
Write-Success "Agent RBAC applied"

Write-Step "Applying Controller RBAC..."
# Create controller RBAC inline
$controllerRbac = @"
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: mbcas-controller
rules:
- apiGroups: ["allocation.mbcas.io"]
  resources: ["podallocations"]
  verbs: ["get", "list", "watch", "update", "patch"]
- apiGroups: ["allocation.mbcas.io"]
  resources: ["podallocations/status"]
  verbs: ["get", "update", "patch"]
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["get", "list", "watch", "patch"]
- apiGroups: [""]
  resources: ["pods/resize"]
  verbs: ["patch"]
- apiGroups: [""]
  resources: ["events"]
  verbs: ["create", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: mbcas-controller
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: mbcas-controller
subjects:
- kind: ServiceAccount
  name: mbcas-controller
  namespace: mbcas-system
"@
$controllerRbac | kubectl apply -f -
Write-Success "Controller RBAC applied"

Write-Step "Deploying Agent DaemonSet..."
kubectl apply -f "$ProjectRoot\config\agent\daemonset.yaml"
Write-Success "Agent DaemonSet deployed"

Write-Step "Deploying Controller..."
kubectl apply -f "$ProjectRoot\config\controller\deployment.yaml"
Write-Success "Controller deployed"

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
