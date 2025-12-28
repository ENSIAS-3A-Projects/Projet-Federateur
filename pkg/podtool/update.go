package podtool

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"mbcas/pkg/actuator"
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
	policyStr := fs.String("policy", "both", "Resize policy: both, limits, or requests")
	wait := fs.Bool("wait", false, "Wait for resize to complete before returning")
	waitTimeoutStr := fs.String("wait-timeout", "30s", "Maximum time to wait for resize completion (e.g. 30s, 1m)")
	pollIntervalStr := fs.String("poll", "500ms", "Poll interval when waiting for resize (e.g. 500ms, 1s)")
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

	// Parse policy
	policy := actuator.ResizePolicy(*policyStr)
	if policy != actuator.PolicyBoth && policy != actuator.PolicyLimits && policy != actuator.PolicyRequests {
		fmt.Fprintf(os.Stderr, "Error: invalid policy %q. Must be one of: both, limits, requests\n", *policyStr)
		os.Exit(1)
	}

	// Parse wait timeout and poll interval
	var waitTimeout time.Duration
	var pollInterval time.Duration
	if *wait {
		var err error
		waitTimeout, err = time.ParseDuration(*waitTimeoutStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid wait-timeout %q: %v\n", *waitTimeoutStr, err)
			os.Exit(1)
		}
		pollInterval, err = time.ParseDuration(*pollIntervalStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid poll interval %q: %v\n", *pollIntervalStr, err)
			os.Exit(1)
		}
	}

	// Phase 1: feature-gate style check for pod/resize support.
	if err := actuator.CheckResizeSupport(ctx, a.Clientset); err != nil {
		fmt.Fprintf(os.Stderr, "[!] Cluster is missing pod/resize support: %v\n", err)
		fmt.Println("\nHint: start Minikube with InPlacePodVerticalScaling feature gate enabled (see README.md).")
		os.Exit(1)
	}

	opts := actuator.Options{
		DryRun:      *dryRun,
		MaxRetries:  3,
		Policy:      policy,
		Wait:        *wait,
		WaitTimeout: waitTimeout,
		PollInterval: pollInterval,
	}

	// For dry-run, use PlanScaling to show the planned patch
	if *dryRun {
		before, patch, err := actuator.PlanScaling(
			ctx,
			a.Clientset,
			*namespace,
			*podName,
			*containerName,
			*newCPU,
			*newMem,
			opts,
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\n[!] Plan Failed: %v\n", err)
			os.Exit(1)
		}

		fmt.Println("Current pod resource spec:")
		printPodResources(before)
		fmt.Println("\nPlanned patch:")
		fmt.Println(string(patch))
		fmt.Println("\nDry-run mode enabled: no changes were applied.")
		return
	}

	before, after, err := actuator.ApplyScaling(
		ctx,
		a.Clientset,
		*namespace,
		*podName,
		*containerName,
		*newCPU,
		*newMem,
		opts,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n[!] Update Failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Current pod resource spec:")
	printPodResources(before)

	fmt.Println("Update request accepted by API Server.")
	if *wait {
		fmt.Println("Waiting for resize to complete...")
	}

	fmt.Println("\nNew Pod Spec:")
	printPodResources(after)

	fmt.Println("\nResize Status (from status.resize):", after.Status.Resize)
}

