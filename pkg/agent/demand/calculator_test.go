package demand

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestParamsForPod_WithRequestsAndLimits(t *testing.T) {
	calc := NewCalculator()
	
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID: "test-pod-uid",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("500m"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("2000m"),
						},
					},
				},
			},
		},
	}

	params := calc.ParamsForPod(pod, 0.5, 100, 4000)

	// Check weight (should be max(1, requestCPU_milli) = max(1, 500) = 500)
	if params.Weight != 500.0 {
		t.Errorf("Expected weight 500.0, got %.2f", params.Weight)
	}

	// Check bid (weight × demand = 500 × 0.5 = 250)
	if params.Bid != 250.0 {
		t.Errorf("Expected bid 250.0, got %.2f", params.Bid)
	}

	// Check min (should be max(baselineMilli, requestCPU_milli) = max(100, 500) = 500)
	if params.MinMilli != 500 {
		t.Errorf("Expected minMilli 500, got %d", params.MinMilli)
	}

	// Check max (should be limitCPU_milli = 2000)
	if params.MaxMilli != 2000 {
		t.Errorf("Expected maxMilli 2000, got %d", params.MaxMilli)
	}

	// Check demand (should be clamped)
	if params.Demand != 0.5 {
		t.Errorf("Expected demand 0.5, got %.2f", params.Demand)
	}
}

func TestParamsForPod_NoRequests(t *testing.T) {
	calc := NewCalculator()
	
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Resources: corev1.ResourceRequirements{
						// No requests
						Limits: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("1000m"),
						},
					},
				},
			},
		},
	}

	params := calc.ParamsForPod(pod, 0.3, 100, 4000)

	// Weight should default to 1.0 (max(1, 0) = 1)
	if params.Weight != 1.0 {
		t.Errorf("Expected weight 1.0, got %.2f", params.Weight)
	}

	// Bid should be 1.0 × 0.3 = 0.3
	if params.Bid != 0.3 {
		t.Errorf("Expected bid 0.3, got %.2f", params.Bid)
	}

	// Min should be baselineMilli (100) since no request
	if params.MinMilli != 100 {
		t.Errorf("Expected minMilli 100, got %d", params.MinMilli)
	}

	// Max should be limit (1000)
	if params.MaxMilli != 1000 {
		t.Errorf("Expected maxMilli 1000, got %d", params.MaxMilli)
	}
}

func TestParamsForPod_NoLimits(t *testing.T) {
	calc := NewCalculator()
	
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("200m"),
						},
						// No limits
					},
				},
			},
		},
	}

	params := calc.ParamsForPod(pod, 0.6, 100, 4000)

	// Max should fall back to nodeCapMilli (4000), but capped at 90% = 3600
	expectedMax := int64(4000 * 0.9)
	if params.MaxMilli != expectedMax {
		t.Errorf("Expected maxMilli %d (90%% of nodeCap), got %d", expectedMax, params.MaxMilli)
	}
}

func TestParamsForPod_DemandClamping(t *testing.T) {
	calc := NewCalculator()
	
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("100m"),
						},
					},
				},
			},
		},
	}

	// Test negative demand
	params := calc.ParamsForPod(pod, -0.5, 100, 4000)
	if params.Demand != 0.0 {
		t.Errorf("Expected demand clamped to 0.0, got %.2f", params.Demand)
	}

	// Test demand > 1
	params = calc.ParamsForPod(pod, 1.5, 100, 4000)
	if params.Demand != 1.0 {
		t.Errorf("Expected demand clamped to 1.0, got %.2f", params.Demand)
	}

	// Test valid demand
	params = calc.ParamsForPod(pod, 0.7, 100, 4000)
	if params.Demand != 0.7 {
		t.Errorf("Expected demand 0.7, got %.2f", params.Demand)
	}
}

func TestParamsForPod_NoContainers(t *testing.T) {
	calc := NewCalculator()
	
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{},
		},
	}

	params := calc.ParamsForPod(pod, 0.5, 100, 4000)

	// Should use defaults
	if params.Weight != 1.0 {
		t.Errorf("Expected default weight 1.0, got %.2f", params.Weight)
	}
	if params.Bid != 1.0*0.5 {
		t.Errorf("Expected bid 0.5, got %.2f", params.Bid)
	}
	if params.MinMilli != 100 {
		t.Errorf("Expected minMilli 100, got %d", params.MinMilli)
	}
	if params.MaxMilli != 4000 {
		t.Errorf("Expected maxMilli 4000, got %d", params.MaxMilli)
	}
}

func TestParamsForPod_MinMaxSanityCheck(t *testing.T) {
	calc := NewCalculator()
	
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("500m"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("300m"), // Limit < request (invalid, but we handle it)
						},
					},
				},
			},
		},
	}

	params := calc.ParamsForPod(pod, 0.5, 100, 4000)

	// Max should be adjusted to >= min
	if params.MaxMilli < params.MinMilli {
		t.Errorf("MaxMilli %d should be >= MinMilli %d", params.MaxMilli, params.MinMilli)
	}
}

func TestParamsForPod_PerPodMaxCap(t *testing.T) {
	calc := NewCalculator()
	
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("100m"),
						},
						// No limits
					},
				},
			},
		},
	}

	nodeCapMilli := int64(10000) // 10 CPUs
	params := calc.ParamsForPod(pod, 0.5, 100, nodeCapMilli)

	// Max should be capped at 90% of nodeCap = 9000
	expectedMax := int64(float64(nodeCapMilli) * 0.9)
	if params.MaxMilli != expectedMax {
		t.Errorf("Expected maxMilli %d (90%% of nodeCap), got %d", expectedMax, params.MaxMilli)
	}
}

func TestParamsForPod_WeightComputation(t *testing.T) {
	calc := NewCalculator()
	
	testCases := []struct {
		name          string
		requestCPU    string
		expectedWeight float64
	}{
		{
			name:          "Request 500m",
			requestCPU:    "500m",
			expectedWeight: 500.0,
		},
		{
			name:          "Request 1",
			requestCPU:    "1",
			expectedWeight: 1000.0, // 1 CPU = 1000m
		},
		{
			name:          "No request",
			requestCPU:    "",
			expectedWeight: 1.0, // Default to 1
		},
		{
			name:          "Request 50m (less than 1)",
			requestCPU:    "50m",
			expectedWeight: 50.0, // max(1, 50) = 50 (50m = 50 millicores)
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			pod := &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Resources: corev1.ResourceRequirements{},
						},
					},
				},
			}

			if tc.requestCPU != "" {
				pod.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse(tc.requestCPU),
				}
			}

			params := calc.ParamsForPod(pod, 0.5, 100, 4000)
			if params.Weight != tc.expectedWeight {
				t.Errorf("Expected weight %.2f, got %.2f", tc.expectedWeight, params.Weight)
			}
		})
	}
}

