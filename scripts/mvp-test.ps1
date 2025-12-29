# MBCAS MVP Deploy and Test Script (PowerShell)
# Prerequisites: minikube, kubectl, docker

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

# Step 1: Create Minikube cluster
function Create-Cluster {
    Write-Info "Creating Minikube cluster with InPlacePodVerticalScaling feature gate..."
    
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
            # Set up Docker environment
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
            return
        }
    }
    
    # Start Minikube with feature gate
    minikube start -p $CLUSTER_NAME `
        --feature-gates=InPlacePodVerticalScaling=true `
        --kubernetes-version=latest `
        --driver=docker `
        --memory=4096 `
        --cpus=2
    
    if ($LASTEXITCODE -ne 0) {
        Write-Error "Failed to start Minikube cluster"
    }
    
    # Set up Docker environment for Minikube
    # Parse minikube docker-env output manually to handle Windows paths
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
    
    Write-Info "Waiting for cluster to be ready..."
    kubectl wait --for=condition=Ready nodes --all --timeout=120s
    
    # Verify feature gate
    Write-Info "Verifying InPlacePodVerticalScaling feature gate..."
    
    # Check kubectl version
    $kubectlVersion = kubectl version --client -o json 2>&1 | ConvertFrom-Json
    if ($kubectlVersion) {
        $clientVersion = $kubectlVersion.clientVersion.gitVersion
        Write-Info "kubectl version: $clientVersion"
    }
    
    # Check Kubernetes server version
    $serverVersion = kubectl version -o json 2>&1 | ConvertFrom-Json
    if ($serverVersion) {
        $k8sVersion = $serverVersion.serverVersion.gitVersion
        Write-Info "Kubernetes server version: $k8sVersion"
        
        # Extract major.minor
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
}

# Step 2: Build images
function Build-Images {
    Write-Info "Building Docker images..."
    
    if (-not (Test-Path "go.mod")) {
        Write-Error "Must run from MBCAS project root (where go.mod is)"
    }
    
    # Ensure we're using Minikube's Docker daemon
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
    
    docker build -f Dockerfile.controller -t mbcas-controller:latest .
    if ($LASTEXITCODE -ne 0) {
        Write-Error "Failed to build controller image"
    }
    
    docker build -f Dockerfile.agent -t mbcas-agent:latest .
    if ($LASTEXITCODE -ne 0) {
        Write-Error "Failed to build agent image"
    }
    
    Write-Info "Images built in Minikube's Docker daemon (no need to load)"
}

# Step 3: Deploy MBCAS
function Deploy-MBCAS {
    Write-Info "Deploying MBCAS components..."
    
    # Namespace
    kubectl apply -f config/namespace.yaml
    
    # CRD
    kubectl apply -f config/crd/bases/allocation.mbcas.io_podallocations.yaml
    
    # Wait for CRD to be established
    kubectl wait --for=condition=Established crd/podallocations.allocation.mbcas.io --timeout=30s
    
    # RBAC
    kubectl apply -f config/rbac/
    kubectl apply -f config/agent/rbac.yaml
    
    # Controller
    kubectl apply -f config/controller/deployment.yaml
    
    # Agent
    kubectl apply -f config/agent/daemonset.yaml
    
    Write-Info "Waiting for controller to be ready..."
    kubectl wait --for=condition=Available deployment/mbcas-controller -n mbcas-system --timeout=60s
    
    Write-Info "Waiting for agent to be ready..."
    kubectl rollout status daemonset/mbcas-agent -n mbcas-system --timeout=60s
    
    Write-Info "[OK] MBCAS deployed successfully"
}

# Step 4: Deploy test workloads
function Deploy-TestWorkloads {
    Write-Info "Deploying test workloads..."
    
    kubectl create namespace test-mbcas --dry-run=client -o yaml | kubectl apply -f -
    
    $testWorkloads = @"
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cpu-stress-high
  namespace: test-mbcas
  labels:
    scenario: high-demand
spec:
  replicas: 1
  selector:
    matchLabels:
      app: cpu-stress-high
  template:
    metadata:
      labels:
        app: cpu-stress-high
    spec:
      containers:
      - name: stress
        image: polinux/stress
        command: ["stress"]
        args: ["-c", "2", "-t", "1800"]
        resources:
          requests:
            cpu: 200m
          limits:
            cpu: 1000m
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cpu-stress-low
  namespace: test-mbcas
  labels:
    scenario: low-demand
spec:
  replicas: 1
  selector:
    matchLabels:
      app: cpu-stress-low
  template:
    metadata:
      labels:
        app: cpu-stress-low
    spec:
      containers:
      - name: stress
        image: polinux/stress
        command: ["stress"]
        args: ["-c", "1", "-t", "1800"]
        resources:
          requests:
            cpu: 100m
          limits:
            cpu: 500m
"@
    
    $testWorkloads | kubectl apply -f -
    
    Write-Info "Waiting for test pods to be ready..."
    kubectl wait --for=condition=Ready pods -l app=cpu-stress-high -n test-mbcas --timeout=120s
    kubectl wait --for=condition=Ready pods -l app=cpu-stress-low -n test-mbcas --timeout=120s
    
    Write-Info "[OK] Test workloads deployed"
}

# Step 5: Monitor allocations
function Monitor-Allocations {
    Write-Info "Monitoring PodAllocations (Ctrl+C to stop)..."
    Write-Host ""
    Write-Host "Initial state:"
    kubectl get podallocations -n test-mbcas -o wide 2>&1 | Out-String
    Write-Host ""
    Write-Host "Watching for changes (allocations appear after ~15-30 seconds):"
    kubectl get podallocations -n test-mbcas -w
}

# Step 6: Check results
function Check-Results {
    Write-Info "Checking MBCAS status..."
    
    Write-Host ""
    Write-Host "=== Controller Logs (last 20 lines) ==="
    kubectl logs -n mbcas-system -l app.kubernetes.io/component=controller --tail=20
    
    Write-Host ""
    Write-Host "=== Agent Logs (last 20 lines) ==="
    kubectl logs -n mbcas-system -l app.kubernetes.io/component=agent --tail=20
    
    Write-Host ""
    Write-Host "=== PodAllocations ==="
    kubectl get podallocations -n test-mbcas -o wide
    
    Write-Host ""
    Write-Host "=== Pod CPU Limits ==="
    kubectl get pods -n test-mbcas -o custom-columns=NAME:.metadata.name,CPU-LIMIT:.spec.containers[0].resources.limits.cpu --no-headers
}

# Cleanup
function Cleanup {
    Write-Info "Cleaning up..."
    kubectl delete namespace test-mbcas --ignore-not-found
    kubectl delete namespace mbcas-system --ignore-not-found
    kubectl delete crd podallocations.allocation.mbcas.io --ignore-not-found
    minikube delete -p $CLUSTER_NAME
    Write-Info "[OK] Cleanup complete"
}

# Main
Write-Host "=========================================="
Write-Host "MBCAS MVP Deploy and Test (PowerShell)"
Write-Host "=========================================="
Write-Host ""

$command = if ($args.Count -gt 0) { $args[0] } else { "all" }

switch ($command.ToLower()) {
    "cluster" {
        Create-Cluster
    }
    "build" {
        Build-Images
    }
    "deploy" {
        Deploy-MBCAS
    }
    "test" {
        Deploy-TestWorkloads
    }
    "monitor" {
        Monitor-Allocations
    }
    "check" {
        Check-Results
    }
    "cleanup" {
        Cleanup
    }
    "all" {
        Create-Cluster
        Build-Images
        Deploy-MBCAS
        Deploy-TestWorkloads
        Start-Sleep -Seconds 5
        Check-Results
        Write-Host ""
        Write-Info "MVP deployment complete!"
        Write-Info "Run .\scripts\mvp-test.ps1 monitor to watch allocations"
        Write-Info "Run .\scripts\mvp-test.ps1 check to see current status"
        Write-Info "Run .\scripts\mvp-test.ps1 cleanup when done"
    }
    default {
        Write-Host "Usage: .\scripts\mvp-test.ps1 {cluster|build|deploy|test|monitor|check|cleanup|all}"
        exit 1
    }
}

