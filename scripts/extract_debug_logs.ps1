# extract_debug_logs.ps1
# Extracts debug logs from agent pods and formats them as NDJSON

$ErrorActionPreference = "Stop"

$logFile = ".cursor\debug.log"
$agentPod = kubectl get pods -n mbcas-system -l app.kubernetes.io/component=agent -o jsonpath='{.items[0].metadata.name}' 2>$null

if (-not $agentPod) {
    Write-Host "ERROR: Agent pod not found" -ForegroundColor Red
    exit 1
}

Write-Host "Extracting debug logs from agent pod: $agentPod" -ForegroundColor Cyan

# Get logs and filter for DEBUG_ entries
$logs = kubectl logs -n mbcas-system $agentPod 2>&1 | Select-String -Pattern "DEBUG_(H1|H2|H3|H4|H5)_" 

if (-not $logs) {
    Write-Host "WARNING: No debug logs found. Make sure the agent was rebuilt with instrumentation." -ForegroundColor Yellow
    exit 0
}

# Parse klog format and convert to NDJSON
$ndjsonLines = @()
foreach ($line in $logs) {
    # klog format: I0107 18:20:00.123456       1 agent.go:610] "DEBUG_H3_AllocationComputed" pod="worker-a-xxx" namespace="evaluation" demand=0.5 minMilli=500 ...
    if ($line -match 'DEBUG_(H\d)_(\w+)\s+(.+)') {
        $hypothesisId = $matches[1]
        $message = $matches[2]
        $rest = $matches[3]
        
        # Extract key-value pairs from the rest
        $data = @{}
        $data.hypothesisId = $hypothesisId
        $data.message = $message
        
        # Parse key="value" or key=value patterns
        $pattern = '(\w+)="([^"]+)"|(\w+)=([^\s]+)'
        $matches2 = [regex]::Matches($rest, $pattern)
        foreach ($match in $matches2) {
            if ($match.Groups[1].Success) {
                $key = $match.Groups[1].Value
                $value = $match.Groups[2].Value
            } else {
                $key = $match.Groups[3].Value
                $value = $match.Groups[4].Value
            }
            $data[$key] = $value
        }
        
        # Create NDJSON entry
        $ndjson = @{
            sessionId = if ($data.sessionId) { $data.sessionId } else { "debug-session" }
            runId = if ($data.runId) { $data.runId } else { "run1" }
            hypothesisId = $hypothesisId
            location = if ($data.location) { $data.location } else { "unknown" }
            message = $message
            data = $data
            timestamp = if ($data.timestamp) { [long]$data.timestamp } else { [DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds() }
        } | ConvertTo-Json -Compress -Depth 10
        
        $ndjsonLines += $ndjson
    }
}

# Write to log file
if ($ndjsonLines.Count -gt 0) {
    $ndjsonLines | Set-Content $logFile
    Write-Host "OK: Extracted $($ndjsonLines.Count) debug log entries to $logFile" -ForegroundColor Green
} else {
    Write-Host "WARNING: No valid debug log entries found" -ForegroundColor Yellow
}

