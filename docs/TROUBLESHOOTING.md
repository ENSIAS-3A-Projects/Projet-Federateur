# MBCAS Troubleshooting Guide

## Common Issues

### Agent Not Starting

**Symptoms:**
- Agent pod in `CrashLoopBackOff`
- Logs show "cgroup validation failed"

**Solutions:**
1. Verify cgroup v2 is available: `ls /sys/fs/cgroup`
2. Check agent has permissions to read cgroups (runs as root by default)
3. Verify kubepods hierarchy exists: `ls /sys/fs/cgroup/kubepods*`

### Pods Not Being Managed

**Symptoms:**
- Pods exist but no PodAllocation CRDs created
- Agent logs show no pods discovered

**Solutions:**
1. Verify pod has `mbcas.io/managed: "true"` label
2. Check agent can list pods: `kubectl auth can-i list pods --as=system:serviceaccount:mbcas-system:mbcas-agent`
3. Verify pod is on the same node as agent (agent is DaemonSet)

### Allocations Not Applied

**Symptoms:**
- PodAllocation CRDs exist but pod resources unchanged
- Controller logs show reconciliation errors

**Solutions:**
1. Check controller can patch pods: `kubectl auth can-i patch pods --as=system:serviceaccount:mbcas-system:mbcas-controller`
2. Verify pod is not Guaranteed QoS (controller skips these)
3. Check PodAllocation status.phase - should be "Applied" or "Pending"
4. Review controller logs for errors

### High Latency Not Triggering Fast Guardrail

**Symptoms:**
- SLO violations occur but allocations don't increase quickly

**Solutions:**
1. Verify Prometheus URL is configured: `MBCAS_PROMETHEUS_URL`
2. Check latency queries work: `curl http://<agent-pod>:8080/api/status`
3. Verify SLO target is set: `MBCAS_SLO_TARGET_LATENCY_MS` or pod annotation
4. Check fast guardrail logs for trigger conditions

### Market Not Clearing

**Symptoms:**
- Total allocations exceed node capacity
- Shadow prices remain zero

**Solutions:**
1. Verify allocation mechanism: `MBCAS_ALLOCATION_MECHANISM` (should be `nash` or `primal-dual`)
2. Check system reserve: `MBCAS_SYSTEM_RESERVE_PERCENT` (default 10%)
3. Review allocation logs for mode (uncongested/congested/overloaded)
4. Verify capacity calculation: check node CPU capacity

### Memory Leaks

**Symptoms:**
- Agent memory usage grows over time
- OOMKilled errors

**Solutions:**
1. Verify cleanup is called: check agent logs for cleanup messages
2. Check QValues map size: `CleanupOldQValues` should be called periodically
3. Verify cgroup reader cleanup: `Cleanup` should remove old pod samples
4. Review agent state map sizes

## Debugging Commands

### Check Agent Status

```bash
# Get agent pod
kubectl get pods -n mbcas-system -l component=agent

# Check agent health
kubectl port-forward -n mbcas-system <agent-pod> 8080:8080
curl http://localhost:8080/api/status

# View agent logs
kubectl logs -n mbcas-system -l component=agent --tail=100
```

### Check Controller Status

```bash
# Get controller pod
kubectl get pods -n mbcas-system -l component=controller

# Check controller health
kubectl port-forward -n mbcas-system <controller-pod> 8081:8081
curl http://localhost:8081/healthz

# View controller logs
kubectl logs -n mbcas-system -l component=controller --tail=100
```

### Check PodAllocations

```bash
# List all PodAllocations
kubectl get podallocations -A

# Get details
kubectl get podallocation <pod-uid> -n <namespace> -o yaml

# Check status
kubectl get podallocation <pod-uid> -n <namespace> -o jsonpath='{.status}'
```

### Check Pod Resources

```bash
# Get current pod resources
kubectl get pod <pod-name> -n <namespace> -o jsonpath='{.spec.containers[0].resources}'

# Watch for changes
kubectl get pod <pod-name> -n <namespace> -w -o jsonpath='{.spec.containers[0].resources}'
```

## Log Levels

Set log verbosity via `-v` flag:

- `-v=0`: Errors only
- `-v=2`: Info level (default)
- `-v=4`: Debug level (detailed sampling)
- `-v=5`: Trace level (very verbose)

Example:
```yaml
# In DaemonSet spec
args:
  - "-v=4"
```

## Metrics

Agent exposes Prometheus metrics at `/metrics`:

- `mbcas_samples_total` - Total cgroup samples
- `mbcas_writes_total` - Total PodAllocation writes
- `mbcas_pods_tracked` - Number of pods being tracked
- `mbcas_allocation_mode` - Current allocation mode (gauge)

## Performance Tuning

### Reduce Sampling Overhead

- Increase `MBCAS_SAMPLING_INTERVAL` (default: 1s)
- Increase `MBCAS_WRITE_INTERVAL` (default: 5s)

### Improve Fast Guardrail Response

- Decrease `MBCAS_FAST_LOOP_INTERVAL` (default: 2s)
- Increase `MBCAS_FAST_STEP_SIZE_MAX` (default: 0.40)

### Reduce Memory Usage

- Decrease `MBCAS_MAX_HISTORY_SIZE` (default: 1000)
- Decrease `MBCAS_AGENT_MEMORY_SIZE` (default: 20)

## Getting Help

1. Check logs with `-v=4` for detailed debugging
2. Review PodAllocation CRD status fields
3. Verify configuration via ConfigMap or environment variables
4. Check Kubernetes RBAC permissions
5. Review cgroup access permissions
