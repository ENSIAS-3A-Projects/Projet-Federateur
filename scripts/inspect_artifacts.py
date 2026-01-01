#!/usr/bin/env python3
"""Inspect MBCAS demo artifacts (load-run + monitor-run JSON files).

Reads all *.json under scripts/artifacts (or a custom directory), prints
summaries for load runs and monitor runs, and aligns them on timestamps.

Usage:
  python inspect_artifacts.py                 # use default ./artifacts
  python inspect_artifacts.py --dir /path/to/artifacts

This script assumes files follow the shapes emitted by 04/05 demo scripts.
"""
from __future__ import annotations

import argparse
import json
import os
from dataclasses import dataclass, field
from datetime import datetime
from typing import Any, Dict, List, Optional

ISO_FMT = "%Y-%m-%dT%H:%M:%S"


def parse_iso(ts: str) -> datetime:
    # Drop offset for simple ordering; we only need relative order.
    # Example: 2026-01-01T14:40:36.7415259+01:00
    if "+" in ts:
        ts = ts.split("+")[0]
    if "." in ts:
        ts = ts.split(".")[0]
    return datetime.strptime(ts, ISO_FMT)


@dataclass
class LoadRun:
    run_id: str
    started: datetime
    namespace: str
    service: str
    duration: int
    intensity: int
    background: bool
    events: List[Dict[str, Any]] = field(default_factory=list)


@dataclass
class MonitorRun:
    run_id: str
    started: datetime
    namespace: str
    service: str
    interval: int
    sample_count: int
    samples: List[Dict[str, Any]]


def load_json(path: str) -> Dict[str, Any]:
    with open(path, "r", encoding="utf-8") as f:
        return json.load(f)


def load_artifacts(folder: str):
    load_runs: List[LoadRun] = []
    monitor_runs: List[MonitorRun] = []

    for name in os.listdir(folder):
        if not name.endswith(".json"):
            continue
        path = os.path.join(folder, name)
        data = load_json(path)
        kind = data.get("kind", "").lower()
        if kind == "load-run":
            load_runs.append(
                LoadRun(
                    run_id=data.get("runId", ""),
                    started=parse_iso(data.get("started", "")),
                    namespace=data.get("namespace", ""),
                    service=data.get("service", ""),
                    duration=int(data.get("durationSeconds", 0)),
                    intensity=int(data.get("intensity", 0)),
                    background=bool(data.get("background", False)),
                    events=data.get("events", []),
                )
            )
        elif kind == "monitor-run":
            samples = data.get("samples", [])
            monitor_runs.append(
                MonitorRun(
                    run_id=data.get("runId", ""),
                    started=parse_iso(data.get("started", "")),
                    namespace=data.get("namespace", ""),
                    service=data.get("service", ""),
                    interval=int(data.get("intervalSeconds", 0)),
                    sample_count=len(samples),
                    samples=samples,
                )
            )
    return load_runs, monitor_runs


def fmt_dt(dt: datetime) -> str:
    return dt.strftime("%Y-%m-%d %H:%M:%S")


def print_load_run(run: LoadRun):
    print(f"Load run {run.run_id}")
    print(f"  started   : {fmt_dt(run.started)}")
    print(f"  ns/service: {run.namespace}/{run.service}")
    print(f"  duration  : {run.duration}s, intensity {run.intensity}, background={run.background}")
    if run.events:
        print(f"  events    : {len(run.events)}")
        for ev in run.events:
            ts = ev.get("timestamp", "")
            typ = ev.get("type", "")
            data = ev.get("data", {})
            print(f"    - {typ} @ {ts} {data}")
    print()


def print_monitor_run(run: MonitorRun):
    print(f"Monitor run {run.run_id}")
    print(f"  started   : {fmt_dt(run.started)}")
    print(f"  ns/service: {run.namespace}/{run.service}")
    print(f"  interval  : {run.interval}s, samples={run.sample_count}")

    if not run.samples:
        print()
        return

    # Show first and last sample summary
    first = run.samples[0]
    last = run.samples[-1]
    print(f"  first sample: iter {first.get('iteration')} @ {first.get('timestamp')}")
    print(f"  last  sample: iter {last.get('iteration')} @ {last.get('timestamp')}")

    # Compute top pods by CPU from last sample
    pods = last.get("pods", [])
    def to_milli(v: str) -> int:
        try:
            return int(v.rstrip("m"))
        except Exception:
            return 0
    top = sorted(pods, key=lambda p: to_milli(p.get("cpuUsage", "0")), reverse=True)[:5]
    if top:
        print("  top CPU (last sample):")
        for p in top:
            print(f"    - {p.get('namespace')}/{p.get('name')}: usage={p.get('cpuUsage')} desired={p.get('desired')} phase={p.get('phase')}")
    print()


def align_runs(load_runs: List[LoadRun], monitor_runs: List[MonitorRun]):
    # Greedy alignment: pick monitor run that started closest before load run.
    for lr in load_runs:
        candidates = [mr for mr in monitor_runs if mr.started <= lr.started]
        if not candidates:
            continue
        best = max(candidates, key=lambda mr: mr.started)
        print(f"Load run {lr.run_id} aligns with monitor run {best.run_id} (monitor started {fmt_dt(best.started)})")

        # Show monitor samples around load start (±1 interval window)
        window = []
        for s in best.samples:
            ts = s.get("timestamp", "")
            try:
                ts_dt = parse_iso(ts)
            except Exception:
                continue
            delta = (ts_dt - lr.started).total_seconds()
            window.append((abs(delta), delta, s))
        window = sorted(window, key=lambda x: x[0])[:3]
        if window:
            print("  nearest monitor samples to load start:")
            for _, delta, s in window:
                sign = "+" if delta >= 0 else "-"
                print(f"    - iter {s.get('iteration')} @ {s.get('timestamp')} (delta {sign}{abs(int(delta))}s)")
        print()


def main():
    parser = argparse.ArgumentParser(description="Inspect MBCAS artifact JSON files")
    parser.add_argument("--dir", default=os.path.join(os.path.dirname(__file__), "artifacts"), help="Directory containing artifact JSON files")
    args = parser.parse_args()

    folder = args.dir
    if not os.path.isdir(folder):
        raise SystemExit(f"Artifacts dir not found: {folder}")

    load_runs, monitor_runs = load_artifacts(folder)

    if not load_runs and not monitor_runs:
        print("No artifact JSON files found.")
        return

    if load_runs:
        print("=== Load runs ===")
        for lr in load_runs:
            print_load_run(lr)
    if monitor_runs:
        print("=== Monitor runs ===")
        for mr in monitor_runs:
            print_monitor_run(mr)

    if load_runs and monitor_runs:
        print("=== Alignment ===")
        align_runs(load_runs, monitor_runs)


if __name__ == "__main__":
    main()
