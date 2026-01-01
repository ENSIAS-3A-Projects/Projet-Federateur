package main

import (
	"flag"
	"fmt"
	"os"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"mbcas/pkg/agent"
)

func main() {
	fmt.Println("================================================================================")
	fmt.Println("  MBCAS - Market-Based CPU Allocation System")
	fmt.Println("  WARNING: This is a demonstration system. Do not use in production.")
	fmt.Println("================================================================================")
	fmt.Println()

	// FIXED: Initialize klog flags BEFORE flag.Parse() so -v flag works
	klog.InitFlags(nil)
	// Default to verbose (v=5) so demand sampling shows in logs unless explicitly overridden
	if vFlag := flag.Lookup("v"); vFlag != nil {
		_ = vFlag.Value.Set("5")
	}

	var nodeName string
	flag.StringVar(&nodeName, "node-name", "", "Name of the node this agent runs on (required)")
	flag.Parse()

	if nodeName == "" {
		// Try to get from environment (set by DaemonSet)
		nodeName = os.Getenv("NODE_NAME")
		if nodeName == "" {
			klog.Fatal("--node-name or NODE_NAME environment variable is required")
		}
	}

	// Get Kubernetes config (in-cluster)
	config, err := rest.InClusterConfig()
	if err != nil {
		klog.Fatalf("Failed to get in-cluster config: %v (agent must run in-cluster)", err)
	}

	// Create clients
	k8sClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		klog.Fatalf("Failed to create Kubernetes client: %v", err)
	}

	// Create agent
	agent, err := agent.NewAgent(k8sClient, config, nodeName)
	if err != nil {
		klog.Fatalf("Failed to create agent: %v", err)
	}

	// Run agent
	klog.InfoS("Starting node agent", "node", nodeName, "verbosity", klog.V(5).Enabled())
	if err := agent.Run(); err != nil {
		klog.Fatalf("Agent error: %v", err)
	}
}
