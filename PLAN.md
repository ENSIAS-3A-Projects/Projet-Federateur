I will propose concrete fixes organized by severity and then enhancements to make the system viable in production. I will include actual code where the fix is non-trivial.

CRITICAL FIX ONE: IMPLEMENT THE WRITER

The writer must actually create and update PodAllocation custom resources. The current stub discards all computed allocations.

```go
package agent

import (
    "context"
    "fmt"

    corev1 "k8s.io/api/core/v1"
    apierrors "k8s.io/apimachinery/pkg/api/errors"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/types"
    "k8s.io/client-go/rest"
    "k8s.io/klog/v2"
    "sigs.k8s.io/controller-runtime/pkg/client"

    allocationv1alpha1 "mbcas/api/v1alpha1"
)

type Writer struct {
    client client.Client
}

func NewWriter(config *rest.Config) (*Writer, error) {
    scheme := runtime.NewScheme()
    _ = allocationv1alpha1.AddToScheme(scheme)
    _ = corev1.AddToScheme(scheme)

    c, err := client.New(config, client.Options{Scheme: scheme})
    if err != nil {
        return nil, fmt.Errorf("create controller-runtime client: %w", err)
    }
    return &Writer{client: c}, nil
}

func (w *Writer) WritePodAllocation(ctx context.Context, pod *corev1.Pod, request, limit string, shadowPrice float64) error {
    name := fmt.Sprintf("%s-%s", pod.Namespace, pod.Name)
    
    pa := &allocationv1alpha1.PodAllocation{}
    err := w.client.Get(ctx, types.NamespacedName{
        Namespace: pod.Namespace,
        Name:      name,
    }, pa)

    if apierrors.IsNotFound(err) {
        pa = &allocationv1alpha1.PodAllocation{
            ObjectMeta: metav1.ObjectMeta{
                Name:      name,
                Namespace: pod.Namespace,
                OwnerReferences: []metav1.OwnerReference{
                    {
                        APIVersion: "v1",
                        Kind:       "Pod",
                        Name:       pod.Name,
                        UID:        pod.UID,
                    },
                },
            },
            Spec: allocationv1alpha1.PodAllocationSpec{
                Namespace:         pod.Namespace,
                PodName:           pod.Name,
                DesiredCPURequest: request,
                DesiredCPULimit:   limit,
            },
        }
        if err := w.client.Create(ctx, pa); err != nil {
            return fmt.Errorf("create PodAllocation: %w", err)
        }
        klog.V(2).InfoS("Created PodAllocation", "name", name, "request", request, "limit", limit)
        return nil
    }

    if err != nil {
        return fmt.Errorf("get PodAllocation: %w", err)
    }

    if pa.Spec.DesiredCPURequest == request && pa.Spec.DesiredCPULimit == limit {
        return nil
    }

    pa.Spec.DesiredCPURequest = request
    pa.Spec.DesiredCPULimit = limit
    if err := w.client.Update(ctx, pa); err != nil {
        return fmt.Errorf("update PodAllocation: %w", err)
    }
    klog.V(2).InfoS("Updated PodAllocation", "name", name, "request", request, "limit", limit)
    return nil
}

func (w *Writer) DeletePodAllocation(ctx context.Context, namespace, podName string) error {
    name := fmt.Sprintf("%s-%s", namespace, podName)
    pa := &allocationv1alpha1.PodAllocation{
        ObjectMeta: metav1.ObjectMeta{
            Name:      name,
            Namespace: namespace,
        },
    }
    if err := w.client.Delete(ctx, pa); err != nil && !apierrors.IsNotFound(err) {
        return fmt.Errorf("delete PodAllocation: %w", err)
    }
    return nil
}
```

The agent must also clean up orphaned PodAllocations when pods are deleted. Add to syncAgents:

```go
func (a *Agent) syncAgents(pods []*corev1.Pod) {
    a.mu.Lock()
    defer a.mu.Unlock()

    activeUIDs := make(map[types.UID]bool)
    for _, pod := range pods {
        activeUIDs[pod.UID] = true
        if _, exists := a.podAgents[pod.UID]; !exists {
            a.podAgents[pod.UID] = NewPodAgent(pod.UID, 0.0)
        }
    }

    for uid := range a.podAgents {
        if !activeUIDs[uid] {
            // Find the pod info before deleting
            for ns, name := range a.uidToName {
                if /* match uid */ {
                    _ = a.writer.DeletePodAllocation(a.ctx, ns, name)
                }
            }
            delete(a.podAgents, uid)
            delete(a.lastAllocations, uid)
        }
    }
}
```

CRITICAL FIX TWO: USE NODE CAPACITY NOT SUM OF LIMITS

The current capacity model only redistributes existing allocations. The agent must use actual node allocatable CPU.

```go
func (a *Agent) Step() {
    start := time.Now()

    pods, err := a.podInformer.ListPods()
    if err != nil || len(pods) == 0 {
        klog.V(4).InfoS("No pods to manage", "err", err)
        return
    }

    a.syncAgents(pods)
    bids := a.collectBids(pods)

    // Use node allocatable capacity, not sum of current limits
    capacity := a.getNodeAllocatableCPU()
    
    // Subtract CPU used by unmanaged pods (kube-system, etc)
    unmanagedUsage := a.getUnmanagedPodsCPU()
    available := capacity - unmanagedUsage
    
    // Apply system reserve
    reserve := int64(float64(available) * (a.config.SystemReservePercent / 100.0))
    available = available - reserve

    if available < 0 {
        available = 0
    }

    results := allocation.NashBargain(available, bids)
    a.apply(pods, results)

    klog.InfoS("MBCAS Step",
        "duration", time.Since(start),
        "pods", len(pods),
        "nodeCapacity", capacity,
        "unmanagedUsage", unmanagedUsage,
        "available", available,
        "totalDemand", a.sumDemand(bids))
}

func (a *Agent) getNodeAllocatableCPU() int64 {
    node, err := a.k8sClient.CoreV1().Nodes().Get(a.ctx, a.nodeName, metav1.GetOptions{})
    if err != nil {
        klog.ErrorS(err, "Failed to get node, using fallback capacity")
        return 4000
    }
    allocatable := node.Status.Allocatable[corev1.ResourceCPU]
    return allocatable.MilliValue()
}

func (a *Agent) getUnmanagedPodsCPU() int64 {
    allPods, err := a.k8sClient.CoreV1().Pods("").List(a.ctx, metav1.ListOptions{
        FieldSelector: "spec.nodeName=" + a.nodeName,
    })
    if err != nil {
        return 0
    }

    var total int64
    for _, pod := range allPods.Items {
        if ExcludedNamespaces[pod.Namespace] {
            for _, container := range pod.Spec.Containers {
                if req, ok := container.Resources.Requests[corev1.ResourceCPU]; ok {
                    total += req.MilliValue()
                }
            }
        }
    }
    return total
}
```

CRITICAL FIX THREE: WIRE CONFIGURATION TO PODAGENT

The agent creates PodAgents with hardcoded parameters ignoring the configuration.

```go
func NewPodAgentWithConfig(uid types.UID, sloTarget float64, config *AgentConfig) *PodAgent {
    return &PodAgent{
        UID:        uid,
        SLOTarget:  sloTarget,
        QTable:     make(map[string]map[string]float64),
        Alpha:      config.AgentLearningRate,
        Gamma:      config.AgentDiscountFactor,
        Epsilon:    config.AgentExplorationRate,
        Allocation: 100,
    }
}
```

Update syncAgents to pass config:

```go
if _, exists := a.podAgents[pod.UID]; !exists {
    sloTarget := extractSLOTarget(pod)
    a.podAgents[pod.UID] = NewPodAgentWithConfig(pod.UID, sloTarget, a.config)
}

func extractSLOTarget(pod *corev1.Pod) float64 {
    if val, ok := pod.Annotations["mbcas.io/target-latency-ms"]; ok {
        if f, err := strconv.ParseFloat(val, 64); err == nil {
            return f
        }
    }
    return 0.0
}
```

FIX FOUR: NASH BARGAINING REDISTRIBUTION ITERATION

The current redistribution only runs once and may leave capacity unused when multiple agents hit their caps.

```go
func NashBargain(capacity int64, bids []Bid) map[types.UID]int64 {
    if len(bids) == 0 {
        return make(map[types.UID]int64)
    }

    totalBaseline := int64(0)
    totalWeight := 0.0
    for _, b := range bids {
        totalBaseline += b.Min
        totalWeight += b.Weight
    }

    if totalBaseline > capacity {
        return scaleBaselinesSimple(capacity, bids, totalWeight)
    }

    allocations := make(map[types.UID]int64)
    for _, b := range bids {
        allocations[b.UID] = b.Min
    }

    remaining := capacity - totalBaseline
    active := make([]Bid, len(bids))
    copy(active, bids)

    // Iterate until no more redistribution possible
    for remaining > 0 && len(active) > 0 {
        activeWeight := 0.0
        for _, b := range active {
            activeWeight += b.Weight
        }

        if activeWeight == 0 {
            break
        }

        newActive := make([]Bid, 0, len(active))
        distributed := int64(0)

        for _, b := range active {
            share := int64(float64(remaining) * (b.Weight / activeWeight))
            newAlloc := allocations[b.UID] + share

            if newAlloc >= b.Max {
                // Capped
                added := b.Max - allocations[b.UID]
                allocations[b.UID] = b.Max
                distributed += added
            } else {
                allocations[b.UID] = newAlloc
                distributed += share
                newActive = append(newActive, b)
            }
        }

        remaining -= distributed
        active = newActive

        // Safety: if nothing distributed, break to avoid infinite loop
        if distributed == 0 {
            break
        }
    }

    return allocations
}
```

FIX FIVE: IMPLEMENT HYSTERESIS

The MinChangePercent configuration exists but is not used. Implement it to prevent oscillation.

```go
func (a *Agent) apply(pods []*corev1.Pod, results map[types.UID]int64) {
    a.mu.Lock()
    defer a.mu.Unlock()

    for _, pod := range pods {
        allocMilli, ok := results[pod.UID]
        if !ok {
            continue
        }

        lastAlloc := a.lastAllocations[pod.UID]
        
        // Hysteresis: skip if change is below threshold
        if lastAlloc > 0 {
            changePct := math.Abs(float64(allocMilli-lastAlloc)) / float64(lastAlloc) * 100
            if changePct < a.config.MinChangePercent {
                continue
            }
        }

        a.lastAllocations[pod.UID] = allocMilli

        reqMilli := int64(float64(allocMilli) * 0.9)
        if reqMilli >= allocMilli {
            reqMilli = allocMilli - 5
        }
        if reqMilli < 10 {
            reqMilli = 10
        }

        limitStr := fmt.Sprintf("%dm", allocMilli)
        requestStr := fmt.Sprintf("%dm", reqMilli)

        if err := a.writer.WritePodAllocation(a.ctx, pod, requestStr, limitStr, 0.0); err != nil {
            klog.ErrorS(err, "Failed to write allocation", "pod", pod.Name)
        }
    }
}
```

FIX SIX: STARTUP GRACE PERIOD

Implement the startup grace period to prevent aggressive downscaling of newly started pods.

```go
type PodAgent struct {
    // ... existing fields
    StartTime time.Time
}

func NewPodAgentWithConfig(uid types.UID, sloTarget float64, config *AgentConfig) *PodAgent {
    return &PodAgent{
        // ... existing initialization
        StartTime: time.Now(),
    }
}

func (pa *PodAgent) ComputeBid(config *AgentConfig) Bid {
    pa.mu.Lock()
    defer pa.mu.Unlock()

    // During startup grace period, only allow increases
    inGracePeriod := time.Since(pa.StartTime) < config.StartupGracePeriod

    state := pa.stateInternal()
    action := pa.selectActionInternal(state)

    pa.PrevState = state
    pa.PrevAction = action

    demandMultiplier := 1.0
    weightMultiplier := 1.0

    switch action {
    case "aggressive":
        demandMultiplier = 1.5
        weightMultiplier = 1.2
    case "normal":
        demandMultiplier = 1.2
        weightMultiplier = 1.0
    case "conservative":
        demandMultiplier = 1.0
        weightMultiplier = 0.8
    }

    baseDemand := pa.Usage
    if baseDemand < config.AbsoluteMinAllocation {
        baseDemand = config.AbsoluteMinAllocation
    }

    demand := int64(float64(baseDemand) * demandMultiplier)

    if pa.Throttling > 0.05 {
        demand = int64(float64(demand) * (1.0 + pa.Throttling*2))
    }

    minBid := int64(float64(pa.Usage) * (1.0 + config.NeedHeadroomFactor))
    if minBid < config.AbsoluteMinAllocation {
        minBid = config.AbsoluteMinAllocation
    }

    // During grace period, min cannot be below current allocation
    if inGracePeriod && minBid < pa.Allocation {
        minBid = pa.Allocation
    }

    var maxBid int64
    if pa.Throttling > 0.05 {
        maxBid = 1000000
    } else if pa.Usage < config.AbsoluteMinAllocation*2 {
        maxBid = int64(float64(pa.Usage) * (1.0 + config.WantHeadroomFactor))
        if maxBid < minBid+10 {
            maxBid = minBid + 10
        }
    } else {
        maxBid = int64(float64(demand) * (1.0 + config.WantHeadroomFactor))
    }

    return Bid{
        UID:    pa.UID,
        Demand: demand,
        Weight: weightMultiplier,
        Min:    minBid,
        Max:    maxBid,
    }
}
```

FIX SEVEN: IMPLEMENT COST EFFICIENCY MODE

The configuration defines CostEfficiencyMode with AlphaUp, AlphaDown, IdleDecayRate, and TargetThrottling but none are used.

```go
type PodAgent struct {
    // ... existing fields
    SmoothedDemand int64 // EMA smoothed demand
}

func (pa *PodAgent) ComputeBidWithEfficiency(config *AgentConfig) Bid {
    pa.mu.Lock()
    defer pa.mu.Unlock()

    rawDemand := pa.computeRawDemand(config)

    if config.CostEfficiencyMode {
        // Asymmetric smoothing: fast down, slow up
        if rawDemand < pa.SmoothedDemand {
            // Going down: fast (high alpha)
            pa.SmoothedDemand = int64(config.AlphaDown*float64(rawDemand) + 
                (1-config.AlphaDown)*float64(pa.SmoothedDemand))
        } else {
            // Going up: slow (low alpha)
            pa.SmoothedDemand = int64(config.AlphaUp*float64(rawDemand) + 
                (1-config.AlphaUp)*float64(pa.SmoothedDemand))
        }

        // Idle decay: if usage is very low, decay allocation
        if pa.Usage < config.AbsoluteMinAllocation && pa.Throttling < config.TargetThrottling {
            pa.SmoothedDemand = int64(float64(pa.SmoothedDemand) * (1.0 - config.IdleDecayRate))
        }

        // Target throttling: allow some throttling before increasing
        if pa.Throttling < config.TargetThrottling {
            // Below target, no urgency to increase
            rawDemand = pa.SmoothedDemand
        }
    } else {
        pa.SmoothedDemand = rawDemand
    }

    return pa.buildBid(pa.SmoothedDemand, config)
}

func (pa *PodAgent) computeRawDemand(config *AgentConfig) int64 {
    state := pa.stateInternal()
    action := pa.selectActionInternal(state)
    pa.PrevState = state
    pa.PrevAction = action

    multiplier := map[string]float64{
        "aggressive":   1.5,
        "normal":       1.2,
        "conservative": 1.0,
    }[action]

    baseDemand := pa.Usage
    if baseDemand < config.AbsoluteMinAllocation {
        baseDemand = config.AbsoluteMinAllocation
    }

    demand := int64(float64(baseDemand) * multiplier)
    if pa.Throttling > 0.05 {
        demand = int64(float64(demand) * (1.0 + pa.Throttling*2))
    }

    return demand
}

func (pa *PodAgent) buildBid(demand int64, config *AgentConfig) Bid {
    // ... existing min/max logic
}
```

FIX EIGHT: Q-TABLE PERSISTENCE

Q-learning state is lost on agent restart. Implement persistence using a ConfigMap or the PodAllocation status.

```go
type QTablePersister struct {
    client    client.Client
    namespace string
    nodeName  string
}

func (p *QTablePersister) Save(ctx context.Context, agents map[types.UID]*PodAgent) error {
    data := make(map[string]string)
    for uid, agent := range agents {
        agent.mu.RLock()
        qtableBytes, _ := json.Marshal(agent.QTable)
        agent.mu.RUnlock()
        data[string(uid)] = string(qtableBytes)
    }

    cm := &corev1.ConfigMap{
        ObjectMeta: metav1.ObjectMeta{
            Name:      fmt.Sprintf("mbcas-qtable-%s", p.nodeName),
            Namespace: p.namespace,
        },
        Data: data,
    }

    existing := &corev1.ConfigMap{}
    err := p.client.Get(ctx, client.ObjectKeyFromObject(cm), existing)
    if apierrors.IsNotFound(err) {
        return p.client.Create(ctx, cm)
    }
    if err != nil {
        return err
    }
    existing.Data = data
    return p.client.Update(ctx, existing)
}

func (p *QTablePersister) Load(ctx context.Context, uid types.UID) (map[string]map[string]float64, error) {
    cm := &corev1.ConfigMap{}
    err := p.client.Get(ctx, types.NamespacedName{
        Name:      fmt.Sprintf("mbcas-qtable-%s", p.nodeName),
        Namespace: p.namespace,
    }, cm)
    if err != nil {
        return nil, err
    }

    if data, ok := cm.Data[string(uid)]; ok {
        var qtable map[string]map[string]float64
        if err := json.Unmarshal([]byte(data), &qtable); err != nil {
            return nil, err
        }
        return qtable, nil
    }
    return nil, nil
}
```

Call Save periodically in the agent loop and Load when creating new PodAgents.

FIX NINE: CGROUP READER CLEANUP

The Cleanup method exists but is never called. Add cleanup to the agent loop.

```go
func (a *Agent) Step() {
    // ... existing code

    // Cleanup stale cgroup samples
    existingPods := make(map[string]bool)
    for _, pod := range pods {
        existingPods[string(pod.UID)] = true
    }
    a.cgroupReader.Cleanup(existingPods)
}
```

FIX TEN: SLO VIOLATION DETECTION

The SLO violation flag is always false. Implement actual SLO checking if Prometheus is configured.

```go
type SLOChecker struct {
    prometheusURL string
    httpClient    *http.Client
}

func NewSLOChecker(prometheusURL string) *SLOChecker {
    if prometheusURL == "" {
        return nil
    }
    return &SLOChecker{
        prometheusURL: prometheusURL,
        httpClient:    &http.Client{Timeout: 5 * time.Second},
    }
}

func (s *SLOChecker) CheckViolation(ctx context.Context, pod *corev1.Pod, targetMs float64) (bool, error) {
    if s == nil || targetMs <= 0 {
        return false, nil
    }

    query := fmt.Sprintf(
        `histogram_quantile(0.99, sum(rate(http_request_duration_seconds_bucket{pod="%s"}[1m])) by (le))`,
        pod.Name,
    )
    
    resp, err := s.httpClient.Get(fmt.Sprintf("%s/api/v1/query?query=%s", 
        s.prometheusURL, url.QueryEscape(query)))
    if err != nil {
        return false, err
    }
    defer resp.Body.Close()

    var result struct {
        Data struct {
            Result []struct {
                Value []interface{} `json:"value"`
            } `json:"result"`
        } `json:"data"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return false, err
    }

    if len(result.Data.Result) == 0 {
        return false, nil
    }

    p99Str, ok := result.Data.Result[0].Value[1].(string)
    if !ok {
        return false, nil
    }
    p99, err := strconv.ParseFloat(p99Str, 64)
    if err != nil {
        return false, nil
    }

    p99Ms := p99 * 1000
    return p99Ms > targetMs, nil
}
```

Wire this into collectBids:

```go
func (a *Agent) collectBids(pods []*corev1.Pod) []allocation.Bid {
    a.mu.RLock()
    defer a.mu.RUnlock()

    var bids []allocation.Bid
    for _, pod := range pods {
        agent := a.podAgents[pod.UID]
        if agent == nil {
            continue
        }

        metrics, err := a.cgroupReader.ReadPodMetrics(pod, a.config.WriteInterval.Seconds())
        if err != nil {
            continue
        }

        lastAlloc := a.lastAllocations[pod.UID]
        if lastAlloc == 0 {
            lastAlloc = 100
        }

        // Check SLO violation
        sloViolation := false
        if a.sloChecker != nil && agent.SLOTarget > 0 {
            sloViolation, _ = a.sloChecker.CheckViolation(a.ctx, pod, agent.SLOTarget)
        }

        agent.UpdateUsage(metrics.ActualUsageMilli)
        agent.Update(lastAlloc, metrics.Demand, sloViolation)

        b := agent.ComputeBid(a.config)
        bids = append(bids, allocation.Bid{
            UID:    b.UID,
            Demand: b.Demand,
            Weight: b.Weight,
            Min:    b.Min,
            Max:    b.Max,
        })
    }
    return bids
}
```

ENHANCEMENT ONE: DUAL LOOP ARCHITECTURE

The configuration defines FastLoopInterval and SlowLoopInterval suggesting a dual loop but only one loop exists. Implement the dual loop for better responsiveness.

```go
func (a *Agent) Run() error {
    klog.InfoS("Starting dual-loop agent", "node", a.nodeName)

    fastTicker := time.NewTicker(a.config.FastLoopInterval)
    slowTicker := time.NewTicker(a.config.SlowLoopInterval)
    defer fastTicker.Stop()
    defer slowTicker.Stop()

    for {
        select {
        case <-a.ctx.Done():
            return a.ctx.Err()
        case <-fastTicker.C:
            a.FastStep()
        case <-slowTicker.C:
            a.SlowStep()
        }
    }
}

func (a *Agent) FastStep() {
    // Fast loop: React to SLO violations and high throttling
    // Only increases allocations, never decreases
    pods, err := a.podInformer.ListPods()
    if err != nil || len(pods) == 0 {
        return
    }

    for _, pod := range pods {
        a.mu.RLock()
        agent := a.podAgents[pod.UID]
        a.mu.RUnlock()
        if agent == nil {
            continue
        }

        metrics, err := a.cgroupReader.ReadPodMetrics(pod, a.config.FastLoopInterval.Seconds())
        if err != nil {
            continue
        }

        needsBoost := false
        
        // Check throttling threshold
        if metrics.Demand > a.config.ThrottlingThreshold {
            needsBoost = true
        }

        // Check SLO
        if a.sloChecker != nil && agent.SLOTarget > 0 {
            violation, _ := a.sloChecker.CheckViolation(a.ctx, pod, agent.SLOTarget*a.config.P99ThresholdMultiplier)
            if violation {
                needsBoost = true
            }
        }

        if needsBoost {
            a.mu.Lock()
            currentAlloc := a.lastAllocations[pod.UID]
            if currentAlloc == 0 {
                currentAlloc = 100
            }

            // Fast step up
            stepSize := a.config.FastStepSizeMin + 
                (a.config.FastStepSizeMax-a.config.FastStepSizeMin)*metrics.Demand
            newAlloc := int64(float64(currentAlloc) * (1.0 + stepSize))
            
            a.lastAllocations[pod.UID] = newAlloc
            a.mu.Unlock()

            reqMilli := int64(float64(newAlloc) * 0.9)
            limitStr := fmt.Sprintf("%dm", newAlloc)
            requestStr := fmt.Sprintf("%dm", reqMilli)

            _ = a.writer.WritePodAllocation(a.ctx, pod, requestStr, limitStr, 0.0)
            klog.V(2).InfoS("Fast loop boost", "pod", pod.Name, "from", currentAlloc, "to", newAlloc)
        }
    }
}

func (a *Agent) SlowStep() {
    // Slow loop: Full Nash bargaining optimization
    // This is the original Step() logic
    a.Step()
}
```

ENHANCEMENT TWO: SHADOW PRICE FEEDBACK

The ShadowPriceCPU field exists in the status but is never meaningfully computed. Implement shadow price calculation as the Lagrange multiplier from the Nash optimization.

```go
type AllocationResultWithPrice struct {
    Allocations map[types.UID]int64
    ShadowPrice float64
}

func NashBargainWithPrice(capacity int64, bids []Bid) AllocationResultWithPrice {
    allocations := NashBargain(capacity, bids)

    // Shadow price is the marginal value of additional capacity
    // Computed as the average weight of binding constraints
    totalDemand := int64(0)
    totalWeight := 0.0
    for _, b := range bids {
        totalDemand += b.Demand
        totalWeight += b.Weight
    }

    var shadowPrice float64
    if totalDemand > capacity && totalWeight > 0 {
        // Congested: shadow price is positive
        // Higher price when more constrained
        congestionRatio := float64(totalDemand-capacity) / float64(capacity)
        shadowPrice = congestionRatio * (totalWeight / float64(len(bids)))
    } else {
        // Uncongested: shadow price is zero
        shadowPrice = 0.0
    }

    return AllocationResultWithPrice{
        Allocations: allocations,
        ShadowPrice: shadowPrice,
    }
}
```

Use shadow price in bids for price-responsive agents:

```go
func (pa *PodAgent) AdjustForPrice(demand int64, shadowPrice float64, config *AgentConfig) int64 {
    if !config.EnablePriceResponse || shadowPrice <= 0 {
        return demand
    }

    // Reduce demand proportionally to price
    // Higher price -> lower demand (price-taking behavior)
    reduction := 1.0 - math.Min(shadowPrice*0.5, 0.5) // Max 50% reduction
    return int64(float64(demand) * reduction)
}
```

ENHANCEMENT THREE: LIVENESS AND HEALTH ENDPOINTS

The agent DaemonSet defines health probes but no HTTP server exists in agent.go.

```go
func (a *Agent) startHealthServer() {
    mux := http.NewServeMux()
    
    mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        w.Write([]byte("ok"))
    })

    mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
        if !a.podInformer.HasSynced() {
            w.WriteHeader(http.StatusServiceUnavailable)
            w.Write([]byte("informer not synced"))
            return
        }
        w.WriteHeader(http.StatusOK)
        w.Write([]byte("ok"))
    })

    mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
        a.mu.RLock()
        podCount := len(a.podAgents)
        a.mu.RUnlock()
        
        fmt.Fprintf(w, "# HELP mbcas_managed_pods Number of pods managed by this agent\n")
        fmt.Fprintf(w, "# TYPE mbcas_managed_pods gauge\n")
        fmt.Fprintf(w, "mbcas_managed_pods %d\n", podCount)
    })

    server := &http.Server{
        Addr:    ":8082",
        Handler: mux,
    }

    go func() {
        if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            klog.ErrorS(err, "Health server error")
        }
    }()
}
```

Call startHealthServer in NewAgent.

ENHANCEMENT FOUR: METRICS AND OBSERVABILITY

Add Prometheus metrics for key system behaviors.

```go
var (
    allocationChanges = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "mbcas_allocation_changes_total",
            Help: "Total number of allocation changes",
        },
        []string{"pod", "namespace", "direction"},
    )

    allocationLatency = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "mbcas_allocation_latency_seconds",
            Help:    "Time from decision to applied",
            Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30},
        },
        []string{"pod", "namespace"},
    )

    nashBargainMode = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "mbcas_nash_mode_total",
            Help: "Nash bargaining mode selections",
        },
        []string{"mode"},
    )

    qlearningReward = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "mbcas_qlearning_reward",
            Help:    "Q-learning reward distribution",
            Buckets: []float64{-100, -50, -20, -10, 0, 5, 10, 15, 20},
        },
        []string{"pod", "namespace"},
    )

    throttlingRatio = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "mbcas_throttling_ratio",
            Help: "Current throttling ratio per pod",
        },
        []string{"pod", "namespace"},
    )
)

func init() {
    prometheus.MustRegister(allocationChanges)
    prometheus.MustRegister(allocationLatency)
    prometheus.MustRegister(nashBargainMode)
    prometheus.MustRegister(qlearningReward)
    prometheus.MustRegister(throttlingRatio)
}
```

ENHANCEMENT FIVE: GRACEFUL DEGRADATION

Handle cases where the controller is unavailable or the resize feature is disabled.

```go
func (a *Agent) apply(pods []*corev1.Pod, results map[types.UID]int64) {
    a.mu.Lock()
    defer a.mu.Unlock()

    var writeErrors int
    for _, pod := range pods {
        allocMilli, ok := results[pod.UID]
        if !ok {
            continue
        }

        // ... existing hysteresis check

        a.lastAllocations[pod.UID] = allocMilli

        reqMilli := int64(float64(allocMilli) * 0.9)
        if reqMilli >= allocMilli {
            reqMilli = allocMilli - 5
        }
        if reqMilli < 10 {
            reqMilli = 10
        }

        limitStr := fmt.Sprintf("%dm", allocMilli)
        requestStr := fmt.Sprintf("%dm", reqMilli)

        if err := a.writer.WritePodAllocation(a.ctx, pod, requestStr, limitStr, 0.0); err != nil {
            writeErrors++
            klog.ErrorS(err, "Failed to write allocation", "pod", pod.Name)
        }
    }

    // If too many write errors, back off
    if writeErrors > len(pods)/2 {
        klog.Warning("High write error rate, backing off")
        time.Sleep(10 * time.Second)
    }
}
```

SUMMARY OF CHANGES

The critical fixes are the writer implementation, the capacity model change, and wiring configuration to PodAgents. These three changes make the system functional. Without them, no allocations are ever created, the Nash bargaining operates on the wrong capacity, and learning parameters are ignored.

The hysteresis, startup grace period, and cost efficiency mode implementations make the system stable and usable in production by preventing oscillation and respecting pod startup requirements.

The dual loop architecture improves responsiveness to SLO violations while keeping the optimization loop less frequent for efficiency.

The Q-table persistence, SLO checking, and shadow price feedback complete the intended features that were defined but not implemented.

The health endpoints and metrics make the system observable and operable.

After these changes, the data flow becomes: cgroup reader samples metrics, PodAgents compute bids using Q-learning and configuration, Nash bargaining allocates capacity from actual node resources, writer creates PodAllocation CRs, controller reconciles them by patching pods via the resize subresource, and the next cycle observes outcomes and updates Q-values.