package check

import (
	"context"
	C "github.com/metacubex/mihomo/constant"
	"github.com/xiecang/speedtest-clash/speedtest/models"
	"github.com/xiecang/speedtest-clash/speedtest/requests"
	"strings"
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

	// ● 若不可用会提示
	// {"cf_details":"Something went wrong. You may be connected to a disallowed ISP. If you are using VPN, try disabling it. Otherwise try a different Wi-Fi network or data connection."}
	//
	// ● 可用提示
	// {"cf_details":"Request is not allowed. Please try again later.", "type":"dc"}
	resp, err := requestChatGPT(ctx, client, GPTTestURLIOS)
	if err != nil {
		return models.NewCheckResult(g.tp, false, ""), err
	}
	if !strings.Contains(resp.CFDetails, errMsg) {
		return models.NewCheckResult(g.tp, true, ""), err
	}

	return models.NewCheckResult(g.tp, false, ""), err
}
