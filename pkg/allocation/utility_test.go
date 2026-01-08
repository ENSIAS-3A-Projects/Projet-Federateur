package allocation

import (
	"math"
	"testing"
)

func TestUtilityParams_SLOScore_NoSLO(t *testing.T) {
	p := UtilityParams{TargetLatencyMs: 0}
	if p.SLOScore() != 1.0 {
		t.Errorf("Expected 1.0 for no SLO, got %f", p.SLOScore())
	}
}

func TestUtilityParams_SLOScore_AtTarget(t *testing.T) {
	p := UtilityParams{
		TargetLatencyMs:  100,
		CurrentLatencyMs: 100,
		Sensitivity:      0.1,
	}
	score := p.SLOScore()
	// At target, sigmoid should return 0.5
	if math.Abs(score-0.5) > 0.01 {
		t.Errorf("Expected ~0.5 at target, got %f", score)
	}
}

func TestUtilityParams_SLOScore_BelowTarget(t *testing.T) {
	p := UtilityParams{
		TargetLatencyMs:  100,
		CurrentLatencyMs: 50, // Half of target
		Sensitivity:      0.1,
	}
	score := p.SLOScore()
	if score < 0.5 {
		t.Errorf("Expected score > 0.5 below target, got %f", score)
	}
}

func TestUtilityParams_SurplusCPU(t *testing.T) {
	p := UtilityParams{AllocCPU: 300, BaselineCPU: 100}
	if p.SurplusCPU() != 200 {
		t.Errorf("Expected surplus 200, got %d", p.SurplusCPU())
	}
}

func TestUtilityParams_SurplusCPU_BelowBaseline(t *testing.T) {
	p := UtilityParams{AllocCPU: 50, BaselineCPU: 100}
	if p.SurplusCPU() != 0 {
		t.Errorf("Expected surplus 0 below baseline, got %d", p.SurplusCPU())
	}
}

func TestUtilityParams_LogSurplusCPU_Positive(t *testing.T) {
	p := UtilityParams{AllocCPU: 300, BaselineCPU: 100}
	log := p.LogSurplusCPU()
	expected := math.Log(200)
	if math.Abs(log-expected) > 0.001 {
		t.Errorf("Expected log(200)=%f, got %f", expected, log)
	}
}

func TestUtilityParams_LogSurplusCPU_AtBaseline(t *testing.T) {
	p := UtilityParams{AllocCPU: 100, BaselineCPU: 100}
	log := p.LogSurplusCPU()
	if !math.IsInf(log, -1) {
		t.Errorf("Expected -Inf at baseline, got %f", log)
	}
}

func TestNewUtilityParamsFromPodParams(t *testing.T) {
	podParams := PodParams{
		Weight:   2.0,
		MinMilli: 100,
		MaxMilli: 1000,
	}
	util := NewUtilityParamsFromPodParams(podParams, 50, 40, 0.5, 0.1)

	if util.SLOWeight != 2.0 {
		t.Errorf("Expected SLOWeight 2.0, got %f", util.SLOWeight)
	}
	if util.BaselineCPU != 100 {
		t.Errorf("Expected BaselineCPU 100, got %d", util.BaselineCPU)
	}
}
