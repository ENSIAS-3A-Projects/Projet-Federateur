#!/usr/bin/env python3
"""
Visualize MBCAS Idle Pod Metrics
Creates plots showing resource allocation, usage, and reduction over time
"""

import json
import sys
import argparse
from typing import Dict, List

try:
    import matplotlib.pyplot as plt
    import matplotlib.dates as mdates
    from datetime import datetime
except ImportError:
    print("Error: matplotlib is required for visualization")
    print("Install with: pip install matplotlib")
    sys.exit(1)

def load_data(filename: str) -> Dict:
    """Load metrics data from JSON file"""
    try:
        with open(filename, 'r') as f:
            return json.load(f)
    except FileNotFoundError:
        print(f"Error: File not found: {filename}")
        sys.exit(1)
    except json.JSONDecodeError:
        print(f"Error: Invalid JSON in {filename}")
        sys.exit(1)

def plot_resource_allocation(data: Dict, output_file: str = None):
    """Create plots showing resource allocation over time"""
    samples = data['samples']
    if not samples:
        print("No samples to plot")
        return
    
    # Extract time series data
    elapsed = [s['elapsed_seconds'] for s in samples]
    request_milli = [s['request_milli'] for s in samples]
    limit_milli = [s['limit_milli'] for s in samples]
    usage_milli = [s['usage_milli'] for s in samples]
    
    # Create figure with subplots
    fig, (ax1, ax2, ax3) = plt.subplots(3, 1, figsize=(12, 10))
    fig.suptitle(f"MBCAS Resource Allocation: {data['pod_name']}", fontsize=16, fontweight='bold')
    
    # Plot 1: Resource Allocation Over Time
    ax1.plot(elapsed, limit_milli, 'b-', linewidth=2, label='Pod Limit (actual)', marker='o', markersize=3)
    ax1.plot(elapsed, request_milli, 'g--', linewidth=2, label='Pod Request (actual)', marker='s', markersize=3)
    ax1.plot(elapsed, usage_milli, 'r:', linewidth=2, label='CPU Usage', marker='^', markersize=3)
    
    ax1.set_xlabel('Time (seconds)', fontsize=12)
    ax1.set_ylabel('CPU (millicores)', fontsize=12)
    ax1.set_title('CPU Allocation and Usage Over Time', fontsize=14, fontweight='bold')
    ax1.legend(loc='upper right', fontsize=10)
    ax1.grid(True, alpha=0.3)
    
    # Add initial allocation line
    initial_limit = samples[0]['limit_milli']
    ax1.axhline(y=initial_limit, color='gray', linestyle='--', alpha=0.5, label=f'Initial Limit ({initial_limit}m)')
    
    # Plot 2: Allocation Changes (step chart)
    ax2.step(elapsed, limit_milli, 'b-', linewidth=2, where='post', label='Limit Changes')
    ax2.fill_between(elapsed, 0, limit_milli, step='post', alpha=0.3)
    
    ax2.set_xlabel('Time (seconds)', fontsize=12)
    ax2.set_ylabel('CPU Limit (millicores)', fontsize=12)
    ax2.set_title('MBCAS Allocation Adjustments', fontsize=14, fontweight='bold')
    ax2.grid(True, alpha=0.3)
    
    # Calculate and annotate allocation changes
    changes = []
    for i in range(1, len(samples)):
        if samples[i]['limit_milli'] != samples[i-1]['limit_milli']:
            changes.append({
                'time': samples[i]['elapsed_seconds'],
                'from': samples[i-1]['limit_milli'],
                'to': samples[i]['limit_milli']
            })
    
    # Annotate first and last change
    if changes:
        first = changes[0]
        ax2.annotate(f"First change\n{first['from']}→{first['to']}m",
                    xy=(first['time'], first['to']),
                    xytext=(first['time'] + 20, first['to'] + 50),
                    arrowprops=dict(arrowstyle='->', color='red', lw=1.5),
                    fontsize=9, color='red')
        
        if len(changes) > 1:
            last = changes[-1]
            ax2.annotate(f"Last change\n{last['from']}→{last['to']}m",
                        xy=(last['time'], last['to']),
                        xytext=(last['time'] - 50, last['to'] + 50),
                        arrowprops=dict(arrowstyle='->', color='blue', lw=1.5),
                        fontsize=9, color='blue')
    
    # Plot 3: Resource Efficiency
    efficiency = []
    for s in samples:
        if s['limit_milli'] > 0:
            eff = (s['usage_milli'] / s['limit_milli']) * 100
            efficiency.append(eff)
        else:
            efficiency.append(0)
    
    ax3.plot(elapsed, efficiency, 'g-', linewidth=2, marker='o', markersize=3)
    ax3.fill_between(elapsed, 0, efficiency, alpha=0.3, color='green')
    
    ax3.set_xlabel('Time (seconds)', fontsize=12)
    ax3.set_ylabel('Utilization (%)', fontsize=12)
    ax3.set_title('CPU Utilization (Usage / Limit)', fontsize=14, fontweight='bold')
    ax3.grid(True, alpha=0.3)
    ax3.set_ylim(0, max(efficiency) * 1.2 if efficiency else 100)
    
    # Add target efficiency line (e.g., 80%)
    ax3.axhline(y=80, color='orange', linestyle='--', alpha=0.5, label='Target (80%)')
    ax3.legend(loc='upper right', fontsize=10)
    
    plt.tight_layout()
    
    # Save or show
    if output_file:
        plt.savefig(output_file, dpi=300, bbox_inches='tight')
        print(f"Plot saved to: {output_file}")
    else:
        plt.show()

def print_statistics(data: Dict):
    """Print detailed statistics"""
    samples = data['samples']
    if not samples:
        return
    
    print("\n" + "="*60)
    print("DETAILED STATISTICS")
    print("="*60)
    
    # Basic info
    print(f"\nTest Information:")
    print(f"  Pod: {data['pod_name']}")
    print(f"  Namespace: {data['namespace']}")
    print(f"  Duration: {data['duration_seconds']}s")
    print(f"  Samples: {len(samples)}")
    
    # Initial vs Final
    initial = samples[0]
    final = samples[-1]
    
    print(f"\nInitial State:")
    print(f"  Request: {initial['pod_request']} ({initial['request_milli']}m)")
    print(f"  Limit: {initial['pod_limit']} ({initial['limit_milli']}m)")
    
    print(f"\nFinal State:")
    print(f"  Request: {final['pod_request']} ({final['request_milli']}m)")
    print(f"  Limit: {final['pod_limit']} ({final['limit_milli']}m)")
    print(f"  Usage: {final['pod_usage']} ({final['usage_milli']}m)")
    
    # Reduction metrics
    if initial['limit_milli'] > 0:
        reduction_abs = initial['limit_milli'] - final['limit_milli']
        reduction_pct = (reduction_abs / initial['limit_milli']) * 100
        
        print(f"\nResource Reduction:")
        print(f"  Absolute: {reduction_abs}m")
        print(f"  Percentage: {reduction_pct:.1f}%")
    
    # Allocation changes
    changes = []
    for i in range(1, len(samples)):
        if samples[i]['limit_milli'] != samples[i-1]['limit_milli']:
            changes.append({
                'time': samples[i]['elapsed_seconds'],
                'from': samples[i-1]['limit_milli'],
                'to': samples[i]['limit_milli'],
                'delta': samples[i]['limit_milli'] - samples[i-1]['limit_milli']
            })
    
    print(f"\nAllocation Changes:")
    print(f"  Total Changes: {len(changes)}")
    
    if changes:
        print(f"  First Change: {changes[0]['time']}s ({changes[0]['from']}m → {changes[0]['to']}m)")
        print(f"  Last Change: {changes[-1]['time']}s ({changes[-1]['from']}m → {changes[-1]['to']}m)")
        
        print(f"\n  All Changes:")
        for i, change in enumerate(changes, 1):
            delta_sign = '+' if change['delta'] > 0 else ''
            print(f"    {i}. t={change['time']:3d}s: {change['from']:4d}m → {change['to']:4d}m ({delta_sign}{change['delta']}m)")
    
    # Usage statistics
    usage_samples = [s['usage_milli'] for s in samples if s['usage_milli'] > 0]
    if usage_samples:
        avg_usage = sum(usage_samples) / len(usage_samples)
        max_usage = max(usage_samples)
        min_usage = min(usage_samples)
        
        print(f"\nUsage Statistics:")
        print(f"  Average: {avg_usage:.1f}m")
        print(f"  Maximum: {max_usage}m")
        print(f"  Minimum: {min_usage}m")
    
    # Efficiency statistics
    efficiency_samples = []
    for s in samples:
        if s['limit_milli'] > 0 and s['usage_milli'] > 0:
            eff = (s['usage_milli'] / s['limit_milli']) * 100
            efficiency_samples.append(eff)
    
    if efficiency_samples:
        avg_eff = sum(efficiency_samples) / len(efficiency_samples)
        max_eff = max(efficiency_samples)
        min_eff = min(efficiency_samples)
        
        print(f"\nUtilization Statistics:")
        print(f"  Average: {avg_eff:.1f}%")
        print(f"  Maximum: {max_eff:.1f}%")
        print(f"  Minimum: {min_eff:.1f}%")
    
    # PodAllocation status
    podalloc_statuses = [s['podallocation_status'] for s in samples]
    unique_statuses = set(podalloc_statuses)
    
    print(f"\nPodAllocation Status:")
    for status in unique_statuses:
        count = podalloc_statuses.count(status)
        pct = (count / len(samples)) * 100
        print(f"  {status}: {count} samples ({pct:.1f}%)")

def main():
    parser = argparse.ArgumentParser(description='Visualize MBCAS idle pod metrics')
    parser.add_argument('input_file', help='Input JSON file with metrics data')
    parser.add_argument('--output', '-o', help='Output PNG file for plot (if not specified, shows interactive plot)')
    parser.add_argument('--stats-only', action='store_true', help='Only print statistics, no plot')
    
    args = parser.parse_args()
    
    # Load data
    data = load_data(args.input_file)
    
    # Print statistics
    print_statistics(data)
    
    # Create plot unless stats-only
    if not args.stats_only:
        output_file = args.output
        if not output_file and args.input_file.endswith('.json'):
            output_file = args.input_file.replace('.json', '_plot.png')
        
        plot_resource_allocation(data, output_file)

if __name__ == '__main__':
    main()
