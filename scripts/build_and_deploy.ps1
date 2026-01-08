# build_and_deploy.ps1
# Builds and deploys the entire MBCAS (CoAllocator) infrastructure to Minikube
# This script handles: building images, loading into Minikube, and deploying all components

$ErrorActionPreference = "Stop"

function Write-Color($text, $color) {
    # Validate color and provide fallback
    $validColors = @("Black", "DarkBlue", "DarkGreen", "DarkCyan", "DarkRed", "DarkMagenta", 
                     "DarkYellow", "Gray", "DarkGray", "Blue", "Green", "Cyan", 
                     "Red", "Magenta", "Yellow", "White")
    
    if ($color -and $validColors -contains $color) {
        Write-Host $text -ForegroundColor $color
    } else {
        # Fallback to default color if invalid
        Write-Host $text
    }
}

function Write-Step($step, $total, $message) {
    Write-Color "`n[${step}/${total}] $message" "Yellow"
}

function Test-Command($cmd) {
    $null = Get-Command $cmd -ErrorAction SilentlyContinue
    return $?
}

# Pre-flight checks
Write-Color "Pre-flight Checks..." "Cyan"
Write-Color "-----------------------------------------" "Gray"

$requiredCommands = @("minikube", "kubectl", "docker", "go")
$missingCommands = @()

foreach ($cmd in $requiredCommands) {
    if (Test-Command $cmd) {
        Write-Color "OK: $cmd is installed" "Green"
    }
    else {
        Write-Color "ERROR: $cmd is NOT installed" "Red"
        $missingCommands += $cmd
    }
}

if ($missingCommands.Count -gt 0) {
    Write-Color "`nERROR: Missing required commands: $($missingCommands -join ', ')" "Red"
    Write-Color "   Please install the missing tools and try again." "Gray"
    exit 1
}

# Check if minikube is running
Write-Color "`nChecking Minikube status..." "Gray"
$minikubeStatus = minikube status --format='{{.Host}}' 2>$null
if ($minikubeStatus -ne "Running") {
    Write-Color "ERROR: Minikube is not running!" "Red"
    Write-Color "   Please run: .\scripts\start_minikube.ps1" "Gray"
    exit 1
}
Write-Color "OK: Minikube is running" "Green"

# Verify pod/resize subresource (required for MBCAS)
Write-Color "Verifying pod/resize subresource..." "Gray"
$podResize = kubectl api-resources --api-group="" 2>$null | Select-String "pods/resize"
if (-not $podResize) {
    Write-Color "WARNING: pod/resize subresource not found." "Magenta"
    Write-Color "   MBCAS requires InPlacePodVerticalScaling feature gate." "Gray"
    Write-Color "   Make sure you started Minikube with: .\scripts\start_minikube.ps1" "Gray"
    $continue = Read-Host "Continue anyway? (y/N)"
    if ($continue -ne "y" -and $continue -ne "Y") {
        exit 1
    }
}
else {
    Write-Color "OK: pod/resize subresource is available" "Green"
}

# Configure Docker for Minikube
Write-Color "`nConfiguring Docker for Minikube..." "Gray"
$dockerEnv = minikube docker-env --shell powershell
if ($LASTEXITCODE -ne 0) {
    Write-Color "ERROR: Failed to get Minikube Docker environment" "Red"
    exit 1
}

# Join array elements with newlines if it's an array, otherwise use as-is
if ($dockerEnv -is [Array]) {
    $dockerEnv = $dockerEnv -join "`n"
}
# Execute the docker-env output to set environment variables
Invoke-Expression $dockerEnv

# Verify we can connect to minikube docker
docker ps > $null 2>&1
if ($LASTEXITCODE -ne 0) {
    Write-Color "ERROR: Cannot connect to Minikube Docker daemon" "Red"
    Write-Color "   Make sure Minikube is running: minikube status" "Gray"
    exit 1
}
Write-Color "OK: Connected to Minikube Docker daemon" "Green"

# Get project root directory
$projectRoot = Split-Path -Parent $PSScriptRoot
Set-Location $projectRoot

Write-Color "`nBuilding MBCAS Infrastructure..." "Cyan"
Write-Color "-----------------------------------------" "Gray"

# Step 1: Build Go binaries
Write-Step 1 7 "Building Go binaries..."
Write-Color "Building agent..." "Gray"
go build -o agent.exe ./cmd/agent
if ($LASTEXITCODE -ne 0) {
    Write-Color "ERROR: Failed to build agent" "Red"
    exit 1
}

Write-Color "Building controller..." "Gray"
go build -o controller.exe ./cmd/controller
if ($LASTEXITCODE -ne 0) {
    Write-Color "ERROR: Failed to build controller" "Red"
    exit 1
}
Write-Color "OK: Binaries built successfully" "Green"

# Step 2: Build Docker images
Write-Step 2 7 "Building Docker images..."

Write-Color "Building agent image..." "Gray"
docker build -f Dockerfile.agent -t mbcas-agent:latest .
if ($LASTEXITCODE -ne 0) {
    Write-Color "ERROR: Failed to build agent image" "Red"
    exit 1
}

Write-Color "Building controller image..." "Gray"
docker build -f Dockerfile.controller -t mbcas-controller:latest .
if ($LASTEXITCODE -ne 0) {
    Write-Color "ERROR: Failed to build controller image" "Red"
    exit 1
}
Write-Color "OK: Docker images built successfully" "Green"

# Step 3: Images are already in Minikube (built directly)
Write-Step 3 7 "Verifying images in Minikube..."
$agentImage = docker images mbcas-agent:latest --format '{{.Repository}}:{{.Tag}}' 2>$null
$controllerImage = docker images mbcas-controller:latest --format '{{.Repository}}:{{.Tag}}' 2>$null

if ($agentImage -and $controllerImage) {
    Write-Color "OK: Images are available in Minikube" "Green"
}
else {
    Write-Color "WARNING: Images may not be visible. They should be available since we built in Minikube's Docker." "Magenta"
}

# Step 4: Deploy CRD
Write-Step 4 7 "Deploying CustomResourceDefinition..."
kubectl apply -f k8s/mbcas/allocation.mbcas.io_podallocations.yaml
if ($LASTEXITCODE -ne 0) {
    Write-Color "ERROR: Failed to deploy CRD" "Red"
    exit 1
}

# Wait for CRD to be ready
Write-Color "Waiting for CRD to be established..." "Gray"
$maxWait = 30
$waited = 0
while ($waited -lt $maxWait) {
    # Use a simpler check - just verify the CRD exists and can be queried
    $crdExists = kubectl get crd podallocations.allocation.mbcas.io 2>$null
    if ($LASTEXITCODE -eq 0 -and $crdExists) {
        # Try to get the CRD as JSON to check if it's fully established
        $crdJson = kubectl get crd podallocations.allocation.mbcas.io -o json 2>$null | ConvertFrom-Json
        if ($crdJson -and $crdJson.status -and $crdJson.status.conditions) {
            $established = $crdJson.status.conditions | Where-Object { $_.type -eq "Established" -and $_.status -eq "True" }
            if ($established) {
                break
            }
        } else {
            # If no conditions yet, just check if CRD exists (may be enough for some versions)
            break
        }
    }
    Start-Sleep -Seconds 1
    $waited++
    Write-Host "." -NoNewline
}
Write-Host ""
if ($waited -ge $maxWait) {
    Write-Color "WARNING: CRD may not be fully established yet" "Magenta"
    Write-Color "   Continuing anyway - CRD should work shortly" "Gray"
}
else {
    Write-Color "OK: CRD is established" "Green"
}

# Step 5: Deploy namespace and RBAC
Write-Step 5 7 "Deploying namespace and RBAC..."
kubectl apply -f k8s/mbcas/namespace.yaml
kubectl apply -f k8s/mbcas/service_account.yaml
kubectl apply -f k8s/mbcas/role.yaml
kubectl apply -f k8s/mbcas/role_binding.yaml
kubectl apply -f k8s/mbcas/agent-rbac.yaml
if ($LASTEXITCODE -ne 0) {
    Write-Color "ERROR: Failed to deploy RBAC resources" "Red"
    exit 1
}
Write-Color "OK: RBAC resources deployed" "Green"

# Step 6: Deploy controller
Write-Step 6 7 "Deploying controller..."
kubectl apply -f k8s/mbcas/controller-deployment.yaml
if ($LASTEXITCODE -ne 0) {
    Write-Color "ERROR: Failed to deploy controller" "Red"
    exit 1
}
Write-Color "OK: Controller deployed" "Green"

# Step 7: Deploy agent DaemonSet
Write-Step 7 7 "Deploying agent DaemonSet..."
kubectl apply -f k8s/mbcas/agent-daemonset.yaml
if ($LASTEXITCODE -ne 0) {
    Write-Color "ERROR: Failed to deploy agent DaemonSet" "Red"
    exit 1
}
Write-Color "OK: Agent DaemonSet deployed" "Green"

# Wait for pods to be ready
Write-Color "`nWaiting for pods to be ready..." "Cyan"
Write-Color "-----------------------------------------" "Gray"

$maxWait = 120
$waited = 0
while ($waited -lt $maxWait) {
    # Use JSON parsing instead of complex jsonpath
    $controllerPods = kubectl get pod -n mbcas-system -l app.kubernetes.io/component=controller -o json 2>$null | ConvertFrom-Json
    $agentPods = kubectl get pod -n mbcas-system -l app.kubernetes.io/component=agent -o json 2>$null | ConvertFrom-Json
    
    $controllerReady = $false
    $agentReady = $false
    
    if ($controllerPods -and $controllerPods.items -and $controllerPods.items.Count -gt 0) {
        $controllerPod = $controllerPods.items[0]
        if ($controllerPod.status -and $controllerPod.status.conditions) {
            $readyCondition = $controllerPod.status.conditions | Where-Object { $_.type -eq "Ready" -and $_.status -eq "True" }
            $controllerReady = ($readyCondition -ne $null)
        }
    }
    
    if ($agentPods -and $agentPods.items -and $agentPods.items.Count -gt 0) {
        $agentPod = $agentPods.items[0]
        if ($agentPod.status -and $agentPod.status.conditions) {
            $readyCondition = $agentPod.status.conditions | Where-Object { $_.type -eq "Ready" -and $_.status -eq "True" }
            $agentReady = ($readyCondition -ne $null)
        }
    }
    
    if ($controllerReady -and $agentReady) {
        break
    }
    
    Start-Sleep -Seconds 2
    $waited += 2
    Write-Host "." -NoNewline
}
Write-Host ""

# Display deployment status
Write-Color "`nDeployment Status:" "Cyan"
Write-Color "-----------------------------------------" "Gray"
kubectl get pods -n mbcas-system
Write-Color ""

# Check pod status
$controllerPods = kubectl get pod -n mbcas-system -l app.kubernetes.io/component=controller -o json 2>$null | ConvertFrom-Json
$agentPods = kubectl get pod -n mbcas-system -l app.kubernetes.io/component=agent -o json 2>$null | ConvertFrom-Json

$controllerPhase = ""
$agentPhase = ""

if ($controllerPods -and $controllerPods.items -and $controllerPods.items.Count -gt 0) {
    $controllerPhase = $controllerPods.items[0].status.phase
}

if ($agentPods -and $agentPods.items -and $agentPods.items.Count -gt 0) {
    $agentPhase = $agentPods.items[0].status.phase
}

if ($controllerPhase -eq "Running" -and $agentPhase -eq "Running") {
    Write-Color "OK: MBCAS is deployed and running!" "Green"
    Write-Color "`nNext Steps:" "Cyan"
    Write-Color "-----------------------------------------" "Gray"
    Write-Color "1. Check agent logs: kubectl logs -n mbcas-system -l app.kubernetes.io/component=agent" "Gray"
    Write-Color "2. Check controller logs: kubectl logs -n mbcas-system -l app.kubernetes.io/component=controller" "Gray"
    Write-Color "3. Deploy test workloads to see MBCAS in action" "Gray"
    Write-Color "4. View PodAllocations: kubectl get podallocations -A" "Gray"
    Write-Color ""
    
    # Show service URLs if available
    Write-Color "Service Endpoints:" "Cyan"
    Write-Color "-----------------------------------------" "Gray"
    Write-Color "Agent Dashboard: kubectl port-forward -n mbcas-system -l app.kubernetes.io/component=agent 8082:8082" "Gray"
    Write-Color "Controller Metrics: kubectl port-forward -n mbcas-system -l app.kubernetes.io/component=controller 8080:8080" "Gray"
    Write-Color ""
}
else {
    Write-Color "WARNING: Some pods may not be ready yet. Check status with:" "Magenta"
    Write-Color "   kubectl get pods -n mbcas-system" "Gray"
    Write-Color "   kubectl describe pod -n mbcas-system <pod-name>" "Gray"
    Write-Color ""
}

# Verify CRD is working
Write-Color "Verifying CRD..." "Cyan"
$crdExists = kubectl get crd podallocations.allocation.mbcas.io 2>$null
if ($crdExists) {
    Write-Color "OK: PodAllocation CRD is available" "Green"
    Write-Color "   Try: kubectl get podallocations -A" "Gray"
}
else {
    Write-Color "ERROR: PodAllocation CRD not found" "Red"
}

Write-Color "`nDeployment complete!" "Green"

