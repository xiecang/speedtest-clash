// SPDX-License-Identifier: GPL-3.0-only
package model

import (
	"context"
	"crypto/tls"
	"time"
)

type ServiceCheck struct {
	Kind string `json:"kind" yaml:"kind"`
	URL  string `json:"url" yaml:"url"`
}

type AIPlan struct {
	Concurrency    int           `json:"concurrency" yaml:"concurrency"`
	CandidateLimit int           `json:"candidate_limit" yaml:"candidate_limit"`
	Duration       time.Duration `json:"duration" yaml:"duration"`
	// Deprecated: publication limits belong to the subscription application.
	PublishedLimit int           `json:"published_limit" yaml:"published_limit"`
	RetestInterval time.Duration `json:"retest_interval" yaml:"retest_interval"`
	Validity       time.Duration `json:"validity" yaml:"validity"`
}

type Plan struct {
	Filter              string `json:"filter,omitempty" yaml:"filter"`
	NameRegexContain    string `json:"name_regex_contain,omitempty" yaml:"name_regex_contain"`
	NameRegexNotContain string `json:"name_regex_not_contain,omitempty" yaml:"name_regex_not_contain"`
	TargetSelector      interface {
		Match(context.Context, []Node) ([]bool, error)
		Active() bool
	} `json:"-" yaml:"-"`
	GeneralEvidence     *Report                                                            `json:"-" yaml:"-"`
	ServiceEvidence     map[string]ServiceResult                                           `json:"-" yaml:"-"`
	ServiceChecks       map[string]ServiceCheck                                            `json:"service_checks,omitempty" yaml:"service_checks"`
	ExitIPURL           string                                                             `json:"exit_ip_url,omitempty" yaml:"exit_ip_url"`
	LookupIP            func(context.Context, string, int, string) (IPResult, bool, error) `json:"-" yaml:"-"`
	SaveIP              func(context.Context, IPResult) error                              `json:"-" yaml:"-"`
	TargetKeys          []string                                                           `json:"target_keys,omitempty" yaml:"-"`
	Core                Core                                                               `json:"core" yaml:"core"`
	Profile             string                                                             `json:"profile" yaml:"profile"`
	ProbeURL            string                                                             `json:"probe_url" yaml:"probe_url"`
	ProbeToken          string                                                             `json:"-" yaml:"probe_token"`
	DownloadBytes       int64                                                              `json:"download_bytes" yaml:"download_bytes"`
	DownloadConcurrency int                                                                `json:"download_concurrency" yaml:"download_concurrency"`
	SampleWindow        time.Duration                                                      `json:"sample_window" yaml:"sample_window"`
	ProbeTimeout        time.Duration                                                      `json:"probe_timeout" yaml:"probe_timeout"`
	LivenessConcurrency int                                                                `json:"liveness_concurrency" yaml:"liveness_concurrency"`
	AI                  AIPlan                                                             `json:"ai" yaml:"ai"`
	ServiceURLs         map[string]string                                                  `json:"service_urls,omitempty" yaml:"service_urls"`
	IPProviders         map[string]string                                                  `json:"ip_providers,omitempty" yaml:"ip_providers"`
	// ProbeTLSConfig is set only by callers explicitly trusting a private test CA.
	ProbeTLSConfig *tls.Config `json:"-" yaml:"-"`
	WorkerID       string      `json:"worker_id" yaml:"worker_id"`
	Environment    string      `json:"environment" yaml:"environment"`
}

func DefaultPlan() Plan {
	return Plan{ServiceChecks: map[string]ServiceCheck{"OpenAI-Android": {Kind: "openai-mobile", URL: "https://android.chat.openai.com"}, "OpenAI-iOS": {Kind: "openai-mobile", URL: "https://ios.chat.openai.com/"}, "Imgur": {Kind: "imgur", URL: "https://i.imgur.com/hI88z6W.jpeg"}}, ExitIPURL: "https://api.ipify.org?format=json", Core: Mihomo, Profile: "general", DownloadBytes: 50 << 20, DownloadConcurrency: 5,
		SampleWindow: 15 * time.Second, ProbeTimeout: 10 * time.Second, LivenessConcurrency: 64,
		AI: AIPlan{Concurrency: 5, CandidateLimit: 50, Duration: 180 * time.Second, RetestInterval: 2 * time.Hour, Validity: 6 * time.Hour}}
}
