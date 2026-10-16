// SPDX-License-Identifier: GPL-3.0-only
package nodequality

import (
	"net/http"

	"github.com/xiecang/node-quality/v2/internal/httpapi"
	"github.com/xiecang/node-quality/v2/internal/probe"
)

// Handler serves the V2 conversion, measurement and probe HTTP API.
func Handler(plan Plan, budget *Budget, probe ProbeOptions) http.Handler {
	return httpapi.Handler(plan, budget, probe)
}

// ProbeHandler serves the controlled HTTP, SSE and WebSocket probe endpoints.
func ProbeHandler(options ProbeOptions) http.Handler { return probe.ProbeHandler(options) }
