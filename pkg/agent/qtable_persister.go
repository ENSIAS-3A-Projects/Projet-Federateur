package agent

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// QTablePersister persists and loads Q-tables for PodAgents
type QTablePersister struct {
	client    client.Client
	namespace string
	nodeName  string
}

// NewQTablePersister creates a new Q-table persister
func NewQTablePersister(config *rest.Config, nodeName string) (*QTablePersister, error) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	c, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("create controller-runtime client: %w", err)
	}
	return &QTablePersister{
		client:    c,
		namespace: "mbcas-system",
		nodeName:  nodeName,
	}, nil
}

// Save saves Q-tables for all agents to a ConfigMap
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

// Load loads a Q-table for a specific agent
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
