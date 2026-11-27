package nodequality

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/xiecang/node-quality/v2/engine"
	"github.com/xiecang/node-quality/v2/filter"
)

const MetricVersion = 2

type ServiceCheck struct {
	Kind string `json:"kind" yaml:"kind"`
	URL  string `json:"url" yaml:"url"`
}

type AIPlan struct {
	Concurrency    int           `json:"concurrency" yaml:"concurrency"`
	CandidateLimit int           `json:"candidate_limit" yaml:"candidate_limit"`
	Duration       time.Duration `json:"duration" yaml:"duration"`
	RetestInterval time.Duration `json:"retest_interval" yaml:"retest_interval"`
	Validity       time.Duration `json:"validity" yaml:"validity"`
}

type IPCacheKey struct {
	IP            string
	Family        int
	Provider      string
	CheckRevision string
}

type Plan struct {
	Filter              string                                                    `json:"filter,omitempty" yaml:"filter"`
	NameRegexContain    string                                                    `json:"name_regex_contain,omitempty" yaml:"name_regex_contain"`
	NameRegexNotContain string                                                    `json:"name_regex_not_contain,omitempty" yaml:"name_regex_not_contain"`
	TargetSelector      *filter.Selector                                          `json:"-" yaml:"-"`
	preparedFilter      [3]string                                                 `json:"-" yaml:"-"`
	hasPreparedFilter   bool                                                      `json:"-" yaml:"-"`
	GeneralEvidence     map[string]Report                                         `json:"-" yaml:"-"`
	ServiceEvidence     map[string]map[string]ServiceResult                       `json:"-" yaml:"-"`
	ServiceChecks       map[string]ServiceCheck                                   `json:"service_checks,omitempty" yaml:"service_checks"`
	ExitIPURL           string                                                    `json:"exit_ip_url,omitempty" yaml:"exit_ip_url"`
	LookupIP            func(context.Context, IPCacheKey) (IPResult, bool, error) `json:"-" yaml:"-"`
	SaveIP              func(context.Context, IPResult) error                     `json:"-" yaml:"-"`
	TargetKeys          []string                                                  `json:"target_keys,omitempty" yaml:"-"`
	Core                engine.Core                                               `json:"core" yaml:"core"`
	Profile             string                                                    `json:"profile" yaml:"profile"`
	ProbeURL            string                                                    `json:"probe_url" yaml:"probe_url"`
	ProbeToken          string                                                    `json:"-" yaml:"probe_token"`
	DownloadBytes       int64                                                     `json:"download_bytes" yaml:"download_bytes"`
	DownloadConcurrency int                                                       `json:"download_concurrency" yaml:"download_concurrency"`
	SampleWindow        time.Duration                                             `json:"sample_window" yaml:"sample_window"`
	ProbeTimeout        time.Duration                                             `json:"probe_timeout" yaml:"probe_timeout"`
	LivenessConcurrency int                                                       `json:"liveness_concurrency" yaml:"liveness_concurrency"`
	AI                  AIPlan                                                    `json:"ai" yaml:"ai"`
	IPProviders         map[string]string                                         `json:"ip_providers,omitempty" yaml:"ip_providers"`
	ProbeTLSConfig      *tls.Config                                               `json:"-" yaml:"-"`
	WorkerID            string                                                    `json:"worker_id" yaml:"worker_id"`
	Environment         string                                                    `json:"environment" yaml:"environment"`
}

func DefaultPlan() Plan {
	return Plan{
		ServiceChecks: map[string]ServiceCheck{
			"OpenAI-Android": {Kind: "openai-mobile", URL: "https://android.chat.openai.com"},
			"OpenAI-iOS":     {Kind: "openai-mobile", URL: "https://ios.chat.openai.com/"},
			"Imgur":          {Kind: "imgur", URL: "https://i.imgur.com/hI88z6W.jpeg"},
			"Anthropic":      {Kind: "anthropic", URL: "https://api.anthropic.com/v1/models"},
		},
		ExitIPURL:           "https://api.ipify.org?format=json",
		Core:                engine.Mihomo,
		Profile:             "general",
		DownloadBytes:       50 << 20,
		DownloadConcurrency: 5,
		SampleWindow:        15 * time.Second,
		ProbeTimeout:        10 * time.Second,
		LivenessConcurrency: 64,
		AI: AIPlan{
			Concurrency:    5,
			CandidateLimit: 50,
			Duration:       180 * time.Second,
			RetestInterval: 2 * time.Hour,
			Validity:       6 * time.Hour,
		},
	}
}

func PreparePlan(plan Plan) (Plan, error) {
	if err := ValidatePlan(plan); err != nil {
		return plan, err
	}
	selector, err := filter.Compile(filter.Options{
		Filter:              plan.Filter,
		NameRegexContain:    plan.NameRegexContain,
		NameRegexNotContain: plan.NameRegexNotContain,
	})
	if err != nil {
		return plan, err
	}
	plan.TargetSelector = selector
	plan.preparedFilter = [3]string{plan.Filter, plan.NameRegexContain, plan.NameRegexNotContain}
	plan.hasPreparedFilter = true
	return plan, nil
}

func ValidatePlan(plan Plan) error {
	if plan.Core != engine.Mihomo && plan.Core != engine.SingBox {
		return errors.New("unsupported core")
	}
	if plan.Profile != "general" && plan.Profile != "ai" && plan.Profile != "availability" && plan.Profile != "services" {
		return errors.New("profile must be general, ai, availability, or services")
	}
	if plan.ProbeTimeout <= 0 || plan.ProbeTimeout > time.Minute || plan.LivenessConcurrency < 1 || plan.LivenessConcurrency > 128 {
		return errors.New("invalid liveness limits")
	}
	if err := validateServiceChecks(plan); err != nil {
		return err
	}
	if plan.Profile == "services" {
		return nil
	}
	address, err := url.Parse(plan.ProbeURL)
	if err != nil || address.Host == "" || (address.Scheme != "https" && address.Scheme != "http") {
		return errors.New("valid probe_url required")
	}
	if plan.Profile == "availability" {
		return nil
	}
	if len(plan.IPProviders) > 3 {
		return errors.New("at most three IP providers per plan")
	}
	if plan.DownloadBytes <= 0 || plan.DownloadBytes > 256<<20 || plan.DownloadConcurrency < 1 || plan.DownloadConcurrency > 32 || plan.SampleWindow <= 0 || plan.SampleWindow > time.Minute {
		return errors.New("invalid download limits")
	}
	if plan.Profile == "ai" && (plan.AI.Concurrency < 1 || plan.AI.Concurrency > 32 || plan.AI.Duration <= 0 || plan.AI.Duration > 10*time.Minute || plan.AI.Validity <= 0 || plan.AI.Validity > 6*time.Hour || plan.AI.CandidateLimit < 1 || plan.AI.CandidateLimit > 200 || plan.AI.RetestInterval <= 0) {
		return errors.New("invalid AI limits")
	}
	return nil
}

func validateServiceChecks(plan Plan) error {
	if len(plan.ServiceChecks) > 8 {
		return errors.New("at most eight optional service checks")
	}
	names := make(map[string]bool, len(plan.ServiceChecks))
	for name, check := range plan.ServiceChecks {
		lower := strings.ToLower(name)
		if name == "" || len(name) > 64 || names[lower] {
			return errors.New("duplicate or invalid service ID")
		}
		names[lower] = true
		if check.Kind != "openai-mobile" && check.Kind != "http" && check.Kind != "imgur" && check.Kind != "anthropic" {
			return errors.New("unsupported service checker")
		}
		u, err := url.Parse(check.URL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return errors.New("invalid service check URL")
		}
	}
	return nil
}

func PlanRevision(p Plan) string {
	if p.Profile == "availability" {
		content, _ := json.Marshal(struct {
			Core    engine.Core
			Probe   string
			Timeout time.Duration
			Version string
		}{p.Core, p.ProbeURL, p.ProbeTimeout, "availability-v1"})
		sum := sha256.Sum256(content)
		return hex.EncodeToString(sum[:])
	}
	if p.Profile == "services" {
		content, _ := json.Marshal(struct {
			Core    engine.Core
			Checks  map[string]ServiceCheck
			Version string
		}{p.Core, p.ServiceChecks, "service-v1"})
		sum := sha256.Sum256(content)
		return hex.EncodeToString(sum[:])
	}
	input := struct {
		MetricVersion                              int
		Core                                       engine.Core
		Profile                                    string
		ProbeURL                                   string
		DownloadBytes                              int64
		DownloadConcurrency                        int
		SampleWindow, ProbeTimeout, StreamDuration time.Duration
		ExitIPURL                                  string
		IPProviders                                map[string]string
	}{
		MetricVersion,
		p.Core,
		p.Profile,
		p.ProbeURL,
		p.DownloadBytes,
		p.DownloadConcurrency,
		p.SampleWindow,
		p.ProbeTimeout,
		0,
		p.ExitIPURL,
		p.IPProviders,
	}
	if p.Profile == "ai" {
		input.StreamDuration = p.AI.Duration
	}
	content, _ := json.Marshal(input)
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func ServiceCheckRevision(check ServiceCheck) string {
	sum := sha256.Sum256([]byte("service-check-v1\x00" + check.Kind + "\x00" + check.URL))
	return hex.EncodeToString(sum[:])
}

func MinimumStreamDuration(duration time.Duration) time.Duration {
	return duration - min(2*time.Second, duration/20)
}
