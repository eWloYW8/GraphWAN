package resources

import (
	"errors"
	"math"
	"sync"
	"testing"
	"time"
)

func TestSamplingIntervalsMissingCPUAndRecovery(t *testing.T) {
	now := time.Unix(100, 0)
	cpu, cpuErr, calls := 10.0, error(nil), 0
	s := &Sampler{now: func() time.Time { return now }, started: now, cpu: func() (float64, error) { calls++; return cpu, cpuErr }}
	first := s.Sample()
	if first.CPUPercent != nil || first.Validate() != nil || first.GoMemoryBytes == 0 || first.HeapBytes == 0 {
		t.Fatal(first)
	}
	now = now.Add(500 * time.Millisecond)
	if s.Sample().CPUPercent != nil || calls != 1 {
		t.Fatal("too frequent collection")
	}
	now = now.Add(1500 * time.Millisecond)
	cpu += 3 // Three CPU seconds in two wall seconds, across multiple cores.
	second := s.Sample()
	if second.CPUPercent == nil || *second.CPUPercent != 150 || second.UptimeSeconds != 2 || second.Validate() != nil {
		t.Fatal(second)
	}
	*second.CPUPercent = -1
	second.GoMemoryBytes = 0
	if cached := s.Sample(); *cached.CPUPercent != 150 || cached.GoMemoryBytes == 0 {
		t.Fatal("caller mutated cached sample")
	}
	now = now.Add(time.Second)
	cpuErr = errors.New("OS sampling unavailable")
	if failed := s.Sample(); failed.CPUPercent != nil || failed.GoMemoryBytes == 0 {
		t.Fatal("failed CPU sampling retained a stale number or lost memory")
	}
	now = now.Add(time.Second)
	cpuErr = nil
	cpu += 1
	if s.Sample().CPUPercent != nil {
		t.Fatal("error interval used for CPU rate")
	}
	now = now.Add(time.Second)
	cpu += 0.25
	if recovered := s.Sample(); recovered.CPUPercent == nil || *recovered.CPUPercent != 25 {
		t.Fatal(recovered)
	}
	now = now.Add(time.Second)
	if idle := s.Sample(); idle.CPUPercent == nil || *idle.CPUPercent != 0 {
		t.Fatal("idle interval confused with missing sample")
	}
	now = now.Add(time.Second)
	cpu = 1
	if s.Sample().CPUPercent != nil {
		t.Fatal("decreasing counter yielded utilization")
	}
	for _, invalid := range []float64{math.NaN(), math.Inf(1), -1, 0} {
		now = now.Add(time.Second)
		cpu = invalid
		if s.Sample().CPUPercent != nil {
			t.Fatal("invalid or decreasing counter yielded utilization")
		}
	}
}

func TestConcurrentSamplingAndNativeProcessCounter(t *testing.T) {
	s := New()
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			for range 10 {
				if got := s.Sample(); got.Validate() != nil || got.Goroutines == 0 {
					t.Error("invalid runtime sample", got)
				}
			}
		})
	}
	wg.Wait()
	before, err := processCPUSeconds()
	if err != nil {
		t.Skip("native CPU counter unavailable:", err)
	}
	// Spend actual CPU time in this process; a cached host/system counter or a
	// FILETIME interpreted as an epoch date would not satisfy these invariants.
	until := time.Now().Add(50 * time.Millisecond)
	for time.Now().Before(until) {
	}
	after, err := processCPUSeconds()
	if err != nil || before < 0 || after <= before {
		t.Fatal("process CPU did not advance:", before, after, err)
	}
}
