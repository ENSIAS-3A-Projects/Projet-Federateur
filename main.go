package main

import (
	"fmt"
	"os"

	"list-k8s-resources/pkg/podtool"
)

// Package main wires the CLI flags/arguments to the podtool package.
// It intentionally stays very small so library code can be reused by a future controller.
func main() {
	if len(os.Args) < 2 {
		podtool.PrintGlobalUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	app, err := podtool.NewApp()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating Kubernetes client: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := podtool.WithTimeout()
	defer cancel()

	switch cmd {
	case "list":
		app.RunList(ctx, os.Args[2:])
	case "usage":
		app.RunUsage(ctx, os.Args[2:])
	case "update":
		app.RunUpdate(ctx, os.Args[2:])
	case "help", "-h", "--help":
		podtool.PrintGlobalUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		podtool.PrintGlobalUsage()
		os.Exit(1)
	}
}
