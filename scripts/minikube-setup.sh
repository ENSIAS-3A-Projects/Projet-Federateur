#!/bin/bash
# Minikube setup script for MBCAS
# Creates Minikube cluster with InPlacePodVerticalScaling feature gate enabled

set -e

CLUSTER_NAME="mbcas"

echo "=========================================="
echo "MBCAS Minikube Setup"
echo "=========================================="

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info() { echo -e "${GREEN}[INFO]${NC} $1"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

# Check if minikube is installed
if ! command -v minikube &> /dev/null; then
    error "minikube is not installed. Please install it first: https://minikube.sigs.k8s.io/docs/start/"
fi

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
        exit 0
    fi
fi

# Start Minikube with feature gate
info "Starting Minikube cluster with InPlacePodVerticalScaling feature gate..."

minikube start -p "$CLUSTER_NAME" \
    --feature-gates=InPlacePodVerticalScaling=true \
    --kubernetes-version=latest \
    --driver=docker \
    --memory=4096 \
    --cpus=2

if [ $? -ne 0 ]; then
    error "Failed to start Minikube cluster"
fi

# Wait for cluster to be ready
info "Waiting for cluster to be ready..."
kubectl wait --for=condition=Ready nodes --all --timeout=120s

# Verify feature gate
info "Verifying InPlacePodVerticalScaling feature gate..."

# Check Kubernetes version
K8S_VERSION=$(kubectl version -o json 2>&1 | grep -oP '"serverVersion.*?"gitVersion":\s*"\K[^"]+' | head -1 || echo "unknown")
if [ "$K8S_VERSION" != "unknown" ]; then
    info "✓ Kubernetes server version: $K8S_VERSION"
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

# Set up Docker environment for Minikube
info "Setting up Docker environment for Minikube..."
eval $(minikube -p "$CLUSTER_NAME" docker-env)

info "✓ Minikube cluster is ready!"
info ""
info "To use Minikube's Docker daemon, run:"
info "  eval \$(minikube -p $CLUSTER_NAME docker-env)"
info ""
info "To stop the cluster:"
info "  minikube stop -p $CLUSTER_NAME"
info ""
info "To delete the cluster:"
info "  minikube delete -p $CLUSTER_NAME"

