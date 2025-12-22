package podtool

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// RunUpdate implements the `update` subcommand using the pod/resize subresource.
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

	// 1. Check if at least one resource is provided
	if *newCPU == "" && *newMem == "" {
		fmt.Println("No resource changes requested.")
		return
	}

	// 2. Fetch Pod to validate existence and get default container name
	pod, err := a.Clientset.CoreV1().Pods(*namespace).Get(ctx, *podName, metav1.GetOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching pod: %v\n", err)
		os.Exit(1)
	}

	targetContainer := *containerName
	if targetContainer == "" {
		if len(pod.Spec.Containers) == 0 {
			fmt.Fprintln(os.Stderr, "Error: Pod has no containers")
			os.Exit(1)
		}
		targetContainer = pod.Spec.Containers[0].Name
	}

	fmt.Println("Current pod resource spec:")
	printPodResources(pod)

	if *dryRun {
		fmt.Println("\nDry-run mode enabled: no changes will be applied.")
		return
	}

	// 3. Construct the Patch Object for the "resize" subresource
	// Matches: kubectl patch pod <name> --subresource resize --patch '{...}'
	patchObj := map[string]interface{}{
		"spec": map[string]interface{}{
			"containers": []map[string]interface{}{
				{
					"name": targetContainer,
					"resources": map[string]interface{}{
						"limits":   map[string]interface{}{},
						"requests": map[string]interface{}{},
					},
				},
			},
		},
	}

	// Helper to set values
	setResources := func(resType, val string) {
		if val == "" {
			return
		}
		spec := patchObj["spec"].(map[string]interface{})
		containers := spec["containers"].([]map[string]interface{})
		resources := containers[0]["resources"].(map[string]interface{})
		limits := resources["limits"].(map[string]interface{})
		requests := resources["requests"].(map[string]interface{})

		limits[resType] = val
		requests[resType] = val
	}

	setResources("cpu", *newCPU)
	setResources("memory", *newMem)

	patchData, err := json.Marshal(patchObj)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error building patch: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Applying in-place update (resize subresource) to Pod %s/%s...\n", *namespace, *podName)

	// 4. Send the Patch to the pod/resize subresource
	_, err = a.Clientset.CoreV1().Pods(*namespace).Patch(
		ctx,
		*podName,
		types.StrategicMergePatchType,
		patchData,
		metav1.PatchOptions{},
		"resize",
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n[!] Update Failed: %v\n", err)
		fmt.Println("\nPossible Causes:")
		fmt.Println("1. The feature gate 'InPlacePodVerticalScaling' is disabled on this cluster.")
		fmt.Println("2. You are using a Kubernetes version older than 1.27.")
		fmt.Println("3. Your Container Runtime is too old.")
		os.Exit(1)
	}

	fmt.Println("Update request accepted by API Server.")

	// 5. Verify
	updated, err := a.Clientset.CoreV1().Pods(*namespace).Get(ctx, *podName, metav1.GetOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error verifying pod: %v\n", err)
		return
	}

	fmt.Println("\nNew Pod Spec:")
	printPodResources(updated)

	fmt.Println("\nResize Status (from status.resize):", updated.Status.Resize)
}


