package model

import (
	"errors"
	"math"
)

// ResourceUsage describes this Agent process, not host-wide utilization.
// CPUPercent uses one logical core as 100%; nil means no valid interval sample.
// GoMemoryBytes excludes released heap pages and non-Go allocations/mappings.
type ResourceUsage struct {
	CPUPercent    *float64 `json:"cpu_percent,omitempty"`
	LogicalCPUs   int      `json:"logical_cpus"`
	GoMemoryBytes uint64   `json:"go_memory_bytes"`
	HeapBytes     uint64   `json:"heap_bytes"`
	Goroutines    uint64   `json:"goroutines"`
	UptimeSeconds uint64   `json:"uptime_seconds"`
}

// Large but finite limits reject nonsensical reports and preserve exact integer
// representation in browser consumers. CPU may legitimately exceed 100%.
const MaxCPUPercent = 100 * (1 << 20)

func (r ResourceUsage) Validate() error {
	const maxSafeInteger = 1<<53 - 1
	if r.LogicalCPUs < 1 || r.LogicalCPUs > 1<<20 || r.GoMemoryBytes > maxSafeInteger || r.HeapBytes > r.GoMemoryBytes || r.Goroutines == 0 || r.Goroutines > 1<<32 || r.UptimeSeconds > maxSafeInteger {
		return errors.New("invalid agent resource metrics")
	}
	if r.CPUPercent != nil && (math.IsNaN(*r.CPUPercent) || math.IsInf(*r.CPUPercent, 0) || *r.CPUPercent < 0 || *r.CPUPercent > MaxCPUPercent) {
		return errors.New("invalid agent CPU utilization")
	}
	return nil
}

func (r *ResourceUsage) Clone() *ResourceUsage {
	if r == nil {
		return nil
	}
	copy := *r
	if r.CPUPercent != nil {
		value := *r.CPUPercent
		copy.CPUPercent = &value
	}
	return &copy
}
