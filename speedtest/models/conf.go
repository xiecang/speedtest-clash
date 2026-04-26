package models

import (
	"encoding/json"
	"fmt"
	"time"
)

type CProxyWithResult struct {
	Result
	Proxy CProxy
}

type Result struct {
	Name          string          `json:"name"`
	Bandwidth     float64         `json:"bandwidth"` // 带宽，单位为 B/s
	TTFB          time.Duration   `json:"TTFB"`
	Delay         uint16          `json:"delay"`
	DelayP50      uint16          `json:"delay_p50"`
	DelayP90      uint16          `json:"delay_p90"`
	DelayP95      uint16          `json:"delay_p95"`
	Jitter        uint16          `json:"jitter"`    // 抖动 (ms)
	LossRate      float64         `json:"loss_rate"` // 丢包率 (0.0-1.0)
	Country       string          `json:"country"`
	CheckResults  []CheckResult   `json:"check_results"`
	URLForTest    map[string]bool `json:"url_for_test"`
	TestDuration  time.Duration   `json:"test_duration"`
	DownloadBytes int64           `json:"download_bytes"`
}

func (r *Result) Alive() bool {
	return (r.Delay > 0) || (r.Bandwidth > 0 && r.TTFB > 0)
}

func (r *Result) FormattedBandwidth() string {
	var v = r.Bandwidth
	if v <= 0 {
		return "N/A"
	}
	if v < 1024 {
		return fmt.Sprintf("%.02fB/s", v)
	}
	v /= 1024
	if v < 1024 {
		return fmt.Sprintf("%.02fKB/s", v)
	}
	v /= 1024
	if v < 1024 {
		return fmt.Sprintf("%.02fMB/s", v)
	}
	v /= 1024
	if v < 1024 {
		return fmt.Sprintf("%.02fGB/s", v)
	}
	v /= 1024
	return fmt.Sprintf("%.02fTB/s", v)
}

func (r *Result) FormattedTTFB() string {
	if r.TTFB <= 0 {
		return "N/A"
	}
	var t = r.TTFB.Milliseconds()
	if t < 1000 {
		return fmt.Sprintf("%.02fms", float64(t))
	}
	return fmt.Sprintf("%.02fs", float64(t)/1000)
}

func (r *Result) FormattedCheckResult() string {
	if len(r.CheckResults) == 0 {
		return "N/A"
	}
	var rs, _ = json.Marshal(r.CheckResults)
	return string(rs)
}

func (r *Result) FormattedUrlCheck() string {
	if r.URLForTest == nil {
		return "N/A"
	}
	var rs, _ = json.Marshal(r.URLForTest)
	return string(rs)
}
