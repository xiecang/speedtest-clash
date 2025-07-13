package check

import (
	"context"
	C "github.com/metacubex/mihomo/constant"
	"github.com/xiecang/speedtest-clash/speedtest/models"
	"github.com/xiecang/speedtest-clash/speedtest/requests"
)

const cfTrace = "https://www.cloudflare.com/cdn-cgi/trace"

type countryChecker struct {
	tp models.CheckType
}

func NewCountryChecker() models.Checker {
	return &countryChecker{
		tp: models.CheckTypeCountry,
	}
}

func (c *countryChecker) Check(ctx context.Context, proxy C.Proxy) (result models.CheckResult, err error) {
	client := requests.GetClient(proxy, timeout)

	loc, err := getCountryCode(ctx, client, cfTrace)
	if err != nil {
		return models.NewCheckResult(c.tp, false, loc), err
	}
	return models.NewCheckResult(c.tp, true, loc), nil
}
