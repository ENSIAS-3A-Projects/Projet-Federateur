package podtool

// Package podtool contains the human-facing CLI commands:
// - "list"  : list non-system pods
// - "usage" : show CPU/memory requests & limits for a pod
// - "update": call the actuator library to perform in-place scaling
//
// This package is the current "CLI shell" around the lower-level libraries.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

// App holds shared Kubernetes clients and configuration.
type App struct {
	Clientset *kubernetes.Clientset
}

// NewApp builds a new App using kubeconfig (or in-cluster config).
func NewApp() (*App, error) {
	config, err := buildConfig()
	if err != nil {
		return nil, fmt.Errorf("build kube config: %w", err)
	}
	cs, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create clientset: %w", err)
	}
	return &App{Clientset: cs}, nil
}

// WithTimeout returns a context with a sensible default timeout for commands.
func WithTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

// buildConfig prefers KUBECONFIG, then ~/.kube/config, then in-cluster config.
func buildConfig() (*rest.Config, error) {
	var kubeconfigPath string
	if env := os.Getenv("KUBECONFIG"); env != "" {
		kubeconfigPath = env
	} else if home := homedir.HomeDir(); home != "" {
		kubeconfigPath = filepath.Join(home, ".kube", "config")
	}

	if kubeconfigPath != "" {
		if _, err := os.Stat(kubeconfigPath); err == nil {
			if cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath); err == nil {
				return cfg, nil
			}
		}
	}

	return rest.InClusterConfig()
}

// PrintGlobalUsage prints CLI help.
func PrintGlobalUsage() {
	fmt.Println("k8s-pod-tool: manage Kubernetes pod resources")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  k8s-pod-tool <command> [flags]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  list      List user pods (non-system namespaces)")
	fmt.Println("  usage     Show CPU/memory requests & limits for a pod")
	fmt.Println("  update    Change CPU/memory requests & limits for a pod (pod/resize) and show before/after")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  k8s-pod-tool list")
	fmt.Println("  k8s-pod-tool list -namespace default")
	fmt.Println("  k8s-pod-tool usage -namespace default -pod user-service-abc-123")
	fmt.Println("  k8s-pod-tool update -namespace default -pod user-service-abc-123 -container user-service -cpu 250m -memory 512Mi")
}



