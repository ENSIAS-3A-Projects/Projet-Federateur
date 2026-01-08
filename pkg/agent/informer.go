package agent

import (
	"context"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
)

// PodInformer wraps the Kubernetes pod informer with node-specific filtering.
type PodInformer struct {
	informer cache.SharedInformer
	indexer  cache.Indexer
	mu       sync.RWMutex
	nodeName string
	stopper  chan struct{}
}

// NewPodInformer creates a new pod informer filtered to a specific node.
func NewPodInformer(ctx context.Context, k8sClient kubernetes.Interface, nodeName string) (*PodInformer, error) {
	// Create shared informer factory
	factory := informers.NewSharedInformerFactory(k8sClient, 0)

	// Create pod informer
	podInformer := factory.Core().V1().Pods().Informer()

	// Create indexer for fast lookups
	indexer := podInformer.GetIndexer()

	pi := &PodInformer{
		informer: podInformer,
		indexer:  indexer,
		nodeName: nodeName,
		stopper:  make(chan struct{}),
	}

	// Add event handlers
	podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    pi.onPodAdd,
		UpdateFunc: pi.onPodUpdate,
		DeleteFunc: pi.onPodDelete,
	})

	// Start the informer
	go podInformer.Run(ctx.Done())

	// Wait for cache to sync
	if !cache.WaitForCacheSync(ctx.Done(), podInformer.HasSynced) {
		return nil, fmt.Errorf("failed to sync pod informer cache")
	}

	klog.InfoS("Pod informer started and synced", "node", nodeName)

	return pi, nil
}

// onPodAdd handles pod add events.
func (pi *PodInformer) onPodAdd(obj interface{}) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return
	}

	// Only log pods on this node
	if pod.Spec.NodeName == pi.nodeName {
		klog.V(4).InfoS("Pod added to informer cache",
			"pod", pod.Name,
			"namespace", pod.Namespace,
			"node", pi.nodeName)
	}
}

// onPodUpdate handles pod update events.
func (pi *PodInformer) onPodUpdate(oldObj, newObj interface{}) {
	newPod, ok := newObj.(*corev1.Pod)
	if !ok {
		return
	}

	// Only log pods on this node
	if newPod.Spec.NodeName == pi.nodeName {
		klog.V(5).InfoS("Pod updated in informer cache",
			"pod", newPod.Name,
			"namespace", newPod.Namespace,
			"node", pi.nodeName)
	}
}

// onPodDelete handles pod delete events.
func (pi *PodInformer) onPodDelete(obj interface{}) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		// Handle deleted final state unknown
		deletedState, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			return
		}
		pod, ok = deletedState.Obj.(*corev1.Pod)
		if !ok {
			return
		}
	}

	// Only log pods on this node
	if pod.Spec.NodeName == pi.nodeName {
		klog.V(4).InfoS("Pod deleted from informer cache",
			"pod", pod.Name,
			"namespace", pod.Namespace,
			"node", pi.nodeName)
	}
}

// ListPods returns all pods on this node that match the filter criteria.
// This replaces the direct API call in discoverPods().
func (pi *PodInformer) ListPods() ([]*corev1.Pod, error) {
	pi.mu.RLock()
	defer pi.mu.RUnlock()

	// List all pods from cache
	allPods := pi.indexer.List()

	// Filter to running pods on this node
	var pods []*corev1.Pod
	for _, obj := range allPods {
		pod, ok := obj.(*corev1.Pod)
		if !ok {
			continue
		}

		// Filter by node name
		if pod.Spec.NodeName != pi.nodeName {
			continue
		}

		// Filter to running pods only
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}

		// Skip excluded namespaces
		if ExcludedNamespaces[pod.Namespace] {
			continue
		}

		// Skip pods with explicit opt-out label (EXCEPT BestEffort pods - we want to manage them with lower priority)
		// BestEffort pods should be included in allocation so we can reclaim CPU from them
		if val, ok := pod.Labels[ManagedLabel]; ok && val == "false" {
			// Special case: Include BestEffort pods even if marked unmanaged, but with lower priority
			// This allows MBCAS to reclaim CPU from low-priority pods during contention
			if pod.Status.QOSClass != corev1.PodQOSBestEffort {
				continue
			}
		}

		pods = append(pods, pod)
	}

	return pods, nil
}

// GetPod retrieves a specific pod by namespace and name from the cache.
func (pi *PodInformer) GetPod(namespace, name string) (*corev1.Pod, error) {
	pi.mu.RLock()
	defer pi.mu.RUnlock()

	key := fmt.Sprintf("%s/%s", namespace, name)
	obj, exists, err := pi.indexer.GetByKey(key)
	if err != nil {
		return nil, fmt.Errorf("get pod from cache: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("pod %s not found in cache", key)
	}

	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil, fmt.Errorf("object is not a pod: %T", obj)
	}

	// Verify it's on this node
	if pod.Spec.NodeName != pi.nodeName {
		return nil, fmt.Errorf("pod %s is not on node %s", key, pi.nodeName)
	}

	return pod, nil
}

// HasSynced returns true if the informer cache has synced.
func (pi *PodInformer) HasSynced() bool {
	return pi.informer.HasSynced()
}

// Stop stops the informer (for testing/cleanup).
func (pi *PodInformer) Stop() {
	close(pi.stopper)
}

