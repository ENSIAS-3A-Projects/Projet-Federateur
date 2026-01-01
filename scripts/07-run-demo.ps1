# MBCAS Demo Script 07 - Full Demo Run
# Deploys demo workloads and walks through the demonstration
#
# Usage: .\scripts\07-run-demo.ps1

$ErrorActionPreference = "Stop"

function Write-Info { Write-Host "[INFO] $args" -ForegroundColor Green }
function Write-Step { Write-Host "`n>> $args" -ForegroundColor Cyan }
function Pause-Demo { 
    Write-Host "`nPress Enter to continue..." -ForegroundColor Yellow
    Read-Host 
}

Write-Host @"

================================================================================
                         MBCAS DEMONSTRATION
================================================================================

This demo shows how MBCAS dynamically adjusts CPU limits based on actual demand,
compared to static Kubernetes resource limits.

Scenario:
  - Service A: Over-provisioned (wastes resources)
  - Service B: Under-provisioned (throttled)
  - Service C: Bursty workload (variable demand)
  - Service D: Well-configured (control group)

================================================================================

"@ -ForegroundColor White

Pause-Demo

# Step 1: Deploy workloads
Write-Step "STEP 1: Deploying demo workloads"
kubectl apply -k k8s/demo/
Write-Info "Waiting for pods to start (30s)..."
Start-Sleep -Seconds 30

kubectl get pods -n demo-workloads -o wide
Pause-Demo

# Step 2: Show initial state
Write-Step "STEP 2: Observing initial state (static limits)"
Write-Host "Notice: Service B is being throttled (high CPU usage vs limit)"
Write-Host ""

kubectl top pods -n demo-workloads 2>$null
Write-Host ""
kubectl get pods -n demo-workloads -o custom-columns="NAME:.metadata.name,REQUEST:.spec.containers[0].resources.requests.cpu,LIMIT:.spec.containers[0].resources.limits.cpu"
Pause-Demo

# Step 3: Enable MBCAS
Write-Step "STEP 3: Enabling MBCAS"
Write-Info "MBCAS agent is already running and observing pods..."
Write-Info "Checking PodAllocations..."

kubectl get podallocations -n demo-workloads -o wide 2>$null
if ($LASTEXITCODE -ne 0) {
    Write-Info "No allocations yet - waiting for agent to discover pods..."
    Start-Sleep -Seconds 20
    kubectl get podallocations -n demo-workloads -o wide
}
Pause-Demo

# Step 4: Wait for convergence
Write-Step "STEP 4: Waiting for MBCAS to converge (60s)"
Write-Info "Watch the allocations adjust based on demand signals..."
Write-Host ""

for ($i = 1; $i -le 6; $i++) {
    Write-Host "--- Iteration $i/6 ---" -ForegroundColor Gray
    kubectl get podallocations -n demo-workloads -o custom-columns="POD:.spec.podName,DESIRED:.spec.desiredCPULimit,APPLIED:.status.appliedCPULimit,PHASE:.status.phase" 2>$null
    Write-Host ""
    Start-Sleep -Seconds 10
}
Pause-Demo

# Step 5: Compare
Write-Step "STEP 5: Comparing results"
Write-Host "Current CPU usage:" -ForegroundColor White
kubectl top pods -n demo-workloads 2>$null

Write-Host "`nCurrent limits (adjusted by MBCAS):" -ForegroundColor White
kubectl get pods -n demo-workloads -o custom-columns="NAME:.metadata.name,LIMIT:.spec.containers[0].resources.limits.cpu"

Write-Host "`nMBCAS allocations:" -ForegroundColor White
kubectl get podallocations -n demo-workloads -o wide
Pause-Demo

# Step 6: Inject burst
Write-Step "STEP 6: Injecting burst load on Service C"
Write-Info "Triggering sustained CPU burst..."

$pod = kubectl get pods -n demo-workloads -l app=service-c -o jsonpath='{.items[0].metadata.name}'
kubectl exec -n demo-workloads $pod -- sh -c "timeout 30 sh -c 'while true; do :; done'"

Write-Info "Burst running for 30 seconds. Watching MBCAS respond..."
for ($i = 1; $i -le 6; $i++) {
    Start-Sleep -Seconds 5
    Write-Host "--- Second $($i*5) ---" -ForegroundColor Gray
    kubectl get podallocations -n demo-workloads -l app=service-c -o custom-columns="POD:.spec.podName,DESIRED:.spec.desiredCPULimit,PHASE:.status.phase" 2>$null
}
Pause-Demo

# Step 7: Summary
Write-Step "STEP 7: Demo Summary"
Write-Host @"

================================================================================
                         DEMONSTRATION COMPLETE
================================================================================

Key observations (fill from what you just saw above):

1. Service A (over-provisioned): Did MBCAS pull back the limit? How much headroom freed?
2. Service B (under-provisioned): Did throttling clear after allocations applied? What limit did it settle on?
3. Service C (bursty): How quickly did allocations rise during burst and decay after?
4. Service D (control): Did it stay near its configured limit (minimal change)?

Game Theory in Action:
- Each pod's allocation is based on measured demand (truthful signal)
- Total allocation respects node capacity (constraint)
- Under contention, surplus is reduced proportionally (Nash bargaining)
- Idle pods release resources passively (collaboration without negotiation)

================================================================================

"@ -ForegroundColor White

Write-Info "Cleanup: kubectl delete -k k8s/demo/"
