// SPDX-License-Identifier: GPL-3.0-only
package nodequality

import (
	"context"
	"time"

	"github.com/xiecang/node-quality/v2/internal/engine"
	"github.com/xiecang/node-quality/v2/internal/measure"
	"github.com/xiecang/node-quality/v2/internal/model"
)

// Capabilities describes the embedded cores and enabled build features.
func Capabilities() BuildInfo { return engine.Capabilities() }

// NewBudget creates a bandwidth budget that callers can share across measurements.
func NewBudget(bytesPerSecond int64) *Budget { return measure.NewBudget(bytesPerSecond) }

// NewBudgetWithConcurrency creates a worker-wide rate and parallel node budget.
func NewBudgetWithConcurrency(bytesPerSecond int64, nodes int) (*Budget, error) {
	return measure.NewBudgetWithConcurrency(bytesPerSecond, nodes)
}

// ValidatePlan validates measurement settings before starting work.
func ValidatePlan(plan Plan) error { return measure.ValidatePlan(plan) }

// Evaluate measures nodes with the supplied shared bandwidth budget.
func Evaluate(ctx context.Context, nodes []Node, plan Plan, budget *Budget) ([]Report, error) {
	return measure.Evaluate(ctx, nodes, plan, budget)
}

// PlanRevision returns the revision used to match core measurement evidence.
func PlanRevision(p Plan) string { return measure.PlanRevision(p) }

// MinimumStreamDuration returns the stream duration required by the AI plan.
func MinimumStreamDuration(duration time.Duration) time.Duration {
	return measure.MinimumStreamDuration(duration)
}

// OpenBatch loads a bounded node graph into an embedded core.
func OpenBatch(ctx context.Context, nodes []Node, core Core) (*Batch, error) {
	return engine.OpenBatch(ctx, nodes, core)
}

// CheckSingBox validates a native sing-box configuration without starting it.
func CheckSingBox(ctx context.Context, content []byte) error {
	return engine.CheckSingBox(ctx, content)
}

// ServiceCheckRevision returns the independent revision of an optional service check.
func ServiceCheckRevision(check ServiceCheck) string { return measure.ServiceCheckRevision(check) }

// DefaultPlan returns the default measurement plan.
func DefaultPlan() Plan { return model.DefaultPlan() }
