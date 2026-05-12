package check

import (
	"context"
	"github.com/metacubex/mihomo/common/convert"
	C "github.com/metacubex/mihomo/constant"
	"github.com/xiecang/speedtest-clash/speedtest/models"
	"github.com/xiecang/speedtest-clash/speedtest/requests"
	"net/http"
	"strings"
)

const geminiRegionMarker = `,2,1,200,"`

var geminiBlockedRegions = map[string]struct{}{
	"CHN": {},
	"RUS": {},
	"BLR": {},
	"CUB": {},
	"IRN": {},
	"PRK": {},
	"SYR": {},
	"HKG": {},
	"MAC": {},
}

type geminiChecker struct {
	tp models.CheckType
}

func NewGeminiChecker() models.Checker {
	return &geminiChecker{
		tp: models.CheckTypeGemini,
	}
}

func (g geminiChecker) Check(ctx context.Context, proxy C.Proxy) (result models.CheckResult, err error) {
	client := requests.GetClient(proxy, timeout)
	resp, err := requests.Request(ctx, &requests.RequestOption{
		Method:       http.MethodGet,
		URL:          "https://gemini.google.com/",
		Timeout:      timeout,
		RetryTimes:   retryTimes,
		RetryTimeOut: retryTimeOut,
		Client:       client,
		Headers: map[string]string{
			"User-Agent": convert.RandUserAgent(),
		},
	})
	if err != nil {
		return models.NewCheckResult(g.tp, false, ""), err
	}
	region, ok := parseGeminiRegion(string(resp.Body))
	if !ok {
		return models.NewCheckResult(g.tp, false, ""), nil
	}
	return models.NewCheckResult(g.tp, isGeminiRegionAllowed(region), region), nil
}

func parseGeminiRegion(body string) (string, bool) {
	idx := strings.Index(body, geminiRegionMarker)
	if idx < 0 {
		return "", false
	}
	start := idx + len(geminiRegionMarker)
	if len(body) < start+3 {
		return "", false
	}
	code := body[start : start+3]
	for _, b := range []byte(code) {
		if b < 'A' || b > 'Z' {
			return "", false
		}
	}
	return code, true
}

func isGeminiRegionAllowed(region string) bool {
	if region == "" {
		return false
	}
	_, blocked := geminiBlockedRegions[region]
	return !blocked
}
