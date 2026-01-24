#!/usr/bin/env python3
"""
MBCAS Idle Pod Metrics Tracker (Fixed & Hardened Version)
"""

import json
import subprocess
import time
import sys
import argparse
from datetime import datetime, timezone
from typing import Dict, List


# ===============================
# Terminal Colors
# ===============================

class Colors:
    GREEN = '\033[0;32m'
    BLUE = '\033[0;34m'
    YELLOW = '\033[1;33m'
    RED = '\033[0;31m'
    CYAN = '\033[0;36m'
    BOLD = '\033[1m'
    NC = '\033[0m'


# ===============================
# Kubectl Wrapper
# ===============================

def run_kubectl(args: List[str],
                input_data: str = None,
                timeout: int = 30) -> subprocess.CompletedProcess:
    """
    Safe kubectl runner with timeout and error handling
    """

    cmd = ["kubectl"] + args

    try:
        result = subprocess.run(
            cmd,
            input=input_data,
            capture_output=True,
            text=True,
            timeout=timeout
        )
        return result

    except subprocess.TimeoutExpired:
        print(f"{Colors.RED}kubectl timeout: {' '.join(cmd)}{Colors.NC}")
        sys.exit(1)

    except Exception as e:
        print(f"{Colors.RED}kubectl error: {e}{Colors.NC}")
        sys.exit(1)


# ===============================
# Environment Checks
# ===============================

def check_cluster():
    """Ensure Kubernetes API is reachable"""

    r = run_kubectl(["get", "nodes"])

    if r.returncode != 0:
        print(f"{Colors.RED}Kubernetes cluster not reachable:{Colors.NC}")
        print(r.stderr)
        sys.exit(1)


def check_metrics_server():
    """Verify metrics-server availability"""

    r = run_kubectl(["top", "nodes"])

    if r.returncode != 0:
        print(f"{Colors.RED}metrics-server not working.{Colors.NC}")
        print("Run: minikube addons enable metrics-server")
        sys.exit(1)


# ===============================
# Utils
# ===============================

def parse_cpu_to_milli(cpu: str) -> int:

    if not cpu or cpu == "N/A":
        return 0

    cpu = cpu.strip()

    if cpu.endswith("m"):
        return int(cpu[:-1])

    try:
        return int(float(cpu) * 1000)
    except Exception:
        return 0


# ===============================
# Namespace / Pod Creation
# ===============================

def create_namespace(namespace: str):

    print(f"{Colors.YELLOW}Creating namespace {namespace}...{Colors.NC}")

    gen = run_kubectl([
        "create", "namespace", namespace,
        "--dry-run=client", "-o", "yaml"
    ])

    if gen.returncode != 0:
        print(gen.stderr)
        sys.exit(1)

    apply = run_kubectl(
        ["apply", "-f", "-"],
        input_data=gen.stdout
    )

    if apply.returncode != 0:
        print(apply.stderr)
        sys.exit(1)

    run_kubectl([
        "label", "namespace", namespace,
        "mbcas.io/managed=true",
        "--overwrite"
    ])

    print(f"{Colors.GREEN}✓ Namespace ready{Colors.NC}")


def create_pod(namespace, pod_name, request, limit):

    print(f"{Colors.YELLOW}Creating pod {pod_name}...{Colors.NC}")

    manifest = f"""
apiVersion: v1
kind: Pod
metadata:
  name: {pod_name}
  namespace: {namespace}
  labels:
    mbcas.io/managed: "true"
spec:
  containers:
  - name: idle
    image: busybox
    command: ["/bin/sh","-c","while true; do sleep 3600; done"]
    resources:
      requests:
        cpu: "{request}"
        memory: "64Mi"
      limits:
        cpu: "{limit}"
        memory: "128Mi"
"""

    r = run_kubectl(
        ["apply", "-f", "-"],
        input_data=manifest
    )

    if r.returncode != 0:
        print(r.stderr)
        sys.exit(1)

    print(f"{Colors.GREEN}✓ Pod created{Colors.NC}")

    wait = run_kubectl([
        "wait",
        "--for=condition=Ready",
        f"pod/{pod_name}",
        "-n", namespace,
        "--timeout=90s"
    ])

    if wait.returncode != 0:
        print(wait.stderr)
        sys.exit(1)

    print(f"{Colors.GREEN}✓ Pod ready{Colors.NC}")


# ===============================
# Metrics
# ===============================

def get_resources(ns, pod):

    req = run_kubectl([
        "get", "pod", pod, "-n", ns,
        "-o", "jsonpath={.spec.containers[0].resources.requests.cpu}"
    ]).stdout.strip() or "N/A"

    lim = run_kubectl([
        "get", "pod", pod, "-n", ns,
        "-o", "jsonpath={.spec.containers[0].resources.limits.cpu}"
    ]).stdout.strip() or "N/A"

    return req, lim


def get_usage(ns, pod):

    r = run_kubectl([
        "top", "pod", pod, "-n", ns, "--no-headers"
    ])

    if r.returncode == 0 and r.stdout:
        return r.stdout.split()[1]

    return "N/A"


def get_podallocation(ns, pod):

    base = {
        "request": "N/A",
        "limit": "N/A",
        "status": "N/A",
        "shadow": "N/A"
    }

    chk = run_kubectl([
        "get", "podallocation", pod, "-n", ns
    ])

    if chk.returncode != 0:
        return base

    base["request"] = run_kubectl([
        "get", "podallocation", pod, "-n", ns,
        "-o", "jsonpath={.spec.desiredCPURequest}"
    ]).stdout.strip() or "N/A"

    base["limit"] = run_kubectl([
        "get", "podallocation", pod, "-n", ns,
        "-o", "jsonpath={.spec.desiredCPULimit}"
    ]).stdout.strip() or "N/A"

    base["status"] = run_kubectl([
        "get", "podallocation", pod, "-n", ns,
        "-o", "jsonpath={.status.phase}"
    ]).stdout.strip() or "N/A"

    base["shadow"] = run_kubectl([
        "get", "podallocation", pod, "-n", ns,
        "-o", "jsonpath={.status.shadowPriceCPU}"
    ]).stdout.strip() or "N/A"

    return base


# ===============================
# Sampling
# ===============================

def collect_sample(ns, pod, elapsed):

    req, lim = get_resources(ns, pod)
    use = get_usage(ns, pod)
    pa = get_podallocation(ns, pod)

    return {
        "timestamp": datetime.now(timezone.utc).isoformat(),
        "elapsed": elapsed,

        "request": req,
        "limit": lim,
        "usage": use,

        "request_m": parse_cpu_to_milli(req),
        "limit_m": parse_cpu_to_milli(lim),
        "usage_m": parse_cpu_to_milli(use),

        "pa_request": pa["request"],
        "pa_limit": pa["limit"],
        "pa_status": pa["status"],
        "shadow_price": pa["shadow"]
    }


# ===============================
# Output
# ===============================

def print_header():

    print(f"{Colors.BLUE}Time | Req | Lim | Use | PA-Lim | Shadow | Status{Colors.NC}")
    print("-" * 65)


def print_row(s):

    print(
        f"{s['elapsed']:4d}s | "
        f"{s['request']:>5} | "
        f"{s['limit']:>5} | "
        f"{s['usage']:>5} | "
        f"{s['pa_limit']:>6} | "
        f"{s['shadow_price']:>6} | "
        f"{s['pa_status']}"
    )


# ===============================
# Cleanup
# ===============================

def cleanup(ns, pod):

    print(f"{Colors.YELLOW}Cleaning up...{Colors.NC}")

    run_kubectl(["delete", "pod", pod, "-n", ns])
    run_kubectl(["delete", "namespace", ns])

    print(f"{Colors.GREEN}✓ Cleanup done{Colors.NC}")


# ===============================
# Main
# ===============================

def main():

    parser = argparse.ArgumentParser()

    parser.add_argument("--namespace", default="mbcas-test")
    parser.add_argument("--pod-name", default="idle-overprovisioned")
    parser.add_argument("--duration", type=int, default=300)
    parser.add_argument("--interval", type=int, default=5)
    parser.add_argument("--request", default="500m")
    parser.add_argument("--limit", default="1000m")
    parser.add_argument("--no-cleanup", action="store_true")

    args = parser.parse_args()

    output = f"idle_metrics_{datetime.now().strftime('%Y%m%d_%H%M%S')}.json"


    print(f"{Colors.BOLD}MBCAS Idle Pod Tracker{Colors.NC}")
    print("=" * 50)


    # Checks
    check_cluster()
    check_metrics_server()


    # Setup
    create_namespace(args.namespace)
    create_pod(
        args.namespace,
        args.pod_name,
        args.request,
        args.limit
    )


    data = {
        "start": datetime.now(timezone.utc).isoformat(),
        "namespace": args.namespace,
        "pod": args.pod_name,
        "samples": []
    }


    print("\nCollecting metrics...\n")

    print_header()

    iterations = args.duration // args.interval


    try:

        for i in range(iterations):

            elapsed = (i + 1) * args.interval

            s = collect_sample(
                args.namespace,
                args.pod_name,
                elapsed
            )

            data["samples"].append(s)

            print_row(s)

            if i < iterations - 1:
                time.sleep(args.interval)


    except KeyboardInterrupt:
        print("\nInterrupted by user.")


    finally:

        data["end"] = datetime.now(timezone.utc).isoformat()

        with open(output, "w") as f:
            json.dump(data, f, indent=2)

        print(f"\nSaved: {output}")

        if not args.no_cleanup:
            cleanup(args.namespace, args.pod_name)


# ===============================
# Entry
# ===============================

if __name__ == "__main__":
    main()
