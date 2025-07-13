package check

import (
	"context"
	C "github.com/metacubex/mihomo/constant"
	"github.com/xiecang/speedtest-clash/speedtest/models"
	"github.com/xiecang/speedtest-clash/speedtest/requests"
)

const (
	GPTTestURLIOS = "https://ios.chat.openai.com/"
)

type gptIOSChecker struct {
	tp models.CheckType
}

func NewGPTIOSChecker() models.Checker {
	return &gptIOSChecker{
		tp: models.CheckTypeGPTIOS,
	}
}

func (g *gptIOSChecker) Check(ctx context.Context, proxy C.Proxy) (result models.CheckResult, err error) {
	client := requests.GetClient(proxy, timeout)
	loc, err := getCountryCode(ctx, client)
	if err != nil {
		return models.NewCheckResult(g.tp, false, loc), err
	}

	ok, err := requestChatGPT(ctx, client, GPTTestURLIOS)
	if err != nil {
		return models.NewCheckResult(g.tp, false, loc), err
	}

	return models.NewCheckResult(g.tp, ok, loc), err
}
