#!/usr/bin/env pwsh
# MBCAS Latency Impact Test
# Deploys a CPU-intensive Web Server and measures response time under node contention.

param(
    [string]$Namespace = "demo",
    [int]$AggressorCount = 7,    # Number of "noise" pods
    [int]$DurationSeconds = 60,  # Duration of the load test
    [switch]$Cleanup             # Delete resources after test
)

$ErrorActionPreference = "Stop"

# --- Helper Functions ---
function Write-Step { param($msg) Write-Host "`n==> $msg" -ForegroundColor Cyan }
function Write-Success { param($msg) Write-Host "[PASS] $msg" -ForegroundColor Green }
function Write-Info { param($msg) Write-Host "[INFO] $msg" -ForegroundColor Yellow }

$timestamp = Get-Date -Format "yyyy-MM-dd_HH-mm-ss"
$testId = "latency-test_$timestamp"
$logFile = ".\test-results\${testId}_log.txt"
$reportFile = ".\test-results\${testId}_report.md"

# Ensure output directory exists
New-Item -ItemType Directory -Force -Path ".\test-results" | Out-Null

Start-Transcript -Path $logFile -Append | Out-Null

Write-Host "================================================================================" -ForegroundColor Magenta
Write-Host "  MBCAS Latency Impact Test" -ForegroundColor Magenta
Write-Host "  Test ID: $testId" -ForegroundColor Magenta
Write-Host "================================================================================" -ForegroundColor Magenta

# 1. Setup Namespace
Write-Step "Setting up namespace '$Namespace'..."
kubectl create namespace $Namespace --dry-run=client -o yaml | kubectl apply -f -

# 2. Deploy The "Victim" (Latency-Sensitive App)
Write-Step "Deploying Victim App (CPU-bound HTTP Server)..."

# Define Python script
$pyScript = @"
import http.server, math, socketserver
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        x=0.0001
        for i in range(15000):
            x=x+math.sqrt(i)
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b'Done')

print('Starting server on port 8000...')
socketserver.TCPServer(('', 8000), H).serve_forever()
"@

# Indent for YAML
$pyScriptIndented = ($pyScript -split "`n" | ForEach-Object { "      " + $_ }) -join "`n"

$victimYaml = @"
apiVersion: v1
kind: Pod
metadata:
  name: victim-api
  namespace: $Namespace
  labels:
    app: victim
    mbcas.io/managed: "true"
spec:
  containers:
  - name: python
    image: python:3.9-slim
    command: ["python", "-c"]
    args: 
    - |
$pyScriptIndented
    ports:
    - containerPort: 8000
    resources:
      requests:
        cpu: "100m"
      limits:
        cpu: "2000m"
"@

$victimYaml | kubectl apply -f -
Write-Success "Victim API deployed"

# 3. Deploy "Aggressors" (Noise Makers) using YAML
Write-Step "Deploying $AggressorCount Aggressor Pods (Stress-ng)..."
for ($i=1; $i -le $AggressorCount; $i++) {
    $name = "aggressor-$i"
    Write-Host "  - Creating $name"

    $aggressorYaml = @"
apiVersion: v1
kind: Pod
metadata:
  name: $name
  namespace: $Namespace
  labels:
    app: aggressor
    mbcas.io/managed: "true"
spec:
  restartPolicy: Never
  containers:
  - name: stress
    image: polinux/stress-ng
    args: ["--cpu", "2", "--timeout", "300s"]
    resources:
      requests:
        cpu: "100m"
      limits:
        cpu: "2000m"
"@
    $aggressorYaml | kubectl apply -f - | Out-Null
}

# 4. Wait for Setup
Write-Step "Waiting for all pods to be READY..."
Write-Info "Waiting for victim-api..."
kubectl wait --for=condition=Ready pod/victim-api -n $Namespace --timeout=60s
Write-Success "Victim API is ready"

Write-Info "Waiting for aggressors to schedule..."
Start-Sleep -Seconds 10 

# 5. Get Victim IP
# Force string conversion and trim whitespace/newlines
$victimIP = (kubectl get pod victim-api -n $Namespace -o jsonpath='{.status.podIP}').ToString().Trim()

if ([string]::IsNullOrWhiteSpace($victimIP)) {
    Write-Error "Failed to retrieve Victim IP. Pod might not be ready."
}
Write-Info "Victim IP: $victimIP"

# 6. Run Load Test (Hey)
$targetUrl = "http://${victimIP}:8000"
Write-Step "Starting Load Test Generator..."
Write-Info "Running 'hey' for $DurationSeconds seconds against $targetUrl"

$loadPodYaml = @"
apiVersion: v1
kind: Pod
metadata:
  name: load-generator
  namespace: $Namespace
spec:
  restartPolicy: Never
  containers:
  - name: hey
    image: williamyeh/hey
    args: ["-z", "${DurationSeconds}s", "-c", "10", "$targetUrl"]
"@

# Clean up previous load generator if it exists
kubectl delete pod load-generator -n $Namespace --ignore-not-found | Out-Null
$loadPodYaml | kubectl apply -f -

# Wait for load test to finish
Write-Host "  - Waiting for load test to complete ($DurationSeconds s)..."
# Wait for pod to be running first
kubectl wait --for=condition=Ready pod/load-generator -n $Namespace --timeout=20s 2>$null 
# Then wait for completion
kubectl wait --for=condition=Completed pod/load-generator -n $Namespace --timeout="$($DurationSeconds + 30)s"

# 7. Collect Results
Write-Step "Collecting Latency Metrics..."
$results = kubectl logs load-generator -n $Namespace

Write-Host "`n----- LOAD TEST RESULTS -----" -ForegroundColor Yellow
$results | Write-Host
Write-Host "-----------------------------" -ForegroundColor Yellow

$results | Out-File -FilePath $reportFile -Encoding UTF8

if ($Cleanup) {
    Write-Step "Cleaning up..."
    kubectl delete namespace $Namespace --ignore-not-found
}

Stop-Transcript