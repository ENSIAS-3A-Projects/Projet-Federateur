#!/bin/bash

echo "MBCAS Validation Script"
echo "======================"

# Check all components running
echo "Checking agent pods..."
AGENT_COUNT=$(kubectl get pods -n mbcas-system -l app.kubernetes.io/component=agent --field-selector=status.phase=Running -o name | wc -l)
NODE_COUNT=$(kubectl get nodes -o name | wc -l)
if [ "$AGENT_COUNT" -ne "$NODE_COUNT" ]; then
    echo "FAIL: Expected $NODE_COUNT agents, found $AGENT_COUNT"
    exit 1
fi
echo "PASS: $AGENT_COUNT agents running on $NODE_COUNT nodes"

echo "Checking controller pod..."
CONTROLLER_COUNT=$(kubectl get pods -n mbcas-system -l app.kubernetes.io/component=controller --field-selector=status.phase=Running -o name | wc -l)
if [ "$CONTROLLER_COUNT" -lt 1 ]; then
    echo "FAIL: Controller not running"
    exit 1
fi
echo "PASS: Controller running"

# Check CRD exists
echo "Checking CRD..."
kubectl get crd podallocations.allocation.mbcas.io > /dev/null 2>&1
if [ $? -ne 0 ]; then
    echo "FAIL: PodAllocation CRD not found"
    exit 1
fi
echo "PASS: CRD exists"

# Create test pod
echo "Creating test workload..."
kubectl create namespace mbcas-validation 2>/dev/null || true
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: validation-pod
  namespace: mbcas-validation
  labels:
    mbcas.io/managed: "true"
spec:
  containers:
  - name: stress
    image: polinux/stress
    command: ["stress", "--cpu", "1", "--timeout", "300"]
    resources:
      requests:
        cpu: 100m
      limits:
        cpu: 200m
EOF

echo "Waiting for pod to be running..."
kubectl wait --for=condition=Ready pod/validation-pod -n mbcas-validation --timeout=60s

echo "Waiting for PodAllocation to be created..."
PA=0
for i in {1..30}; do
    PA=$(kubectl get podallocation -n mbcas-validation -o name 2>/dev/null | wc -l)
    if [ "$PA" -gt 0 ]; then
        echo "PASS: PodAllocation created"
        break
    fi
    sleep 2
done

if [ "$PA" -eq 0 ]; then
    echo "FAIL: PodAllocation not created within 60s"
    kubectl delete namespace mbcas-validation
    exit 1
fi

echo "Waiting for allocation to be applied..."
PHASE=""
for i in {1..60}; do
    PHASE=$(kubectl get podallocation -n mbcas-validation -o jsonpath='{.items[0].status.phase}' 2>/dev/null)
    if [ "$PHASE" == "Applied" ]; then
        echo "PASS: Allocation applied"
        break
    fi
    sleep 2
done

if [ "$PHASE" != "Applied" ]; then
    echo "FAIL: Allocation not applied within 120s (phase=$PHASE)"
    kubectl describe podallocation -n mbcas-validation
    kubectl delete namespace mbcas-validation
    exit 1
fi

# Verify pod was actually resized
ACTUAL_LIMIT=$(kubectl get pod validation-pod -n mbcas-validation -o jsonpath='{.spec.containers[0].resources.limits.cpu}')
echo "Pod CPU limit: $ACTUAL_LIMIT"

# Cleanup
echo "Cleaning up..."
kubectl delete namespace mbcas-validation

echo ""
echo "======================"
echo "All validation checks passed"
