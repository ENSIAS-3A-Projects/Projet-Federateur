#!/bin/bash
# MBCAS MVP Deploy and Test Script
# Prerequisites: minikube, kubectl, docker

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLUSTER_NAME="mbcas"

echo "=========================================="
echo "MBCAS MVP Deploy and Test"
echo "=========================================="

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

info() { echo -e "${GREEN}[INFO]${NC} $1"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

# Step 1: Create Minikube cluster
create_cluster() {
    info "Creating Minikube cluster with InPlacePodVerticalScaling feature gate..."
    
    # Check if minikube cluster exists
    if minikube status -p "$CLUSTER_NAME" &> /dev/null; then
        warn "Minikube cluster '$CLUSTER_NAME' already exists"
        read -p "Delete existing cluster? (y/N): " -n 1 -r
        echo
        if [[ $REPLY =~ ^[Yy]$ ]]; then
            info "Deleting existing cluster..."
            minikube delete -p "$CLUSTER_NAME"
        else
            info "Using existing cluster"
            # Set up Docker environment
            eval $(minikube -p "$CLUSTER_NAME" docker-env)
            return 0
        fi
    fi
    
    # Start Minikube with feature gate
    minikube start -p "$CLUSTER_NAME" \
        --feature-gates=InPlacePodVerticalScaling=true \
        --kubernetes-version=latest \
        --driver=docker \
        --memory=4096 \
        --cpus=2
    
    if [ $? -ne 0 ]; then
        error "Failed to start Minikube cluster"
    fi
    
    # Set up Docker environment for Minikube
    eval $(minikube -p "$CLUSTER_NAME" docker-env)
    
    info "Waiting for cluster to be ready..."
    kubectl wait --for=condition=Ready nodes --all --timeout=120s
    
    # Verify feature gate
    # Note: kubectl api-resources may not list subresources reliably
    # Try a more direct test: attempt to patch a test pod (if kubectl 1.32+)
    info "Verifying InPlacePodVerticalScaling feature gate..."
    
    # Method 1: Check if kubectl supports --subresource (kubectl 1.32+)
    if kubectl patch --help 2>&1 | grep -q "subresource"; then
        info "✓ kubectl supports --subresource flag (kubectl 1.32+)"
    else
        warn "kubectl may not support --subresource flag (requires kubectl 1.32+)"
        warn "Feature gate may still work, but manual testing recommended"
    fi
    
    # Method 2: Check API server version
    K8S_VERSION=$(kubectl version -o json 2>/dev/null | grep -oP '"serverVersion.*?"gitVersion":\s*"\K[^"]+' | head -1 || echo "unknown")
    if [ "$K8S_VERSION" != "unknown" ]; then
        info "✓ Kubernetes server version: $K8S_VERSION"
        # Extract major.minor version
        MAJOR_MINOR=$(echo "$K8S_VERSION" | grep -oP 'v?\K\d+\.\d+' | head -1)
        if [ -n "$MAJOR_MINOR" ]; then
            MAJOR=$(echo "$MAJOR_MINOR" | cut -d. -f1)
            MINOR=$(echo "$MAJOR_MINOR" | cut -d. -f2)
            if [ "$MAJOR" -lt 1 ] || ([ "$MAJOR" -eq 1 ] && [ "$MINOR" -lt 27 ]); then
                error "Kubernetes version $K8S_VERSION is too old. Requires 1.27+ for InPlacePodVerticalScaling"
            else
                info "✓ Kubernetes version supports InPlacePodVerticalScaling (1.27+)"
            fi
        fi
    fi
}

# Step 2: Build images
build_images() {
    info "Building Docker images..."
    
    # Check if we're in the right directory
    if [ ! -f "go.mod" ]; then
        error "Must run from MBCAS project root (where go.mod is)"
    fi
    
    # Ensure we're using Minikube's Docker daemon
    eval $(minikube -p "$CLUSTER_NAME" docker-env)
    
    docker build -f Dockerfile.controller -t mbcas-controller:latest .
    docker build -f Dockerfile.agent -t mbcas-agent:latest .
    
    info "Images built in Minikube's Docker daemon (no need to load)"
}

# Step 3: Deploy MBCAS
deploy_mbcas() {
    info "Deploying MBCAS components..."
    
    # Namespace
    kubectl apply -f config/namespace.yaml
    
    # CRD
    kubectl apply -f config/crd/bases/allocation.mbcas.io_podallocations.yaml
    
    # Wait for CRD to be established
    kubectl wait --for=condition=Established crd/podallocations.allocation.mbcas.io --timeout=30s
    
    # RBAC
    kubectl apply -f config/rbac/ || true
    kubectl apply -f config/agent/rbac.yaml || true
    
    # Controller
    kubectl apply -f config/controller/deployment.yaml
    
    # Agent (fixed version)
    kubectl apply -f config/agent/daemonset.yaml
    
    info "Waiting for controller to be ready..."
    kubectl wait --for=condition=Available deployment/mbcas-controller -n mbcas-system --timeout=60s
    
    info "Waiting for agent to be ready..."
    kubectl rollout status daemonset/mbcas-agent -n mbcas-system --timeout=60s
    
    info "✓ MBCAS deployed successfully"
}

# Step 4: Deploy test workloads
deploy_test_workloads() {
    info "Deploying test workloads..."
    
    kubectl create namespace test-mbcas --dry-run=client -o yaml | kubectl apply -f -
    
    cat <<EOF | kubectl apply -f -
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
EOF
    
    info "Waiting for test pods to be ready..."
    kubectl wait --for=condition=Ready pods -l app=cpu-stress-high -n test-mbcas --timeout=120s
    kubectl wait --for=condition=Ready pods -l app=cpu-stress-low -n test-mbcas --timeout=120s
    
    info "✓ Test workloads deployed"
}

# Step 5: Monitor allocations
monitor() {
    info "Monitoring PodAllocations (Ctrl+C to stop)..."
    echo ""
    echo "Initial state:"
    kubectl get podallocations -n test-mbcas -o wide 2>/dev/null || echo "No PodAllocations yet..."
    echo ""
    echo "Watching for changes (allocations appear after ~15-30 seconds):"
    kubectl get podallocations -n test-mbcas -w
}

# Step 6: Check results
check_results() {
    info "Checking MBCAS status..."
    
    echo ""
    echo "=== Controller Logs (last 20 lines) ==="
    kubectl logs -n mbcas-system -l app.kubernetes.io/component=controller --tail=20
    
    echo ""
    echo "=== Agent Logs (last 20 lines) ==="
    kubectl logs -n mbcas-system -l app.kubernetes.io/component=agent --tail=20
    
    echo ""
    echo "=== PodAllocations ==="
    kubectl get podallocations -n test-mbcas -o wide
    
    echo ""
    echo "=== Pod CPU Limits ==="
    kubectl get pods -n test-mbcas -o jsonpath='{range .items[*]}{.metadata.name}: limits.cpu={.spec.containers[0].resources.limits.cpu}{"\n"}{end}'
}

# Cleanup
cleanup() {
    info "Cleaning up..."
    kubectl delete namespace test-mbcas --ignore-not-found
    kubectl delete namespace mbcas-system --ignore-not-found
    kubectl delete crd podallocations.allocation.mbcas.io --ignore-not-found
    minikube delete -p "$CLUSTER_NAME"
    info "✓ Cleanup complete"
}

# Main
case "${1:-all}" in
    cluster)
        create_cluster
        ;;
    build)
        build_images
        ;;
    deploy)
        deploy_mbcas
        ;;
    test)
        deploy_test_workloads
        ;;
    monitor)
        monitor
        ;;
    check)
        check_results
        ;;
    cleanup)
        cleanup
        ;;
    all)
        create_cluster
        build_images
        deploy_mbcas
        deploy_test_workloads
        sleep 5
        check_results
        echo ""
        info "MVP deployment complete!"
        info "Run '$0 monitor' to watch allocations"
        info "Run '$0 check' to see current status"
        info "Run '$0 cleanup' when done"
        ;;
    *)
        echo "Usage: $0 {cluster|build|deploy|test|monitor|check|cleanup|all}"
        exit 1
        ;;
esac
