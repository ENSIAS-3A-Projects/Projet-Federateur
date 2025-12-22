package podtool

import (
	"context"
	"flag"
	"fmt"
	"os"

	"list-k8s-resources/pkg/actuator"
)

// RunUpdate implements the `update` subcommand using the pod/resize subresource.
// It is a thin CLI wrapper around the Phase 1 actuator library.
func (a *App) RunUpdate(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	namespace := fs.String("namespace", "default", "Pod namespace")
	podName := fs.String("pod", "", "Pod name (required)")
	containerName := fs.String("container", "", "Container name (optional, defaults to first container)")
	newCPU := fs.String("cpu", "", "New CPU value (e.g. 250m, 1)")
	newMem := fs.String("memory", "", "New memory value (e.g. 512Mi)")
	dryRun := fs.Bool("dry-run", false, "Show planned changes without applying them")
	_ = fs.Parse(args)

	if *podName == "" {
		fmt.Fprintln(os.Stderr, "Error: -pod is required")
		fs.Usage()
		os.Exit(1)
	}

	if *newCPU == "" && *newMem == "" {
		fmt.Println("No resource changes requested.")
		return
	}

	// Phase 1: feature-gate style check for pod/resize support.
	if err := actuator.CheckResizeSupport(ctx, a.Clientset); err != nil {
		fmt.Fprintf(os.Stderr, "[!] Cluster is missing pod/resize support: %v\n", err)
		fmt.Println("\nHint: start Minikube with InPlacePodVerticalScaling feature gate enabled (see README.md).")
		os.Exit(1)
	}

	before, after, err := actuator.ApplyScaling(
		ctx,
		a.Clientset,
		*namespace,
		*podName,
		*containerName,
		*newCPU,
		*newMem,
		actuator.Options{
			DryRun:     *dryRun,
			MaxRetries: 3,
		},
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n[!] Update Failed: %v\n", err)
		fmt.Println("\nPossible Causes:")
		fmt.Println("1. The feature gate 'InPlacePodVerticalScaling' is disabled on this cluster.")
		fmt.Println("2. You are using a Kubernetes version older than 1.27.")
		fmt.Println("3. Your Container Runtime is too old.")
		os.Exit(1)
	}

	fmt.Println("Current pod resource spec:")
	printPodResources(before)

	if *dryRun {
		fmt.Println("\nDry-run mode enabled: no changes were applied.")
		return
	}

	fmt.Println("Update request accepted by API Server.")

	fmt.Println("\nNew Pod Spec:")
	printPodResources(after)

	fmt.Println("\nResize Status (from status.resize):", after.Status.Resize)
}

