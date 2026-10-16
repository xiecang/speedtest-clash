// SPDX-License-Identifier: GPL-3.0-only
package measure

import (
	"time"

	"github.com/xiecang/node-quality/v2/internal/model"
)

func minTimeEvidence(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// Reuse only the bounded download stage; health and all AI streams still run.
func reusableDownload(node model.Node, plan model.Plan) (model.Metrics, time.Time, bool) {
	r := plan.GeneralEvidence
	// Services and IP providers run serially before the three parallel streams.
	remaining := plan.AI.Duration + 20*time.Second + time.Duration(len(plan.ServiceURLs)+len(plan.IPProviders)+1)*plan.ProbeTimeout
	if plan.Profile != "ai" || r == nil || r.Profile != "general" || r.NodeKey != node.Key || r.ConfigRevision != node.Revision || r.Core != plan.Core || r.WorkerID != plan.WorkerID || r.Environment != plan.Environment || r.Status != model.Pass || r.Cached || r.InfrastructureFailure || r.GeneralMeasuredAt.IsZero() || !time.Now().Add(remaining).Before(r.FreshUntil) || !time.Now().Before(r.GeneralMeasuredAt.Add(24*time.Hour)) {
		return model.Metrics{}, time.Time{}, false
	}
	version := model.MihomoVersion
	if plan.Core == model.SingBox {
		version = model.SingBoxVersion
	}
	if r.CoreVersion != version {
		return model.Metrics{}, time.Time{}, false
	}
	general := plan
	general.Profile = "general"
	if r.EvaluationRevision != PlanRevision(general) || r.Metrics.DownloadBytes <= 0 || r.Metrics.DownloadDuration <= 0 {
		return model.Metrics{}, time.Time{}, false
	}
	return r.Metrics, r.GeneralMeasuredAt, true
}
