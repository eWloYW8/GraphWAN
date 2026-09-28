// Package resources samples this Agent process without a background worker.
package resources

import (
	"math"
	"runtime"
	"runtime/metrics"
	"sync"
	"time"

	"github.com/graphwan/graphwan/internal/model"
)

type Sampler struct {
	mu            sync.Mutex
	now           func() time.Time
	cpu           func() (float64, error)
	started, last time.Time
	lastCPU       float64
	cpuValid      bool
	cached        model.ResourceUsage
}

func New() *Sampler {
	return &Sampler{now: time.Now, cpu: processCPUSeconds, started: time.Now()}
}

// Sample limits OS/runtime collection to once per second and returns an owned
// copy. CPU is unavailable until two successful, nondecreasing samples exist.
func (s *Sampler) Sample() *model.ResourceUsage {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.last.IsZero() || now.Sub(s.last) >= time.Second {
		values := []metrics.Sample{
			{Name: "/memory/classes/total:bytes"},
			{Name: "/memory/classes/heap/released:bytes"},
			{Name: "/memory/classes/heap/objects:bytes"},
			{Name: "/sched/goroutines:goroutines"},
		}
		metrics.Read(values)
		next := model.ResourceUsage{UptimeSeconds: uint64(max(0, now.Sub(s.started).Seconds())), LogicalCPUs: runtime.NumCPU(),
			GoMemoryBytes: values[0].Value.Uint64() - values[1].Value.Uint64(), HeapBytes: values[2].Value.Uint64(), Goroutines: values[3].Value.Uint64()}
		cpu, err := s.cpu()
		valid := err == nil && cpu >= 0 && !math.IsNaN(cpu) && !math.IsInf(cpu, 0)
		if valid && s.cpuValid && cpu >= s.lastCPU && !s.last.IsZero() {
			percent := 100 * (cpu - s.lastCPU) / now.Sub(s.last).Seconds()
			if !math.IsNaN(percent) && !math.IsInf(percent, 0) && percent <= model.MaxCPUPercent {
				next.CPUPercent = &percent
			}
		}
		s.last, s.lastCPU, s.cpuValid, s.cached = now, cpu, valid, next
	}
	return s.cached.Clone()
}
