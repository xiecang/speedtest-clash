// SPDX-License-Identifier: GPL-3.0-only
package measure

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xiecang/node-quality/v2/internal/engine"
	"github.com/xiecang/node-quality/v2/internal/filter"
	"github.com/xiecang/node-quality/v2/internal/model"
)

func probeEndpoint(base, path string) string {
	return strings.TrimRight(base, "/") + "/api/v2/probe/" + path
}

func setProbeHeaders(request *http.Request, plan model.Plan) {
	request.Header.Set("Accept-Encoding", "identity")
	if plan.ProbeToken != "" {
		request.Header.Set("Authorization", "Bearer "+plan.ProbeToken)
	}
}

func probeClient(dial model.DialContext, plan model.Plan, h2 bool) (*http.Client, *http.Transport) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if plan.ProbeTLSConfig != nil {
		tlsConfig = plan.ProbeTLSConfig.Clone()
	}
	if !h2 {
		tlsConfig.NextProtos = []string{"http/1.1"}
	}
	transport := &http.Transport{DialContext: dial, TLSClientConfig: tlsConfig, ForceAttemptHTTP2: h2, DisableCompression: true, ResponseHeaderTimeout: plan.ProbeTimeout, TLSHandshakeTimeout: plan.ProbeTimeout, MaxIdleConnsPerHost: 5}
	if !h2 {
		transport.TLSNextProto = make(map[string]func(string, *tls.Conn) http.RoundTripper)
	}
	return &http.Client{Transport: transport, CheckRedirect: func(r *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}, transport
}

func ValidatePlan(plan model.Plan) error {
	if plan.Core != model.Mihomo && plan.Core != model.SingBox {
		return errors.New("unsupported core")
	}
	if len(plan.ServiceURLs) > 3 || len(plan.IPProviders) > 3 {
		return errors.New("at most three service/IP providers per plan")
	}
	if err := validateServiceChecks(plan); err != nil {
		return err
	}
	address, err := url.Parse(plan.ProbeURL)
	if err != nil || address.Host == "" || (address.Scheme != "https" && address.Scheme != "http") {
		return errors.New("valid probe_url required")
	}
	if plan.DownloadBytes <= 0 || plan.DownloadBytes > 256<<20 || plan.DownloadConcurrency < 1 || plan.DownloadConcurrency > 32 || plan.SampleWindow <= 0 || plan.SampleWindow > time.Minute {
		return errors.New("invalid download limits")
	}
	if plan.ProbeTimeout <= 0 || plan.ProbeTimeout > time.Minute || plan.LivenessConcurrency < 1 || plan.LivenessConcurrency > 128 {
		return errors.New("invalid liveness limits")
	}
	if plan.Profile != "general" && plan.Profile != "ai" && plan.Profile != "availability" && plan.Profile != "services" {
		return errors.New("profile must be general or ai")
	}
	if plan.Profile == "ai" && (plan.AI.Concurrency < 1 || plan.AI.Concurrency > 32 || plan.AI.Duration <= 0 || plan.AI.Duration > 10*time.Minute || plan.AI.Validity <= 0 || plan.AI.Validity > 6*time.Hour || plan.AI.CandidateLimit < 1 || plan.AI.CandidateLimit > 200 || plan.AI.RetestInterval <= 0) {
		return errors.New("invalid AI limits")
	}
	return nil
}

// Evaluate runs bounded native-core measurements. A Budget must be shared by
// all concurrent calls from the same worker; reports never contain credentials.
func Evaluate(ctx context.Context, nodes []model.Node, plan model.Plan, budget *Budget) ([]model.Report, error) {
	if err := ValidatePlan(plan); err != nil {
		return nil, err
	}
	if len(nodes) > 200 {
		return nil, errors.New("evaluation batch exceeds 200 nodes")
	}
	if budget == nil {
		return nil, errors.New("shared bandwidth budget required")
	}
	targets := nodes
	if len(plan.TargetKeys) > 0 {
		targets = nil
		wanted := make(map[string]bool)
		for _, key := range plan.TargetKeys {
			wanted[key] = true
		}
		for _, node := range nodes {
			if wanted[node.Key] {
				targets = append(targets, node)
				delete(wanted, node.Key)
			}
		}
		if len(wanted) > 0 {
			return nil, errors.New("target is absent from dependency graph")
		}
	}
	selector := plan.TargetSelector
	if selector == nil {
		var err error
		selector, err = filter.Compile(ctx, filter.Options{Filter: plan.Filter, NameRegexContain: plan.NameRegexContain, NameRegexNotContain: plan.NameRegexNotContain})
		if err != nil {
			return nil, err
		}
	}
	mask, err := selector.Match(ctx, targets)
	if err != nil {
		return nil, err
	}
	selectedTargets := make([]model.Node, 0, len(targets))
	for i, node := range targets {
		if mask[i] {
			selectedTargets = append(selectedTargets, node)
		}
	}
	targets = selectedTargets
	if len(targets) == 0 {
		return []model.Report{}, nil
	}
	if plan.Profile == "ai" && len(targets) > plan.AI.CandidateLimit {
		return nil, errors.New("AI candidate limit exceeded")
	}
	batch, err := engine.OpenBatch(ctx, nodes, plan.Core)
	if err != nil {
		return nil, err
	}
	defer batch.Close()
	control := true
	if plan.Profile != "services" {
		control = probeControl(ctx, plan)
	}

	concurrency := plan.LivenessConcurrency
	if plan.Profile == "ai" {
		concurrency = plan.AI.Concurrency
	}
	concurrency = min(concurrency, len(targets))
	jobs := make(chan int)
	reports := make([]model.Report, len(targets))
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				node := targets[index]
				dial, e := batch.Dialer(node.Key)
				if e != nil {
					reports[index] = newReport(node, plan)
					reports[index].FailureCode = capabilityFailureCode(e)
					reports[index].CompletedAt = time.Now()
					continue
				}
				reports[index] = evaluateNode(ctx, node, dial, plan, budget, control)
			}
		}()
	}
	for i := range targets {
		select {
		case jobs <- i:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return nil, ctx.Err()
		}
	}
	close(jobs)
	wg.Wait()
	return reports, nil
}

func newReport(node model.Node, plan model.Plan) model.Report {
	version := model.MihomoVersion
	if plan.Core == model.SingBox {
		version = model.SingBoxVersion
	}
	return model.Report{SchemaVersion: model.SchemaVersion, MetricVersion: model.MetricVersion, NodeKey: node.Key, ConfigRevision: node.Revision, EvaluationRevision: PlanRevision(plan), WorkerID: plan.WorkerID, Environment: plan.Environment, Core: plan.Core, CoreVersion: version, Profile: plan.Profile, Status: model.Unknown, MeasuredAt: time.Now(), Services: make(map[string]model.ServiceResult)}
}

func probeControl(ctx context.Context, plan model.Plan) bool {
	client, transport := probeClient(nil, plan, false)
	defer transport.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, plan.ProbeTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, probeEndpoint(plan.ProbeURL, "health"), nil)
	if err != nil {
		return false
	}
	setProbeHeaders(request, plan)
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	response.Body.Close()
	return response.StatusCode == http.StatusNoContent && response.Header.Get("X-Node-Quality-Probe") == "2"
}

func evaluateNode(ctx context.Context, node model.Node, dial model.DialContext, plan model.Plan, budget *Budget, control bool) (report model.Report) {
	report = newReport(node, plan)
	defer func() {
		report.CompletedAt = time.Now()
		if plan.Profile == "availability" || plan.Profile == "services" {
			report.GeneralStatus = model.Unknown
			return
		}
		if report.GeneralMeasuredAt.IsZero() {
			report.GeneralMeasuredAt = report.CompletedAt
		}
		if report.GeneralStatus != model.Pass {
			report.GeneralStatus = report.Status
			report.GeneralFailureCode = report.FailureCode
			report.GeneralFailureOrigin = report.FailureOrigin
		}
	}()
	client, transport := probeClient(dial, plan, false)
	defer transport.CloseIdleConnections()
	if plan.Profile == "services" {
		report.Status = model.Pass
		report.FreshUntil = time.Now().Add(2 * time.Hour)
		for name, check := range plan.ServiceChecks {
			previous := plan.ServiceEvidence[name]
			if previous.CheckRevision == ServiceCheckRevision(check) && !previous.MeasuredAt.IsZero() && time.Now().Before(previous.MeasuredAt.Add(2*time.Hour)) {
				report.Services[name] = previous
			} else {
				report.Services[name] = checkOptionalService(ctx, client, check, plan.ProbeTimeout)
			}
			report.FreshUntil = minTimeEvidence(report.FreshUntil, report.Services[name].MeasuredAt.Add(2*time.Hour))
		}
		return
	}
	var cold, reused []time.Duration
	failed := 0
	var healthError error
	healthHTTPStatus := 0
	attempts := 5
	if plan.Profile == "availability" {
		attempts = 1
	}
	for i := 0; i < attempts; i++ {
		report.Metrics.HealthAttempts++
		probeCtx, cancel := context.WithTimeout(ctx, plan.ProbeTimeout)
		request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, probeEndpoint(plan.ProbeURL, "health"), nil)
		if err == nil {
			setProbeHeaders(request, plan)
			wasReused := false
			request = request.WithContext(httptrace.WithClientTrace(request.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { wasReused = info.Reused }}))
			started := time.Now()
			response, e := client.Do(request)
			err = e
			if e == nil {
				response.Body.Close()
				if response.StatusCode != http.StatusNoContent {
					err = &probeHTTPError{status: response.StatusCode}
					healthHTTPStatus = response.StatusCode
				} else {
					if wasReused {
						reused = append(reused, time.Since(started))
					} else {
						cold = append(cold, time.Since(started))
					}
				}
			}
		}
		cancel()
		if err != nil {
			healthError = err
			failed++
			if i == 0 {
				break
			}
		}
	}
	report.Metrics.HealthFailures = failed
	report.Metrics.ColdHealthSamples = len(cold)
	report.Metrics.ReusedHealthSamples = len(reused)
	report.Metrics.ColdHealthMedian = median(cold)
	report.Metrics.ReusedHealthMedian = median(reused)
	if len(cold)+len(reused) == 0 {
		report.FailureOrigin, report.FailureCode = failureOrigin(healthError, ctx)
		if report.FailureOrigin != model.FailureOriginNode && ctx.Err() == nil && (healthHTTPStatus != 0 || !control || !probeControl(ctx, plan)) {
			report.FailureOrigin = model.FailureOriginProbe
			report.FailureCode = "probe_unavailable"
			if healthHTTPStatus != 0 {
				report.FailureCode = fmt.Sprintf("probe_http_%d", healthHTTPStatus)
			}
		} else if report.FailureOrigin != model.FailureOriginNode && report.FailureCode == "" {
			report.FailureCode = networkFailureCode(healthError, "unreachable")
		}
		report.Status = model.Unknown
		if report.FailureOrigin == model.FailureOriginNode {
			report.Status = model.Fail
		} else if report.FailureOrigin == model.FailureOriginEnvironment || report.FailureOrigin == model.FailureOriginProbe {
			report.InfrastructureFailure = true
		}
		return
	}
	if plan.Profile == "availability" {
		report.Status = model.Pass
		report.FreshUntil = time.Now().Add(24 * time.Hour)
		return
	}
	if failed > 0 {
		report.FailureOrigin, report.FailureCode = failureOrigin(healthError, ctx)
		if report.FailureOrigin != model.FailureOriginNode && ctx.Err() == nil && (healthHTTPStatus != 0 || !control || !probeControl(ctx, plan)) {
			report.FailureOrigin = model.FailureOriginProbe
			report.FailureCode = "probe_unavailable"
			if healthHTTPStatus != 0 {
				report.FailureCode = fmt.Sprintf("probe_http_%d", healthHTTPStatus)
			}
		} else if report.FailureOrigin != model.FailureOriginNode && report.FailureCode == "" {
			report.FailureCode = "request_failures"
		}
		report.Status = model.Unknown
		if report.FailureOrigin == model.FailureOriginNode {
			report.Status = model.Fail
		} else if report.FailureOrigin == model.FailureOriginEnvironment || report.FailureOrigin == model.FailureOriginProbe {
			report.InfrastructureFailure = true
		}
		return
	}
	download, measured, downloadReused := reusableDownload(node, plan)
	var err error
	if !downloadReused {
		download, err = measureDownload(ctx, client, plan, budget)
		measured = time.Now()
	}
	report.GeneralReused = downloadReused
	report.Metrics.DownloadHeadersMedian = download.DownloadHeadersMedian
	report.Metrics.DownloadHeaderSamples = download.DownloadHeaderSamples
	report.Metrics.DownloadReusedHeaderSamples = download.DownloadReusedHeaderSamples
	report.Metrics.RateLimitBytesPerSecond = download.RateLimitBytesPerSecond
	report.Metrics.BandwidthQueueWait = download.BandwidthQueueWait
	report.Metrics.DownloadEndReason = download.DownloadEndReason
	report.Metrics.DownloadNodeConcurrency = download.DownloadNodeConcurrency
	report.Metrics.DownloadBytes = download.DownloadBytes
	report.Metrics.DownloadDuration = download.DownloadDuration
	report.Metrics.GoodputBytesPerSecond = download.GoodputBytesPerSecond
	report.Metrics.AggregateLimiterWait = download.AggregateLimiterWait
	report.Metrics.RateLimited = download.RateLimited
	report.GeneralMeasuredAt = measured
	if err != nil {
		report.FailureOrigin, report.FailureCode = failureOrigin(err, ctx)
		if report.FailureCode == "" {
			report.FailureCode = "download_incomplete"
		}
		var probeErr *probeHTTPError
		if errors.As(err, &probeErr) {
			report.FailureOrigin = model.FailureOriginProbe
			report.FailureCode = fmt.Sprintf("probe_http_%d", probeErr.status)
		}
		if report.FailureOrigin != model.FailureOriginNode && ctx.Err() == nil && (!control || !probeControl(ctx, plan)) {
			report.FailureOrigin = model.FailureOriginProbe
		}
		report.Status = model.Unknown
		if report.FailureOrigin == model.FailureOriginNode {
			report.Status = model.Fail
		} else if report.FailureOrigin == model.FailureOriginEnvironment || report.FailureOrigin == model.FailureOriginProbe {
			report.InfrastructureFailure = true
		}
		return
	}

	report.Status = model.Pass
	report.GeneralStatus = model.Pass
	report.FreshUntil = report.GeneralMeasuredAt.Add(24 * time.Hour)
	for name, address := range plan.ServiceURLs {
		report.Services[name] = checkService(ctx, client, address, plan.ProbeTimeout)
	}
	report.IP = queryIP(ctx, client, plan)
	if plan.Profile == "ai" {
		report.Streams = measureStreams(ctx, dial, plan)
		report.FreshUntil = time.Now().Add(plan.AI.Validity)
		for _, stream := range report.Streams {
			if stream.Status == model.Fail || stream.Status == model.Unknown && report.Status == model.Pass {
				report.Status = stream.Status
				report.FailureCode = stream.FailureCode
				report.FailureOrigin = stream.FailureOrigin
			}
		}
		if report.Status == model.Unknown && (report.FailureOrigin == model.FailureOriginEnvironment || report.FailureOrigin == model.FailureOriginProbe) {
			report.InfrastructureFailure = true
		}
		if report.Status != model.Pass && report.FailureOrigin != model.FailureOriginNode && (!control || !probeControl(ctx, plan)) {
			report.Status = model.Unknown
			report.FailureOrigin = model.FailureOriginProbe
			report.InfrastructureFailure = true
		}

	}
	return
}

func median(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	middle := len(values) / 2
	if len(values)%2 == 1 {
		return values[middle]
	}
	return values[middle-1] + (values[middle]-values[middle-1])/2
}

func checkService(ctx context.Context, client *http.Client, address string, timeout time.Duration) model.ServiceResult {
	result := model.ServiceResult{Status: model.Unknown, MeasuredAt: time.Now()}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		result.FailureCode = "invalid_url"
		return result
	}
	response, err := client.Do(request)
	if err != nil {
		result.FailureCode = "network"
		return result
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if readErr != nil {
		result.FailureCode = "body_incomplete"
		return result
	}
	result.HTTPStatus = response.StatusCode
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		result.Status, result.FailureCode = serviceBodyStatus(body)
	} else {
		result.FailureCode = fmt.Sprintf("http_%d", response.StatusCode)
	}
	return result
}
