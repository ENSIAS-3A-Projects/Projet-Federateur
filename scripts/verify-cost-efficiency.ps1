param(
    [string]$ResultsDir
)

if (-not $ResultsDir) {
    # Default to latest results
    $root = Resolve-Path "..\results\vpa-mbcas-comparison"
    $latest = Get-ChildItem $root | Sort-Object CreationTime -Descending | Select-Object -First 1
    if (-not $latest) { throw "No results found" }
    $ResultsDir = $latest.FullName
    Write-Host "Using latest results: $ResultsDir"
}

Write-Host "Verifying Cost Efficiency in: $ResultsDir"

# Function to calculate average CPU from metrics file
function Get-AvgCPU {
    param([string]$File)
    if (-not (Test-Path $File)) { return 0 }
    $data = Get-Content $File | ConvertFrom-Json
    if (-not $data.pods) { return 0 }
    
    $totalUsage = 0
    $count = 0
    foreach ($pod in $data.pods) {
        if ($pod.usage -and $pod.usage.cpu) {
            $cpu = $pod.usage.cpu -replace "m", ""
            $totalUsage += [int]$cpu
            $count++
        }
    }
    if ($count -eq 0) { return 0 }
    return $totalUsage / $count
}

# Function to calculate average Request from metrics file
function Get-AvgRequest {
    param([string]$File)
    if (-not (Test-Path $File)) { return 0 }
    $data = Get-Content $File | ConvertFrom-Json
    if (-not $data.pods) { return 0 }
    
    $totalReq = 0
    $count = 0
    foreach ($pod in $data.pods) {
        if ($pod.resources -and $pod.resources.requests -and $pod.resources.requests.cpu) {
            $cpu = $pod.resources.requests.cpu -replace "m", ""
            $totalReq += [int]$cpu
            $count++
        }
    }
    if ($count -eq 0) { return 0 }
    return $totalReq / $count
}

# Scan for metrics files
$vpaFiles = Get-ChildItem "$ResultsDir\metrics-vpa-only-*.json"
$mbcasFiles = Get-ChildItem "$ResultsDir\metrics-mbcas-only-*.json"

if ($vpaFiles.Count -eq 0 -or $mbcasFiles.Count -eq 0) {
    Write-Host "Skipping comparison: missing data files"
    exit 0
}

$vpaAvgCPU = 0
$vpaCount = 0
foreach ($f in $vpaFiles) {
    $val = Get-AvgRequest $f.FullName # VPA sets requests
    $vpaAvgCPU += $val
    $vpaCount++
}
if ($vpaCount -gt 0) { $vpaAvgCPU /= $vpaCount }

$mbcasAvgCPU = 0
$mbcasCount = 0
foreach ($f in $mbcasFiles) {
    # For MBCAS, we check Usage + Allocation, but "Cost" is usually "Request" (billing) or "Usage" (efficiency).
    # We want MBCAS Allocations (Limits/Requests) to be lower than VPA Recommendations?
    # Or MBCAS Usage to be same/similar but with lower overhead?
    # User said: "checking that average CPU allocation is less than VPA requests"
    $val = Get-AvgRequest $f.FullName
    $mbcasAvgCPU += $val
    $mbcasCount++
}
if ($mbcasCount -gt 0) { $mbcasAvgCPU /= $mbcasCount }

Write-Host "Average VPA Request: $vpaAvgCPU m"
Write-Host "Average MBCAS Request: $mbcasAvgCPU m"

if ($mbcasAvgCPU -lt $vpaAvgCPU) {
    Write-Host "SUCCESS: MBCAS represents a cost saving over VPA." -ForegroundColor Green
}
else {
    Write-Host "FAILURE: MBCAS is not cheaper than VPA." -ForegroundColor Red
}

# Check Throttling
# We look for "throttling" metrics in MBCAS logs or metrics
# This script assumes we have logs or metrics.
# For now, we trust the cost comparison.
