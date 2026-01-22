#!/bin/bash
# File: test/benchmark/run_benchmarks.sh

set -e

# Get the directory where this script is located
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Get the project root (two levels up from test/benchmark)
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Change to project root for consistent paths
cd "$PROJECT_ROOT"

echo "========================================"
echo "MBCAS vs VPA Benchmark Suite"
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

if ! kubectl get crd podallocations.allocation.mbcas.io &>/dev/null; then
    echo "ERROR: MBCAS CRD not found. Please deploy MBCAS first."
    exit 1
fi

if ! kubectl get pods -n mbcas-system -l app.kubernetes.io/component=agent --no-headers | grep -q Running; then
    echo "ERROR: MBCAS agent not running"
    exit 1
fi

if ! kubectl get pods -n kube-system -l app=vpa-recommender --no-headers | grep -q Running 2>/dev/null; then
    echo "WARNING: VPA recommender not found in kube-system, checking other namespaces..."
fi

echo "Prerequisites check passed"
echo ""

# Build benchmark binaries (output to script directory)
echo "Building benchmark binaries..."
go build -tags mbcasbench -o "$SCRIPT_DIR/mbcas-benchmark" ./test/benchmark
go build -tags vpabench -o "$SCRIPT_DIR/vpa-benchmark" ./test/benchmark
go build -o "$SCRIPT_DIR/compare-results" ./cmd/compare-results

# Run MBCAS benchmark (from script directory)
echo ""
echo "========================================"
echo "Running MBCAS Benchmark"
echo "========================================"
cd "$SCRIPT_DIR"
./mbcas-benchmark

# Wait between tests
echo ""
echo "Waiting 60 seconds before VPA benchmark..."
sleep 60

# Run VPA benchmark
echo ""
echo "========================================"
echo "Running VPA Benchmark"
echo "========================================"
./vpa-benchmark

# Generate comparison
echo ""
echo "========================================"
echo "Generating Comparison Report"
echo "========================================"
./compare-results

# Cleanup binaries
rm -f mbcas-benchmark vpa-benchmark compare-results

echo ""
echo "========================================"
echo "Benchmark Complete"
echo "========================================"
echo ""
echo "Output files:"
echo "  - metrics-mbcas.json"
echo "  - metrics-vpa.json"
echo "  - metrics-comparison.json"
echo ""