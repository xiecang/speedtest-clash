package models

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	red        = "\033[31m"
	green      = "\033[32m"
	emojiRegex = regexp.MustCompile(`[\x{1F600}-\x{1F64F}\x{1F300}-\x{1F5FF}\x{1F680}-\x{1F6FF}\x{2600}-\x{26FF}\x{1F1E0}-\x{1F1FF}]`)
	spaceRegex = regexp.MustCompile(`\s{2,}`)
)

type CProxyWithResult struct {
	Proxies CProxy
	Results Result
}

type Result struct {
	Name         string          `json:"name"`
	Bandwidth    float64         `json:"bandwidth"`
	TTFB         time.Duration   `json:"TTFB"`
	Delay        uint16          `json:"delay"`
	Country      string          `json:"country"`
	CheckResults []CheckResult   `json:"check_results"`
	URLForTest   map[string]bool `json:"url_for_test"`
}

func (r *Result) Alive() bool {
	return (r.Delay > 0) || (r.Bandwidth > 0 && r.TTFB > 0)
}

func formatName(name string) string {
	noEmoji := emojiRegex.ReplaceAllString(name, "")
	mergedSpaces := spaceRegex.ReplaceAllString(noEmoji, " ")
	return strings.TrimSpace(mergedSpaces)
}

func formatBandwidth(v float64) string {
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

func formatMilliseconds(v time.Duration) string {
	if v <= 0 {
		return "N/A"
	}
	return fmt.Sprintf("%.02fms", float64(v.Milliseconds()))
}

func (r *Result) Printf(format string) {
	color := ""
	if r.Bandwidth < 1024*1024 {
		color = red
	} else if r.Bandwidth > 1024*1024*10 {
		color = green
	}
	fmt.Printf(format, color, formatName(r.Name), formatBandwidth(r.Bandwidth), formatMilliseconds(r.TTFB),
		fmt.Sprintf("%d", r.Delay),
	)
}
