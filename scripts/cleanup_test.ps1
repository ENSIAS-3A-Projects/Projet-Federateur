# cleanup_test.ps1
# Quick cleanup script to remove test resources if the E2E test was interrupted

$ErrorActionPreference = "Stop"

function Write-Color($text, $color) {
    $validColors = @("Black", "DarkBlue", "DarkGreen", "DarkCyan", "DarkRed", "DarkMagenta", 
                     "DarkYellow", "Gray", "DarkGray", "Blue", "Green", "Cyan", 
                     "Red", "Magenta", "Yellow", "White")
    
    if ($color -and $validColors -contains $color) {
        Write-Host $text -ForegroundColor $color
    } else {
        Write-Host $text
    }
}

Write-Color "CLEANUP: Cleaning up test resources..." "Cyan"
Write-Color "-----------------------------------------" "Gray"

# 1. Stop any background PowerShell jobs
Write-Color "`n[1/4] Stopping background jobs..." "Yellow"
$jobs = Get-Job -ErrorAction SilentlyContinue
if ($jobs) {
    $jobs | Stop-Job -ErrorAction SilentlyContinue
    $jobs | Remove-Job -ErrorAction SilentlyContinue
    Write-Color "OK: Stopped $($jobs.Count) background job(s)" "Green"
} else {
    Write-Color "OK: No background jobs found" "Green"
}

# 2. Kill any kubectl port-forward processes (Windows)
Write-Color "`n[2/4] Stopping kubectl port-forward processes..." "Yellow"
$portForwards = Get-Process -Name kubectl -ErrorAction SilentlyContinue | Where-Object { $_.CommandLine -like "*port-forward*" }
if ($portForwards) {
    $portForwards | Stop-Process -Force -ErrorAction SilentlyContinue
    Write-Color "OK: Stopped port-forward processes" "Green"
} else {
    Write-Color "OK: No port-forward processes found" "Green"
}

# 3. Delete evaluation namespace (test workloads)
Write-Color "`n[3/4] Deleting evaluation namespace..." "Yellow"
kubectl delete namespace evaluation --ignore-not-found=true 2>&1 | Out-Null
if ($LASTEXITCODE -eq 0) {
    Write-Color "OK: Evaluation namespace deleted (or didn't exist)" "Green"
} else {
    Write-Color "WARNING: Could not delete evaluation namespace" "Yellow"
}

# 4. Check for any remaining pods in evaluation namespace
Write-Color "`n[4/4] Checking for remaining resources..." "Yellow"
$remainingPods = kubectl get pods -n evaluation --ignore-not-found=true 2>&1
if ($remainingPods -and $remainingPods -notmatch "No resources found") {
    Write-Color "WARNING: Some pods may still exist in evaluation namespace" "Yellow"
    Write-Color "   Run: kubectl delete namespace evaluation --force --grace-period=0" "Gray"
} else {
    Write-Color "OK: No remaining pods in evaluation namespace" "Green"
}

Write-Color "`nCleanup complete!" "Green"
Write-Color "`nNote: MBCAS components (mbcas-system namespace) are NOT deleted." "Gray"
Write-Color "   This allows you to restart tests without redeploying MBCAS." "Gray"
Write-Color "   To remove MBCAS: kubectl delete namespace mbcas-system" "Gray"

