#!/usr/bin/env python3
"""
VPA Idle Pod Metrics Tracker (Fixed & Hardened)
"""

import json
import subprocess
import time
import sys
import argparse
from datetime import datetime, timezone
from typing import Dict, List


# ===============================
# Colors
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
                timeout: int = 30):

    cmd = ["kubectl"] + args

    try:
        return subprocess.run(
            cmd,
            input=input_data,
            capture_output=True,
            text=True,
            timeout=timeout
        )

    except subprocess.TimeoutExpired:
        print(f"{Colors.RED}kubectl timeout: {' '.join(cmd)}{Colors.NC}")
        sys.exit(1)

    except Exception as e:
        print(f"{Colors.RED}kubectl error: {e}{Colors.NC}")
        sys.exit(1)


# ===============================
# Checks
# ===============================

def check_cluster():

    r = run_kubectl(["get", "nodes"])

    if r.returncode != 0:
        print(r.stderr)
        sys.exit(1)


def wait_for_metrics(timeout=180):

    print("Waiting for metrics-server...")

    start = time.time()

    while time.time() - start < timeout:

        r = run_kubectl(["top", "nodes"])

        if r.returncode == 0:
            print("✓ metrics ready")
            return

        time.sleep(5)

    print("metrics-server timeout")
    sys.exit(1)


def check_vpa():

    r = run_kubectl([
        "get", "crd",
        "verticalpodautoscalers.autoscaling.k8s.io"
    ])

    if r.returncode != 0:
        print("VPA not installed")
        sys.exit(1)


# ===============================
# Utils
# ===============================

def parse_cpu(v):

    if not v or v == "N/A":
        return 0

    v = v.strip()

    if v.endswith("m"):
        return int(v[:-1])

    try:
        return int(float(v) * 1000)
    except:
        return 0


# ===============================
# Resources
# ===============================

def create_namespace(ns):

    print(f"Creating namespace {ns}")

    gen = run_kubectl([
        "create", "namespace", ns,
        "--dry-run=client", "-o", "yaml"
    ])

    if gen.returncode != 0:
        print(gen.stderr)
        sys.exit(1)

    ap = run_kubectl(
        ["apply", "-f", "-"],
        input_data=gen.stdout
    )

    if ap.returncode != 0:
        print(ap.stderr)
        sys.exit(1)


def create_deployment(ns, name, req, lim):

    print(f"Creating deployment {name}")

    manifest = f"""
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {name}
  namespace: {ns}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: idle-vpa
  template:
    metadata:
      labels:
        app: idle-vpa
    spec:
      containers:
      - name: idle
        image: busybox
        command: ["/bin/sh","-c","while true; do sleep 3600; done"]
        resources:
          requests:
            cpu: "{req}"
          limits:
            cpu: "{lim}"
"""

    r = run_kubectl(
        ["apply", "-f", "-"],
        input_data=manifest
    )

    if r.returncode != 0:
        print(r.stderr)
        sys.exit(1)

    # Wait pod

    for _ in range(30):

        p = run_kubectl([
            "get", "pods", "-n", ns,
            "-l", "app=idle-vpa",
            "-o", "jsonpath={.items[0].metadata.name}"
        ])

        if p.stdout:
            pod = p.stdout.strip()
            break

        time.sleep(2)

    else:
        print("Pod not created")
        sys.exit(1)

    w = run_kubectl([
        "wait", "--for=condition=Ready",
        f"pod/{pod}", "-n", ns,
        "--timeout=90s"
    ])

    if w.returncode != 0:
        print(w.stderr)
        sys.exit(1)

    return pod


def create_vpa(ns, name, mode):

    print("Creating VPA")

    manifest = f"""
apiVersion: autoscaling.k8s.io/v1
kind: VerticalPodAutoscaler
metadata:
  name: {name}-vpa
  namespace: {ns}
spec:
  targetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: {name}
  updatePolicy:
    updateMode: "{mode}"
  resourcePolicy:
    containerPolicies:
    - containerName: idle
      controlledResources:
        - cpu
      minAllowed:
        cpu: "100m"
      maxAllowed:
        cpu: "2000m"

"""

    r = run_kubectl(
        ["apply", "-f", "-"],
        input_data=manifest
    )

    if r.returncode != 0:
        print(r.stderr)
        sys.exit(1)


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
        "top", "pod", pod, "-n", ns,
        "--no-headers"
    ])

    if r.returncode == 0 and r.stdout:
        return r.stdout.split()[1]

    return "N/A"


def get_vpa(ns, name):

    base = {
        "target": "N/A",
        "lower": "N/A",
        "upper": "N/A",
        "mode": "N/A"
    }

    r = run_kubectl([
        "get", "vpa", name, "-n", ns
    ])

    if r.returncode != 0:
        return base

    def q(path):

        v = run_kubectl([
            "get", "vpa", name, "-n", ns,
            "-o", f"jsonpath={path}"
        ]).stdout.strip()

        return v or "N/A"

    base["mode"] = q("{.spec.updatePolicy.updateMode}")
    base["target"] = q("{.status.recommendation.containerRecommendations[0].target.cpu}")
    base["lower"] = q("{.status.recommendation.containerRecommendations[0].lowerBound.cpu}")
    base["upper"] = q("{.status.recommendation.containerRecommendations[0].upperBound.cpu}")

    return base

def wait_for_vpa(ns, name, timeout=300):

    print("Waiting for VPA recommendation...")

    start = time.time()

    while time.time() - start < timeout:

        r = run_kubectl([
            "get", "vpa", name, "-n", ns,
            "-o", "jsonpath={.status.recommendation}"
        ])

        if r.stdout and r.stdout != "{}":
            print("✓ VPA recommendation ready")
            return

        time.sleep(10)

    print("VPA recommendation timeout")

# ===============================
# Sampling
# ===============================

def sample(ns, pod, vpa, t):

    req, lim = get_resources(ns, pod)
    use = get_usage(ns, pod)
    rec = get_vpa(ns, vpa)

    return {
        "time": t,

        "req": req,
        "lim": lim,
        "use": use,

        "req_m": parse_cpu(req),
        "lim_m": parse_cpu(lim),
        "use_m": parse_cpu(use),

        "vpa_target": rec["target"],
        "vpa_lower": rec["lower"],
        "vpa_upper": rec["upper"],
        "mode": rec["mode"]
    }


# ===============================
# Main
# ===============================

def main():

    p = argparse.ArgumentParser()

    p.add_argument("--ns", default="vpa-test")
    p.add_argument("--name", default="idle-vpa")
    p.add_argument("--duration", type=int, default=300)
    p.add_argument("--interval", type=int, default=5)
    p.add_argument("--req", default="500m")
    p.add_argument("--lim", default="1000m")
    p.add_argument("--mode", default="InPlaceOrRecreate")
    p.add_argument("--no-cleanup", action="store_true")

    a = p.parse_args()

    out = f"vpa_metrics_{datetime.now().strftime('%Y%m%d_%H%M%S')}.json"


    print("VPA Idle Tracker")
    print("=" * 40)


    check_cluster()
    wait_for_metrics()
    check_vpa()


    create_namespace(a.ns)

    pod = create_deployment(
        a.ns, a.name, a.req, a.lim
    )

    create_vpa(a.ns, a.name, a.mode)

    vpa_name = f"{a.name}-vpa"
    wait_for_vpa(a.ns, vpa_name)


    data = {
        "start": datetime.now(timezone.utc).isoformat(),
        "samples": []
    }


    print("\nCollecting...\n")


    steps = a.duration // a.interval


    try:

        for i in range(steps):

            t = (i + 1) * a.interval

            s = sample(a.ns, pod, vpa_name, t)

            data["samples"].append(s)

            print(s)

            if i < steps - 1:
                time.sleep(a.interval)


    finally:

        data["end"] = datetime.now(timezone.utc).isoformat()

        with open(out, "w") as f:
            json.dump(data, f, indent=2)

        print("Saved:", out)

        if not a.no_cleanup:

            run_kubectl(["delete", "vpa", vpa_name, "-n", a.ns])
            run_kubectl(["delete", "deployment", a.name, "-n", a.ns])
            run_kubectl(["delete", "namespace", a.ns])


# ===============================

if __name__ == "__main__":
    main()
