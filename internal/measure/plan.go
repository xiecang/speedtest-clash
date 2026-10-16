// SPDX-License-Identifier: GPL-3.0-only
package measure

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/xiecang/node-quality/v2/internal/model"
)

// PlanRevision identifies the measurements, excluding credentials, environment,
// scheduling and publication settings. Changing a probe invalidates old results.
func PlanRevision(p model.Plan) string {
	if p.Profile == "availability" {
		content, _ := json.Marshal(struct {
			Core    model.Core
			Probe   string
			Timeout time.Duration
			Version string
		}{p.Core, p.ProbeURL, p.ProbeTimeout, "availability-v1"})
		sum := sha256.Sum256(content)
		return hex.EncodeToString(sum[:])
	}
	if p.Profile == "services" {
		content, _ := json.Marshal(struct {
			Core    model.Core
			Checks  map[string]model.ServiceCheck
			Version string
		}{p.Core, p.ServiceChecks, "service-v1"})
		sum := sha256.Sum256(content)
		return hex.EncodeToString(sum[:])
	}
	input := struct {
		MetricVersion                              int
		Core                                       model.Core
		Profile                                    string
		ProbeURL                                   string
		DownloadBytes                              int64
		DownloadConcurrency                        int
		SampleWindow, ProbeTimeout, StreamDuration time.Duration
		ExitIPURL                                  string
		Services, IPProviders                      map[string]string
	}{model.MetricVersion, p.Core, p.Profile, p.ProbeURL, p.DownloadBytes, p.DownloadConcurrency, p.SampleWindow, p.ProbeTimeout, 0, p.ExitIPURL, p.ServiceURLs, p.IPProviders}
	if p.Profile == "ai" {
		input.StreamDuration = p.AI.Duration
	}
	content, _ := json.Marshal(input) // Only concrete JSON primitives above.
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func MinimumStreamDuration(duration time.Duration) time.Duration {
	return duration - min(2*time.Second, duration/20)
}
