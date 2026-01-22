package agent

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	allocationv1alpha1 "mbcas/api/v1alpha1"
)

func TestAgent_HysteresisSuppressesSmallChanges(t *testing.T) {
	config := DefaultConfig()
	config.MinChangePercent = 5.0

	scheme := runtime.NewScheme()
	_ = allocationv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	writer := &Writer{client: fakeClient}

	agent := &Agent{
		config:            config,
		lastAllocations:   make(map[types.UID]int64),
		smoothedAllocations: make(map[types.UID]int64),
		lastWriteTime:     make(map[types.UID]time.Time),
		writer:            writer,
		ctx:               context.Background(),
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID: types.UID("pod-1"),
		},
	}

	agent.lastAllocations[types.UID("pod-1")] = 1000

	results := map[types.UID]int64{
		types.UID("pod-1"): 1020, // 2% change, below threshold
	}

	// First call with small change - should be suppressed
	agent.apply([]*corev1.Pod{pod}, results, 0.0)

	// Check that allocation wasn't updated (hysteresis suppressed it)
	if agent.lastAllocations[types.UID("pod-1")] != 1000 {
		t.Error("Should not update allocation when change is below MinChangePercent")
	}

	results[types.UID("pod-1")] = 1100 // 10% change, above threshold

	// Second call with larger change - should be applied
	agent.apply([]*corev1.Pod{pod}, results, 0.0)

	if agent.lastAllocations[types.UID("pod-1")] != 1100 {
		t.Error("Should update allocation when change exceeds MinChangePercent")
	}
}
