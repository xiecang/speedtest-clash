package check

import (
	"context"
	"errors"
	"github.com/metacubex/mihomo/common/convert"
	C "github.com/metacubex/mihomo/constant"
	"github.com/xiecang/speedtest-clash/speedtest/models"
	"github.com/xiecang/speedtest-clash/speedtest/requests"
	"net/http"
	"strings"
	"time"
)

const (
	GPTTestURLAndroid = "https://android.chat.openai.com"
)

func requestChatGPT(ctx context.Context, client *http.Client, url string) (bool, error) {
	resp, err := requests.Request(ctx, &requests.RequestOption{
		Method: http.MethodPost,
		URL:    url,
		Headers: map[string]string{
			"Content-Type": "application/json",
			"User-Agent":   convert.RandUserAgent(),
		},
		Timeout:      timeout,
		RetryTimes:   3,
		RetryTimeOut: 3 * time.Second,
		Client:       client,
	})
	if err != nil {
		return false, err
	}
	ok, msg := checkGPTRes(string(resp.Body))
	if !ok {
		err = errors.New(msg)
	}
	return ok, err
}

func checkGPTRes(bodyStr string) (bool, string) {
	// ● 若不可用会提示
	// {"cf_details":"Something went wrong. You may be connected to a disallowed ISP. If you are using VPN, try disabling it. Otherwise try a different Wi-Fi network or data connection."}
	//
	// ● 可用提示
	// {"cf_details":"Request is not allowed. Please try again later.", "type":"dc"}
	bodyStr = strings.ToLower(bodyStr)
	switch {
	case strings.Contains(bodyStr, "you may be connected to a disallowed isp"):
		return false, "Disallowed ISP"
	case strings.Contains(bodyStr, "request is not allowed. please try again later."):
		return true, ""
	case strings.Contains(bodyStr, "sorry, you have been blocked"):
		return false, "Blocked"
	default:
		return false, "Failed"
	}
}

type gptAndroidChecker struct {
	tp models.CheckType
}

func NewGPTAndroidChecker() models.Checker {
	return &gptAndroidChecker{
		tp: models.CheckTypeGPTAndroid,
	}
}

func (g *gptAndroidChecker) Check(ctx context.Context, proxy C.Proxy) (result models.CheckResult, err error) {

	client := requests.GetClient(proxy, timeout)
	loc, err := getCountryCode(ctx, client, GPTTrace)
	if err != nil {
		return models.NewCheckResult(g.tp, false, loc), err
	}

	ok, err := requestChatGPT(ctx, client, GPTTestURLAndroid)
	if err != nil {
		return models.NewCheckResult(g.tp, false, loc), err
	}

	return models.NewCheckResult(g.tp, ok, loc), err
}
