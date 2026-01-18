package demand

import (
	"testing"

	"k8s.io/apimachinery/pkg/types"
)

func TestPredictor_Basic(t *testing.T) {
	p := NewPredictor()
	uid := types.UID("test-pod")

	// First prediction (initialization)
	pred1 := p.PredictNext(uid, 0.5)
	if pred1 < 0 || pred1 > 1 {
		t.Errorf("Prediction should be in [0, 1], got %f", pred1)
	}

	// Second prediction (should update state)
	pred2 := p.PredictNext(uid, 0.6)
	if pred2 < 0 || pred2 > 1 {
		t.Errorf("Prediction should be in [0, 1], got %f", pred2)
	}

	// State should be accessible
	demand, velocity, ok := p.GetState(uid)
	if !ok {
		t.Error("State should be available after predictions")
	}
	if demand < 0 || demand > 1 {
		t.Errorf("Demand state should be in [0, 1], got %f", demand)
	}
	_ = velocity // Velocity can be any value
}

func TestPredictor_Reset(t *testing.T) {
	p := NewPredictor()
	uid := types.UID("test-pod")

	p.PredictNext(uid, 0.5)
	_, _, ok := p.GetState(uid)
	if !ok {
		t.Error("State should exist after prediction")
	}

	p.Reset(uid)
	_, _, ok = p.GetState(uid)
	if ok {
		t.Error("State should not exist after reset")
	}
}

func TestPredictor_Clamping(t *testing.T) {
	p := NewPredictor()
	uid := types.UID("test-pod")

	// Test with extreme values
	pred := p.PredictNext(uid, 2.0) // Above 1.0
	if pred > 1.0 {
		t.Errorf("Prediction should be clamped to 1.0, got %f", pred)
	}

	pred = p.PredictNext(uid, -1.0) // Below 0.0
	if pred < 0.0 {
		t.Errorf("Prediction should be clamped to 0.0, got %f", pred)
	}
}





