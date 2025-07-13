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

// https://github.com/clash-verge-rev/clash-verge-rev/blob/c894a15d13d5bcce518f8412cc393b56272a9afa/src-tauri/src/cmd/media_unlock_checker.rs#L241

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
		RetryTimes:   2,
		RetryTimeOut: retryTimeOut,
		Client:       client,
		Headers: map[string]string{
			"User-Agent": convert.RandUserAgent(),
		},
	})
	if err != nil {
		return models.NewCheckResult(g.tp, false, ""), err
	}
	if strings.Contains(string(resp.Body), "45631641,null,true") {
		return models.NewCheckResult(g.tp, true, ""), nil
	}
	return models.NewCheckResult(g.tp, false, ""), err
}
