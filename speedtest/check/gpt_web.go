package check

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/metacubex/mihomo/common/convert"
	C "github.com/metacubex/mihomo/constant"
	"github.com/xiecang/speedtest-clash/speedtest/models"
	"github.com/xiecang/speedtest-clash/speedtest/requests"
)

const (
	GPTTrace           = "https://chat.openai.com/cdn-cgi/trace"
	GPTWebCheckURL     = "https://api.openai.com/compliance/cookie_requirements"
	unsupportedCountry = "unsupported_country"
)

func getCountryCode(ctx context.Context, client *http.Client, url string) (string, error) {
	resp, err := requests.Request(ctx, &requests.RequestOption{
		Method:       http.MethodGet,
		URL:          url,
		Timeout:      timeout,
		RetryTimes:   retryTimes,
		RetryTimeOut: retryTimeOut,
		Client:       client,
		Headers: map[string]string{
			"User-Agent": convert.RandUserAgent(),
		},
	})
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(resp.Body), "\n")
	var loc string
	for _, line := range lines {
		if !strings.Contains(line, "loc=") {
			continue
		}
		loc = strings.TrimPrefix(line, "loc=")
		break
	}
	return loc, nil
}

func checkGPTWebRes(bodyStr string) (bool, string) {
	if strings.Contains(strings.ToLower(bodyStr), unsupportedCountry) {
		return false, "Unsupported Country/Region"
	}
	return true, ""
}

func requestChatGPTWeb(ctx context.Context, client *http.Client) (bool, error) {
	resp, err := requests.Request(ctx, &requests.RequestOption{
		Method:       http.MethodGet,
		URL:          GPTWebCheckURL,
		Timeout:      timeout,
		RetryTimes:   retryTimes,
		RetryTimeOut: retryTimeOut,
		Client:       client,
		Headers: map[string]string{
			"User-Agent": convert.RandUserAgent(),
		},
	})
	if err != nil {
		return false, err
	}
	if resp.StatusCode != http.StatusOK {
		msg := http.StatusText(resp.StatusCode)
		if msg == "" {
			msg = "Unexpected Status"
		}
		return false, errors.New(msg)
	}
	ok, msg := checkGPTWebRes(string(resp.Body))
	if !ok {
		return false, errors.New(msg)
	}
	return true, nil
}

type gptWebChecker struct {
	tp models.CheckType
}

func NewGPTWebChecker() models.Checker {
	return &gptWebChecker{
		tp: models.CheckTypeGPTWeb,
	}
}

func (g *gptWebChecker) Check(ctx context.Context, proxy C.Proxy) (result models.CheckResult, err error) {
	client := requests.GetClient(proxy, timeout)

	loc, err := getCountryCode(ctx, client, GPTTrace)
	if err != nil {
		return models.NewCheckResult(g.tp, false, loc), err
	}

	ok, err := requestChatGPTWeb(ctx, client)
	if err != nil {
		return models.NewCheckResult(g.tp, false, loc), err
	}

	return models.NewCheckResult(g.tp, ok, loc), nil
}
