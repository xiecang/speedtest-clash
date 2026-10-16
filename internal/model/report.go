// SPDX-License-Identifier: GPL-3.0-only
package model

import (
	"time"
)

// EvidenceAt is the completion clock of the relevant stage, independent of task start.
func (r Report) EvidenceAt() time.Time {
	if r.Profile == "general" && !r.GeneralMeasuredAt.IsZero() {
		return r.GeneralMeasuredAt
	}
	return r.CompletedAt
}

type Status string

const (
	Pass    Status = "pass"
	Fail    Status = "fail"
	Unknown Status = "unknown"
)

type FailureOrigin string

const (
	FailureOriginNode        FailureOrigin = "node"
	FailureOriginEnvironment FailureOrigin = "environment"
	FailureOriginProbe       FailureOrigin = "probe"
	FailureOriginUnknown     FailureOrigin = "unknown"
)

type StreamResult struct {
	Protocol         string        `json:"protocol"`
	Status           Status        `json:"status"`
	FailureOrigin    FailureOrigin `json:"failure_origin,omitempty"`
	MeasuredAt       time.Time     `json:"measured_at"`
	Duration         time.Duration `json:"duration_ns"`
	Frames           int64         `json:"frames"`
	MaxActiveSilence time.Duration `json:"max_active_silence_ns"`
	IdleMarkerSeen   bool          `json:"idle_marker_seen"`
	ResumeObserved   bool          `json:"resume_observed"`
	FailureCode      string        `json:"failure_code,omitempty"`
}

type ServiceResult struct {
	CheckRevision string    `json:"check_revision,omitempty"`
	Status        Status    `json:"status"`
	MeasuredAt    time.Time `json:"measured_at"`
	HTTPStatus    int       `json:"http_status,omitempty"`
	FailureCode   string    `json:"failure_code,omitempty"`
}

type IPResult struct {
	Conflict      bool      `json:"conflict,omitempty"`
	CheckRevision string    `json:"check_revision,omitempty"`
	IP            string    `json:"ip,omitempty"`
	Family        int       `json:"family,omitempty"`
	Provider      string    `json:"provider"`
	Status        Status    `json:"status"`
	MeasuredAt    time.Time `json:"measured_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	Country       string    `json:"country,omitempty"`
	ASN           string    `json:"asn,omitempty"`
	NetworkType   string    `json:"network_type,omitempty"`
	Reputation    *float64  `json:"reputation,omitempty"`
	Confidence    float64   `json:"confidence,omitempty"`
}

// Metrics describe application reads and successful HTTP health/header samples.
// Durations are nanoseconds; all bandwidth values use bytes/second, never bits.
type Metrics struct {
	DownloadNodeConcurrency     int           `json:"download_node_concurrency,omitempty"`
	BandwidthQueueWait          time.Duration `json:"bandwidth_queue_wait_ns"`
	HealthAttempts              int           `json:"health_attempts"`
	HealthFailures              int           `json:"health_failures"`
	ColdHealthSamples           int           `json:"cold_health_samples"`
	ReusedHealthSamples         int           `json:"reused_health_samples"`
	ColdHealthMedian            time.Duration `json:"cold_health_median_ns"`
	ReusedHealthMedian          time.Duration `json:"reused_health_median_ns"`
	DownloadHeadersMedian       time.Duration `json:"download_headers_median_ns"`
	DownloadHeaderSamples       int           `json:"download_header_samples"`
	DownloadReusedHeaderSamples int           `json:"download_reused_header_samples"`
	DownloadEndReason           string        `json:"download_end_reason"`
	DownloadBytes               int64         `json:"download_bytes"`
	DownloadDuration            time.Duration `json:"download_duration_ns"`
	GoodputBytesPerSecond       float64       `json:"goodput_bytes_per_second"`
	RateLimitBytesPerSecond     int64         `json:"rate_limit_bytes_per_second"`
	AggregateLimiterWait        time.Duration `json:"aggregate_limiter_wait_ns"`
	RateLimited                 bool          `json:"rate_limited"`
}

type Report struct {
	GeneralReused         bool                     `json:"general_reused,omitempty"`
	SchemaVersion         int                      `json:"schema_version"`
	MetricVersion         int                      `json:"metric_version"`
	NodeKey               string                   `json:"node_key"`
	ConfigRevision        string                   `json:"config_revision"`
	EvaluationRevision    string                   `json:"evaluation_revision"`
	WorkerID              string                   `json:"worker_id"`
	Environment           string                   `json:"environment"`
	InstanceID            string                   `json:"instance_id"`
	Sequence              int64                    `json:"sequence"`
	JobID                 string                   `json:"job_id,omitempty"`
	Core                  Core                     `json:"core"`
	CoreVersion           string                   `json:"core_version"`
	Profile               string                   `json:"profile"`
	Status                Status                   `json:"status"`
	MeasuredAt            time.Time                `json:"measured_at"`
	CompletedAt           time.Time                `json:"completed_at"`
	FreshUntil            time.Time                `json:"fresh_until"`
	GeneralStatus         Status                   `json:"general_status"`
	GeneralFailureCode    string                   `json:"general_failure_code,omitempty"`
	GeneralFailureOrigin  FailureOrigin            `json:"general_failure_origin,omitempty"`
	GeneralMeasuredAt     time.Time                `json:"general_measured_at"`
	FailureCode           string                   `json:"failure_code,omitempty"`
	FailureOrigin         FailureOrigin            `json:"failure_origin,omitempty"`
	InfrastructureFailure bool                     `json:"infrastructure_failure,omitempty"`
	Cached                bool                     `json:"cached,omitempty"`
	OriginalObservation   string                   `json:"original_observation,omitempty"`
	Metrics               Metrics                  `json:"metrics"`
	Streams               []StreamResult           `json:"streams,omitempty"`
	Services              map[string]ServiceResult `json:"services,omitempty"`
	IP                    []IPResult               `json:"ip,omitempty"`
}
