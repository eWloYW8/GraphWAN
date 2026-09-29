package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/resources"
)

// Runtime.Apply must either install the whole snapshot or return an error while
// retaining its previous working configuration. Report must be concurrency safe.
// Controller disconnection never invokes Apply or closes the runtime.
type Runtime interface {
	Apply(context.Context, model.Snapshot) error
	Report() []model.LinkStatus
}

type ApplyError struct{ Err error }

func (e *ApplyError) Error() string { return "apply configuration: " + e.Err.Error() }
func (e *ApplyError) Unwrap() error { return e.Err }

type Reconciler struct {
	cache       *Cache
	resources   *resources.Sampler
	runtime     Runtime
	applyMu     sync.Mutex
	mu          sync.Mutex
	applied     uint64
	running     bool
	configError string
}

func NewReconciler(cache *Cache, runtime Runtime) *Reconciler {
	return &Reconciler{cache: cache, runtime: runtime, resources: resources.New()}
}
func (r *Reconciler) Report(version string) model.AgentReport {
	r.mu.Lock()
	report := model.AgentReport{Version: version, AppliedRevision: r.applied, ConfigError: r.configError}
	r.mu.Unlock()
	report.Resources = r.resources.Sample()
	report.Links = r.runtime.Report()
	if health, ok := r.runtime.(interface{ Health() error }); ok {
		if err := health.Health(); err != nil {
			report.RuntimeError = err.Error()
			if len(report.RuntimeError) > 4096 {
				report.RuntimeError = report.RuntimeError[:4096]
			}
		}
	}
	return report
}
func (r *Reconciler) status(revision uint64, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.applied = revision
	r.running = true
	r.configError = ""
	if err != nil {
		r.configError = err.Error()
		if len(r.configError) > 4096 {
			r.configError = r.configError[:4096]
		}
	}
}
func (r *Reconciler) failure(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.configError = err.Error()
	if len(r.configError) > 4096 {
		r.configError = r.configError[:4096]
	}
}

// Restore starts the last durable desired configuration without contacting the
// controller. If it cannot be applied, the last successful snapshot is restored.
func (r *Reconciler) Restore(ctx context.Context) error {
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	desired, applied, err := r.cache.Snapshots()
	if err != nil {
		return err
	}
	if desired == nil {
		return nil
	}
	if err = r.runtime.Apply(ctx, desired.Clone()); err == nil {
		if err := r.cache.MarkApplied(*desired); err != nil {
			return err
		}
		r.status(desired.Revision, nil)
		return nil
	}
	failure := &ApplyError{Err: err}
	if applied != nil && applied.Revision != desired.Revision {
		if err := r.runtime.Apply(ctx, applied.Clone()); err != nil {
			return errors.Join(failure, fmt.Errorf("restore prior configuration: %w", err))
		}
		r.status(applied.Revision, failure)
		return nil
	}
	r.failure(failure)
	return failure
}

// Accept provides the ordering boundary: validate -> persist -> apply -> ACK.
// The report never advances to a revision that failed runtime application.
func (r *Reconciler) Accept(ctx context.Context, snapshot model.Snapshot) error {
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	if err := r.cache.SaveDesired(snapshot); err != nil {
		r.failure(err)
		return err
	}
	r.mu.Lock()
	alreadyApplied := r.running && r.applied == snapshot.Revision && r.configError == ""
	r.mu.Unlock()
	if alreadyApplied {
		return nil
	}
	if err := r.runtime.Apply(ctx, snapshot.Clone()); err != nil {
		failure := &ApplyError{Err: err}
		r.failure(failure)
		return failure
	}
	if err := r.cache.MarkApplied(snapshot); err != nil {
		r.failure(err)
		return err
	}
	r.status(snapshot.Revision, nil)
	return nil
}
