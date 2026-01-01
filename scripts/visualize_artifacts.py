#!/usr/bin/env python3
"""Visualize MBCAS artifact JSON as simple graphs.

- Reads monitor-run JSON (time-series samples) and optional load-run JSON for markers.
- Plots CPU usage over time for selected pods (top N by peak usage) and desired/applied limits if present.

Usage:
  python visualize_artifacts.py --monitor scripts/artifacts/monitor-*.json \
                                 --load scripts/artifacts/load-run-*.json \
                                 --top 5

Dependencies:
  pip install matplotlib
"""
from __future__ import annotations

import argparse
import glob
import json
import os
from datetime import datetime
from typing import Dict, List, Tuple

import matplotlib.pyplot as plt

ISO_FMT = "%Y-%m-%dT%H:%M:%S"


def parse_iso(ts: str) -> datetime:
    # Strip offset and subseconds for simple parsing
    if "+" in ts:
        ts = ts.split("+")[0]
    if "." in ts:
        ts = ts.split(".")[0]
    return datetime.strptime(ts, ISO_FMT)


def load_json(path: str) -> Dict:
    # Handle possible BOM from Powershell/Windows encodings
    with open(path, "r", encoding="utf-8-sig") as f:
        return json.load(f)


def pick_monitor(path_glob: str) -> Dict:
    paths = sorted(glob.glob(path_glob))
    if not paths:
        raise SystemExit(f"No monitor file matches: {path_glob}")
    return load_json(paths[-1])  # pick latest


def pick_load(path_glob: str) -> Dict:
    paths = sorted(glob.glob(path_glob))
    if not paths:
        return {}
    return load_json(paths[-1])


def gather_series(monitor: Dict, top: int) -> Tuple[List[datetime], Dict[str, List[Tuple[datetime, float]]]]:
    """Return timestamps and per-pod CPU(m) series for top-N pods by peak usage."""
    samples = monitor.get("samples", [])
    # Build per-pod series
    series: Dict[str, List[Tuple[datetime, float]]] = {}
    for s in samples:
        ts_str = s.get("timestamp", "")
        try:
            ts = parse_iso(ts_str)
        except Exception:
            continue
        for p in s.get("pods", []):
            name = f"{p.get('namespace')}/{p.get('name')}"
            usage_str = p.get("cpuUsage", "0").rstrip("m")
            try:
                usage = float(usage_str)
            except Exception:
                usage = 0.0
            series.setdefault(name, []).append((ts, usage))
    # Rank by peak usage
    peaks = [(max((v for _, v in vals), default=0.0), name) for name, vals in series.items()]
    peaks.sort(reverse=True)
    keep = {name for _, name in peaks[:top]}
    filtered = {name: series[name] for name in keep}
    return [parse_iso(s.get("timestamp", "")) for s in samples if s.get("timestamp")], filtered


def plot_series(series: Dict[str, List[Tuple[datetime, float]]], load: Dict, title: str):
    fig, ax = plt.subplots(figsize=(10, 6))
    for name, points in series.items():
        points = sorted(points, key=lambda x: x[0])
        xs = [t for t, _ in points]
        ys = [v for _, v in points]
        ax.plot(xs, ys, label=name)

    # Mark load start if available
    if load.get("events"):
        for ev in load["events"]:
            if ev.get("type") == "load-started":
                try:
                    ts = parse_iso(ev.get("timestamp", ""))
                    ax.axvline(ts, color="red", linestyle="--", alpha=0.6, label="load-start")
                except Exception:
                    pass

    ax.set_title(title)
    ax.set_ylabel("CPU usage (millicores)")
    ax.set_xlabel("Time")
    ax.legend()
    ax.grid(True, linestyle="--", alpha=0.3)
    fig.autofmt_xdate()
    plt.tight_layout()
    plt.show()


def main():
    parser = argparse.ArgumentParser(description="Plot monitor/load artifact JSON")
    parser.add_argument("--monitor", default=os.path.join("scripts", "artifacts", "monitor-*.json"), help="Glob for monitor-run JSON")
    parser.add_argument("--load", default=os.path.join("scripts", "artifacts", "load-run-*.json"), help="Glob for load-run JSON")
    parser.add_argument("--top", type=int, default=5, help="Top N pods by peak CPU to plot")
    args = parser.parse_args()

    monitor = pick_monitor(args.monitor)
    load = pick_load(args.load)

    _, series = gather_series(monitor, top=args.top)
    if not series:
        raise SystemExit("No pod series found to plot.")

    title = f"Monitor {monitor.get('runId','')} (top {args.top})"
    plot_series(series, load, title)


if __name__ == "__main__":
    main()
