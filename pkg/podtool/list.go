package podtool

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PodSummary is a lightweight view of a pod for listing.
type PodSummary struct {
	Namespace string
	Name      string
	Phase     corev1.PodPhase
	NodeName  string
}

// RunList implements the `list` subcommand.
func (a *App) RunList(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	namespace := fs.String("namespace", "", "Namespace to list pods from (empty = all non-system namespaces)")
	_ = fs.Parse(args)

	pods, err := a.listPods(ctx, *namespace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing pods: %v\n", err)
		os.Exit(1)
	}

	if len(pods) == 0 {
		fmt.Println("No user pods found.")
		return
	}

	fmt.Println("User pods currently running in the cluster:")
	for _, p := range pods {
		fmt.Printf("- ns=%s name=%s phase=%s node=%s\n",
			p.Namespace, p.Name, p.Phase, p.NodeName)
	}
}

func (a *App) listPods(ctx context.Context, namespace string) ([]PodSummary, error) {
	ignoredNamespaces := map[string]struct{}{
		"kube-system":        {},
		"kube-public":        {},
		"kube-node-lease":    {},
		"local-path-storage": {},
	}

	var pods *corev1.PodList
	var err error

	if namespace != "" {
		pods, err = a.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	} else {
		pods, err = a.Clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	}
	if err != nil {
		return nil, err
	}

	var summaries []PodSummary
	for _, p := range pods.Items {
		if _, ignored := ignoredNamespaces[p.Namespace]; ignored {
			continue
		}
		summaries = append(summaries, PodSummary{
			Namespace: p.Namespace,
			Name:      p.Name,
			Phase:     p.Status.Phase,
			NodeName:  p.Spec.NodeName,
		})
	}

	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].Namespace != summaries[j].Namespace {
			return summaries[i].Namespace < summaries[j].Namespace
		}
		return summaries[i].Name < summaries[j].Name
	})

	return summaries, nil
}
