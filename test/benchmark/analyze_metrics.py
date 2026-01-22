#!/usr/bin/env python3
"""
Compare MBCAS vs VPA benchmark metrics
"""
import json
import sys
from pathlib import Path

def load_metrics(filepath):
    with open(filepath, 'r') as f:
        return json.load(f)

def analyze_workload(workload):
    """Extract key metrics from a workload"""
    return {
        'name': workload['name'],
        'type': workload['type'],
        'allocation_changes': workload['allocation_changes'],
        'avg_usage_milli': workload['avg_usage_milli'],
        'max_usage_milli': workload['max_usage_milli'],
        'min_usage_milli': workload['min_usage_milli'],
        'avg_throttling_ratio': workload['avg_throttling_ratio'],
        'max_throttling_ratio': workload['max_throttling_ratio'],
        'throttling_duration_seconds': workload['throttling_duration_seconds'],
        'initial_request_milli': workload['initial_request_milli'],
        'initial_limit_milli': workload['initial_limit_milli'],
        'final_request_milli': workload['final_request_milli'],
        'final_limit_milli': workload['final_limit_milli'],
    }

def calculate_efficiency(workload):
    """Calculate resource efficiency metrics"""
    avg_usage = workload['avg_usage_milli']
    final_request = workload['final_request_milli']
    final_limit = workload['final_limit_milli']
    
    request_utilization = (avg_usage / final_request * 100) if final_request > 0 else 0
    limit_utilization = (avg_usage / final_limit * 100) if final_limit > 0 else 0
    overhead = ((final_limit - avg_usage) / final_limit * 100) if final_limit > 0 else 0
    
    return {
        'request_utilization_pct': request_utilization,
        'limit_utilization_pct': limit_utilization,
        'overhead_pct': overhead,
    }

def compare_workloads(mbcas_wl, vpa_wl):
    """Compare two workloads and return differences"""
    mbcas_eff = calculate_efficiency(mbcas_wl)
    vpa_eff = calculate_efficiency(vpa_wl)
    
    comparison = {
        'name': mbcas_wl['name'],
        'allocation_changes': {
            'mbcas': mbcas_wl['allocation_changes'],
            'vpa': vpa_wl['allocation_changes'],
            'diff': mbcas_wl['allocation_changes'] - vpa_wl['allocation_changes'],
            'diff_pct': ((mbcas_wl['allocation_changes'] - vpa_wl['allocation_changes']) / vpa_wl['allocation_changes'] * 100) if vpa_wl['allocation_changes'] > 0 else 0,
        },
        'avg_usage': {
            'mbcas': mbcas_wl['avg_usage_milli'],
            'vpa': vpa_wl['avg_usage_milli'],
            'diff': mbcas_wl['avg_usage_milli'] - vpa_wl['avg_usage_milli'],
            'diff_pct': ((mbcas_wl['avg_usage_milli'] - vpa_wl['avg_usage_milli']) / vpa_wl['avg_usage_milli'] * 100) if vpa_wl['avg_usage_milli'] > 0 else 0,
        },
        'throttling': {
            'mbcas_avg': mbcas_wl['avg_throttling_ratio'],
            'vpa_avg': vpa_wl['avg_throttling_ratio'],
            'mbcas_duration': mbcas_wl['throttling_duration_seconds'],
            'vpa_duration': vpa_wl['throttling_duration_seconds'],
        },
        'final_allocation': {
            'mbcas_request': mbcas_wl['final_request_milli'],
            'vpa_request': vpa_wl['final_request_milli'],
            'mbcas_limit': mbcas_wl['final_limit_milli'],
            'vpa_limit': vpa_wl['final_limit_milli'],
        },
        'efficiency': {
            'mbcas_request_util': mbcas_eff['request_utilization_pct'],
            'vpa_request_util': vpa_eff['request_utilization_pct'],
            'mbcas_limit_util': mbcas_eff['limit_utilization_pct'],
            'vpa_limit_util': vpa_eff['limit_utilization_pct'],
            'mbcas_overhead': mbcas_eff['overhead_pct'],
            'vpa_overhead': vpa_eff['overhead_pct'],
        },
    }
    return comparison

def main():
    mbcas_file = Path('test/benchmark/metrics-mbcas.json')
    vpa_file = Path('test/benchmark/metrics-vpa.json')
    
    if not mbcas_file.exists() or not vpa_file.exists():
        print(f"Error: Files not found")
        sys.exit(1)
    
    mbcas_data = load_metrics(mbcas_file)
    vpa_data = load_metrics(vpa_file)
    
    print("=" * 80)
    print("MBCAS vs VPA BENCHMARK COMPARISON")
    print("=" * 80)
    print()
    
    print(f"Test Configuration:")
    print(f"  Duration: MBCAS={mbcas_data['duration']}, VPA={vpa_data['duration']}")
    print(f"  Workloads: {len(mbcas_data['workloads'])}")
    print(f"  Node CPU: {mbcas_data['configuration']['total_node_cpu_milli']}m")
    print()
    
    # Create workload maps
    mbcas_workloads = {w['name']: analyze_workload(w) for w in mbcas_data['workloads']}
    vpa_workloads = {w['name']: analyze_workload(w) for w in vpa_data['workloads']}
    
    # Compare each workload
    comparisons = []
    for name in sorted(mbcas_workloads.keys()):
        if name in vpa_workloads:
            comp = compare_workloads(mbcas_workloads[name], vpa_workloads[name])
            comparisons.append(comp)
    
    # Print detailed comparison for each workload
    for comp in comparisons:
        print("-" * 80)
        print(f"Workload: {comp['name']}")
        print("-" * 80)
        
        print(f"\nAllocation Changes:")
        print(f"  MBCAS: {comp['allocation_changes']['mbcas']}")
        print(f"  VPA:   {comp['allocation_changes']['vpa']}")
        diff = comp['allocation_changes']['diff']
        diff_pct = comp['allocation_changes']['diff_pct']
        print(f"  Difference: {diff:+d} ({diff_pct:+.1f}%)")
        if diff < 0:
            print(f"    [OK] MBCAS has {abs(diff)} fewer changes (more stable)")
        else:
            print(f"    [WARN] MBCAS has {diff} more changes")
        
        print(f"\nAverage CPU Usage:")
        print(f"  MBCAS: {comp['avg_usage']['mbcas']:.1f}m")
        print(f"  VPA:   {comp['avg_usage']['vpa']:.1f}m")
        usage_diff = comp['avg_usage']['diff']
        usage_diff_pct = comp['avg_usage']['diff_pct']
        print(f"  Difference: {usage_diff:+.1f}m ({usage_diff_pct:+.1f}%)")
        
        print(f"\nThrottling:")
        print(f"  MBCAS: avg={comp['throttling']['mbcas_avg']:.3f}, duration={comp['throttling']['mbcas_duration']}s")
        print(f"  VPA:   avg={comp['throttling']['vpa_avg']:.3f}, duration={comp['throttling']['vpa_duration']}s")
        
        print(f"\nFinal Allocation:")
        print(f"  MBCAS: request={comp['final_allocation']['mbcas_request']}m, limit={comp['final_allocation']['mbcas_limit']}m")
        print(f"  VPA:   request={comp['final_allocation']['vpa_request']}m, limit={comp['final_allocation']['vpa_limit']}m")
        
        print(f"\nResource Efficiency:")
        print(f"  Request Utilization:")
        print(f"    MBCAS: {comp['efficiency']['mbcas_request_util']:.1f}%")
        print(f"    VPA:   {comp['efficiency']['vpa_request_util']:.1f}%")
        print(f"  Limit Utilization:")
        print(f"    MBCAS: {comp['efficiency']['mbcas_limit_util']:.1f}%")
        print(f"    VPA:   {comp['efficiency']['vpa_limit_util']:.1f}%")
        print(f"  Overhead (unused limit):")
        print(f"    MBCAS: {comp['efficiency']['mbcas_overhead']:.1f}%")
        print(f"    VPA:   {comp['efficiency']['vpa_overhead']:.1f}%")
        print()
    
    # Summary statistics
    print("=" * 80)
    print("SUMMARY STATISTICS")
    print("=" * 80)
    
    total_mbcas_changes = sum(c['allocation_changes']['mbcas'] for c in comparisons)
    total_vpa_changes = sum(c['allocation_changes']['vpa'] for c in comparisons)
    avg_mbcas_usage = sum(c['avg_usage']['mbcas'] for c in comparisons) / len(comparisons)
    avg_vpa_usage = sum(c['avg_usage']['vpa'] for c in comparisons) / len(comparisons)
    
    print(f"\nTotal Allocation Changes:")
    print(f"  MBCAS: {total_mbcas_changes}")
    print(f"  VPA:   {total_vpa_changes}")
    print(f"  Reduction: {total_vpa_changes - total_mbcas_changes} ({((total_vpa_changes - total_mbcas_changes) / total_vpa_changes * 100):.1f}% fewer)")
    
    print(f"\nAverage CPU Usage Across Workloads:")
    print(f"  MBCAS: {avg_mbcas_usage:.1f}m")
    print(f"  VPA:   {avg_vpa_usage:.1f}m")
    print(f"  Difference: {avg_mbcas_usage - avg_vpa_usage:+.1f}m")
    
    avg_mbcas_throttling = sum(c['throttling']['mbcas_avg'] for c in comparisons) / len(comparisons)
    avg_vpa_throttling = sum(c['throttling']['vpa_avg'] for c in comparisons) / len(comparisons)
    print(f"\nAverage Throttling Ratio:")
    print(f"  MBCAS: {avg_mbcas_throttling:.3f}")
    print(f"  VPA:   {avg_vpa_throttling:.3f}")
    
    avg_mbcas_util = sum(c['efficiency']['mbcas_request_util'] for c in comparisons) / len(comparisons)
    avg_vpa_util = sum(c['efficiency']['vpa_request_util'] for c in comparisons) / len(comparisons)
    print(f"\nAverage Request Utilization:")
    print(f"  MBCAS: {avg_mbcas_util:.1f}%")
    print(f"  VPA:   {avg_vpa_util:.1f}%")
    print()

if __name__ == '__main__':
    main()
