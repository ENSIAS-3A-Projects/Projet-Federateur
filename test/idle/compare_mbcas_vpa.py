#!/usr/bin/env python3
"""
Compare MBCAS vs VPA Performance
Analyzes and visualizes side-by-side comparison of both systems
"""

import json
import sys
import argparse
from typing import Dict, List

try:
    import matplotlib.pyplot as plt
    import matplotlib.gridspec as gridspec
    from matplotlib.patches import Rectangle
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

def compare_systems(mbcas_file: str, vpa_file: str, output_file: str = None):
    """Create side-by-side comparison plots"""
    
    # Load data
    print("Loading data...")
    mbcas_data = load_data(mbcas_file)
    vpa_data = load_data(vpa_file)
    
    mbcas_samples = mbcas_data['samples']
    vpa_samples = vpa_data['samples']
    
    # Extract MBCAS time series
    mbcas_elapsed = [s['elapsed'] for s in mbcas_samples]
    mbcas_limit = [s['limit_m'] for s in mbcas_samples]
    mbcas_usage = [s['usage_m'] for s in mbcas_samples]
    
    # Extract VPA time series
    vpa_elapsed = [s['elapsed_seconds'] for s in vpa_samples]
    vpa_limit = [s['limit_milli'] for s in vpa_samples]
    vpa_usage = [s['usage_milli'] for s in vpa_samples]
    vpa_target = [s['vpa_target_milli'] for s in vpa_samples]
    
    # Create comprehensive comparison figure
    fig = plt.figure(figsize=(16, 12))
    gs = gridspec.GridSpec(3, 2, figure=fig, hspace=0.3, wspace=0.3)
    
    # Title
    fig.suptitle('MBCAS vs VPA: Idle Pod Resource Allocation Comparison', 
                 fontsize=18, fontweight='bold', y=0.98)
    
    # Plot 1: MBCAS allocation over time
    ax1 = fig.add_subplot(gs[0, 0])
    ax1.plot(mbcas_elapsed, mbcas_limit, 'b-', linewidth=2.5, marker='o', 
             markersize=3, label='MBCAS Limit', alpha=0.8)
    ax1.plot(mbcas_elapsed, mbcas_usage, 'r:', linewidth=2, marker='^', 
             markersize=3, label='Usage', alpha=0.6)
    ax1.set_xlabel('Time (seconds)', fontsize=11)
    ax1.set_ylabel('CPU (millicores)', fontsize=11)
    ax1.set_title('MBCAS: Resource Allocation', fontsize=12, fontweight='bold')
    ax1.legend(loc='upper right', fontsize=9)
    ax1.grid(True, alpha=0.3)
    ax1.set_xlim(0, max(mbcas_elapsed))
    
    # Annotate MBCAS convergence
    mbcas_final = mbcas_limit[-1]
    mbcas_initial = mbcas_limit[0]
    reduction_pct = ((mbcas_initial - mbcas_final) / mbcas_initial * 100) if mbcas_initial > 0 else 0
    
    ax1.text(0.98, 0.98, f'Reduction: {reduction_pct:.0f}%\nFinal: {mbcas_final}m',
             transform=ax1.transAxes, fontsize=9, verticalalignment='top',
             horizontalalignment='right', bbox=dict(boxstyle='round', facecolor='wheat', alpha=0.5))
    
    # Plot 2: VPA allocation over time
    ax2 = fig.add_subplot(gs[0, 1])
    ax2.plot(vpa_elapsed, vpa_limit, 'g-', linewidth=2.5, marker='o', 
             markersize=3, label='VPA Actual Limit', alpha=0.8)
    ax2.plot(vpa_elapsed, vpa_target, 'm--', linewidth=2, marker='s', 
             markersize=3, label='VPA Target', alpha=0.6)
    ax2.plot(vpa_elapsed, vpa_usage, 'r:', linewidth=2, marker='^', 
             markersize=3, label='Usage', alpha=0.6)
    ax2.set_xlabel('Time (seconds)', fontsize=11)
    ax2.set_ylabel('CPU (millicores)', fontsize=11)
    ax2.set_title('VPA: Resource Allocation', fontsize=12, fontweight='bold')
    ax2.legend(loc='upper right', fontsize=9)
    ax2.grid(True, alpha=0.3)
    ax2.set_xlim(0, max(vpa_elapsed))
    
    # Annotate VPA status
    vpa_final = vpa_limit[-1]
    vpa_initial = vpa_limit[0]
    vpa_reduction_pct = ((vpa_initial - vpa_final) / vpa_initial * 100) if vpa_initial > 0 else 0
    
    vpa_target_final = vpa_target[-1] if vpa_target[-1] > 0 else 'N/A'
    target_text = f'{vpa_target_final}m' if isinstance(vpa_target_final, int) else vpa_target_final
    
    ax2.text(0.98, 0.98, f'Reduction: {vpa_reduction_pct:.0f}%\nFinal: {vpa_final}m\nTarget: {target_text}',
             transform=ax2.transAxes, fontsize=9, verticalalignment='top',
             horizontalalignment='right', bbox=dict(boxstyle='round', facecolor='lightgreen', alpha=0.5))
    
    # Plot 3: Direct comparison on same scale
    ax3 = fig.add_subplot(gs[1, :])
    ax3.plot(mbcas_elapsed, mbcas_limit, 'b-', linewidth=2.5, label='MBCAS', marker='o', markersize=3)
    ax3.plot(vpa_elapsed, vpa_limit, 'g-', linewidth=2.5, label='VPA', marker='s', markersize=3)
    
    # Mark allocation changes for both
    for i in range(1, len(mbcas_samples)):
        if mbcas_samples[i]['limit_m'] != mbcas_samples[i-1]['limit_m']:
            ax3.axvline(x=mbcas_samples[i]['elapsed'], color='blue', alpha=0.2, linewidth=1)
    
    for i in range(1, len(vpa_samples)):
        if vpa_samples[i]['limit_milli'] != vpa_samples[i-1]['limit_milli']:
            ax3.axvline(x=vpa_samples[i]['elapsed_seconds'], color='green', alpha=0.2, linewidth=1)
    
    ax3.set_xlabel('Time (seconds)', fontsize=11)
    ax3.set_ylabel('CPU Limit (millicores)', fontsize=11)
    ax3.set_title('Direct Comparison: MBCAS vs VPA', fontsize=13, fontweight='bold')
    ax3.legend(loc='upper right', fontsize=10)
    ax3.grid(True, alpha=0.3)
    
    # Plot 4: Allocation changes count
    ax4 = fig.add_subplot(gs[2, 0])
    
    mbcas_changes = sum(1 for i in range(1, len(mbcas_samples)) 
                       if mbcas_samples[i]['limit_m'] != mbcas_samples[i-1]['limit_m'])
    vpa_changes = sum(1 for i in range(1, len(vpa_samples)) 
                     if vpa_samples[i]['limit_milli'] != vpa_samples[i-1]['limit_milli'])
    
    systems = ['MBCAS', 'VPA']
    changes = [mbcas_changes, vpa_changes]
    colors = ['blue', 'green']
    
    bars = ax4.bar(systems, changes, color=colors, alpha=0.7, edgecolor='black', linewidth=2)
    ax4.set_ylabel('Number of Allocation Changes', fontsize=11)
    ax4.set_title('Allocation Stability Comparison', fontsize=12, fontweight='bold')
    ax4.grid(True, alpha=0.3, axis='y')
    
    # Add value labels on bars
    for bar, change in zip(bars, changes):
        height = bar.get_height()
        ax4.text(bar.get_x() + bar.get_width()/2., height,
                f'{int(change)}', ha='center', va='bottom', fontsize=12, fontweight='bold')
    
    # Plot 5: Time metrics comparison
    ax5 = fig.add_subplot(gs[2, 1])
    
    # Find time to first change for each system
    mbcas_first_change = None
    for i in range(1, len(mbcas_samples)):
        if mbcas_samples[i]['limit_m'] != mbcas_samples[0]['limit_m']:
            mbcas_first_change = mbcas_samples[i]['elapsed']
            break
    
    vpa_first_change = None
    for i in range(1, len(vpa_samples)):
        if vpa_samples[i]['limit_milli'] != vpa_samples[0]['limit_milli']:
            vpa_first_change = vpa_samples[i]['elapsed_seconds']
            break
    
    # Find time to VPA recommendation
    vpa_first_rec = None
    for sample in vpa_samples:
        if sample['vpa_target_milli'] > 0:
            vpa_first_rec = sample['elapsed_seconds']
            break
    
    # Create grouped bar chart
    metrics = ['Time to First\nChange (s)', 'Final Reduction\n(%)']
    mbcas_values = [mbcas_first_change if mbcas_first_change else 0, reduction_pct]
    vpa_values = [vpa_first_change if vpa_first_change else 0, vpa_reduction_pct]
    
    x = range(len(metrics))
    width = 0.35
    
    bars1 = ax5.bar([i - width/2 for i in x], mbcas_values, width, 
                    label='MBCAS', color='blue', alpha=0.7, edgecolor='black', linewidth=1.5)
    bars2 = ax5.bar([i + width/2 for i in x], vpa_values, width,
                    label='VPA', color='green', alpha=0.7, edgecolor='black', linewidth=1.5)
    
    ax5.set_ylabel('Value', fontsize=11)
    ax5.set_title('Performance Metrics Comparison', fontsize=12, fontweight='bold')
    ax5.set_xticks(x)
    ax5.set_xticklabels(metrics)
    ax5.legend(fontsize=9)
    ax5.grid(True, alpha=0.3, axis='y')
    
    # Add value labels
    for bars in [bars1, bars2]:
        for bar in bars:
            height = bar.get_height()
            if height > 0:
                ax5.text(bar.get_x() + bar.get_width()/2., height,
                        f'{height:.0f}', ha='center', va='bottom', fontsize=9)
    
    # Add statistics summary
    summary_text = f"""
Test Duration: {mbcas_data.get('duration_seconds', 300)}s
    
MBCAS:
• Changes: {mbcas_changes}
• First change: {mbcas_first_change}s
• Reduction: {reduction_pct:.0f}%
• Final: {mbcas_final}m

VPA:
• Changes: {vpa_changes}
• First change: {vpa_first_change if vpa_first_change else 'None'}
• First rec: {vpa_first_rec if vpa_first_rec else 'None'}s
• Reduction: {vpa_reduction_pct:.0f}%
• Final: {vpa_final}m
"""
    
    fig.text(0.99, 0.01, summary_text, fontsize=8, 
             bbox=dict(boxstyle='round', facecolor='wheat', alpha=0.3),
             verticalalignment='bottom', horizontalalignment='right',
             family='monospace')
    
    # Save or show
    if output_file:
        plt.savefig(output_file, dpi=300, bbox_inches='tight')
        print(f"✓ Comparison plot saved to: {output_file}")
    else:
        plt.show()

def print_comparison_stats(mbcas_file: str, vpa_file: str):
    """Print detailed comparison statistics"""
    
    mbcas_data = load_data(mbcas_file)
    vpa_data = load_data(vpa_file)
    
    mbcas_samples = mbcas_data['samples']
    vpa_samples = vpa_data['samples']
    
    print("\n" + "="*80)
    print("MBCAS vs VPA: DETAILED COMPARISON")
    print("="*80)
    
    # Initial vs Final
    mbcas_initial = mbcas_samples[0]['limit_m']
    mbcas_final = mbcas_samples[-1]['limit_m']
    vpa_initial = vpa_samples[0]['limit_milli']
    vpa_final = vpa_samples[-1]['limit_milli']
    
    print("\nInitial Configuration:")
    print(f"  Both systems started with: {mbcas_initial}m limit")
    
    print("\nFinal State:")
    print(f"  MBCAS: {mbcas_final}m")
    print(f"  VPA:   {vpa_final}m")
    
    # Reduction
    mbcas_reduction = ((mbcas_initial - mbcas_final) / mbcas_initial * 100) if mbcas_initial > 0 else 0
    vpa_reduction = ((vpa_initial - vpa_final) / vpa_initial * 100) if vpa_initial > 0 else 0
    
    print("\nReduction:")
    print(f"  MBCAS: {mbcas_initial - mbcas_final}m ({mbcas_reduction:.1f}%)")
    print(f"  VPA:   {vpa_initial - vpa_final}m ({vpa_reduction:.1f}%)")
    
    # Changes
    mbcas_changes = sum(1 for i in range(1, len(mbcas_samples)) 
                       if mbcas_samples[i]['limit_m'] != mbcas_samples[i-1]['limit_m'])
    vpa_changes = sum(1 for i in range(1, len(vpa_samples)) 
                     if vpa_samples[i]['limit_milli'] != vpa_samples[i-1]['limit_milli'])
    
    print("\nAllocation Changes:")
    print(f"  MBCAS: {mbcas_changes} changes")
    print(f"  VPA:   {vpa_changes} changes")
    
    # Time to first change
    mbcas_first = None
    for i in range(1, len(mbcas_samples)):
        if mbcas_samples[i]['limit_m'] != mbcas_samples[0]['limit_m']:
            mbcas_first = mbcas_samples[i]['elapsed']
            break
    
    vpa_first = None
    for i in range(1, len(vpa_samples)):
        if vpa_samples[i]['limit_milli'] != vpa_samples[0]['limit_milli']:
            vpa_first = vpa_samples[i]['elapsed_seconds']
            break
    
    print("\nTime to First Change:")
    print(f"  MBCAS: {mbcas_first}s" if mbcas_first else "  MBCAS: No changes")
    print(f"  VPA:   {vpa_first}s" if vpa_first else "  VPA:   No changes")
    
    if mbcas_first and vpa_first:
        speedup = vpa_first / mbcas_first
        print(f"  MBCAS is {speedup:.1f}x faster to first change")
    
    # VPA recommendation time
    vpa_first_rec = None
    for sample in vpa_samples:
        if sample['vpa_target_milli'] > 0:
            vpa_first_rec = sample['elapsed_seconds']
            break
    
    if vpa_first_rec:
        print(f"\nVPA First Recommendation: {vpa_first_rec}s")
    
    print("\n" + "="*80)

def main():
    parser = argparse.ArgumentParser(description='Compare MBCAS and VPA metrics')
    parser.add_argument('mbcas_file', help='MBCAS metrics JSON file')
    parser.add_argument('vpa_file', help='VPA metrics JSON file')
    parser.add_argument('--output', '-o', help='Output PNG file for comparison plot')
    parser.add_argument('--stats-only', action='store_true', help='Only print statistics, no plot')
    
    args = parser.parse_args()
    
    # Print statistics
    print_comparison_stats(args.mbcas_file, args.vpa_file)
    
    # Create plot unless stats-only
    if not args.stats_only:
        output_file = args.output
        if not output_file:
            output_file = 'mbcas_vs_vpa_comparison.png'
        
        compare_systems(args.mbcas_file, args.vpa_file, output_file)

if __name__ == '__main__':
    main()
