#!/bin/bash
# File: test/benchmark/run_vpa_only.sh
# Run only VPA benchmark

set -e

# Get the directory where this script is located
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Get the project root (two levels up from test/benchmark)
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Change to project root for consistent paths
cd "$PROJECT_ROOT"

echo "========================================"
echo "VPA Benchmark Only"
echo "========================================"
echo ""
echo "Project root: $PROJECT_ROOT"
echo "Script directory: $SCRIPT_DIR"
echo ""

# Check prerequisites
echo "Checking prerequisites..."

if ! kubectl cluster-info &>/dev/null; then
    echo "ERROR: Cannot connect to Kubernetes cluster"
    exit 1
fi

if ! kubectl get crd verticalpodautoscalers.autoscaling.k8s.io &>/dev/null; then
    echo "ERROR: VPA CRD not found. Please install VPA first."
    exit 1
fi

if ! kubectl get pods -n kube-system -l app=vpa-updater --no-headers | grep -q Running 2>/dev/null; then
    echo "WARNING: VPA updater not found in kube-system"
fi

echo "Prerequisites check passed"
echo ""

# Build VPA benchmark binary (output to script directory)
echo "Building VPA benchmark binary..."
go build -tags vpabench -o "$SCRIPT_DIR/vpa-benchmark" ./test/benchmark

# Run VPA benchmark (from script directory)
echo ""
echo "========================================"
echo "Running VPA Benchmark"
echo "========================================"
cd "$SCRIPT_DIR"
./vpa-benchmark

# Cleanup binary
rm -f vpa-benchmark

echo ""
echo "========================================"
echo "VPA Benchmark Complete"
echo "========================================"
echo ""
echo "Output file: metrics-vpa.json"
echo ""
