package model

import (
	"math"
	"testing"
)

func TestResourceAdmissionAndSnapshotOwnership(t *testing.T) {
	percent := 150.0
	valid := ResourceUsage{CPUPercent: &percent, LogicalCPUs: 4, GoMemoryBytes: 64 << 20, HeapBytes: 32 << 20, Goroutines: 50, UptimeSeconds: 100}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, percent := range []float64{math.NaN(), math.Inf(1), -0.1, MaxCPUPercent + 1} {
		bad := *valid.Clone()
		bad.CPUPercent = &percent
		if bad.Validate() == nil {
			t.Fatal("invalid CPU accepted", percent)
		}
	}
	for _, mutate := range []func(*ResourceUsage){
		func(r *ResourceUsage) { r.LogicalCPUs = 0 },
		func(r *ResourceUsage) { r.LogicalCPUs = 1<<20 + 1 },
		func(r *ResourceUsage) { r.HeapBytes = r.GoMemoryBytes + 1 },
		func(r *ResourceUsage) { r.GoMemoryBytes = 1 << 53 },
		func(r *ResourceUsage) { r.Goroutines = 0 },
		func(r *ResourceUsage) { r.Goroutines = 1<<32 + 1 },
		func(r *ResourceUsage) { r.UptimeSeconds = 1 << 53 },
	} {
		bad := *valid.Clone()
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatal("invalid resources accepted", bad)
		}
	}
	copy := valid.Clone()
	*copy.CPUPercent = 1
	if *valid.CPUPercent != 150 {
		t.Fatal("clone aliases CPU sample")
	}
	copy.CPUPercent = nil
	if err := copy.Validate(); err != nil {
		t.Fatal("missing CPU is valid", err)
	}
}
