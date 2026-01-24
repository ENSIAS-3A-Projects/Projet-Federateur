package agent

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

func TestPodAgent_StartupGracePeriodPreventsDecrease(t *testing.T) {
	config := DefaultConfig()
	config.StartupGracePeriod = 1 * time.Minute

	agent := NewPodAgentWithConfig(types.UID("pod-1"), 0.0, config, time.Now())
	agent.Usage = 200
	agent.Allocation = 500
	agent.Throttling = 0.0

	bid := agent.ComputeBid(config)

	if bid.Min < 500 {
		t.Errorf("During grace period, Min should not decrease below current allocation. Got Min=%d, Allocation=%d", bid.Min, agent.Allocation)
	}
}

func TestPodAgent_AfterGracePeriodAllowsDecrease(t *testing.T) {
	config := DefaultConfig()
	config.StartupGracePeriod = 1 * time.Millisecond

	agent := NewPodAgentWithConfig(types.UID("pod-1"), 0.0, config, time.Now())
	agent.StartTime = time.Now().Add(-1 * time.Second)
	agent.Usage = 200
	agent.Allocation = 500
	agent.Throttling = 0.0

	bid := agent.ComputeBid(config)

	expectedMin := int64(float64(200) * (1.0 + config.NeedHeadroomFactor))
	if bid.Min > expectedMin+50 {
		t.Errorf("After grace period, Min should be based on usage. Got Min=%d, Expected ~%d", bid.Min, expectedMin)
	}
}
