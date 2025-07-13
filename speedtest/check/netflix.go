package check

import (
	"context"
	"github.com/metacubex/mihomo/common/convert"
	C "github.com/metacubex/mihomo/constant"
	"github.com/xiecang/speedtest-clash/speedtest/models"
	"github.com/xiecang/speedtest-clash/speedtest/requests"
	"net/http"
)

type netflixChecker struct {
	tp models.CheckType
}

func NewNetflixChecker() models.Checker {
	return &netflixChecker{
		tp: models.CheckTypeNetflix,
	}
}

func (n netflixChecker) Check(ctx context.Context, proxy C.Proxy) (result models.CheckResult, err error) {
	client := requests.GetClient(proxy, timeout)
	resp, err := requests.Request(ctx, &requests.RequestOption{
		Method:       http.MethodGet,
		URL:          "https://www.netflix.com/sg/title/81498621",
		Timeout:      timeout,
		RetryTimes:   2,
		RetryTimeOut: retryTimeOut,
		Client:       client,
		Headers: map[string]string{
			"User-Agent": convert.RandUserAgent(),
		},
	})
	if err != nil {
		return models.NewCheckResult(n.tp, false, ""), err
	}
	return models.NewCheckResult(n.tp, resp.StatusCode == http.StatusOK, ""), nil
}
