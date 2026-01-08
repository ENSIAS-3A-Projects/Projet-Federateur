# build_eval_service.ps1
# Builds and loads the eval-service Docker image into Minikube
# Required before running e2e tests

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

# Get project root directory
$projectRoot = Split-Path -Parent $PSScriptRoot
Set-Location $projectRoot

Write-Color "Building Eval Service..." "Cyan"
Write-Color "-----------------------------------------" "Gray"

# Check if minikube is running
Write-Step 1 3 "Checking Minikube status..."
$minikubeStatus = minikube status --format='{{.Host}}' 2>$null
if ($minikubeStatus -ne "Running") {
    Write-Color "ERROR: Minikube is not running!" "Red"
    Write-Color "   Please run: .\scripts\start_minikube.ps1" "Gray"
    exit 1
}
Write-Color "OK: Minikube is running" "Green"

# Configure Docker for Minikube
Write-Step 2 3 "Configuring Docker for Minikube..."
$dockerEnv = minikube docker-env --shell powershell
if ($LASTEXITCODE -ne 0) {
    Write-Color "ERROR: Failed to get Minikube Docker environment" "Red"
    exit 1
}

# Join array elements with newlines if it's an array, otherwise use as-is
if ($dockerEnv -is [Array]) {
    $dockerEnv = $dockerEnv -join "`n"
}
Invoke-Expression $dockerEnv

# Verify we can connect to minikube docker
docker ps > $null 2>&1
if ($LASTEXITCODE -ne 0) {
    Write-Color "ERROR: Cannot connect to Minikube Docker daemon" "Red"
    exit 1
}
Write-Color "OK: Connected to Minikube Docker daemon" "Green"

# Build eval-service image
Write-Step 3 3 "Building eval-service image..."
$evalServicePath = Join-Path $projectRoot "apps\eval-service"
if (-not (Test-Path $evalServicePath)) {
    Write-Color "ERROR: Eval service directory not found: $evalServicePath" "Red"
    Write-Color "   Current directory: $(Get-Location)" "Gray"
    Write-Color "   Project root: $projectRoot" "Gray"
    exit 1
}

$dockerfilePath = Join-Path $evalServicePath "Dockerfile"
if (-not (Test-Path $dockerfilePath)) {
    Write-Color "ERROR: Dockerfile not found: $dockerfilePath" "Red"
    exit 1
}

Set-Location $evalServicePath
Write-Color "Building Docker image from: $evalServicePath" "Gray"
docker build -t eval-service:latest .
if ($LASTEXITCODE -ne 0) {
    Write-Color "ERROR: Failed to build eval-service image" "Red"
    Write-Color "   Check the Docker output above for details" "Gray"
    exit 1
}

Write-Color "OK: Eval service image built successfully" "Green"
Write-Color "`nEval service is ready for testing!" "Green"

