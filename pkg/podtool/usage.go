package podtool

import (
	"context"
	"flag"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RunUsage implements the `usage` subcommand.
func (a *App) RunUsage(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("usage", flag.ExitOnError)
	namespace := fs.String("namespace", "default", "Pod namespace")
	podName := fs.String("pod", "", "Pod name (required)")
	_ = fs.Parse(args)

	if *podName == "" {
		fmt.Fprintln(os.Stderr, "Error: -pod is required")
		fs.Usage()
		os.Exit(1)
	}

	pod, err := a.Clientset.CoreV1().Pods(*namespace).Get(ctx, *podName, metav1.GetOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching pod: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Pod resource usage (spec) for ns=%s name=%s\n", pod.Namespace, pod.Name)
	printPodResources(pod)
}

// printPodResources prints CPU/memory requests and limits for all containers in the pod.
func printPodResources(pod *corev1.Pod) {
	for _, c := range pod.Spec.Containers {
		reqs := c.Resources.Requests
		lims := c.Resources.Limits

		reqCPU, reqMem := reqs.Cpu(), reqs.Memory()
		limCPU, limMem := lims.Cpu(), lims.Memory()

		fmt.Printf("  container=%s\n", c.Name)
		fmt.Printf("    request.cpu=%s  request.memory=%s\n", quantityOrDash(reqCPU), quantityOrDash(reqMem))
		fmt.Printf("    limit.cpu=%s    limit.memory=%s\n", quantityOrDash(limCPU), quantityOrDash(limMem))
	}
}

func quantityOrDash(q *resource.Quantity) string {
	if q == nil {
		return "-"
	}
	return q.String()
}
