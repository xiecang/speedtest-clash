// SPDX-License-Identifier: GPL-3.0-only
package nodequality

import (
	"context"

	"github.com/xiecang/node-quality/v2/internal/filter"
)

type SelectionOptions = filter.Options
type Selector = filter.Selector

// CompileSelector compiles cvt expressions and V1 name regexes once. Configured
// conditions are ANDed; empty options select everything without starting JS.
func CompileSelector(ctx context.Context, options SelectionOptions) (*Selector, error) {
	return filter.Compile(ctx, options)
}

// PreparePlan compiles target selection for repeated measurements without
// changing evidence revisions or dependency configurations.
func PreparePlan(ctx context.Context, plan Plan) (Plan, error) {
	selector, err := CompileSelector(ctx, SelectionOptions{Filter: plan.Filter, NameRegexContain: plan.NameRegexContain, NameRegexNotContain: plan.NameRegexNotContain})
	if err != nil {
		return plan, err
	}
	plan.TargetSelector = selector
	return plan, nil
}
