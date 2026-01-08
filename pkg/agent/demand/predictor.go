package demand

import (
	"sync"

	"k8s.io/apimachinery/pkg/types"
)

// Predictor implements a Kalman filter for demand prediction.
// State vector: [demand, velocity] (2D)
// Process model: constant velocity with noise
// Measurement: raw throttling ratio
type Predictor struct {
	mu    sync.RWMutex
	state map[types.UID]*kalmanState
}

type kalmanState struct {
	// State vector: [demand, velocity]
	demand  float64
	velocity float64
	
	// Covariance matrix P (2x2, stored as [P00, P01, P10, P11])
	// P00 = variance of demand
	// P01 = P10 = covariance(demand, velocity)
	// P11 = variance of velocity
	P00, P01, P11 float64
	
	// Process noise covariance Q (assumed diagonal)
	Q00, Q11 float64
	
	// Measurement noise variance R
	R float64
}

// NewPredictor creates a new Kalman filter predictor.
func NewPredictor() *Predictor {
	return &Predictor{
		state: make(map[types.UID]*kalmanState),
	}
}

// PredictNext predicts the next demand value for a pod.
// Returns the predicted demand (state[0]) after processing the measurement.
func (p *Predictor) PredictNext(uid types.UID, measurement float64) float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	
	state, exists := p.state[uid]
	if !exists {
		// Initialize state: demand = measurement, velocity = 0
		state = &kalmanState{
			demand:   measurement,
			velocity: 0.0,
			P00:      1.0, // High initial uncertainty
			P01:      0.0,
			P11:      1.0,
			Q00:      0.01, // Process noise: small for demand
			Q11:      0.1,  // Process noise: larger for velocity
			R:        0.1,  // Measurement noise
		}
		p.state[uid] = state
	}
	
	// Predict step (time update)
	// x_k|k-1 = F * x_k-1|k-1
	// P_k|k-1 = F * P_k-1|k-1 * F^T + Q
	// F = [1, dt; 0, 1] where dt = 1 (one time step)
	// For constant velocity model: demand_new = demand_old + velocity, velocity_new = velocity_old
	predictedDemand := state.demand + state.velocity
	predictedVelocity := state.velocity
	
	// Update covariance: P = F*P*F^T + Q
	// F*P*F^T = [P00 + 2*P01 + P11, P01 + P11; P01 + P11, P11]
	newP00 := state.P00 + 2*state.P01 + state.P11 + state.Q00
	newP01 := state.P01 + state.P11
	newP11 := state.P11 + state.Q11
	
	// Update step (measurement update)
	// K = P * H^T * (H * P * H^T + R)^-1
	// H = [1, 0] (we only measure demand, not velocity)
	// K = [P00/(P00+R), P01/(P00+R)]
	denom := newP00 + state.R
	if denom < 1e-10 {
		denom = 1e-10 // Avoid division by zero
	}
	K0 := newP00 / denom
	K1 := newP01 / denom
	
	// x_k|k = x_k|k-1 + K * (z - H * x_k|k-1)
	// z = measurement, H * x = predictedDemand
	innovation := measurement - predictedDemand
	state.demand = predictedDemand + K0*innovation
	state.velocity = predictedVelocity + K1*innovation
	
	// P_k|k = (I - K*H) * P_k|k-1
	// I - K*H = [1-K0, 0; -K1, 1]
	state.P00 = (1 - K0) * newP00
	state.P01 = (1 - K0) * newP01
	state.P11 = newP11 - K1*newP01
	
	// Clamp to valid ranges
	if state.demand < 0 {
		state.demand = 0
	}
	if state.demand > 1 {
		state.demand = 1
	}
	
	// Predict next value (one step ahead)
	nextDemand := state.demand + state.velocity
	if nextDemand < 0 {
		nextDemand = 0
	}
	if nextDemand > 1 {
		nextDemand = 1
	}
	
	return nextDemand
}

// GetState returns the current state for a pod (for debugging).
func (p *Predictor) GetState(uid types.UID) (demand, velocity float64, ok bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	
	state, exists := p.state[uid]
	if !exists {
		return 0, 0, false
	}
	return state.demand, state.velocity, true
}

// Reset clears the state for a pod.
func (p *Predictor) Reset(uid types.UID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.state, uid)
}

// ResetAll clears all state.
func (p *Predictor) ResetAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state = make(map[types.UID]*kalmanState)
}

