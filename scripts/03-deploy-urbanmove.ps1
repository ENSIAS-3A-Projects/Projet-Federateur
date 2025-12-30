# MBCAS Demo Script 03 - Deploy UrbanMoveMS Microservices
# Builds and deploys the full microservices stack (bus, ticket, trajet, user, gateway)
# to test MBCAS with real multi-service workloads
#
# Prerequisites: 02-deploy-mbcas.ps1 completed, Docker images built
# Usage: .\scripts\03-deploy-urbanmove.ps1

param(
    [switch]$BuildImages,  # Build Docker images locally
    [switch]$Push          # Push images to registry
)

$ErrorActionPreference = "Stop"
$SCRIPT_DIR = Split-Path -Parent $MyInvocation.MyCommand.Path
$PROJECT_ROOT = Split-Path -Parent $SCRIPT_DIR
$APP_ROOT = "$PROJECT_ROOT\Software-Oriented-Application"

# Helper functions
function Write-Info { Write-Host "[INFO] $args" -ForegroundColor Green }
function Write-Warn { Write-Host "[WARN] $args" -ForegroundColor Yellow }
function Write-Err { Write-Host "[ERROR] $args" -ForegroundColor Red; exit 1 }

function Build-Images {
    Write-Info "Building UrbanMoveMS Docker images..."
    
    if (-not (Test-Path "$APP_ROOT\build-local.ps1")) {
        Write-Err "build-local.ps1 not found in $APP_ROOT"
    }
    
    # Build images for Minikube
    Write-Info "Building images (this may take 5-10 minutes)..."
    & "$APP_ROOT\build-local.ps1"
    
    if ($LASTEXITCODE -ne 0) {
        Write-Err "Failed to build images"
    }
    
    Write-Info "[OK] Images built successfully"
}

function Prepare-Namespace {
    Write-Info "Preparing urbanmove namespace with MBCAS management..."
    
    # Create namespace for UrbanMoveMS
    $namespaceManifest = @"
apiVersion: v1
kind: Namespace
metadata:
  name: urbanmove
  labels:
    mbcas.io/managed: "true"
---
apiVersion: v1
kind: LimitRange
metadata:
  name: cpu-defaults
  namespace: urbanmove
spec:
  limits:
  - type: Container
    default:
      cpu: "500m"      # Default limit
    defaultRequest:
      cpu: "100m"      # Default request
    min:
      cpu: "50m"       # Min allowed
    max:
      cpu: "4000m"     # Max allowed
"@
    
    $namespaceManifest | kubectl apply -f -
    Write-Info "[OK] Namespace configured"
}

function Deploy-Microservices {
    Write-Info "Deploying UrbanMoveMS microservices to Kubernetes..."
    
    # Check if k8s manifests exist
    $k8sDir = "$APP_ROOT\k8s"
    if (-not (Test-Path $k8sDir)) {
        Write-Err "Kubernetes manifests not found in $k8sDir"
    }
    
    # Apply all manifests in the k8s directory
    Write-Info "Applying Kubernetes manifests..."
    
    # Get all YAML files (PVCs must come first)
    $yamlFiles = @(
        "postgres-bus-data-persistentvolumeclaim.yaml"
        "postgres-ticket-data-persistentvolumeclaim.yaml"
        "postgres-trajet-data-persistentvolumeclaim.yaml"
        "postgres-user-data-persistentvolumeclaim.yaml"
        "postgres-bus-deployment.yaml"
        "postgres-ticket-deployment.yaml"
        "postgres-trajet-deployment.yaml"
        "postgres-user-deployment.yaml"
        "postgres-bus-service.yaml"
        "postgres-ticket-service.yaml"
        "postgres-trajet-service.yaml"
        "postgres-user-service.yaml"
        "zookeeper-deployment.yaml"
        "zookeeper-service.yaml"
        "kafka-deployment.yaml"
        "kafka-service.yaml"
        "bus-service-deployment.yaml"
        "bus-service-service.yaml"
        "ticket-service-deployment.yaml"
        "ticket-service-service.yaml"
        "trajet-service-deployment.yaml"
        "trajet-service-service.yaml"
        "user-service-deployment.yaml"
        "user-service-service.yaml"
        "gateway-deployment.yaml"
        "gateway-service.yaml"
    )
    
    foreach ($file in $yamlFiles) {
        $path = "$k8sDir\$file"
        if (Test-Path $path) {
            Write-Info "  Applying $file..."
            $ErrorActionPreference = "SilentlyContinue"
            kubectl apply -f $path -n urbanmove 2>&1 | Out-Null
            $ErrorActionPreference = "Stop"
        }
    }
    
    Write-Info "[OK] Manifests applied"
}

function Wait-ForServices {
    Write-Info "Waiting for services to be ready..."
    
    # Wait for database pods
    Write-Info "  Waiting for PostgreSQL instances..."
    $ErrorActionPreference = "SilentlyContinue"
    kubectl wait --for=condition=Ready pods -l app=postgres-bus -n urbanmove --timeout=120s 2>&1 | Out-Null
    kubectl wait --for=condition=Ready pods -l app=postgres-ticket -n urbanmove --timeout=120s 2>&1 | Out-Null
    kubectl wait --for=condition=Ready pods -l app=postgres-trajet -n urbanmove --timeout=120s 2>&1 | Out-Null
    kubectl wait --for=condition=Ready pods -l app=postgres-user -n urbanmove --timeout=120s 2>&1 | Out-Null
    $ErrorActionPreference = "Stop"
    
    # Wait for Kafka/Zookeeper
    Write-Info "  Waiting for Kafka/Zookeeper..."
    $ErrorActionPreference = "SilentlyContinue"
    kubectl wait --for=condition=Ready pods -l app=zookeeper -n urbanmove --timeout=120s 2>&1 | Out-Null
    kubectl wait --for=condition=Ready pods -l app=kafka -n urbanmove --timeout=120s 2>&1 | Out-Null
    $ErrorActionPreference = "Stop"
    
    # Wait for application services
    Write-Info "  Waiting for application services..."
    $ErrorActionPreference = "SilentlyContinue"
    kubectl wait --for=condition=Ready pods -l app=bus-service -n urbanmove --timeout=180s 2>&1 | Out-Null
    kubectl wait --for=condition=Ready pods -l app=ticket-service -n urbanmove --timeout=180s 2>&1 | Out-Null
    kubectl wait --for=condition=Ready pods -l app=trajet-service -n urbanmove --timeout=180s 2>&1 | Out-Null
    kubectl wait --for=condition=Ready pods -l app=user-service -n urbanmove --timeout=180s 2>&1 | Out-Null
    kubectl wait --for=condition=Ready pods -l app=gateway -n urbanmove --timeout=180s 2>&1 | Out-Null
    $ErrorActionPreference = "Stop"
    
    Write-Info "[OK] Services ready (or timeout reached - check status below)"
}

function Verify-MBCAS-Management {
    Write-Info "Verifying MBCAS is managing UrbanMoveMS pods..."
    
    Write-Info "Waiting for MBCAS to discover pods (20s)..."
    Start-Sleep -Seconds 20
    
    # Check PodAllocations
    $ErrorActionPreference = "SilentlyContinue"
    $allocations = kubectl get podallocations -n urbanmove -o json 2>&1 | ConvertFrom-Json
    $ErrorActionPreference = "Stop"
    
    if ($allocations -and $allocations.items) {
        Write-Info "[OK] MBCAS is managing $($allocations.items.Count) pods in urbanmove namespace"
    } else {
        Write-Warn "No PodAllocations found yet - MBCAS may still be initializing"
        Write-Warn "Check manually: kubectl get podallocations -n urbanmove -o wide"
    }
}

function Show-Status {
    Write-Host ""
    Write-Host "========================================" -ForegroundColor Cyan
    Write-Host "  UrbanMoveMS Deployment Status" -ForegroundColor Cyan
    Write-Host "========================================" -ForegroundColor Cyan
    Write-Host ""
    
    Write-Host "Pods in urbanmove namespace:" -ForegroundColor White
    kubectl get pods -n urbanmove -o wide
    
    Write-Host ""
    Write-Host "Services:" -ForegroundColor White
    kubectl get svc -n urbanmove
    
    Write-Host ""
    Write-Host "Pod Resource Allocations:" -ForegroundColor White
    kubectl get pods -n urbanmove -o custom-columns="NAME:.metadata.name,CPU_REQ:.spec.containers[0].resources.requests.cpu,CPU_LIM:.spec.containers[0].resources.limits.cpu,STATUS:.status.phase" --no-headers
    
    Write-Host ""
    Write-Host "MBCAS PodAllocations:" -ForegroundColor White
    $ErrorActionPreference = "SilentlyContinue"
    kubectl get podallocations -n urbanmove -o wide 2>&1 | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Write-Host "  (Still initializing...)"
    }
    $ErrorActionPreference = "Stop"
    
    Write-Host ""
    Write-Host "Services deployed:" -ForegroundColor Green
    Write-Host "  - PostgreSQL (4 instances): bus, ticket, trajet, user"
    Write-Host "  - Zookeeper & Kafka: Message broker"
    Write-Host "  - bus-service: Fleet and geolocation"
    Write-Host "  - ticket-service: Ticket purchases"
    Write-Host "  - trajet-service: Routes and schedules"
    Write-Host "  - user-service: User management"
    Write-Host "  - gateway: Spring Cloud Gateway (port 8888)"
    Write-Host ""
    Write-Host "Gateway endpoint: localhost:8888 (via port-forward)"
    Write-Host "  kubectl port-forward svc/gateway 8888:8888 -n urbanmove"
    Write-Host ""
    Write-Host "Next step: .\scripts\04-generate-load.ps1" -ForegroundColor Cyan
    Write-Host ""
}

# Main execution
Write-Host ""
Write-Host "MBCAS Demo - Step 3: Deploy UrbanMoveMS" -ForegroundColor Cyan
Write-Host "=======================================" -ForegroundColor Cyan
Write-Host ""

if ($BuildImages) {
    Build-Images
}

Prepare-Namespace
Deploy-Microservices
Wait-ForServices
Verify-MBCAS-Management
Show-Status
