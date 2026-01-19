package agent

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// AgentConfig holds all configurable parameters for the agent.
// Values can be loaded from ConfigMap or environment variables.
type AgentConfig struct {
	// SamplingInterval is how often we sample kernel signals.
	SamplingInterval time.Duration

	// WriteInterval is how often we write PodAllocation updates to the API.
	WriteInterval time.Duration

	// MinChangePercent is the minimum change required to write an update (hysteresis).
	MinChangePercent float64

	// SystemReservePercent is the percentage of node CPU to reserve for system.
	SystemReservePercent float64

	// BaselineCPUPerPod is the minimum CPU to allocate per pod.
	BaselineCPUPerPod string

	// StartupGracePeriod is the duration after startup during which
	// allocations are only allowed to increase, not decrease.
	StartupGracePeriod time.Duration

	// SLOTargetLatencyMs is the target p99 latency for SLO scoring (0 = disabled).
	// Used for utility-based allocation.
	SLOTargetLatencyMs float64

	// PrometheusURL is the URL for Prometheus API (empty = disabled).
	// Used for querying latency metrics for SLO scoring.
	PrometheusURL string

	// FastLoopInterval is the interval for the fast SLO guardrail loop (1-2s).
	// This loop responds quickly to SLO violations and throttling pressure.
	FastLoopInterval time.Duration

	// SlowLoopInterval is the interval for the slow economic optimizer loop (5-15s).
	// This loop runs market clearing and optimization.
	SlowLoopInterval time.Duration

	// P99ThresholdMultiplier is the multiplier for p99 latency threshold.
	// Fast loop triggers when p99 > target * P99ThresholdMultiplier.
	P99ThresholdMultiplier float64

	// ThrottlingThreshold is the throttling ratio threshold (0-1).
	// Fast loop triggers when throttling ratio > ThrottlingThreshold.
	ThrottlingThreshold float64

	// FastStepSizeMin is the minimum fast-up step size (0-1).
	// When fast loop triggers, allocation increases by at least this percentage.
	FastStepSizeMin float64

	// FastStepSizeMax is the maximum fast-up step size (0-1).
	// When fast loop triggers, allocation increases by at most this percentage.
	FastStepSizeMax float64

	// AllocationMechanism selects the market clearing mechanism.
	// "nash" = Nash Bargaining (default), "primal-dual" = Distributed Primal-Dual.
	AllocationMechanism string

	// EnableKalmanPrediction enables Kalman filter for demand prediction.
	EnableKalmanPrediction bool

	// EnableBatchReconciliation enables batch reconciliation of multiple PodAllocations.
	EnableBatchReconciliation bool

	// CoalitionGroupingAnnotation is the annotation key for coalition grouping.
	// Pods with the same annotation value are grouped for joint optimization.
	CoalitionGroupingAnnotation string

	// EnablePriceResponse enables price-responsive demand adjustment.
	// When true, agents adjust demand based on shadow prices (price-taking behavior).
	// High prices cause agents to reduce demand, low prices allow increased demand.
	EnablePriceResponse bool

	// EnableAgentBasedModeling enables agent-based modeling features.
	// When true, pods act as autonomous agents with learning and adaptation.
	EnableAgentBasedModeling bool

	// AgentLearningRate controls how quickly agents adapt (0.01-0.5).
	// Higher values mean faster adaptation but potentially more instability.
	AgentLearningRate float64

	// AgentMemorySize is the number of past decisions to remember.
	// Used for learning from history and strategy evolution.
	AgentMemorySize int

	// AgentExplorationRate is the initial exploration rate for Q-learning (0.0-1.0).
	// Higher values mean more exploration (trying different strategies).
	AgentExplorationRate float64

	// AgentDiscountFactor is the discount factor for Q-learning (0.0-1.0).
	// Controls importance of future rewards vs immediate rewards.
	AgentDiscountFactor float64

	// MaxCoalitionSize limits the number of members in a single coalition.
	// Prevents O(2^n) explosion in coalition stability checks.
	MaxCoalitionSize int

	// MaxHistorySize is the maximum number of decision outcomes to remember per pod.
	// Used for learning from history and strategy evolution.
	MaxHistorySize int

	// MinUsageMicroseconds is the minimum CPU usage in microseconds for valid demand samples.
	// Samples below this threshold are considered invalid.
	MinUsageMicroseconds int64

	// AbsoluteMinAllocation is the minimum CPU allocation per pod in millicores.
	// Prevents allocations from going below this threshold.
	AbsoluteMinAllocation int64

	// NeedHeadroomFactor is the conservative headroom for actual need (default 15%).
	// This is what the pod truly requires to avoid throttling.
	NeedHeadroomFactor float64

	// WantHeadroomFactor is the base headroom for want calculation (default 10%).
	WantHeadroomFactor float64

	// MaxDemandMultiplier controls aggressive scaling in want (default 4.0x max growth).
	// Used when pod signals it would like more resources.
	MaxDemandMultiplier float64

	// CostEfficiencyMode enables aggressive cost optimization logic.
	// When true: Fast Down / Slow Up, idle decay, target throttling.
	CostEfficiencyMode bool

	// TargetThrottling is the acceptable throttling ratio (0-1).
	// Allocator will try to keep throttling below this but won't panic if it's non-zero.
	TargetThrottling float64

	// IdleDecayRate is the rate at which allocation decays when usage is low (per tick).
	IdleDecayRate float64

	// AlphaDown is the smoothing factor for downward demand adjustments (0-1).
	// Higher = faster decay.
	AlphaDown float64

	// AlphaUp is the smoothing factor for upward demand adjustments (0-1).
	// Lower = slower growth (damped).
	AlphaUp float64
}

// DefaultConfig returns a configuration with default values.
func DefaultConfig() *AgentConfig {
	return &AgentConfig{
		SamplingInterval:            1 * time.Second,
		WriteInterval:               5 * time.Second,
		MinChangePercent:            2.0,
		SystemReservePercent:        10.0,
		BaselineCPUPerPod:           "100m",
		StartupGracePeriod:          90 * time.Second,
		SLOTargetLatencyMs:          0,  // Disabled by default
		PrometheusURL:               "", // Empty = disabled
		FastLoopInterval:            2 * time.Second,
		SlowLoopInterval:            10 * time.Second,
		P99ThresholdMultiplier:      1.2,
		ThrottlingThreshold:         0.1,
		FastStepSizeMin:             0.20, // 20%
		FastStepSizeMax:             0.40, // 40%
		AllocationMechanism:         "nash",
		EnableKalmanPrediction:      true, // Enabled by default
		EnableBatchReconciliation:   true, // Enabled by default
		EnablePriceResponse:         true, // Enabled by default
		EnableAgentBasedModeling:    true, // Enabled by default
		AgentLearningRate:           0.1,  // Moderate learning rate
		AgentMemorySize:             20,   // Remember last 20 decisions
		AgentExplorationRate:        0.2,  // 20% exploration initially
		AgentDiscountFactor:         0.9,  // Value future rewards
		CoalitionGroupingAnnotation: "mbcas.io/coalition",
		MaxCoalitionSize:            8,     // Limit coalition size to prevent O(2^n) explosion
		MaxHistorySize:              1000,  // Maximum decision history per pod
		MinUsageMicroseconds:        1000,  // 1ms minimum usage for valid samples
		AbsoluteMinAllocation:       10,    // Minimum allocation in millicores
		NeedHeadroomFactor:          0.15,  // 15% conservative headroom
		WantHeadroomFactor:          0.10,  // 10% base headroom
		MaxDemandMultiplier:         4.0,   // 4x max growth for want calculation
		CostEfficiencyMode:          false, // Disabled by default, enable via flag/env
		TargetThrottling:            0.05,  // Target 5% throttling
		IdleDecayRate:               0.005, // 0.5% decay per tick
		AlphaDown:                   0.9,   // Aggressive downward adjustment for cost efficiency
		AlphaUp:                     0.4,   // Increased from 0.1 for better responsiveness
	}
}

// LoadConfig loads configuration from ConfigMap with environment variable fallbacks.
// ConfigMap is loaded from namespace "mbcas-system" with name "mbcas-agent-config".
// If ConfigMap doesn't exist or fields are missing, environment variables are used.
func LoadConfig(ctx context.Context, k8sClient kubernetes.Interface) (*AgentConfig, error) {
	config := DefaultConfig()

	// Try to load from ConfigMap first
	cm, err := k8sClient.CoreV1().ConfigMaps("mbcas-system").Get(ctx, "mbcas-agent-config", metav1.GetOptions{})
	if err != nil {
		klog.V(2).InfoS("ConfigMap not found, using defaults and environment variables", "error", err)
	} else {
		// Load from ConfigMap data
		if err := config.loadFromConfigMap(cm); err != nil {
			klog.Warningf("Error loading from ConfigMap, using defaults: %v", err)
		} else {
			klog.InfoS("Loaded configuration from ConfigMap", "namespace", "mbcas-system", "name", "mbcas-agent-config")
		}
	}

	// Override with environment variables (environment takes precedence)
	config.loadFromEnvironment()

	// Validate configuration
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	// Log final configuration
	config.Log()

	return config, nil
}

// loadFromConfigMap loads configuration values from a ConfigMap.
func (c *AgentConfig) loadFromConfigMap(cm *corev1.ConfigMap) error {
	if cm.Data == nil {
		return fmt.Errorf("ConfigMap data is nil")
	}

	data := cm.Data

	// Parse SamplingInterval
	if val, ok := data["samplingInterval"]; ok && val != "" {
		d, err := time.ParseDuration(val)
		if err != nil {
			return fmt.Errorf("invalid samplingInterval: %w", err)
		}
		c.SamplingInterval = d
	}

	// Parse WriteInterval
	if val, ok := data["writeInterval"]; ok && val != "" {
		d, err := time.ParseDuration(val)
		if err != nil {
			return fmt.Errorf("invalid writeInterval: %w", err)
		}
		c.WriteInterval = d
	}

	// Parse MinChangePercent
	if val, ok := data["minChangePercent"]; ok && val != "" {
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return fmt.Errorf("invalid minChangePercent: %w", err)
		}
		c.MinChangePercent = f
	}

	// Parse SystemReservePercent
	if val, ok := data["systemReservePercent"]; ok && val != "" {
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return fmt.Errorf("invalid systemReservePercent: %w", err)
		}
		c.SystemReservePercent = f
	}

	// Parse BaselineCPUPerPod
	if val, ok := data["baselineCPUPerPod"]; ok && val != "" {
		c.BaselineCPUPerPod = val
	}

	// Parse StartupGracePeriod
	if val, ok := data["startupGracePeriod"]; ok && val != "" {
		d, err := time.ParseDuration(val)
		if err != nil {
			return fmt.Errorf("invalid startupGracePeriod: %w", err)
		}
		c.StartupGracePeriod = d
	}

	// Parse SLOTargetLatencyMs
	if val, ok := data["sloTargetLatencyMs"]; ok && val != "" {
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return fmt.Errorf("invalid sloTargetLatencyMs: %w", err)
		}
		c.SLOTargetLatencyMs = f
	}

	// Parse PrometheusURL
	if val, ok := data["prometheusURL"]; ok && val != "" {
		c.PrometheusURL = val
	}

	// Parse FastLoopInterval
	if val, ok := data["fastLoopInterval"]; ok && val != "" {
		d, err := time.ParseDuration(val)
		if err != nil {
			return fmt.Errorf("invalid fastLoopInterval: %w", err)
		}
		c.FastLoopInterval = d
	}

	// Parse SlowLoopInterval
	if val, ok := data["slowLoopInterval"]; ok && val != "" {
		d, err := time.ParseDuration(val)
		if err != nil {
			return fmt.Errorf("invalid slowLoopInterval: %w", err)
		}
		c.SlowLoopInterval = d
	}

	// Parse P99ThresholdMultiplier
	if val, ok := data["p99ThresholdMultiplier"]; ok && val != "" {
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return fmt.Errorf("invalid p99ThresholdMultiplier: %w", err)
		}
		c.P99ThresholdMultiplier = f
	}

	// Parse ThrottlingThreshold
	if val, ok := data["throttlingThreshold"]; ok && val != "" {
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return fmt.Errorf("invalid throttlingThreshold: %w", err)
		}
		c.ThrottlingThreshold = f
	}

	// Parse FastStepSizeMin
	if val, ok := data["fastStepSizeMin"]; ok && val != "" {
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return fmt.Errorf("invalid fastStepSizeMin: %w", err)
		}
		c.FastStepSizeMin = f
	}

	// Parse FastStepSizeMax
	if val, ok := data["fastStepSizeMax"]; ok && val != "" {
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return fmt.Errorf("invalid fastStepSizeMax: %w", err)
		}
		c.FastStepSizeMax = f
	}

	// Parse AllocationMechanism
	if val, ok := data["allocationMechanism"]; ok && val != "" {
		c.AllocationMechanism = val
	}

	// Parse EnableKalmanPrediction
	if val, ok := data["enableKalmanPrediction"]; ok && val != "" {
		b, err := strconv.ParseBool(val)
		if err != nil {
			return fmt.Errorf("invalid enableKalmanPrediction: %w", err)
		}
		c.EnableKalmanPrediction = b
	}

	// Parse EnableBatchReconciliation
	if val, ok := data["enableBatchReconciliation"]; ok && val != "" {
		b, err := strconv.ParseBool(val)
		if err != nil {
			return fmt.Errorf("invalid enableBatchReconciliation: %w", err)
		}
		c.EnableBatchReconciliation = b
	}

	// Parse CoalitionGroupingAnnotation
	if val, ok := data["coalitionGroupingAnnotation"]; ok && val != "" {
		c.CoalitionGroupingAnnotation = val
	}

	// Parse EnablePriceResponse
	if val, ok := data["enablePriceResponse"]; ok && val != "" {
		b, err := strconv.ParseBool(val)
		if err != nil {
			return fmt.Errorf("invalid enablePriceResponse: %w", err)
		}
		c.EnablePriceResponse = b
	}

	// Parse EnableAgentBasedModeling
	if val, ok := data["enableAgentBasedModeling"]; ok && val != "" {
		b, err := strconv.ParseBool(val)
		if err != nil {
			return fmt.Errorf("invalid enableAgentBasedModeling: %w", err)
		}
		c.EnableAgentBasedModeling = b
	}

	// Parse AgentLearningRate
	if val, ok := data["agentLearningRate"]; ok && val != "" {
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return fmt.Errorf("invalid agentLearningRate: %w", err)
		}
		c.AgentLearningRate = f
	}

	// Parse AgentMemorySize
	if val, ok := data["agentMemorySize"]; ok && val != "" {
		i, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("invalid agentMemorySize: %w", err)
		}
		c.AgentMemorySize = i
	}

	// Parse AgentExplorationRate
	if val, ok := data["agentExplorationRate"]; ok && val != "" {
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return fmt.Errorf("invalid agentExplorationRate: %w", err)
		}
		c.AgentExplorationRate = f
	}

	// Parse AgentDiscountFactor
	if val, ok := data["agentDiscountFactor"]; ok && val != "" {
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return fmt.Errorf("invalid agentDiscountFactor: %w", err)
		}
		c.AgentDiscountFactor = f
	}

	return nil
}

// loadFromEnvironment loads configuration values from environment variables.
// Environment variables take precedence over ConfigMap values.
func (c *AgentConfig) loadFromEnvironment() {
	// MBCAS_SAMPLING_INTERVAL
	if val := os.Getenv("MBCAS_SAMPLING_INTERVAL"); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			c.SamplingInterval = d
			klog.V(2).InfoS("Loaded SamplingInterval from environment", "value", c.SamplingInterval)
		}
	}

	// MBCAS_WRITE_INTERVAL
	if val := os.Getenv("MBCAS_WRITE_INTERVAL"); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			c.WriteInterval = d
			klog.V(2).InfoS("Loaded WriteInterval from environment", "value", c.WriteInterval)
		}
	}

	// MBCAS_MIN_CHANGE_PERCENT
	if val := os.Getenv("MBCAS_MIN_CHANGE_PERCENT"); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			c.MinChangePercent = f
			klog.V(2).InfoS("Loaded MinChangePercent from environment", "value", c.MinChangePercent)
		}
	}

	// MBCAS_SYSTEM_RESERVE_PERCENT
	if val := os.Getenv("MBCAS_SYSTEM_RESERVE_PERCENT"); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			c.SystemReservePercent = f
			klog.V(2).InfoS("Loaded SystemReservePercent from environment", "value", c.SystemReservePercent)
		}
	}

	// MBCAS_BASELINE_CPU_PER_POD
	if val := os.Getenv("MBCAS_BASELINE_CPU_PER_POD"); val != "" {
		c.BaselineCPUPerPod = val
		klog.V(2).InfoS("Loaded BaselineCPUPerPod from environment", "value", c.BaselineCPUPerPod)
	}

	// MBCAS_STARTUP_GRACE_PERIOD
	if val := os.Getenv("MBCAS_STARTUP_GRACE_PERIOD"); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			c.StartupGracePeriod = d
			klog.V(2).InfoS("Loaded StartupGracePeriod from environment", "value", c.StartupGracePeriod)
		}
	}

	// MBCAS_SLO_TARGET_LATENCY_MS
	if val := os.Getenv("MBCAS_SLO_TARGET_LATENCY_MS"); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			c.SLOTargetLatencyMs = f
			klog.V(2).InfoS("Loaded SLOTargetLatencyMs from environment", "value", c.SLOTargetLatencyMs)
		}
	}

	// MBCAS_PROMETHEUS_URL
	if val := os.Getenv("MBCAS_PROMETHEUS_URL"); val != "" {
		c.PrometheusURL = val
		klog.V(2).InfoS("Loaded PrometheusURL from environment", "value", c.PrometheusURL)
	}

	// MBCAS_FAST_LOOP_INTERVAL
	if val := os.Getenv("MBCAS_FAST_LOOP_INTERVAL"); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			c.FastLoopInterval = d
			klog.V(2).InfoS("Loaded FastLoopInterval from environment", "value", c.FastLoopInterval)
		}
	}

	// MBCAS_SLOW_LOOP_INTERVAL
	if val := os.Getenv("MBCAS_SLOW_LOOP_INTERVAL"); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			c.SlowLoopInterval = d
			klog.V(2).InfoS("Loaded SlowLoopInterval from environment", "value", c.SlowLoopInterval)
		}
	}

	// MBCAS_P99_THRESHOLD_MULTIPLIER
	if val := os.Getenv("MBCAS_P99_THRESHOLD_MULTIPLIER"); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			c.P99ThresholdMultiplier = f
			klog.V(2).InfoS("Loaded P99ThresholdMultiplier from environment", "value", c.P99ThresholdMultiplier)
		}
	}

	// MBCAS_THROTTLING_THRESHOLD
	if val := os.Getenv("MBCAS_THROTTLING_THRESHOLD"); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			c.ThrottlingThreshold = f
			klog.V(2).InfoS("Loaded ThrottlingThreshold from environment", "value", c.ThrottlingThreshold)
		}
	}

	// MBCAS_FAST_STEP_SIZE_MIN
	if val := os.Getenv("MBCAS_FAST_STEP_SIZE_MIN"); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			c.FastStepSizeMin = f
			klog.V(2).InfoS("Loaded FastStepSizeMin from environment", "value", c.FastStepSizeMin)
		}
	}

	// MBCAS_FAST_STEP_SIZE_MAX
	if val := os.Getenv("MBCAS_FAST_STEP_SIZE_MAX"); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			c.FastStepSizeMax = f
			klog.V(2).InfoS("Loaded FastStepSizeMax from environment", "value", c.FastStepSizeMax)
		}
	}

	// MBCAS_ALLOCATION_MECHANISM
	if val := os.Getenv("MBCAS_ALLOCATION_MECHANISM"); val != "" {
		c.AllocationMechanism = val
		klog.V(2).InfoS("Loaded AllocationMechanism from environment", "value", c.AllocationMechanism)
	}

	// MBCAS_ENABLE_KALMAN_PREDICTION
	if val := os.Getenv("MBCAS_ENABLE_KALMAN_PREDICTION"); val != "" {
		if b, err := strconv.ParseBool(val); err == nil {
			c.EnableKalmanPrediction = b
			klog.V(2).InfoS("Loaded EnableKalmanPrediction from environment", "value", c.EnableKalmanPrediction)
		}
	}

	// MBCAS_ENABLE_BATCH_RECONCILIATION
	if val := os.Getenv("MBCAS_ENABLE_BATCH_RECONCILIATION"); val != "" {
		if b, err := strconv.ParseBool(val); err == nil {
			c.EnableBatchReconciliation = b
			klog.V(2).InfoS("Loaded EnableBatchReconciliation from environment", "value", c.EnableBatchReconciliation)
		}
	}

	// MBCAS_COALITION_GROUPING_ANNOTATION
	if val := os.Getenv("MBCAS_COALITION_GROUPING_ANNOTATION"); val != "" {
		c.CoalitionGroupingAnnotation = val
	}

	// MBCAS_ENABLE_PRICE_RESPONSE
	if val := os.Getenv("MBCAS_ENABLE_PRICE_RESPONSE"); val != "" {
		if b, err := strconv.ParseBool(val); err == nil {
			c.EnablePriceResponse = b
			klog.V(2).InfoS("Loaded EnablePriceResponse from environment", "value", c.EnablePriceResponse)
		}
	}

	// MBCAS_ENABLE_AGENT_BASED_MODELING
	if val := os.Getenv("MBCAS_ENABLE_AGENT_BASED_MODELING"); val != "" {
		if b, err := strconv.ParseBool(val); err == nil {
			c.EnableAgentBasedModeling = b
			klog.V(2).InfoS("Loaded EnableAgentBasedModeling from environment", "value", c.EnableAgentBasedModeling)
		}
	}

	// MBCAS_AGENT_LEARNING_RATE
	if val := os.Getenv("MBCAS_AGENT_LEARNING_RATE"); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			c.AgentLearningRate = f
			klog.V(2).InfoS("Loaded AgentLearningRate from environment", "value", c.AgentLearningRate)
		}
	}

	// MBCAS_AGENT_MEMORY_SIZE
	if val := os.Getenv("MBCAS_AGENT_MEMORY_SIZE"); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			c.AgentMemorySize = i
			klog.V(2).InfoS("Loaded AgentMemorySize from environment", "value", c.AgentMemorySize)
		}
	}

	// MBCAS_AGENT_EXPLORATION_RATE
	if val := os.Getenv("MBCAS_AGENT_EXPLORATION_RATE"); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			c.AgentExplorationRate = f
			klog.V(2).InfoS("Loaded AgentExplorationRate from environment", "value", c.AgentExplorationRate)
		}
	}

	// MBCAS_AGENT_DISCOUNT_FACTOR
	if val := os.Getenv("MBCAS_AGENT_DISCOUNT_FACTOR"); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			c.AgentDiscountFactor = f
			klog.V(2).InfoS("Loaded AgentDiscountFactor from environment", "value", c.AgentDiscountFactor)
		}
	}

	// MBCAS_MAX_COALITION_SIZE
	if val := os.Getenv("MBCAS_MAX_COALITION_SIZE"); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			c.MaxCoalitionSize = i
			klog.V(2).InfoS("Loaded MaxCoalitionSize from environment", "value", c.MaxCoalitionSize)
		}
	}

	// MBCAS_MAX_HISTORY_SIZE
	if val := os.Getenv("MBCAS_MAX_HISTORY_SIZE"); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			c.MaxHistorySize = i
			klog.V(2).InfoS("Loaded MaxHistorySize from environment", "value", c.MaxHistorySize)
		}
	}

	// MBCAS_MIN_USAGE_MICROSECONDS
	if val := os.Getenv("MBCAS_MIN_USAGE_MICROSECONDS"); val != "" {
		if i, err := strconv.ParseInt(val, 10, 64); err == nil {
			c.MinUsageMicroseconds = i
			klog.V(2).InfoS("Loaded MinUsageMicroseconds from environment", "value", c.MinUsageMicroseconds)
		}
	}

	// MBCAS_ABSOLUTE_MIN_ALLOCATION
	if val := os.Getenv("MBCAS_ABSOLUTE_MIN_ALLOCATION"); val != "" {
		if i, err := strconv.ParseInt(val, 10, 64); err == nil {
			c.AbsoluteMinAllocation = i
			klog.V(2).InfoS("Loaded AbsoluteMinAllocation from environment", "value", c.AbsoluteMinAllocation)
		}
	}

	// MBCAS_NEED_HEADROOM_FACTOR
	if val := os.Getenv("MBCAS_NEED_HEADROOM_FACTOR"); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			c.NeedHeadroomFactor = f
			klog.V(2).InfoS("Loaded NeedHeadroomFactor from environment", "value", c.NeedHeadroomFactor)
		}
	}

	// MBCAS_WANT_HEADROOM_FACTOR
	if val := os.Getenv("MBCAS_WANT_HEADROOM_FACTOR"); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			c.WantHeadroomFactor = f
			klog.V(2).InfoS("Loaded WantHeadroomFactor from environment", "value", c.WantHeadroomFactor)
		}
	}

	// MBCAS_MAX_DEMAND_MULTIPLIER
	if val := os.Getenv("MBCAS_MAX_DEMAND_MULTIPLIER"); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			c.MaxDemandMultiplier = f
			klog.V(2).InfoS("Loaded MaxDemandMultiplier from environment", "value", c.MaxDemandMultiplier)
		}
	}

	// MBCAS_COST_EFFICIENCY_MODE
	if val := os.Getenv("MBCAS_COST_EFFICIENCY_MODE"); val != "" {
		if b, err := strconv.ParseBool(val); err == nil {
			c.CostEfficiencyMode = b
			klog.V(2).InfoS("Loaded CostEfficiencyMode from environment", "value", c.CostEfficiencyMode)
		}
	}
}

// Validate validates the configuration values.
func (c *AgentConfig) Validate() error {
	if c.SamplingInterval <= 0 {
		return fmt.Errorf("samplingInterval must be > 0, got %v", c.SamplingInterval)
	}
	if c.WriteInterval <= 0 {
		return fmt.Errorf("writeInterval must be > 0, got %v", c.WriteInterval)
	}
	if c.WriteInterval < c.SamplingInterval {
		return fmt.Errorf("writeInterval (%v) must be >= samplingInterval (%v)", c.WriteInterval, c.SamplingInterval)
	}
	if c.MinChangePercent < 0 || c.MinChangePercent > 100 {
		return fmt.Errorf("minChangePercent must be in [0, 100], got %f", c.MinChangePercent)
	}
	if c.SystemReservePercent < 0 || c.SystemReservePercent > 100 {
		return fmt.Errorf("systemReservePercent must be in [0, 100], got %f", c.SystemReservePercent)
	}
	if c.BaselineCPUPerPod == "" {
		return fmt.Errorf("baselineCPUPerPod cannot be empty")
	}
	if c.StartupGracePeriod < 0 {
		return fmt.Errorf("startupGracePeriod must be >= 0, got %v", c.StartupGracePeriod)
	}
	if c.FastLoopInterval <= 0 {
		return fmt.Errorf("fastLoopInterval must be > 0, got %v", c.FastLoopInterval)
	}
	if c.SlowLoopInterval <= 0 {
		return fmt.Errorf("slowLoopInterval must be > 0, got %v", c.SlowLoopInterval)
	}
	if c.SlowLoopInterval < c.FastLoopInterval {
		return fmt.Errorf("slowLoopInterval (%v) must be >= fastLoopInterval (%v)", c.SlowLoopInterval, c.FastLoopInterval)
	}
	if c.P99ThresholdMultiplier <= 0 {
		return fmt.Errorf("p99ThresholdMultiplier must be > 0, got %f", c.P99ThresholdMultiplier)
	}
	if c.ThrottlingThreshold < 0 || c.ThrottlingThreshold > 1 {
		return fmt.Errorf("throttlingThreshold must be in [0, 1], got %f", c.ThrottlingThreshold)
	}
	if c.FastStepSizeMin < 0 || c.FastStepSizeMin > 1 {
		return fmt.Errorf("fastStepSizeMin must be in [0, 1], got %f", c.FastStepSizeMin)
	}
	if c.FastStepSizeMax < 0 || c.FastStepSizeMax > 1 {
		return fmt.Errorf("fastStepSizeMax must be in [0, 1], got %f", c.FastStepSizeMax)
	}
	if c.FastStepSizeMax < c.FastStepSizeMin {
		return fmt.Errorf("fastStepSizeMax (%f) must be >= fastStepSizeMin (%f)", c.FastStepSizeMax, c.FastStepSizeMin)
	}
	if c.AllocationMechanism != "nash" && c.AllocationMechanism != "primal-dual" {
		return fmt.Errorf("allocationMechanism must be 'nash' or 'primal-dual', got %s", c.AllocationMechanism)
	}
	return nil
}

// Log logs the current configuration values.
func (c *AgentConfig) Log() {
	klog.InfoS("Agent configuration",
		"samplingInterval", c.SamplingInterval,
		"writeInterval", c.WriteInterval,
		"minChangePercent", c.MinChangePercent,
		"systemReservePercent", c.SystemReservePercent,
		"baselineCPUPerPod", c.BaselineCPUPerPod,
		"startupGracePeriod", c.StartupGracePeriod,
		"sloTargetLatencyMs", c.SLOTargetLatencyMs,
		"prometheusURL", c.PrometheusURL,
		"fastLoopInterval", c.FastLoopInterval,
		"slowLoopInterval", c.SlowLoopInterval,
		"p99ThresholdMultiplier", c.P99ThresholdMultiplier,
		"throttlingThreshold", c.ThrottlingThreshold,
		"fastStepSizeMin", c.FastStepSizeMin,
		"fastStepSizeMax", c.FastStepSizeMax,
		"allocationMechanism", c.AllocationMechanism,
		"enableKalmanPrediction", c.EnableKalmanPrediction,
		"enablePriceResponse", c.EnablePriceResponse,
		"enableAgentBasedModeling", c.EnableAgentBasedModeling,
		"agentLearningRate", c.AgentLearningRate,
		"agentMemorySize", c.AgentMemorySize,
		"agentExplorationRate", c.AgentExplorationRate,
		"agentDiscountFactor", c.AgentDiscountFactor,
		"enableBatchReconciliation", c.EnableBatchReconciliation,
		"coalitionGroupingAnnotation", c.CoalitionGroupingAnnotation)
}
